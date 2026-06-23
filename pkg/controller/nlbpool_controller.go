package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	eipv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"
)

// NLBPoolReconciler orchestrates child CRs (NLB, EIP, PortAllocation) and
// uses the cloud NLB API during deletion to verify cascade completion.
type NLBPoolReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	NLBClient provider.NLBAPIClient
}

// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=nlbpools,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=nlbpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=nlbpools/finalizers,verbs=update
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=portallocations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=portallocations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=servergroups,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=listeners,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=eip.alibabacloud.com,resources=eips,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile runs the orchestration loop.
func (r *NLBPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the NLBPool CR.
	pool := &nlbpoolv1alpha1.NLBPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Handle deletion. Because the pool itself owns child CRs with
	// blockOwnerDeletion=true, K8s GC will not cascade-delete them while the
	// pool finalizer is still present (deadlock). We therefore proactively
	// delete every child CR and only remove the finalizer once they are all
	// gone.
	//
	// Deletion order is critical:
	//   Phase 1: Delete NLB/EIP CRs first → cloud NLB deletion cascade-deletes
	//            all Listeners (1 API call replaces 1600 individual DeleteListener calls)
	//   Phase 2: Wait for cloud NLBs to fully disappear (listeners cascade-complete)
	//   Phase 3: Delete PAs → PA finalizer only needs to delete SGs (no ResourceInUse)
	if !pool.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(pool, nlbpoolv1alpha1.FinalizerNLBPool) {
			if pool.Status.Phase != nlbpoolv1alpha1.NLBPoolDeleting {
				pool.Status.Phase = nlbpoolv1alpha1.NLBPoolDeleting
				pool.Status.Message = "Deleting child resources"
				if err := r.Status().Update(ctx, pool); err != nil {
					return ctrl.Result{}, err
				}
			}

			// Phase 1: Delete NLB/EIP infrastructure CRs.
			// Cloud NLB deletion cascade-deletes all Listeners, eliminating
			// the need for PA finalizers to delete them individually.
			if err := r.deleteInfrastructure(ctx, pool); err != nil {
				logger.Error(err, "failed to delete infrastructure CRs")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}

			// Phase 2: Wait for cloud NLBs to fully disappear. NLB deletion is
			// async — we must ensure the cascade (listeners) is complete before
			// PA finalizers try to delete SGs, otherwise ResourceInUse.
			if r.hasCloudNLBs(ctx, pool) {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Phase 3: All cloud NLBs gone (listeners cascade-deleted).
			// Now safe to delete PAs — their finalizers only need to delete SGs.
			if err := r.deletePortAllocations(ctx, pool); err != nil {
				logger.Error(err, "failed to delete PortAllocations")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			if r.hasPortAllocations(ctx, pool) {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Verify no remaining child CRs
			if r.hasChildResources(ctx, pool) {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			controllerutil.RemoveFinalizer(pool, nlbpoolv1alpha1.FinalizerNLBPool)
			if err := r.Update(ctx, pool); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// 3. Ensure finalizer.
	if !controllerutil.ContainsFinalizer(pool, nlbpoolv1alpha1.FinalizerNLBPool) {
		controllerutil.AddFinalizer(pool, nlbpoolv1alpha1.FinalizerNLBPool)
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Validate lane config (fail-fast). Backstop for the CRD CEL rule
	// in case CEL is disabled or the cluster K8s version doesn't enforce it.
	if msg := validateLaneConfig(pool.Spec.Lanes); msg != "" {
		r.Recorder.Eventf(pool, "Warning", "LaneConfigInvalid", "%s", msg)
		_ = r.updatePhase(ctx, pool, nlbpoolv1alpha1.NLBPoolFailed, msg)
		// Don't requeue — wait for user to fix the spec, which triggers a watch.
		return ctrl.Result{}, nil
	}

	// 5. Sync pool status to get fresh BoundSlots for expansion calculation.
	r.syncPoolStatus(ctx, pool)
	desiredNLBsPerLane := r.computeDesiredNLBs(pool)

	// 6. Ensure baseline cloud-backed CRs (NLB + EIP) per lane.
	nlbsReady, err := r.ensureNLBsAndEIPs(ctx, pool, desiredNLBsPerLane)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !nlbsReady {
		_ = r.updatePhase(ctx, pool, nlbpoolv1alpha1.NLBPoolProvisioning, "waiting for NLB/EIP CRs to become ready")
		r.syncPoolStatus(ctx, pool)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// 7. Ensure one PortAllocation CR per slot. SG/Listener cloud resources
	// are now provisioned by the PA Controller (V6 architecture).
	allReady, err := r.ensureSlotResources(ctx, pool, desiredNLBsPerLane)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 8. Decide phase + requeue cadence.
	if allReady && nlbsReady {
		_ = r.updatePhase(ctx, pool, nlbpoolv1alpha1.NLBPoolReady, "")
	} else {
		_ = r.updatePhase(ctx, pool, nlbpoolv1alpha1.NLBPoolProvisioning, "")
	}

	// 9. Refresh aggregated pool status counters (after phase update to avoid overwrite).
	r.syncPoolStatus(ctx, pool)

	if allReady && nlbsReady {
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// SetupWithManager wires the controller into the manager.
func (r *NLBPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbpoolv1alpha1.NLBPool{}).
		Owns(&nlbv1.NLB{}).
		Owns(&nlbv1.ServerGroup{}).
		Owns(&nlbv1.Listener{}).
		Owns(&eipv1alpha1.EIP{}).
		Watches(&nlbpoolv1alpha1.PortAllocation{},
			handler.EnqueueRequestsFromMapFunc(r.paToNLBPool)).
		Complete(r)
}

// paToNLBPool maps a PortAllocation event to the owning NLBPool reconcile
// request. This ensures PA status changes (e.g. Available -> Bound) trigger
// NLBPool reconcile so that expansion logic can react promptly.
func (r *NLBPoolReconciler) paToNLBPool(ctx context.Context, obj client.Object) []reconcile.Request {
	pa, ok := obj.(*nlbpoolv1alpha1.PortAllocation)
	if !ok {
		return nil
	}
	poolName := pa.Labels[nlbpoolv1alpha1.LabelPool]
	if poolName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      poolName,
			Namespace: pa.Namespace,
		},
	}}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// computeDesiredNLBs calculates how many NLB groups are needed per lane.
// Expansion is triggered only when AvailableSlots drops below the configured
// minimum free headroom (slotsPerNLB * minAvailableNLBs).
func (r *NLBPoolReconciler) computeDesiredNLBs(pool *nlbpoolv1alpha1.NLBPool) int32 {
	minFree := pool.Spec.SlotsPerNLB * pool.Spec.MinAvailableNLBs
	currentNLBs := pool.Status.TotalSlots / pool.Spec.SlotsPerNLB
	if currentNLBs < 1 {
		currentNLBs = 1
	}
	if pool.Status.AvailableSlots >= minFree {
		return currentNLBs
	}
	needed := pool.Status.BoundSlots + minFree
	desiredNLBs := (needed + pool.Spec.SlotsPerNLB - 1) / pool.Spec.SlotsPerNLB
	if desiredNLBs < currentNLBs {
		desiredNLBs = currentNLBs
	}
	return desiredNLBs
}

// nlbNameForGroup returns the NLB CR name for a given lane and group index.
// Group 0 uses the legacy format for backward compatibility.
func (r *NLBPoolReconciler) nlbNameForGroup(pool *nlbpoolv1alpha1.NLBPool, lane nlbpoolv1alpha1.LaneConfig, groupIdx int32) string {
	return fmt.Sprintf("%s-%s-%d", pool.Name, lane.Name, groupIdx)
}

// eipNameForGroup returns the EIP CR name for a given lane, group, and zone.
func (r *NLBPoolReconciler) eipNameForGroup(pool *nlbpoolv1alpha1.NLBPool, lane nlbpoolv1alpha1.LaneConfig, groupIdx int32, zoneIdx int) string {
	return fmt.Sprintf("%s-%s-%d-z%d", pool.Name, lane.Name, groupIdx, zoneIdx)
}

// deleteAllChildren proactively triggers deletion of every child CR owned by
// the pool. The deletion order (PA -> Listener -> SG -> EIP -> NLB) reflects
// the desired teardown sequence, but each child CR has its own finalizer that
// guarantees the cloud-side ordering, so this function only needs to mark
// the CRs for deletion. Subsequent reconciles wait until they are fully gone
// before removing the pool finalizer.
func (r *NLBPoolReconciler) deleteAllChildren(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) error {
	ns := pool.Namespace
	poolLabels := client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}
	listOpts := []client.ListOption{client.InNamespace(ns), poolLabels}

	// Step 1: Delete PortAllocations (release pod bindings first).
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList, listOpts...); err != nil {
		return fmt.Errorf("list PortAllocations: %w", err)
	}
	for i := range paList.Items {
		pa := &paList.Items[i]
		if !pa.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, pa); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete PortAllocation %s: %w", pa.Name, err)
		}
	}

	// Step 2: Delete Listeners (unbind from SGs/NLBs).
	lsnList := &nlbv1.ListenerList{}
	if err := r.List(ctx, lsnList, listOpts...); err != nil {
		return fmt.Errorf("list Listeners: %w", err)
	}
	for i := range lsnList.Items {
		lsn := &lsnList.Items[i]
		if !lsn.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, lsn); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete Listener %s: %w", lsn.Name, err)
		}
	}

	// Step 3: Delete ServerGroups.
	sgList := &nlbv1.ServerGroupList{}
	if err := r.List(ctx, sgList, listOpts...); err != nil {
		return fmt.Errorf("list ServerGroups: %w", err)
	}
	for i := range sgList.Items {
		sg := &sgList.Items[i]
		if !sg.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, sg); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete ServerGroup %s: %w", sg.Name, err)
		}
	}

	// Step 4: Delete EIPs owned by this pool. Filter by ownerReference to
	// avoid touching unrelated EIPs in the same namespace.
	eipList := &eipv1alpha1.EIPList{}
	if err := r.List(ctx, eipList, listOpts...); err != nil {
		return fmt.Errorf("list EIPs: %w", err)
	}
	for i := range eipList.Items {
		eip := &eipList.Items[i]
		if !metav1.IsControlledBy(eip, pool) {
			continue
		}
		if !eip.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, eip); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete EIP %s: %w", eip.Name, err)
		}
	}

	// Step 5: Delete NLBs owned by this pool.
	nlbList := &nlbv1.NLBList{}
	if err := r.List(ctx, nlbList, listOpts...); err != nil {
		return fmt.Errorf("list NLBs: %w", err)
	}
	for i := range nlbList.Items {
		nlb := &nlbList.Items[i]
		if !metav1.IsControlledBy(nlb, pool) {
			continue
		}
		if !nlb.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, nlb); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete NLB %s: %w", nlb.Name, err)
		}
	}

	return nil
}

