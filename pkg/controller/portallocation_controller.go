package controller

import (
	"context"
	"crypto/md5"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"
)

// PortAllocationReconciler reconciles a PortAllocation object.
//
// State machine:
//
//	Provisioning -> (NLBPool controller) -> Available
//	Available    -> (Pod claim)          -> Binding
//	Binding      -> (AddServers ok)      -> Bound
//	Bound        -> (Pod deleted)        -> Releasing -> Available
//	Bound        -> (Pod disabled)       -> Disabled  -> (re-enable) -> Binding
//
// PA Controller is the only writer of spec.BoundPod / spec.BoundPodIP /
// status.Phase. Pod-side AnnotationPAClaim is the optimistic lock that
// guards races among multiple reconcile workers.
type PortAllocationReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	NLBClient     provider.NLBAPIClient
	Recorder      record.EventRecorder
	nlbSemaphores sync.Map // key=nlbId -> chan struct{} (cap=1), per-NLB CreateListener serialization
}

// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=portallocations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=portallocations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlbpool.alibabacloud.com,resources=portallocations/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile dispatches by current Phase.
func (r *PortAllocationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pa := &nlbpoolv1alpha1.PortAllocation{}
	if err := r.Get(ctx, req.NamespacedName, pa); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion path.
	if !pa.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, pa)
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(pa, nlbpoolv1alpha1.FinalizerPortAllocation) {
		controllerutil.AddFinalizer(pa, nlbpoolv1alpha1.FinalizerPortAllocation)
		if err := r.Update(ctx, pa); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	logger.V(1).Info("reconcile PA",
		"phase", pa.Status.Phase, "boundPod", pa.Spec.BoundPod, "boundPodIP", pa.Spec.BoundPodIP)

	// Initialize: fill full status arrays and set phase=Provisioning.
	if pa.Status.Phase == "" {
		poolName := pa.Labels[nlbpoolv1alpha1.LabelPool]
		slotLabel := pa.Labels[nlbpoolv1alpha1.LabelSlot]
		if poolName == "" || slotLabel == "" {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		slotIdxInt, err := strconv.Atoi(slotLabel)
		if err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		slotIdx := int32(slotIdxInt)

		pool := &nlbpoolv1alpha1.NLBPool{}
		if err := r.Get(ctx, types.NamespacedName{Name: poolName, Namespace: pa.Namespace}, pool); err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		slotInGroup := slotIdx % pool.Spec.SlotsPerNLB
		portCount := int32(len(pool.Spec.Ports))

		// Initialize SG array (index = portIndex)
		var sgs []nlbpoolv1alpha1.ServerGroupCloudStatus
		for _, port := range pool.Spec.Ports {
			sgs = append(sgs, nlbpoolv1alpha1.ServerGroupCloudStatus{
				Name:  port.Name,
				Phase: "Pending",
			})
		}

		// Initialize Listener array (index = portIndex * len(lanes) + laneIndex)
		var lsns []nlbpoolv1alpha1.ListenerCloudStatus
		for portIdx, port := range pool.Spec.Ports {
			listenerPort := pool.Spec.PortRange.Min + slotInGroup*portCount + int32(portIdx)
			for _, lane := range pool.Spec.Lanes {
				lsns = append(lsns, nlbpoolv1alpha1.ListenerCloudStatus{
					PortName:     port.Name,
					LaneName:     lane.Name,
					ListenerPort: listenerPort,
					Phase:        "Pending",
				})
			}
		}

		pa.Status.Phase = nlbpoolv1alpha1.PortAllocationProvisioning
		pa.Status.Message = "Initializing"
		pa.Status.ServerGroups = sgs
		pa.Status.Listeners = lsns
		pa.Status.SGsTotal = int32(len(sgs))
		pa.Status.SGsReady = 0
		pa.Status.ListenersTotal = int32(len(lsns))
		pa.Status.ListenersReady = 0
		if err := r.Status().Update(ctx, pa); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		r.updatePhaseLabel(ctx, pa)
		return ctrl.Result{Requeue: true}, nil
	}

	switch pa.Status.Phase {
	case nlbpoolv1alpha1.PortAllocationProvisioning:
		return r.handleProvisioning(ctx, pa)
	case nlbpoolv1alpha1.PortAllocationAvailable:
		return r.handleAvailable(ctx, pa)
	case nlbpoolv1alpha1.PortAllocationBinding:
		return r.handleBinding(ctx, pa)
	case nlbpoolv1alpha1.PortAllocationBound:
		return r.handleBound(ctx, pa)
	case nlbpoolv1alpha1.PortAllocationReleasing:
		return r.handleReleasing(ctx, pa)
	case nlbpoolv1alpha1.PortAllocationDisabled:
		return r.handleDisabled(ctx, pa)
	default:
		return ctrl.Result{}, nil
	}
}

// ---------------------------------------------------------------------------
// State handlers
// ---------------------------------------------------------------------------

func (r *PortAllocationReconciler) handleDeletion(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handleDeletion", "pa", pa.Name, "phase", pa.Status.Phase)

	if !controllerutil.ContainsFinalizer(pa, nlbpoolv1alpha1.FinalizerPortAllocation) {
		return ctrl.Result{}, nil
	}

	// 1. Best-effort RemoveServers when still bound.
	if pa.Status.Phase == nlbpoolv1alpha1.PortAllocationBound && pa.Spec.BoundPodIP != "" {
		if err := r.removeServers(ctx, pa); err != nil {
			r.eventf(pa, corev1.EventTypeWarning, "RemoveServerFailed",
				"Failed to remove servers during deletion: %v", err)
		}
	}

	// 2. Delete Listeners (must delete Listeners before SGs because Listeners reference SGs).
	logger.Info("handleDeletion step 2: delete listeners", "count", len(pa.Status.Listeners))
	var remainingListeners []nlbpoolv1alpha1.ListenerCloudStatus
	for _, lsn := range pa.Status.Listeners {
		if lsn.ListenerId == "" {
			continue
		}
		if err := r.NLBClient.DeleteListener(ctx, lsn.ListenerId); err != nil {
			if !provider.IsNotFoundError(err) {
				// Keep this listener for retry
				remainingListeners = append(remainingListeners, lsn)
				if provider.IsLocalRateLimited(err) || isThrottlingError(err) {
					// Persist progress and requeue
					pa.Status.Listeners = remainingListeners
					_ = r.Status().Update(ctx, pa)
					if provider.IsLocalRateLimited(err) {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				pa.Status.Listeners = remainingListeners
				_ = r.Status().Update(ctx, pa)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
		// Deleted successfully or NotFound - don't keep it
	}
	// All listeners deleted, clear the list
	pa.Status.Listeners = nil
	pa.Status.ListenersReady = 0
	pa.Status.ListenersTotal = 0

	// 3. Delete ServerGroups.
	logger.Info("handleDeletion step 3: delete SGs", "count", len(pa.Status.ServerGroups))
	var remainingSGs []nlbpoolv1alpha1.ServerGroupCloudStatus
	for _, sg := range pa.Status.ServerGroups {
		if sg.ServerGroupId == "" {
			continue
		}
		if err := r.NLBClient.DeleteServerGroup(ctx, sg.ServerGroupId); err != nil {
			logger.Info("DeleteServerGroup error", "sg", sg.ServerGroupId, "err", err.Error())
			if !provider.IsNotFoundError(err) {
				// ResourceInUse means Listeners still reference this SG. PA.Status.Listeners
				// may have been cleared by an interrupted earlier deletion (e.g. Throttling),
				// so step 2 found nothing while orphan listeners still live on the cloud.
				// Discover and delete those orphan listeners by NLB+port, then requeue so
				// the next pass can drop the SG.
				if strings.Contains(err.Error(), "ResourceInUse") {
					if cleanupErr := r.cleanupOrphanListeners(ctx, pa); cleanupErr != nil {
						logger.Error(cleanupErr, "cleanupOrphanListeners failed", "sg", sg.ServerGroupId)
						if provider.IsLocalRateLimited(cleanupErr) {
							return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
						}
						if isThrottlingError(cleanupErr) {
							return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
						}
					}
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				}
				remainingSGs = append(remainingSGs, sg)
				if provider.IsLocalRateLimited(err) || isThrottlingError(err) {
					pa.Status.ServerGroups = remainingSGs
					_ = r.Status().Update(ctx, pa)
					if provider.IsLocalRateLimited(err) {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				pa.Status.ServerGroups = remainingSGs
				_ = r.Status().Update(ctx, pa)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}
	// All SGs deleted (or NotFound), cloud resources are cleaned up.
	// Remove finalizer directly - PA is about to be garbage collected, status cleanup is best-effort.
	controllerutil.RemoveFinalizer(pa, nlbpoolv1alpha1.FinalizerPortAllocation)
	if err := r.Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	logger.Info("PA deletion completed", "pa", pa.Name)
	return ctrl.Result{}, nil
}

// cleanupOrphanListeners discovers and deletes any cloud Listeners that this PA
// owns by (NLB, port) but no longer tracks in pa.Status.Listeners. Used during
// handleDeletion when DeleteServerGroup returns ResourceInUse.ServerGroup,
// which signals that an earlier listener-deletion pass was interrupted before
// the cloud-side resource was actually removed.
func (r *PortAllocationReconciler) cleanupOrphanListeners(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) error {
	logger := log.FromContext(ctx)

	poolName := pa.Labels[nlbpoolv1alpha1.LabelPool]
	slotLabel := pa.Labels[nlbpoolv1alpha1.LabelSlot]
	if poolName == "" || slotLabel == "" {
		return fmt.Errorf("pa %s missing pool/slot labels", pa.Name)
	}
	slotIdxInt, err := strconv.Atoi(slotLabel)
	if err != nil {
		return fmt.Errorf("invalid slot label %q: %w", slotLabel, err)
	}
	slotIdx := int32(slotIdxInt)

	pool := &nlbpoolv1alpha1.NLBPool{}
	if err := r.Get(ctx, types.NamespacedName{Name: poolName, Namespace: pa.Namespace}, pool); err != nil {
		return fmt.Errorf("get pool %s: %w", poolName, err)
	}
	if pool.Spec.SlotsPerNLB <= 0 || len(pool.Spec.Lanes) == 0 || len(pool.Spec.Ports) == 0 {
		return nil
	}

	nlbGroupIdx := slotIdx / pool.Spec.SlotsPerNLB
	slotInGroup := slotIdx % pool.Spec.SlotsPerNLB
	portCount := int32(len(pool.Spec.Ports))

	for _, lane := range pool.Spec.Lanes {
		nlbName := fmt.Sprintf("%s-%s-%d", pool.Name, lane.Name, nlbGroupIdx)
		nlbCR := &nlbv1.NLB{}
		if err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pa.Namespace}, nlbCR); err != nil {
			logger.Info("cleanupOrphanListeners: skip lane (NLB CR unavailable)",
				"name", nlbName, "err", err.Error())
			continue
		}
		nlbId := nlbCR.Status.LoadBalancerId
		if nlbId == "" {
			continue
		}

		for portIdx := range pool.Spec.Ports {
			listenerPort := pool.Spec.PortRange.Min + slotInGroup*portCount + int32(portIdx)
			existingId, listErr := r.NLBClient.ListListenersByPort(ctx, nlbId, listenerPort)
			if listErr != nil {
				if provider.IsLocalRateLimited(listErr) || isThrottlingError(listErr) {
					return listErr
				}
				logger.Info("cleanupOrphanListeners: ListListenersByPort failed",
					"nlbId", nlbId, "port", listenerPort, "err", listErr.Error())
				continue
			}
			if existingId == "" {
				continue
			}
			if delErr := r.NLBClient.DeleteListener(ctx, existingId); delErr != nil {
				if provider.IsNotFoundError(delErr) {
					continue
				}
				return fmt.Errorf("delete orphan listener %s on nlb %s port %d: %w",
					existingId, nlbId, listenerPort, delErr)
			}
			r.eventf(pa, corev1.EventTypeNormal, "OrphanListenerDeleted",
				"Deleted orphan listener %s on NLB %s port %d", existingId, nlbId, listenerPort)
		}
	}
	return nil
}

// handleProvisioning directly creates cloud ServerGroups and Listeners via NLB API,
// tracks their status in pa.Status.ServerGroups / pa.Status.Listeners using per-instance
// JSON Patch, and promotes the PA to Available once all resources are ready.
func (r *PortAllocationReconciler) handleProvisioning(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	poolName := pa.Labels[nlbpoolv1alpha1.LabelPool]
	slotLabel := pa.Labels[nlbpoolv1alpha1.LabelSlot]
	if poolName == "" || slotLabel == "" {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	slotIdxInt, err := strconv.Atoi(slotLabel)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	slotIdx := int32(slotIdxInt)

	// Fetch NLBPool to get spec (ports, lanes, slotsPerNLB, portRange).
	pool := &nlbpoolv1alpha1.NLBPool{}
	if err := r.Get(ctx, types.NamespacedName{Name: poolName, Namespace: pa.Namespace}, pool); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	nlbGroupIdx := slotIdx / pool.Spec.SlotsPerNLB
	slotInGroup := slotIdx % pool.Spec.SlotsPerNLB
	portCount := int32(len(pool.Spec.Ports))
	laneCount := len(pool.Spec.Lanes)

	// Initialize status arrays if not yet done (handles PAs created by NLBPool controller
	// that only set phase=Provisioning without filling arrays).
	if len(pa.Status.ServerGroups) == 0 {
		var sgs []nlbpoolv1alpha1.ServerGroupCloudStatus
		for _, port := range pool.Spec.Ports {
			sgs = append(sgs, nlbpoolv1alpha1.ServerGroupCloudStatus{
				Name:  port.Name,
				Phase: "Pending",
			})
		}
		var lsns []nlbpoolv1alpha1.ListenerCloudStatus
		for portIdx, port := range pool.Spec.Ports {
			listenerPort := pool.Spec.PortRange.Min + slotInGroup*portCount + int32(portIdx)
			for _, lane := range pool.Spec.Lanes {
				lsns = append(lsns, nlbpoolv1alpha1.ListenerCloudStatus{
					PortName:     port.Name,
					LaneName:     lane.Name,
					ListenerPort: listenerPort,
					Phase:        "Pending",
				})
			}
		}
		pa.Status.ServerGroups = sgs
		pa.Status.Listeners = lsns
		pa.Status.SGsTotal = int32(len(sgs))
		pa.Status.ListenersTotal = int32(len(lsns))
		if err := r.Status().Update(ctx, pa); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// -------------------------------------------------------------------
	// Phase 1: Ensure all ServerGroups (index = portIndex)
	// -------------------------------------------------------------------
	allSGsActive := true

	for i, sg := range pa.Status.ServerGroups {
		switch {
		case sg.ServerGroupId == "" && sg.Phase == "Pending":
			// Create new SG directly (ClientToken ensures idempotency)
			sgName := fmt.Sprintf("%s-s%d-%s", pool.Name, slotIdx, sg.Name)
			clientToken := r.provisioningClientToken(pa, "sg", sg.Name)
			resp, err := r.NLBClient.CreateServerGroup(ctx, &provider.CreateServerGroupRequest{
				VpcId:           pool.Spec.VpcId,
				ServerGroupName: sgName,
				ServerGroupType: "Ip",
				Protocol:        "TCP_UDP",
				Scheduler:       "Wrr",
				ClientToken:     clientToken,
			})
			if err != nil {
				if provider.IsLocalRateLimited(err) {
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
				if isThrottlingError(err) {
					r.eventf(pa, corev1.EventTypeWarning, "Throttled",
						"CreateServerGroup throttled for %s", sgName)
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				logger.Error(err, "CreateServerGroup failed", "name", sgName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			sgId := resp.ServerGroupId
			r.eventf(pa, corev1.EventTypeNormal, "SGCreated",
				"Created ServerGroup %s (%s)", sgName, sgId)

			// JSON Patch: set serverGroupId + phase=Creating
			if err := r.patchSGStatus(ctx, pa, i, sgId, "Creating"); err != nil {
				return ctrl.Result{Requeue: true}, nil
			}
			// Update in-memory
			pa.Status.ServerGroups[i].ServerGroupId = sgId
			pa.Status.ServerGroups[i].Phase = "Creating"
			allSGsActive = false

		case sg.Phase == "Creating":
			// Poll status
			attr, err := r.NLBClient.GetServerGroupAttribute(ctx, sg.ServerGroupId)
			if err != nil {
				if provider.IsLocalRateLimited(err) {
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
				if isThrottlingError(err) {
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				logger.Error(err, "GetServerGroupAttribute failed", "sgId", sg.ServerGroupId)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			if attr != nil && attr.ServerGroupStatus == "Available" {
				if err := r.patchSGStatus(ctx, pa, i, sg.ServerGroupId, "Active"); err != nil {
					return ctrl.Result{Requeue: true}, nil
				}
				pa.Status.ServerGroups[i].Phase = "Active"
			} else {
				allSGsActive = false
			}

		case sg.Phase == "Active":
			// Already done, skip

		default:
			allSGsActive = false
		}
	}

	if !allSGsActive {
		_ = r.patchAggregates(ctx, pa)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// -------------------------------------------------------------------
	// Phase 2: Ensure all Listeners (index = portIndex * laneCount + laneIndex)
	// -------------------------------------------------------------------
	allListenersRunning := true

	for i, lsn := range pa.Status.Listeners {
		// Determine which NLB this listener belongs to
		portIdx := i / laneCount
		laneIdx := i % laneCount
		lane := pool.Spec.Lanes[laneIdx]

		switch {
		case lsn.ListenerId == "" && lsn.Phase == "Pending":
			// Get NLB ID for this lane + nlbGroup
			nlbName := fmt.Sprintf("%s-%s-%d", pool.Name, lane.Name, nlbGroupIdx)
			nlbCR := &nlbv1.NLB{}
			if err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pa.Namespace}, nlbCR); err != nil {
				logger.Error(err, "Get NLB CR failed", "name", nlbName)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			nlbId := nlbCR.Status.LoadBalancerId
			if nlbId == "" {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Get sgId from the SG array by portIndex
			sgId := pa.Status.ServerGroups[portIdx].ServerGroupId
			listenerPort := lsn.ListenerPort

			// Create Listener directly (ClientToken ensures idempotency)
			port := pool.Spec.Ports[portIdx]
			tokenPrefix := "lsn"
			for _, l := range pa.Status.Listeners {
				if l.Phase == "Running" {
					tokenPrefix = "lsn-recreate"
					break
				}
			}
			clientToken := r.provisioningClientToken(pa, tokenPrefix, port.Name, lane.Name)
			resp, err := r.NLBClient.CreateListener(ctx, &provider.CreateListenerRequest{
				LoadBalancerId:   nlbId,
				ListenerProtocol: port.Protocol,
				ListenerPort:     listenerPort,
				ServerGroupId:    sgId,
				ClientToken:      clientToken,
			})
			if err != nil {
				if provider.IsLocalRateLimited(err) {
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
				if isThrottlingError(err) {
					r.eventf(pa, corev1.EventTypeWarning, "Throttled",
						"CreateListener throttled for %s port %d", nlbId, listenerPort)
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				if isIncorrectStatusError(err) {
					// NLB is Configuring (another listener op in progress), short random requeue
					return ctrl.Result{RequeueAfter: 1*time.Second + time.Duration(pa.UID[0]%3)*time.Second}, nil
				}
				if isConflictPortError(err) {
					// Listener already exists (previous create succeeded but patch failed)
					// Fallback: retrieve existing listener ID
					existingId, listErr := r.NLBClient.ListListenersByPort(ctx, nlbId, listenerPort)
					if listErr == nil && existingId != "" {
						if pErr := r.patchListenerStatus(ctx, pa, i, existingId, "Creating"); pErr != nil {
							return ctrl.Result{Requeue: true}, nil
						}
						pa.Status.Listeners[i].ListenerId = existingId
						pa.Status.Listeners[i].Phase = "Creating"
						allListenersRunning = false
						continue
					}
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}
				logger.Error(err, "CreateListener failed", "nlbId", nlbId, "port", listenerPort)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			listenerId := resp.ListenerId
			r.eventf(pa, corev1.EventTypeNormal, "ListenerCreated",
				"Created Listener on %s port %d (%s)", nlbId, listenerPort, listenerId)

			// JSON Patch: set listenerId + phase=Creating
			if err := r.patchListenerStatus(ctx, pa, i, listenerId, "Creating"); err != nil {
				return ctrl.Result{Requeue: true}, nil
			}
			pa.Status.Listeners[i].ListenerId = listenerId
			pa.Status.Listeners[i].Phase = "Creating"
			allListenersRunning = false

		case lsn.Phase == "Creating":
			attr, err := r.NLBClient.GetListenerAttribute(ctx, lsn.ListenerId)
			if err != nil {
				if provider.IsLocalRateLimited(err) {
					return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
				}
				if isThrottlingError(err) {
					return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
				}
				if provider.IsNotFoundError(err) {
					logger.Info("Listener disappeared during Creating, resetting to Pending for re-creation",
						"listenerId", lsn.ListenerId)
					if pErr := r.patchListenerStatus(ctx, pa, i, "", "Pending"); pErr != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					pa.Status.Listeners[i].ListenerId = ""
					pa.Status.Listeners[i].Phase = "Pending"
					allListenersRunning = false
					continue
				}
				logger.Error(err, "GetListenerAttribute failed", "listenerId", lsn.ListenerId)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			if attr == nil {
				// Listener not found by stored ID — try to find it by NLB+port
				// (likely the stored ID is missing the @port suffix)
				nlbName := fmt.Sprintf("%s-%s-%d", pool.Name, lane.Name, nlbGroupIdx)
				nlbCR := &nlbv1.NLB{}
				if err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pa.Namespace}, nlbCR); err != nil {
					logger.Error(err, "Get NLB CR failed", "name", nlbName)
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}
				nlbId := nlbCR.Status.LoadBalancerId
				if nlbId == "" {
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}

				existingId, listErr := r.NLBClient.ListListenersByPort(ctx, nlbId, lsn.ListenerPort)
				if listErr == nil && existingId != "" {
					// Found existing listener — fix the stored ID
					logger.Info("Listener found by port lookup, fixing stored ID",
						"oldId", lsn.ListenerId, "correctId", existingId)
					if pErr := r.patchListenerStatus(ctx, pa, i, existingId, "Creating"); pErr != nil {
						return ctrl.Result{Requeue: true}, nil
					}
					pa.Status.Listeners[i].ListenerId = existingId
					allListenersRunning = false
					continue
				}

				// Listener truly doesn't exist — re-create
				logger.Info("Listener not found on cloud, re-creating",
					"oldListenerId", lsn.ListenerId)
				sgId := pa.Status.ServerGroups[portIdx].ServerGroupId
				port := pool.Spec.Ports[portIdx]
				clientToken := r.provisioningClientToken(pa, "lsn-recreate", port.Name, lane.Name)
				resp, createErr := r.NLBClient.CreateListener(ctx, &provider.CreateListenerRequest{
					LoadBalancerId:   nlbId,
					ListenerProtocol: port.Protocol,
					ListenerPort:     lsn.ListenerPort,
					ServerGroupId:    sgId,
					ClientToken:      clientToken,
				})
				if createErr != nil {
					if provider.IsLocalRateLimited(createErr) {
						return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
					}
					if isThrottlingError(createErr) {
						return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
					}
					if isIncorrectStatusError(createErr) {
						return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
					}
					logger.Error(createErr, "Re-CreateListener failed", "nlbId", nlbId, "port", lsn.ListenerPort)
					return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
				}
				newId := resp.ListenerId
				logger.Info("Re-created listener successfully", "oldId", lsn.ListenerId, "newId", newId)
				if pErr := r.patchListenerStatus(ctx, pa, i, newId, "Creating"); pErr != nil {
					return ctrl.Result{Requeue: true}, nil
				}
				pa.Status.Listeners[i].ListenerId = newId
				allListenersRunning = false
			} else if attr.ListenerStatus == "Running" {
				if err := r.patchListenerStatus(ctx, pa, i, lsn.ListenerId, "Running"); err != nil {
					return ctrl.Result{Requeue: true}, nil
				}
				pa.Status.Listeners[i].Phase = "Running"
			} else {
				allListenersRunning = false
			}

		case lsn.Phase == "Running":
			// Already done, skip

		default:
			allListenersRunning = false
		}
	}

	if !allListenersRunning {
		_ = r.patchAggregates(ctx, pa)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// -------------------------------------------------------------------
	// Phase 3: All ready -> fill Spec and promote to Available
	// -------------------------------------------------------------------
	var sgRefs []nlbpoolv1alpha1.ServerGroupRef
	for _, sg := range pa.Status.ServerGroups {
		sgRefs = append(sgRefs, nlbpoolv1alpha1.ServerGroupRef{
			Name:          sg.Name,
			ServerGroupId: sg.ServerGroupId,
		})
	}

	var endpoints []nlbpoolv1alpha1.LaneEndpoint
	for laneIdx, lane := range pool.Spec.Lanes {
		// Get NLB DNS name for this lane
		nlbName := fmt.Sprintf("%s-%s-%d", pool.Name, lane.Name, nlbGroupIdx)
		nlbCR := &nlbv1.NLB{}
		if err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pa.Namespace}, nlbCR); err != nil {
			logger.Error(err, "Get NLB CR failed for endpoint", "name", nlbName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if nlbCR.Status.DNSName == "" {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		laneEndpoint := nlbpoolv1alpha1.LaneEndpoint{
			Lane: lane.Name,
			EIP:  nlbCR.Status.DNSName,
		}

		for portIdx, port := range pool.Spec.Ports {
			listenerPort := pool.Spec.PortRange.Min + slotInGroup*portCount + int32(portIdx)
			// Listener index = portIdx * laneCount + laneIdx
			lsnIdx := portIdx*laneCount + laneIdx
			listenerId := pa.Status.Listeners[lsnIdx].ListenerId
			laneEndpoint.Ports = append(laneEndpoint.Ports, nlbpoolv1alpha1.EndpointPort{
				Name:          port.Name,
				ListenerPort:  listenerPort,
				ContainerPort: port.ContainerPort,
				Protocol:      port.Protocol,
				ListenerId:    listenerId,
			})
		}
		endpoints = append(endpoints, laneEndpoint)
	}

	// Update spec
	pa.Spec.ServerGroups = sgRefs
	pa.Spec.Endpoints = endpoints
	if err := r.Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	// Re-fetch for fresh resourceVersion before status update.
	fresh := &nlbpoolv1alpha1.PortAllocation{}
	if err := r.Get(ctx, types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, fresh); err != nil {
		return ctrl.Result{Requeue: true}, nil
	}
	fresh.Status.Phase = nlbpoolv1alpha1.PortAllocationAvailable
	fresh.Status.Message = ""
	r.computeProvisioningAggregates(fresh, pool)
	if err := r.Status().Update(ctx, fresh); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, fresh)
	r.eventf(fresh, corev1.EventTypeNormal, "Available",
		"PortAllocation %s is now Available", fresh.Name)
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------------------
// Provisioning helpers
// ---------------------------------------------------------------------------

// patchSGStatus uses JSON Patch to update a single ServerGroup's status at the given index.
func (r *PortAllocationReconciler) patchSGStatus(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation, index int, sgId, phase string) error {
	patch := fmt.Sprintf(`[{"op":"replace","path":"/status/serverGroups/%d/serverGroupId","value":"%s"},{"op":"replace","path":"/status/serverGroups/%d/phase","value":"%s"}]`,
		index, sgId, index, phase)
	return r.Status().Patch(ctx, pa, client.RawPatch(types.JSONPatchType, []byte(patch)))
}

// patchListenerStatus uses JSON Patch to update a single Listener's status at the given index.
func (r *PortAllocationReconciler) patchListenerStatus(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation, index int, listenerId, phase string) error {
	patch := fmt.Sprintf(`[{"op":"replace","path":"/status/listeners/%d/listenerId","value":"%s"},{"op":"replace","path":"/status/listeners/%d/phase","value":"%s"}]`,
		index, listenerId, index, phase)
	return r.Status().Patch(ctx, pa, client.RawPatch(types.JSONPatchType, []byte(patch)))
}

// patchAggregates recalculates and patches the aggregate counters (sgsReady/listenersReady).
func (r *PortAllocationReconciler) patchAggregates(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) error {
	var sgsReady, listenersReady int32
	for _, sg := range pa.Status.ServerGroups {
		if sg.Phase == "Active" {
			sgsReady++
		}
	}
	for _, lsn := range pa.Status.Listeners {
		if lsn.Phase == "Running" {
			listenersReady++
		}
	}
	patch := fmt.Sprintf(`[{"op":"replace","path":"/status/sgsReady","value":%d},{"op":"replace","path":"/status/listenersReady","value":%d}]`,
		sgsReady, listenersReady)
	return r.Status().Patch(ctx, pa, client.RawPatch(types.JSONPatchType, []byte(patch)))
}

// provisioningClientToken builds a deterministic ClientToken for provisioning operations.
// Uses md5(pa.UID + parts...) to ensure idempotency.
func (r *PortAllocationReconciler) provisioningClientToken(pa *nlbpoolv1alpha1.PortAllocation, parts ...string) string {
	data := string(pa.UID)
	for _, p := range parts {
		data += "|" + p
	}
	return fmt.Sprintf("%x", md5.Sum([]byte(data)))
}

// computeProvisioningAggregates recalculates SGsReady/SGsTotal/ListenersReady/ListenersTotal.
func (r *PortAllocationReconciler) computeProvisioningAggregates(pa *nlbpoolv1alpha1.PortAllocation, pool *nlbpoolv1alpha1.NLBPool) {
	pa.Status.SGsTotal = int32(len(pool.Spec.Ports))
	pa.Status.ListenersTotal = int32(len(pool.Spec.Ports) * len(pool.Spec.Lanes))

	var sgsReady, listenersReady int32
	for _, sg := range pa.Status.ServerGroups {
		if sg.Phase == "Active" {
			sgsReady++
		}
	}
	for _, lsn := range pa.Status.Listeners {
		if lsn.Phase == "Running" {
			listenersReady++
		}
	}
	pa.Status.SGsReady = sgsReady
	pa.Status.ListenersReady = listenersReady
}

func (r *PortAllocationReconciler) handleAvailable(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	// --- Phase A: PA already has a BoundPod (resume after partial bind) ---
	if pa.Spec.BoundPod != "" {
		return r.availableResumeBind(ctx, pa)
	}

	// --- Phase B: Find a candidate Pod and perform PA CAS + Pod CAS ---
	poolName := pa.Labels[nlbpoolv1alpha1.LabelPool]
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(pa.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	var candidate *corev1.Pod
	for i := range podList.Items {
		p := &podList.Items[i]
		// Must belong to the same pool.
		if p.Annotations[nlbpoolv1alpha1.AnnotationNLBPoolName] != poolName {
			continue
		}
		// Must have a PodIP.
		if p.Status.PodIP == "" {
			continue
		}
		// No claim or already claims this PA -> ideal candidate.
		claim := p.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]
		if claim == "" {
			candidate = p
			break
		}
		if claim == pa.Name {
			candidate = p
			break
		}
		// Has a stale claim pointing to a PA that is not bound to it.
		if claim != "" {
			otherPA := &nlbpoolv1alpha1.PortAllocation{}
			if err := r.Get(ctx, types.NamespacedName{Name: claim, Namespace: p.Namespace}, otherPA); err != nil || otherPA.Spec.BoundPod != p.Name {
				// Stale claim: the PA it points to either doesn't exist or is bound to someone else.
				candidate = p
				break
			}
		}
	}

	if candidate == nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// --- Step 1: CAS write PA.Spec.BoundPod (reserve the PA) ---
	pa.Spec.BoundPod = candidate.Name
	pa.Spec.BoundPodIP = candidate.Status.PodIP
	if err := r.Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.eventf(pa, corev1.EventTypeNormal, "PAReserved",
		"Reserved PA for pod %s (CAS on PA)", candidate.Name)

	// --- Step 2: CAS write Pod annotation (confirm the binding) ---
	// Use optimistic locking (Update with resourceVersion) so that if another
	// PA concurrently writes the annotation, one of them gets a Conflict error.
	// Additionally, check if the annotation was already set by someone else
	// between our List and now (read-modify-write with conflict detection).
	if candidate.Annotations == nil {
		candidate.Annotations = map[string]string{}
	}
	existingClaim := candidate.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]
	if existingClaim != "" && existingClaim != pa.Name {
		// Another PA already claimed this Pod before us. Roll back.
		r.eventf(pa, corev1.EventTypeNormal, "PodAlreadyClaimed",
			"Pod %s already claimed by %s, rolling back", candidate.Name, existingClaim)
		pa.Spec.BoundPod = ""
		pa.Spec.BoundPodIP = ""
		if updateErr := r.Update(ctx, pa); updateErr != nil {
			if errors.IsConflict(updateErr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{Requeue: true}, nil
	}
	candidate.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] = pa.Name
	if err := r.Update(ctx, candidate); err != nil {
		if errors.IsConflict(err) {
			// Pod resourceVersion conflict: another PA wrote concurrently.
			// Roll back PA.Spec.BoundPod.
			r.eventf(pa, corev1.EventTypeWarning, "PodCASFailed",
				"Pod CAS conflict for %s, rolling back PA reservation", candidate.Name)
			pa.Spec.BoundPod = ""
			pa.Spec.BoundPodIP = ""
			if updateErr := r.Update(ctx, pa); updateErr != nil {
				if errors.IsConflict(updateErr) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{Requeue: true}, nil
		}
		// Non-conflict error: roll back and retry.
		r.eventf(pa, corev1.EventTypeWarning, "PodCASFailed",
			"Pod CAS failed for %s, rolling back PA reservation: %v", candidate.Name, err)
		pa.Spec.BoundPod = ""
		pa.Spec.BoundPodIP = ""
		if updateErr := r.Update(ctx, pa); updateErr != nil {
			if errors.IsConflict(updateErr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{Requeue: true}, nil
	}
	r.eventf(pa, corev1.EventTypeNormal, "PodClaimed",
		"Pod %s claim annotation set (CAS on Pod)", candidate.Name)

	// --- Step 3: Transition to Binding ---
	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationBinding
	pa.Status.Message = fmt.Sprintf("Binding to pod %s (IP: %s)", pa.Spec.BoundPod, pa.Spec.BoundPodIP)
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Binding", "Start binding to pod %s", pa.Spec.BoundPod)
	return ctrl.Result{Requeue: true}, nil
}

// availableResumeBind handles the case where PA.Spec.BoundPod is already set
// (e.g., after a crash between PA CAS and Pod CAS). It verifies or completes
// the dual-CAS binding.
func (r *PortAllocationReconciler) availableResumeBind(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod)
	if errors.IsNotFound(err) {
		return r.clearClaim(ctx, pa, "reserved pod no longer exists")
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// Check if Pod's claim matches us.
	claim := pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]
	switch {
	case claim == pa.Name:
		// Dual binding confirmed. Proceed to Binding.
	case claim == "":
		// PA CAS succeeded but Pod CAS didn't complete. Retry Pod CAS.
		// Use Update (optimistic lock) to ensure only one PA can claim.
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] = pa.Name
		if err := r.Update(ctx, pod); err != nil {
			// Pod was taken by someone else; roll back.
			r.eventf(pa, corev1.EventTypeWarning, "ResumePodCASFailed",
				"Resume Pod CAS failed for %s, resetting: %v", pod.Name, err)
			return r.clearClaim(ctx, pa, "Pod CAS failed during resume")
		}
		r.eventf(pa, corev1.EventTypeNormal, "ResumedPodClaim",
			"Resumed Pod claim annotation for %s", pod.Name)
	default:
		// Pod claims a different PA. We lost the race; roll back.
		r.eventf(pa, corev1.EventTypeNormal, "ClaimConflict",
			"Pod %s claims PA %s, not us; releasing reservation", pod.Name, claim)
		return r.clearClaim(ctx, pa, fmt.Sprintf("Pod %s claimed by PA %s", pod.Name, claim))
	}

	// Pod must have an IP.
	if pod.Status.PodIP == "" {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Persist BoundPodIP if changed.
	if pa.Spec.BoundPodIP != pod.Status.PodIP {
		pa.Spec.BoundPodIP = pod.Status.PodIP
		if err := r.Update(ctx, pa); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// Transition to Binding.
	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationBinding
	pa.Status.Message = fmt.Sprintf("Binding to pod %s (IP: %s)", pa.Spec.BoundPod, pa.Spec.BoundPodIP)
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Binding", "Start binding to pod %s", pa.Spec.BoundPod)
	return ctrl.Result{Requeue: true}, nil
}

func (r *PortAllocationReconciler) handleBinding(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	if pa.Spec.BoundPodIP == "" {
		return r.resetToAvailable(ctx, pa, "No pod IP for binding")
	}

	// Dual verification before calling cloud API.
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod); err != nil {
		if errors.IsNotFound(err) {
			return r.resetToAvailable(ctx, pa, "Pod not found before binding")
		}
		return ctrl.Result{}, err
	}
	if pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] != pa.Name {
		return r.resetToAvailable(ctx, pa, "Pod claim mismatch before cloud API call")
	}

	registered := make(map[string]bool, len(pa.Status.RegisteredSGs))
	for _, id := range pa.Status.RegisteredSGs {
		registered[id] = true
	}

	var addThrottled bool
	var newlyRegistered []string
	for _, sgRef := range pa.Spec.ServerGroups {
		if sgRef.ServerGroupId == "" {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if registered[sgRef.ServerGroupId] {
			continue
		}
		port := r.lookupContainerPortForSG(pa, sgRef.Name)
		if port == 0 {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		req := &provider.AddServersRequest{
			ServerGroupId: sgRef.ServerGroupId,
			Servers: []provider.BackendServer{{
				ServerType: "Ip",
				ServerId:   pa.Spec.BoundPodIP,
				ServerIp:   pa.Spec.BoundPodIP,
				Port:       port,
				Weight:     100,
			}},
			ClientToken: r.generateClientToken(pa.Namespace, pa.Name, "add", sgRef.ServerGroupId, pa.Spec.BoundPodIP),
		}
		if _, err := r.NLBClient.AddServersToServerGroup(ctx, req); err != nil {
			if isDuplicatedServerError(err) {
				newlyRegistered = append(newlyRegistered, sgRef.ServerGroupId)
				continue
			}
			if provider.IsLocalRateLimited(err) || isThrottlingError(err) {
				addThrottled = true
				continue
			}
			r.eventf(pa, corev1.EventTypeWarning, "AddServerFailed",
				"Failed to add server to SG %s: %v", sgRef.ServerGroupId, err)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		newlyRegistered = append(newlyRegistered, sgRef.ServerGroupId)
	}

	if len(newlyRegistered) > 0 {
		patch := client.MergeFrom(pa.DeepCopy())
		pa.Status.RegisteredSGs = append(pa.Status.RegisteredSGs, newlyRegistered...)
		if err := r.Status().Patch(ctx, pa, patch); err != nil {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	if addThrottled {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if len(pa.Status.RegisteredSGs) < len(pa.Spec.ServerGroups) {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationBound
	pa.Status.ExternalAddresses = r.buildExternalAddresses(pa)
	pa.Status.Message = fmt.Sprintf("Bound to pod %s", pa.Spec.BoundPod)
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Bound", "Successfully bound to pod %s", pa.Spec.BoundPod)
	return ctrl.Result{}, nil
}

func (r *PortAllocationReconciler) handleBound(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod)
	if errors.IsNotFound(err) {
		return r.startReleasing(ctx, pa)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if pod.Annotations[nlbpoolv1alpha1.AnnotationNetworkDisabled] == "true" {
		return r.handleDisable(ctx, pa)
	}

	if pod.Annotations[nlbpoolv1alpha1.AnnotationNLBPoolName] == "" {
		return r.startReleasing(ctx, pa)
	}

	// --- Dual-CAS verification: PA.BoundPod == Pod AND Pod.claim == PA ---
	claim := pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]
	if claim != pa.Name {
		// Pod does not claim this PA. Release.
		r.eventf(pa, corev1.EventTypeNormal, "DedupRelease",
			"Pod %s claim is %q, not %s; releasing", pod.Name, claim, pa.Name)
		return r.startReleasing(ctx, pa)
	}

	// Binding is stable. Periodic requeue to detect Pod deletion if watch event is lost.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *PortAllocationReconciler) handleReleasing(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	if pa.Spec.BoundPodIP == "" {
		r.clearPodClaim(ctx, pa)
		return r.resetToAvailable(ctx, pa, "Released, ready for rebinding")
	}

	remaining := make(map[string]bool, len(pa.Status.RegisteredSGs))
	for _, id := range pa.Status.RegisteredSGs {
		remaining[id] = true
	}

	var removeThrottled bool
	var removed []string
	for sgID := range remaining {
		port := r.lookupContainerPortForSGById(pa, sgID)
		req := &provider.RemoveServersRequest{
			ServerGroupId: sgID,
			Servers: []provider.BackendServer{{
				ServerType: "Ip",
				ServerId:   pa.Spec.BoundPodIP,
				ServerIp:   pa.Spec.BoundPodIP,
				Port:       port,
				Weight:     100,
			}},
			ClientToken: r.generateClientToken(pa.Namespace, pa.Name, "remove", sgID, pa.Spec.BoundPodIP),
		}
		if _, err := r.NLBClient.RemoveServersFromServerGroup(ctx, req); err != nil {
			if isServerNotFoundError(err) || isDuplicatedServerError(err) {
				removed = append(removed, sgID)
				continue
			}
			if provider.IsLocalRateLimited(err) || isThrottlingError(err) {
				removeThrottled = true
				continue
			}
			r.eventf(pa, corev1.EventTypeWarning, "RemoveServerFailed",
				"Failed to remove server from SG %s: %v", sgID, err)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		removed = append(removed, sgID)
	}

	if len(removed) > 0 {
		newList := make([]string, 0, len(pa.Status.RegisteredSGs)-len(removed))
		removedSet := make(map[string]bool, len(removed))
		for _, id := range removed {
			removedSet[id] = true
		}
		for _, id := range pa.Status.RegisteredSGs {
			if !removedSet[id] {
				newList = append(newList, id)
			}
		}
		patch := client.MergeFrom(pa.DeepCopy())
		pa.Status.RegisteredSGs = newList
		if err := r.Status().Patch(ctx, pa, patch); err != nil {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	if removeThrottled {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if len(pa.Status.RegisteredSGs) > 0 {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	r.clearPodClaim(ctx, pa)
	return r.resetToAvailable(ctx, pa, "Released, ready for rebinding")
}

func (r *PortAllocationReconciler) handleDisabled(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod)
	if errors.IsNotFound(err) {
		return r.startReleasing(ctx, pa)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if pod.Annotations[nlbpoolv1alpha1.AnnotationNetworkDisabled] != "true" {
		// Re-enable: go back to Binding so the server is re-added.
		pa.Status.Phase = nlbpoolv1alpha1.PortAllocationBinding
		pa.Status.Message = "Re-enabling network"
		if err := r.Status().Update(ctx, pa); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		r.updatePhaseLabel(ctx, pa)
		r.eventf(pa, corev1.EventTypeNormal, "ReEnabling",
			"Network re-enabled for pod %s, re-binding", pa.Spec.BoundPod)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

func (r *PortAllocationReconciler) handleDisable(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	if err := r.removeServers(ctx, pa); err != nil {
		if provider.IsLocalRateLimited(err) {
			return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
		}
		if isThrottlingError(err) {
			return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
		}
		r.eventf(pa, corev1.EventTypeWarning, "RemoveServerFailed",
			"Failed to remove servers during disable: %v", err)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationDisabled
	pa.Status.ExternalAddresses = nil
	pa.Status.Message = "Network disabled"
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Disabled",
		"Network disabled for pod %s", pa.Spec.BoundPod)
	return ctrl.Result{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (r *PortAllocationReconciler) startReleasing(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) (ctrl.Result, error) {
	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationReleasing
	pa.Status.Message = fmt.Sprintf("Releasing from pod %s", pa.Spec.BoundPod)
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Releasing",
		"Start releasing from pod %s", pa.Spec.BoundPod)
	return ctrl.Result{Requeue: true}, nil
}

func (r *PortAllocationReconciler) resetToAvailable(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation, message string) (ctrl.Result, error) {
	pa.Spec.BoundPod = ""
	pa.Spec.BoundPodIP = ""
	if err := r.Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	pa.Status.Phase = nlbpoolv1alpha1.PortAllocationAvailable
	pa.Status.ExternalAddresses = nil
	pa.Status.RegisteredSGs = nil
	pa.Status.Message = message
	if err := r.Status().Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	r.updatePhaseLabel(ctx, pa)
	r.eventf(pa, corev1.EventTypeNormal, "Available", "%s", message)
	return ctrl.Result{}, nil
}

// clearClaim drops a stale BoundPod/BoundPodIP claim while staying in Available.
func (r *PortAllocationReconciler) clearClaim(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation, reason string) (ctrl.Result, error) {
	pa.Spec.BoundPod = ""
	pa.Spec.BoundPodIP = ""
	if err := r.Update(ctx, pa); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	if pa.Status.Message != reason {
		pa.Status.Message = reason
		_ = r.Status().Update(ctx, pa)
	}
	return ctrl.Result{}, nil
}

// removeServers calls RemoveServersFromServerGroup for every SG attached to
// the PA. Throttle/rate-limit errors continue to next SG; only hard errors
// are returned. Returns a throttle sentinel if any SG was skipped.
func (r *PortAllocationReconciler) removeServers(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) error {
	if pa.Spec.BoundPodIP == "" {
		return nil
	}
	var removeThrottled bool
	for _, sgRef := range pa.Spec.ServerGroups {
		if sgRef.ServerGroupId == "" {
			continue
		}
		port := r.lookupContainerPortForSG(pa, sgRef.Name)
		req := &provider.RemoveServersRequest{
			ServerGroupId: sgRef.ServerGroupId,
			Servers: []provider.BackendServer{{
				ServerType: "Ip",
				ServerId:   pa.Spec.BoundPodIP,
				ServerIp:   pa.Spec.BoundPodIP,
				Port:       port,
				Weight:     100,
			}},
			ClientToken: r.generateClientToken(pa.Namespace, pa.Name, "remove", sgRef.ServerGroupId, pa.Spec.BoundPodIP),
		}
		if _, err := r.NLBClient.RemoveServersFromServerGroup(ctx, req); err != nil {
			if isServerNotFoundError(err) || isDuplicatedServerError(err) {
				continue
			}
			if provider.IsLocalRateLimited(err) || isThrottlingError(err) {
				removeThrottled = true
				continue
			}
			return err
		}
	}
	if removeThrottled {
		return provider.ErrLocalRateLimited
	}
	return nil
}

func (r *PortAllocationReconciler) updatePhaseLabel(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) {
	if pa.Labels == nil {
		pa.Labels = map[string]string{}
	}
	if pa.Labels[nlbpoolv1alpha1.LabelPhase] == string(pa.Status.Phase) {
		return
	}
	pa.Labels[nlbpoolv1alpha1.LabelPhase] = string(pa.Status.Phase)
	_ = r.Update(ctx, pa)
}

// generateClientToken builds a deterministic, idempotent ClientToken bound
// to (namespace, name, op, sgId, podIP).
func (r *PortAllocationReconciler) generateClientToken(namespace, name, op, sgId, podIP string) string {
	data := fmt.Sprintf("%s|%s|%s|%s|%s", namespace, name, op, sgId, podIP)
	return fmt.Sprintf("%x", md5.Sum([]byte(data)))
}

// lookupPortForSG returns the listener port allocated for the logical port
// name carried by the given SG. All lanes share the same listener port for
// a slot, so the first non-zero match wins.
func (r *PortAllocationReconciler) lookupPortForSG(pa *nlbpoolv1alpha1.PortAllocation, sgName string) int32 {
	for _, ep := range pa.Spec.Endpoints {
		for _, p := range ep.Ports {
			if p.Name == sgName && p.ListenerPort != 0 {
				return p.ListenerPort
			}
		}
	}
	return 0
}

// lookupContainerPortForSG returns the container port (backend server port)
// for the given SG name. If ContainerPort is not set (zero), falls back to
// ListenerPort for backward compatibility with older CRs.
func (r *PortAllocationReconciler) lookupContainerPortForSG(pa *nlbpoolv1alpha1.PortAllocation, sgName string) int32 {
	for _, ep := range pa.Spec.Endpoints {
		for _, p := range ep.Ports {
			if p.Name == sgName {
				if p.ContainerPort != 0 {
					return p.ContainerPort
				}
				// Fallback: use ListenerPort for backward compatibility.
				if p.ListenerPort != 0 {
					return p.ListenerPort
				}
			}
		}
	}
	return 0
}

func (r *PortAllocationReconciler) lookupContainerPortForSGById(pa *nlbpoolv1alpha1.PortAllocation, sgId string) int32 {
	for _, sg := range pa.Spec.ServerGroups {
		if sg.ServerGroupId == sgId {
			return r.lookupContainerPortForSG(pa, sg.Name)
		}
	}
	return 0
}

func (r *PortAllocationReconciler) buildExternalAddresses(pa *nlbpoolv1alpha1.PortAllocation) []nlbpoolv1alpha1.ExternalAddress {
	out := make([]nlbpoolv1alpha1.ExternalAddress, 0, len(pa.Spec.Endpoints))
	for _, ep := range pa.Spec.Endpoints {
		addr := nlbpoolv1alpha1.ExternalAddress{
			Lane: ep.Lane,
			IP:   ep.EIP,
		}
		for _, p := range ep.Ports {
			addr.Ports = append(addr.Ports, nlbpoolv1alpha1.ExternalAddressPort{
				Name:     p.Name,
				Port:     p.ListenerPort,
				Protocol: p.Protocol,
			})
		}
		out = append(out, addr)
	}
	return out
}

// clearPodClaim drops AnnotationPAClaim from the previously bound Pod, when
// the annotation still points at this PA. Best-effort: errors are swallowed.
func (r *PortAllocationReconciler) clearPodClaim(ctx context.Context, pa *nlbpoolv1alpha1.PortAllocation) {
	if pa.Spec.BoundPod == "" {
		return
	}
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: pa.Spec.BoundPod, Namespace: pa.Namespace}, pod); err != nil {
		return
	}
	if pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] != pa.Name {
		return
	}
	patch := client.MergeFrom(pod.DeepCopy())
	delete(pod.Annotations, nlbpoolv1alpha1.AnnotationPAClaim)
	_ = r.Patch(ctx, pod, patch)
}

func (r *PortAllocationReconciler) eventf(obj runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(obj, eventtype, reason, messageFmt, args...)
}

// acquireNLBSlot tries to acquire the per-NLB semaphore (non-blocking).
// Returns true if acquired, false if another goroutine holds it.
func (r *PortAllocationReconciler) acquireNLBSlot(nlbId string) bool {
	v, _ := r.nlbSemaphores.LoadOrStore(nlbId, make(chan struct{}, 1))
	select {
	case v.(chan struct{}) <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseNLBSlot releases the per-NLB semaphore.
func (r *PortAllocationReconciler) releaseNLBSlot(nlbId string) {
	v, ok := r.nlbSemaphores.Load(nlbId)
	if ok {
		<-v.(chan struct{})
	}
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

func isThrottlingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Throttling") ||
		strings.Contains(msg, "RequestLimitExceeded") ||
		strings.Contains(msg, "ServiceUnavailable")
}

func isIncorrectStatusError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "IncorrectStatus")
}

func isConflictPortError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Conflict.Port")
}

func isDuplicatedServerError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "DuplicatedParam.Server") ||
		strings.Contains(msg, "ResourceAlreadyExist.Server")
}

func isServerNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ServerNotFound") ||
		strings.Contains(msg, "InvalidParam.Server") ||
		strings.Contains(msg, "ResourceNotFound.Server") ||
		strings.Contains(msg, "ResourceNotFound.BackendServer")
}

// ---------------------------------------------------------------------------
// Pod -> PortAllocation mapping
// ---------------------------------------------------------------------------

// SetupWithManager wires the reconciler into the manager.
func (r *PortAllocationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbpoolv1alpha1.PortAllocation{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.podToPortAllocation),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: 100}).
		Complete(r)
}

// podToPortAllocation translates Pod events to PA reconcile requests.
//
// This function is a pure router — it does NO allocation. Allocation is
// handled exclusively by handleAvailable using PA CAS + Pod CAS dual
// protection.
//
//  1. Pod has AnnotationPAClaim -> enqueue that PA.
//  2. Pod has no claim -> enqueue all Available PAs in the pool so they
//     compete to claim the Pod via CAS.
func (r *PortAllocationReconciler) podToPortAllocation(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil {
		return nil
	}

	poolName := pod.Annotations[nlbpoolv1alpha1.AnnotationNLBPoolName]
	if poolName == "" {
		return nil
	}

	// Already has a claim -> enqueue the corresponding PA.
	if claim := pod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]; claim != "" {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{
			Name: claim, Namespace: pod.Namespace,
		}}}
	}

	// No claim -> enqueue Available PAs in this pool so they can compete.
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := r.List(ctx, paList,
		client.InNamespace(pod.Namespace),
		client.MatchingLabels{
			nlbpoolv1alpha1.LabelPool:  poolName,
			nlbpoolv1alpha1.LabelPhase: string(nlbpoolv1alpha1.PortAllocationAvailable),
		}); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, pa := range paList.Items {
		if pa.Spec.BoundPod == "" {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace},
			})
		}
	}
	return requests
}