// deletePortAllocations deletes only PortAllocation CRs owned by the pool.
// PA finalizers will handle cloud-side SG/Listener cleanup before the CR is
// garbage collected.
func (r *NLBPoolReconciler) deletePortAllocations(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) error {
	ns := pool.Namespace
	poolLabels := client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}
	listOpts := []client.ListOption{client.InNamespace(ns), poolLabels}

	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList, listOpts...); err != nil {
		return fmt.Errorf("list PortAllocations: %w", err)
	}
	for i := range paList.Items {
		pa := &paList.Items[i]
		if !pa.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, pa); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete PortAllocation %s: %w", pa.Name, err)
		}
	}
	return nil
}

// hasPortAllocations reports whether any PortAllocation CR still exists for
// the pool (including those pending finalizer completion).
func (r *NLBPoolReconciler) hasPortAllocations(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) bool {
	sel := client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}
	ns := client.InNamespace(pool.Namespace)
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList, ns, sel); err == nil && len(paList.Items) > 0 {
		return true
	}
	return false
}

// hasCloudNLBs verifies whether cloud NLB instances still exist by calling the
// cloud API directly. This avoids race conditions where K8s NLB CRs are removed
// before the cloud cascade (listener deletion) is complete.
func (r *NLBPoolReconciler) hasCloudNLBs(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) bool {
	logger := log.FromContext(ctx)

	if len(pool.Status.CloudNLBIds) == 0 {
		return false
	}

	for _, nlbId := range pool.Status.CloudNLBIds {
		exists, err := r.NLBClient.LoadBalancerExists(ctx, nlbId)
		if err != nil {
			if provider.IsLocalRateLimited(err) {
				return true
			}
			logger.Error(err, "LoadBalancerExists failed, assuming still exists", "nlbId", nlbId)
			return true
		}
		if exists {
			return true
		}
	}

	pool.Status.CloudNLBIds = nil
	_ = r.Status().Update(ctx, pool)
	return false
}

// deleteInfrastructure deletes EIP, NLB, and legacy Listener/ServerGroup CRs
// owned by the pool. Called BEFORE PAs are deleted so that cloud NLB deletion
// cascade-deletes all listeners, eliminating the need for PA finalizers to
// individually delete them.
func (r *NLBPoolReconciler) deleteInfrastructure(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) error {
	ns := pool.Namespace
	poolLabels := client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}
	listOpts := []client.ListOption{client.InNamespace(ns), poolLabels}

	// Delete legacy Listener CRs (V5 backward compatibility).
	lsnList := &nlbv1.ListenerList{}
	if err := r.List(ctx, lsnList, listOpts...); err != nil {
		return fmt.Errorf("list Listeners: %w", err)
	}
	for i := range lsnList.Items {
		lsn := &lsnList.Items[i]
		if !lsn.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, lsn); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete Listener %s: %w", lsn.Name, err)
		}
	}

	// Delete legacy ServerGroup CRs (V5 backward compatibility).
	sgList := &nlbv1.ServerGroupList{}
	if err := r.List(ctx, sgList, listOpts...); err != nil {
		return fmt.Errorf("list ServerGroups: %w", err)
	}
	for i := range sgList.Items {
		sg := &sgList.Items[i]
		if !sg.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, sg); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete ServerGroup %s: %w", sg.Name, err)
		}
	}

	// Delete EIPs owned by this pool.
	eipList := &eipv1alpha1.EIPList{}
	if err := r.List(ctx, eipList, listOpts...); err != nil {
		return fmt.Errorf("list EIPs: %w", err)
	}
	for i := range eipList.Items {
		eip := &eipList.Items[i]
		if !metav1.IsControlledBy(eip, pool) {
			continue
		}
		if !eip.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, eip); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete EIP %s: %w", eip.Name, err)
		}
	}

	// Delete NLBs owned by this pool.
	// First, collect cloud NLB IDs for later verification (before CR is gone).
	nlbList := &nlbv1.NLBList{}
	if err := r.List(ctx, nlbList, listOpts...); err != nil {
		return fmt.Errorf("list NLBs: %w", err)
	}
	if len(pool.Status.CloudNLBIds) == 0 {
		var cloudIds []string
		for i := range nlbList.Items {
			nlb := &nlbList.Items[i]
			if !metav1.IsControlledBy(nlb, pool) {
				continue
			}
			if id := nlb.Status.LoadBalancerId; id != "" {
				cloudIds = append(cloudIds, id)
			}
		}
		if len(cloudIds) > 0 {
			pool.Status.CloudNLBIds = cloudIds
			if err := r.Status().Update(ctx, pool); err != nil {
				return fmt.Errorf("persist CloudNLBIds: %w", err)
			}
		}
	}

	for i := range nlbList.Items {
		nlb := &nlbList.Items[i]
		if !metav1.IsControlledBy(nlb, pool) {
			continue
		}
		if !nlb.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.Delete(ctx, nlb); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete NLB %s: %w", nlb.Name, err)
		}
	}

	return nil
}

// hasChildResources reports whether any child CR still exists for the pool.
func (r *NLBPoolReconciler) hasChildResources(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) bool {
	sel := client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}
	ns := client.InNamespace(pool.Namespace)

	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList, ns, sel); err == nil && len(paList.Items) > 0 {
		return true
	}
	nlbList := &nlbv1.NLBList{}
	if err := r.List(ctx, nlbList, ns, sel); err == nil && len(nlbList.Items) > 0 {
		return true
	}
	sgList := &nlbv1.ServerGroupList{}
	if err := r.List(ctx, sgList, ns, sel); err == nil && len(sgList.Items) > 0 {
		return true
	}
	lsnList := &nlbv1.ListenerList{}
	if err := r.List(ctx, lsnList, ns, sel); err == nil && len(lsnList.Items) > 0 {
		return true
	}
	eipList := &eipv1alpha1.EIPList{}
	if err := r.List(ctx, eipList, ns, sel); err == nil && len(eipList.Items) > 0 {
		return true
	}
	return false
}

// ensureNLBsAndEIPs creates EIP CRs per zone and NLB CRs per lane for each
// group [0, desiredNLBsPerLane). Each zone in NLB ZoneMappings must reference
// an independent EIP AllocationId (Aliyun rejects duplicates with
// DuplicatedParam.AllocationId). All zone EIPs must have an AllocationID
// before the NLB CR is created.
func (r *NLBPoolReconciler) ensureNLBsAndEIPs(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool, desiredNLBsPerLane int32) (bool, error) {
	allReady := true
	for nlbGroupIdx := int32(0); nlbGroupIdx < desiredNLBsPerLane; nlbGroupIdx++ {
		for _, lane := range pool.Spec.Lanes {
			nlbName := r.nlbNameForGroup(pool, lane, nlbGroupIdx)

			// Step 1: Decide whether to create EIP CRs.
			//   - BGP/BGP_PRO + bandwidthPackageId → pure B path: skip EIP CR, NLB auto-creates BGP EIP
			//   - BGP/BGP_PRO without bandwidthPackageId → A path: create EIP CR (PayByTraffic)
			//   - Single-ISP (with or without bandwidthPackageId) → always A path: create EIP CR (PayByBandwidth)
			//     because NLB can only auto-create BGP EIPs; single-ISP EIPs must be pre-created.
			//     If bandwidthPackageId is also set, NLB will accept both AllocationId + BandwidthPackageId
			//     and automatically join the EIP to the bandwidth package (A+B hybrid).
			isSingleISP := lane.ISPType == "ChinaTelecom" || lane.ISPType == "ChinaUnicom" || lane.ISPType == "ChinaMobile" ||
				lane.ISPType == "ChinaTelecom_L2" || lane.ISPType == "ChinaUnicom_L2" || lane.ISPType == "ChinaMobile_L2"
			skipEIPCR := !isSingleISP && lane.BandwidthPackageId != ""
			var eipAllocationIds []string
			if !skipEIPCR {
				eipAllocationIds = make([]string, len(pool.Spec.ZoneMaps))
				eipsReady := true
				for zIdx, zone := range pool.Spec.ZoneMaps {
					eipName := r.eipNameForGroup(pool, lane, nlbGroupIdx, zIdx)
					eipCR := &eipv1alpha1.EIP{}
					err := r.Get(ctx, types.NamespacedName{Name: eipName, Namespace: pool.Namespace}, eipCR)
					if errors.IsNotFound(err) {
						eipCR = r.buildEIPCR(pool, lane, eipName)
						if err := controllerutil.SetControllerReference(pool, eipCR, r.Scheme); err != nil {
							return false, err
						}
						if err := r.Create(ctx, eipCR); err != nil && !errors.IsAlreadyExists(err) {
							return false, err
						}
						r.Recorder.Eventf(pool, "Normal", "EIPCreated",
							"Created EIP CR %s for lane %s zone %s", eipName, lane.Name, zone.Zone)
						eipsReady = false
						continue
					} else if err != nil {
						return false, err
					}

					// AllocationID is enough; InUse only flips after NLB binds the EIP.
					if eipCR.Status.AllocationID == "" {
						eipsReady = false
						continue
					}
					eipAllocationIds[zIdx] = eipCR.Status.AllocationID
				}

				if !eipsReady {
					allReady = false
					continue
				}
			}

			// Step 2: Ensure NLB CR exists. eipAllocationIds is nil for path B;
			// buildNLBCR will leave ZoneMappings[].AllocationId empty so the NLB
			// API auto-creates EIPs joined to BandwidthPackageId.
			nlbCR := &nlbv1.NLB{}
			err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pool.Namespace}, nlbCR)
			if errors.IsNotFound(err) {
				nlbCR = r.buildNLBCR(pool, lane, nlbName, eipAllocationIds)
				if err := controllerutil.SetControllerReference(pool, nlbCR, r.Scheme); err != nil {
					return false, err
				}
				if err := r.Create(ctx, nlbCR); err != nil && !errors.IsAlreadyExists(err) {
					return false, err
				}
				if lane.BandwidthPackageId != "" {
					r.Recorder.Eventf(pool, "Normal", "NLBCreated",
						"Created NLB CR %s for lane %s with bandwidthPackageId %s", nlbName, lane.Name, lane.BandwidthPackageId)
				} else {
					r.Recorder.Eventf(pool, "Normal", "NLBCreated",
						"Created NLB CR %s for lane %s with %d zone EIPs", nlbName, lane.Name, len(eipAllocationIds))
				}
				allReady = false
				continue
			} else if err != nil {
				return false, err
			}

			// Drift handling: sync AllocationId when EIP CRs exist (path A or A+B hybrid).
			// Pure B path (BGP + bandwidthPackageId) has nil AllocationId — skip.
			if !skipEIPCR {
				needUpdate := false
				for i := range nlbCR.Spec.ZoneMappings {
					if i >= len(eipAllocationIds) {
						break
					}
					if eipAllocationIds[i] != "" && nlbCR.Spec.ZoneMappings[i].AllocationId != eipAllocationIds[i] {
						nlbCR.Spec.ZoneMappings[i].AllocationId = eipAllocationIds[i]
						needUpdate = true
					}
				}
				if needUpdate {
					if err := r.Update(ctx, nlbCR); err != nil {
						return false, err
					}
					r.Recorder.Eventf(pool, "Normal", "NLBUpdated",
						"Updated NLB CR %s ZoneMappings AllocationIds", nlbName)
				}
			}
			// NLB Active implies all bound EIPs have flipped to InUse.
			if nlbCR.Status.LoadBalancerStatus != "Active" {
				allReady = false
			}
		}
	}
	return allReady, nil
}

// ensureSlotResources creates one PortAllocation CR per slot. The PA
// Controller is responsible for provisioning the underlying SG/Listener
// cloud resources (V6 architecture); NLBPool Controller no longer creates
// SG/Listener CRs directly. Readiness is determined by checking that no PA
// is still in Provisioning phase.
func (r *NLBPoolReconciler) ensureSlotResources(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool, desiredNLBsPerLane int32) (bool, error) {
	totalSlots := desiredNLBsPerLane * pool.Spec.SlotsPerNLB

	// Fast-path: if all slots exist, check readiness via PA phases.
	startSlot := pool.Status.TotalSlots
	if startSlot < 0 {
		startSlot = 0
	}
	if startSlot >= totalSlots {
		paList := &nlbpoolv1alpha1.PortAllocationList{}
		if err := r.List(ctx, paList,
			client.InNamespace(pool.Namespace),
			client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}); err != nil {
			return false, err
		}
		for _, pa := range paList.Items {
			if pa.Status.Phase == nlbpoolv1alpha1.PortAllocationProvisioning || pa.Status.Phase == "" {
				return false, nil
			}
		}
		return true, nil
	}

	// Create new PA CRs only (no SG/Listener CR creation).
	for slotIdx := startSlot; slotIdx < totalSlots; slotIdx++ {
		paName := fmt.Sprintf("%s-s%d", pool.Name, slotIdx)
		paCR := &nlbpoolv1alpha1.PortAllocation{}
		err := r.Get(ctx, types.NamespacedName{Name: paName, Namespace: pool.Namespace}, paCR)
		if errors.IsNotFound(err) {
			paCR = r.buildPortAllocationCR(pool, slotIdx, paName)
			if err := controllerutil.SetControllerReference(pool, paCR, r.Scheme); err != nil {
				return false, err
			}
			if err := r.Create(ctx, paCR); err != nil {
				return false, err
			}
			// Initialize status.Phase = Provisioning.
			fresh := &nlbpoolv1alpha1.PortAllocation{}
			if gerr := r.Get(ctx, types.NamespacedName{Name: paName, Namespace: pool.Namespace}, fresh); gerr == nil {
				fresh.Status.Phase = nlbpoolv1alpha1.PortAllocationProvisioning
				_ = r.Status().Update(ctx, fresh)
			}
			r.Recorder.Eventf(pool, "Normal", "PortAllocationCreated",
				"Created PortAllocation CR %s for slot %d", paName, slotIdx)
		} else if err != nil {
			return false, err
		}
	}
	// Still provisioning (new PAs just created).
	return false, nil
}

// syncPoolStatus refreshes the pool's slot accounting in status.
func (r *NLBPoolReconciler) syncPoolStatus(ctx context.Context, pool *nlbpoolv1alpha1.NLBPool) {
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{nlbpoolv1alpha1.LabelPool: pool.Name}); err != nil {
		return
	}

	var available, bound int32
	for _, pa := range paList.Items {
		switch pa.Status.Phase {
		case nlbpoolv1alpha1.PortAllocationAvailable:
			available++
		case nlbpoolv1alpha1.PortAllocationBound:
			bound++
		}
	}

	fresh := &nlbpoolv1alpha1.NLBPool{}
	if err := r.Get(ctx, types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, fresh); err != nil {
		return
	}
	fresh.Status.TotalSlots = int32(len(paList.Items))
	fresh.Status.AvailableSlots = available
	fresh.Status.BoundSlots = bound
	// Compute NLBsPerLane from total slots.
	if pool.Spec.SlotsPerNLB > 0 {
		fresh.Status.NLBsPerLane = (fresh.Status.TotalSlots + pool.Spec.SlotsPerNLB - 1) / pool.Spec.SlotsPerNLB
	}
	if fresh.Status.NLBsPerLane < 1 {
		fresh.Status.NLBsPerLane = 1
	}
	if fresh.Status.Phase == "" {
		fresh.Status.Phase = nlbpoolv1alpha1.NLBPoolProvisioning
	}
	_ = r.Status().Update(ctx, fresh)

	// Reflect status back to the in-memory copy so the caller sees the new
	// counters without an extra round-trip.
	pool.Status = fresh.Status
}

// updatePhase patches the pool's phase + message, fetching the latest object
// first to avoid resourceVersion conflicts.
func (r *NLBPoolReconciler) updatePhase(
	ctx context.Context,
	pool *nlbpoolv1alpha1.NLBPool,
	phase nlbpoolv1alpha1.NLBPoolPhase,
	message string,
) error {
	fresh := &nlbpoolv1alpha1.NLBPool{}
	if err := r.Get(ctx, types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}, fresh); err != nil {
		return err
	}
	if fresh.Status.Phase == phase && fresh.Status.Message == message {
		return nil
	}
	fresh.Status.Phase = phase
	fresh.Status.Message = message
	return r.Status().Update(ctx, fresh)
}

// -----------------------------------------------------------------------------
// builders
// -----------------------------------------------------------------------------

func (r *NLBPoolReconciler) buildNLBCR(
	pool *nlbpoolv1alpha1.NLBPool,
	lane nlbpoolv1alpha1.LaneConfig,
	nlbName string,
	eipAllocationIds []string,
) *nlbv1.NLB {
	zoneMappings := make([]nlbv1.ZoneMapping, 0, len(pool.Spec.ZoneMaps))
	for i, z := range pool.Spec.ZoneMaps {
		zm := nlbv1.ZoneMapping{
			ZoneId:    z.Zone,
			VSwitchId: z.VSwitchId,
		}
		// One independent EIP per zone (Aliyun rejects duplicate AllocationIds).
		if i < len(eipAllocationIds) && eipAllocationIds[i] != "" {
			zm.AllocationId = eipAllocationIds[i]
		}
		zoneMappings = append(zoneMappings, zm)
	}
	return &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nlbName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: pool.Name,
				nlbpoolv1alpha1.LabelLane: lane.Name,
			},
		},
		Spec: nlbv1.NLBSpec{
			LoadBalancerName:   nlbName,
			AddressType:        "Internet",
			VpcId:              pool.Spec.VpcId,
			BandwidthPackageId: lane.BandwidthPackageId,
			ZoneMappings:       zoneMappings,
		},
	}
}

// validateLaneConfig backstops the CRD CEL rule on LaneConfig. Returns
// empty string when all lanes are valid; otherwise a human-readable
// message describing the first violation.
//
// validateLaneConfig validates lane configurations. Returns empty string
// when valid; otherwise a human-readable message. Currently no blocking
// validations — single-ISP lanes work both with and without bandwidthPackageId.
func validateLaneConfig(lanes []nlbpoolv1alpha1.LaneConfig) string {
	return ""
}

// isSingleISP returns true for single-line ISP types that require
// PayByBandwidth EIPs (ChinaTelecom, ChinaUnicom, ChinaMobile and L2 variants).
func isSingleISP(ispType string) bool {
	switch ispType {
	case "ChinaTelecom", "ChinaUnicom", "ChinaMobile",
		"ChinaTelecom_L2", "ChinaUnicom_L2", "ChinaMobile_L2":
		return true
	}
	return false
}

// buildEIPCR builds an independent EIP CR (path A).
//
// Charge type is ISP-aware:
//   - BGP/BGP_PRO → PayByTraffic (NLB rejects BGP PayByBandwidth EIPs)
//   - Single-ISP → PayByBandwidth (only option; NLB specially allows it)
func (r *NLBPoolReconciler) buildEIPCR(
	pool *nlbpoolv1alpha1.NLBPool,
	lane nlbpoolv1alpha1.LaneConfig,
	eipName string,
) *eipv1alpha1.EIP {
	chargeType := "PayByTraffic"
	bandwidth := lane.Bandwidth
	if isSingleISP(lane.ISPType) {
		chargeType = "PayByBandwidth"
		if bandwidth == "" {
			bandwidth = "200"
		}
	}
	return &eipv1alpha1.EIP{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eipName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: pool.Name,
				nlbpoolv1alpha1.LabelLane: lane.Name,
			},
		},
		Spec: eipv1alpha1.EIPSpec{
			Name:                    eipName,
			ISP:                     lane.ISPType,
			InternetChargeType:      chargeType,
			Bandwidth:               bandwidth,
			SecurityProtectionTypes: lane.SecurityProtectionTypes,
		},
	}
}

func (r *NLBPoolReconciler) buildServerGroupCR(
	pool *nlbpoolv1alpha1.NLBPool,
	slotIdx int32,
	port nlbpoolv1alpha1.PortConfig,
	sgName string,
) *nlbv1.ServerGroup {
	return &nlbv1.ServerGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sgName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: pool.Name,
				nlbpoolv1alpha1.LabelSlot: fmt.Sprintf("%d", slotIdx),
				nlbpoolv1alpha1.LabelPort: port.Name,
			},
		},
		Spec: nlbv1.ServerGroupSpec{
			Region:          pool.Spec.Region,
			VpcId:           pool.Spec.VpcId,
			ServerGroupName: sgName,
			ServerGroupType: "Ip",
			Protocol:        port.Protocol,
			Scheduler:       "Wrr",
		},
	}
}

func (r *NLBPoolReconciler) buildListenerCR(
	pool *nlbpoolv1alpha1.NLBPool,
	lane nlbpoolv1alpha1.LaneConfig,
	port nlbpoolv1alpha1.PortConfig,
	slotIdx int32,
	lsnName, nlbName, sgName string,
	listenerPort int32,
) *nlbv1.Listener {
	return &nlbv1.Listener{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lsnName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: pool.Name,
				nlbpoolv1alpha1.LabelSlot: fmt.Sprintf("%d", slotIdx),
				nlbpoolv1alpha1.LabelPort: port.Name,
				nlbpoolv1alpha1.LabelLane: lane.Name,
			},
		},
		Spec: nlbv1.ListenerSpec{
			Region:           pool.Spec.Region,
			LoadBalancerRef:  nlbName,
			ServerGroupRef:   sgName,
			ListenerPort:     listenerPort,
			ListenerProtocol: port.Protocol,
		},
	}
}

func (r *NLBPoolReconciler) buildPortAllocationCR(
	pool *nlbpoolv1alpha1.NLBPool,
	slotIdx int32,
	paName string,
) *nlbpoolv1alpha1.PortAllocation {
	return &nlbpoolv1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      paName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool:  pool.Name,
				nlbpoolv1alpha1.LabelSlot:  fmt.Sprintf("%d", slotIdx),
				nlbpoolv1alpha1.LabelPhase: string(nlbpoolv1alpha1.PortAllocationProvisioning),
			},
		},
	}
}
