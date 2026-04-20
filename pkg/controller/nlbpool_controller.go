package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	eipv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/nlboperator/v1"
	nlbpov1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// IntranetEIPType marks intranet EIP type
	IntranetEIPType = "intranet"

	// DefaultEIPBandwidth is the default EIP bandwidth in Mbps
	DefaultEIPBandwidth = "5"

	// RequeueAfterPeriod is the period for periodic reconciliation
	RequeueAfterPeriod = 30 * time.Second
)

// ZoneInfo holds parsed zone mapping info
type ZoneInfo struct {
	ZoneId    string
	VSwitchId string
}

// NLBPoolReconciler reconciles a NLBPool object
type NLBPoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nlbpool.kruise.io,resources=nlbpools,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nlbpool.kruise.io,resources=nlbpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nlboperator.alibabacloud.com,resources=nlbs,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=eip.alibabacloud.com,resources=eips,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconciliation loop for NLBPool
func (r *NLBPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Get NLBPool CR
	pool := &nlbpov1alpha1.NLBPool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("NLBPool resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get NLBPool")
		return ctrl.Result{}, err
	}

	// 2. Calculate pods per NLB
	podsPerNLB := calculatePodsPerNLB(&pool.Spec)
	if podsPerNLB <= 0 {
		logger.Info("Invalid configuration: podsPerNLB <= 0, check port range and PortsPerPod",
			"minPort", pool.Spec.MinPort, "maxPort", pool.Spec.MaxPort,
			"blockPorts", pool.Spec.BlockPorts, "portsPerPod", pool.Spec.PortsPerPod)
		// Update status to Failed
		pool.Status.Phase = nlbpov1alpha1.NLBPoolPhaseFailed
		_ = r.Status().Update(ctx, pool)
		return ctrl.Result{RequeueAfter: RequeueAfterPeriod}, nil
	}

	// 3. For each eipIspType, ensure NLB/EIP and Services
	for _, eipIspType := range pool.Spec.EipIspTypes {
		if err := r.ensurePrewarming(ctx, pool, eipIspType, podsPerNLB); err != nil {
			logger.Error(err, "Failed to ensure prewarming", "eipIspType", eipIspType)
			// Continue with other types, will retry on next reconciliation
		}
	}

	// 4. Update Status
	if err := r.updateStatus(ctx, pool); err != nil {
		logger.Error(err, "Failed to update NLBPool status")
		return ctrl.Result{}, err
	}

	// 5. Periodic requeue
	return ctrl.Result{RequeueAfter: RequeueAfterPeriod}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NLBPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nlbpov1alpha1.NLBPool{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// ensurePrewarming ensures NLB/EIP resources exist and Services are pre-warmed
func (r *NLBPoolReconciler) ensurePrewarming(ctx context.Context, pool *nlbpov1alpha1.NLBPool, eipIspType string, podsPerNLB int) error {
	logger := log.FromContext(ctx)

	// Calculate required NLBs based on current bound service count
	boundServices := r.countBoundServices(ctx, pool)
	requiredNLBs := calculateRequiredNLBs(&pool.Spec, podsPerNLB, boundServices)

	logger.Info("Ensuring prewarming",
		"eipIspType", eipIspType, "podsPerNLB", podsPerNLB,
		"requiredNLBs", requiredNLBs, "boundServices", boundServices)

	// List existing NLBs for this pool and eipIspType
	nlbList := &nlbv1.NLBList{}
	err := r.List(ctx, nlbList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			nlbpov1alpha1.LabelNLBPoolName:       pool.Name,
			nlbpov1alpha1.LabelNLBPoolEipIspType: eipIspType,
		})
	if err != nil {
		return fmt.Errorf("failed to list NLB CRs: %w", err)
	}

	existingCount := len(nlbList.Items)
	logger.Info("Current NLB count", "existing", existingCount, "required", requiredNLBs)

	// Create missing NLBs and EIPs
	for i := existingCount; i < requiredNLBs; i++ {
		logger.Info("Creating NLB instance", "index", i, "eipIspType", eipIspType)

		// Create EIPs first (for each zone)
		if err := r.ensureEIPsForNLB(ctx, pool, eipIspType, i); err != nil {
			logger.Error(err, "Failed to ensure EIPs for NLB", "index", i)
			continue
		}

		// Create NLB CR
		if err := r.createNLBCR(ctx, pool, eipIspType, i); err != nil {
			logger.Error(err, "Failed to create NLB CR", "index", i)
			continue
		}
	}

	// Re-list NLBs after creation (to include newly created ones)
	nlbList = &nlbv1.NLBList{}
	err = r.List(ctx, nlbList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			nlbpov1alpha1.LabelNLBPoolName:       pool.Name,
			nlbpov1alpha1.LabelNLBPoolEipIspType: eipIspType,
		})
	if err != nil {
		return fmt.Errorf("failed to re-list NLB CRs: %w", err)
	}

	// Prewarm Services for all NLBs
	if err := r.prewarmServices(ctx, pool, eipIspType, nlbList.Items, podsPerNLB); err != nil {
		logger.Error(err, "Failed to prewarm services")
		// Don't return error, allow retry on next reconciliation
	}

	return nil
}

// ensureEIPsForNLB creates EIP CRs for all zones of a given NLB
func (r *NLBPoolReconciler) ensureEIPsForNLB(ctx context.Context, pool *nlbpov1alpha1.NLBPool, eipIspType string, nlbIndex int) error {
	logger := log.FromContext(ctx)

	// Parse ZoneMaps
	zones, _, err := parseZoneMaps(pool.Spec.ZoneMaps)
	if err != nil {
		return fmt.Errorf("failed to parse zoneMaps: %w", err)
	}

	// Create EIP for each zone
	for zoneIdx := range zones {
		if err := r.ensureEIPCR(ctx, pool, eipIspType, nlbIndex, zoneIdx); err != nil {
			logger.Error(err, "Failed to ensure EIP CR",
				"nlbIndex", nlbIndex, "zoneIndex", zoneIdx)
			// Continue creating other EIPs
		}
	}

	return nil
}

// ensureEIPCR creates an EIP CR if it doesn't exist
func (r *NLBPoolReconciler) ensureEIPCR(ctx context.Context, pool *nlbpov1alpha1.NLBPool, eipIspType string, nlbIndex, zoneIndex int) error {
	logger := log.FromContext(ctx)

	eipName := fmt.Sprintf("%s-eip-%s-%d-z%d", pool.Name, strings.ToLower(eipIspType), nlbIndex, zoneIndex)

	// Check if EIP CR already exists
	existingEIP := &eipv1.EIP{}
	err := r.Get(ctx, types.NamespacedName{Name: eipName, Namespace: pool.Namespace}, existingEIP)
	if err == nil {
		// EIP already exists
		logger.Info("EIP CR already exists", "name", eipName,
			"allocationID", existingEIP.Status.AllocationID)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get EIP CR %s: %w", eipName, err)
	}

	// Determine InternetChargeType based on ISP type
	internetChargeType := "PayByTraffic"
	if eipIspType == "ChinaTelecom" || eipIspType == "ChinaMobile" || eipIspType == "ChinaUnicom" {
		internetChargeType = "PayByBandwidth"
	}

	// Create EIP CR
	eipCR := &eipv1.EIP{
		ObjectMeta: metav1.ObjectMeta{
			Name:      eipName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpov1alpha1.LabelEIPPoolName:       pool.Name,
				nlbpov1alpha1.LabelEIPPoolIndex:      fmt.Sprintf("%d-z%d", nlbIndex, zoneIndex),
				nlbpov1alpha1.LabelEIPPoolEipIspType: eipIspType,
			},
		},
		Spec: eipv1.EIPSpec{
			Name:                    eipName,
			Bandwidth:               DefaultEIPBandwidth,
			InternetChargeType:      internetChargeType,
			ISP:                     eipIspType,
			ReleaseStrategy:         "OnDelete",
			Description:             fmt.Sprintf("EIP for NLBPool %s, NLB index %d, zone %d", pool.Name, nlbIndex, zoneIndex),
			SecurityProtectionTypes: pool.Spec.SecurityProtectionTypes,
		},
	}

	// Set OwnerReference to NLBPool for automatic cleanup
	if err := ctrl.SetControllerReference(pool, eipCR, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference for EIP: %w", err)
	}

	if err := r.Create(ctx, eipCR); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create EIP CR %s: %w", eipName, err)
		}
		logger.Info("EIP CR already exists (concurrent creation)", "name", eipName)
	} else {
		logger.Info("Successfully created EIP CR", "name", eipName, "ispType", eipIspType)
	}

	return nil
}

// createNLBCR creates an NLB CR if it doesn't exist
func (r *NLBPoolReconciler) createNLBCR(ctx context.Context, pool *nlbpov1alpha1.NLBPool, eipIspType string, index int) error {
	logger := log.FromContext(ctx)

	nlbName := fmt.Sprintf("%s-%s-%d", pool.Name, strings.ToLower(eipIspType), index)

	// Check if NLB CR already exists
	existingNLB := &nlbv1.NLB{}
	err := r.Get(ctx, types.NamespacedName{Name: nlbName, Namespace: pool.Namespace}, existingNLB)
	if err == nil {
		logger.Info("NLB CR already exists", "name", nlbName,
			"loadBalancerId", existingNLB.Status.LoadBalancerId)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get NLB CR %s: %w", nlbName, err)
	}

	// Parse ZoneMaps
	zones, vpcId, err := parseZoneMaps(pool.Spec.ZoneMaps)
	if err != nil {
		return fmt.Errorf("failed to parse zoneMaps: %w", err)
	}

	// Query EIP AllocationIDs for each zone
	eipAllocationIDs := make([]string, 0, len(zones))
	allEIPsReady := true

	for zoneIdx := range zones {
		eipName := fmt.Sprintf("%s-eip-%s-%d-z%d", pool.Name, strings.ToLower(eipIspType), index, zoneIdx)
		eipCR := &eipv1.EIP{}
		err := r.Get(ctx, types.NamespacedName{Name: eipName, Namespace: pool.Namespace}, eipCR)
		if err == nil && eipCR.Status.AllocationID != "" {
			eipAllocationIDs = append(eipAllocationIDs, eipCR.Status.AllocationID)
			logger.Info("Found EIP with allocationID", "eipName", eipName, "allocationID", eipCR.Status.AllocationID)
		} else {
			allEIPsReady = false
			if err != nil {
				logger.Info("EIP not found yet, cannot create NLB", "eipName", eipName)
			} else {
				logger.Info("EIP exists but AllocationID not ready yet", "eipName", eipName)
			}
			break
		}
	}

	if !allEIPsReady {
		return fmt.Errorf("waiting for EIP AllocationIDs: all EIPs must be ready before NLB creation")
	}

	// Build ZoneMappings with EIP AllocationIDs
	nlbZoneMappings := make([]nlbv1.ZoneMapping, 0, len(zones))
	for i, zone := range zones {
		zm := nlbv1.ZoneMapping{
			ZoneId:    zone.ZoneId,
			VSwitchId: zone.VSwitchId,
		}
		if i < len(eipAllocationIDs) {
			zm.AllocationId = eipAllocationIDs[i]
		}
		nlbZoneMappings = append(nlbZoneMappings, zm)
	}

	// Determine address type
	addressType := "Internet"
	if strings.Contains(strings.ToLower(eipIspType), IntranetEIPType) {
		addressType = "Intranet"
	}

	// Create NLB CR
	nlbCR := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nlbName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:       pool.Name,
				nlbpov1alpha1.LabelNLBPoolIndex:      strconv.Itoa(index),
				nlbpov1alpha1.LabelNLBPoolEipIspType: eipIspType,
			},
		},
		Spec: nlbv1.NLBSpec{
			LoadBalancerName: nlbName,
			AddressType:      addressType,
			AddressIpVersion: "ipv4",
			VpcId:            vpcId,
			ZoneMappings:     nlbZoneMappings,
		},
	}

	// Set OwnerReference to NLBPool for automatic cleanup
	if err := ctrl.SetControllerReference(pool, nlbCR, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference for NLB: %w", err)
	}

	if err := r.Create(ctx, nlbCR); err != nil {
		if !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create NLB CR %s: %w", nlbName, err)
		}
		logger.Info("NLB CR already exists (concurrent creation)", "name", nlbName)
	} else {
		logger.Info("Successfully created NLB CR", "name", nlbName,
			"vpcId", vpcId, "addressType", addressType, "zones", len(nlbZoneMappings))
	}

	return nil
}

// prewarmServices creates pre-warmed Services for each NLB
func (r *NLBPoolReconciler) prewarmServices(ctx context.Context, pool *nlbpov1alpha1.NLBPool, eipIspType string, nlbs []nlbv1.NLB, podsPerNLB int) error {
	logger := log.FromContext(ctx)

	createdCount := 0
	skippedCount := 0
	skippedNLBCount := 0

	for _, nlb := range nlbs {
		// Skip NLBs that are not ready yet
		if nlb.Status.LoadBalancerId == "" {
			logger.Info("NLB not ready yet, skipping service prewarming",
				"nlbName", nlb.Name)
			skippedNLBCount++
			continue
		}

		nlbId := nlb.Status.LoadBalancerId

		// Get NLB pool index from labels
		nlbPoolIndexStr, ok := nlb.Labels[nlbpov1alpha1.LabelNLBPoolIndex]
		if !ok {
			logger.Error(fmt.Errorf("missing pool index label"), "NLB missing label",
				"nlbName", nlb.Name, "label", nlbpov1alpha1.LabelNLBPoolIndex)
			continue
		}
		nlbPoolIndex, err := strconv.Atoi(nlbPoolIndexStr)
		if err != nil {
			logger.Error(err, "Invalid pool index label", "nlbName", nlb.Name, "value", nlbPoolIndexStr)
			continue
		}

		// Create Service for each pod slot
		for slotIdx := 0; slotIdx < podsPerNLB; slotIdx++ {
			svcName := fmt.Sprintf("nlbpool-%s-%d-%d-%s",
				pool.Name, nlbPoolIndex, slotIdx, strings.ToLower(eipIspType))

			// Check if Service already exists
			existingSvc := &corev1.Service{}
			err := r.Get(ctx, types.NamespacedName{Name: svcName, Namespace: pool.Namespace}, existingSvc)
			if err == nil {
				skippedCount++
				continue
			}
			if !errors.IsNotFound(err) {
				logger.Error(err, "Failed to get Service", "svcName", svcName)
				continue
			}

			// Calculate ports for this slot
			ports := calculatePortsForSlot(pool.Spec, slotIdx)

			// Build Service
			svc := r.constructService(pool, svcName, nlbId, eipIspType, nlbPoolIndex, slotIdx, ports)

			if err := r.Create(ctx, svc); err != nil {
				if !errors.IsAlreadyExists(err) {
					logger.Error(err, "Failed to create Service", "svcName", svcName)
					continue
				}
				skippedCount++
			} else {
				createdCount++
				logger.Info("Created pre-warmed Service", "svcName", svcName, "nlbId", nlbId)
			}
		}
	}

	logger.Info("Prewarm services completed",
		"eipIspType", eipIspType, "created", createdCount,
		"skipped", skippedCount, "nlbNotReady", skippedNLBCount)

	return nil
}

// constructService builds a Service object for prewarming
func (r *NLBPoolReconciler) constructService(pool *nlbpov1alpha1.NLBPool, svcName, nlbId, eipIspType string, nlbIndex, slotIdx int, ports []corev1.ServicePort) *corev1.Service {
	loadBalancerClass := "alibabacloud.com/nlb"

	svcAnnotations := map[string]string{
		nlbpov1alpha1.AnnotationSlbId:               nlbId,
		nlbpov1alpha1.AnnotationSlbListenerOverride: "true",
	}

	// Add health check annotations if configured
	if pool.Spec.HealthCheck != nil && pool.Spec.HealthCheck.Flag == "on" {
		addHealthCheckAnnotations(svcAnnotations, pool.Spec.HealthCheck)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   pool.Namespace,
			Annotations: svcAnnotations,
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:        pool.Name,
				nlbpov1alpha1.LabelSvcPoolStatus:      nlbpov1alpha1.SvcPoolStatusAvailable,
				nlbpov1alpha1.LabelSvcPoolPortsPerPod: strconv.Itoa(pool.Spec.PortsPerPod),
				nlbpov1alpha1.LabelSvcPoolProtocols:   protocolsToStrings(pool.Spec.Protocols),
				nlbpov1alpha1.LabelNLBPoolEipIspType:  eipIspType,
				nlbpov1alpha1.LabelNLBPoolIndex:       strconv.Itoa(nlbIndex),
				nlbpov1alpha1.LabelServiceProxyName:   "dummy",
			},
		},
		Spec: corev1.ServiceSpec{
			Type:                          corev1.ServiceTypeLoadBalancer,
			AllocateLoadBalancerNodePorts: ptr.To(false),
			ExternalTrafficPolicy:         pool.Spec.ExternalTrafficPolicy,
			LoadBalancerClass:             ptr.To(loadBalancerClass),
			Selector: map[string]string{
				nlbpov1alpha1.LabelNLBPoolPlaceholder: nlbpov1alpha1.PlaceholderValue,
			},
			Ports: ports,
		},
	}

	// Set OwnerReference to NLBPool for automatic cleanup
	_ = ctrl.SetControllerReference(pool, svc, r.Scheme)

	return svc
}

// addHealthCheckAnnotations adds health check annotations to a Service
func addHealthCheckAnnotations(annotations map[string]string, hc *nlbpov1alpha1.NLBHealthCheckConfig) {
	annotations[nlbpov1alpha1.AnnotationHealthCheckFlag] = hc.Flag
	if hc.Type != "" {
		annotations[nlbpov1alpha1.AnnotationHealthCheckType] = hc.Type
	}
	if hc.ConnectPort != "" {
		annotations[nlbpov1alpha1.AnnotationHealthCheckConnectPort] = hc.ConnectPort
	}
	if hc.ConnectTimeout != "" {
		annotations[nlbpov1alpha1.AnnotationHealthCheckConnectTimeout] = hc.ConnectTimeout
	}
	if hc.Interval != "" {
		annotations[nlbpov1alpha1.AnnotationHealthCheckInterval] = hc.Interval
	}
	if hc.HealthyThreshold != "" {
		annotations[nlbpov1alpha1.AnnotationHealthyThreshold] = hc.HealthyThreshold
	}
	if hc.UnhealthyThreshold != "" {
		annotations[nlbpov1alpha1.AnnotationUnhealthyThreshold] = hc.UnhealthyThreshold
	}
	if hc.Type == "http" {
		if hc.Domain != "" {
			annotations[nlbpov1alpha1.AnnotationHealthCheckDomain] = hc.Domain
		}
		if hc.Uri != "" {
			annotations[nlbpov1alpha1.AnnotationHealthCheckUri] = hc.Uri
		}
		if hc.Method != "" {
			annotations[nlbpov1alpha1.AnnotationHealthCheckMethod] = hc.Method
		}
	}
}

// updateStatus updates the NLBPool status
func (r *NLBPoolReconciler) updateStatus(ctx context.Context, pool *nlbpov1alpha1.NLBPool) error {
	logger := log.FromContext(ctx)

	// Count NLBs
	nlbList := &nlbv1.NLBList{}
	if err := r.List(ctx, nlbList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{nlbpov1alpha1.LabelNLBPoolName: pool.Name},
	); err != nil {
		return fmt.Errorf("failed to list NLB CRs: %w", err)
	}

	readyNLBs := 0
	for _, nlb := range nlbList.Items {
		if nlb.Status.LoadBalancerId != "" && nlb.Status.LoadBalancerStatus == "Active" {
			readyNLBs++
		}
	}

	// Count Services
	svcList := &corev1.ServiceList{}
	if err := r.List(ctx, svcList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{nlbpov1alpha1.LabelNLBPoolName: pool.Name},
	); err != nil {
		return fmt.Errorf("failed to list Services: %w", err)
	}

	available := 0
	bound := 0
	for _, svc := range svcList.Items {
		status := svc.Labels[nlbpov1alpha1.LabelSvcPoolStatus]
		switch status {
		case nlbpov1alpha1.SvcPoolStatusAvailable:
			available++
		case nlbpov1alpha1.SvcPoolStatusBound:
			bound++
		}
	}

	// Update status
	pool.Status.TotalNLBs = len(nlbList.Items)
	pool.Status.ReadyNLBs = readyNLBs
	pool.Status.TotalServices = len(svcList.Items)
	pool.Status.AvailableServices = available
	pool.Status.BoundServices = bound

	// Determine Phase
	if available > 0 {
		pool.Status.Phase = nlbpov1alpha1.NLBPoolPhaseReady
	} else if bound > 0 {
		pool.Status.Phase = nlbpov1alpha1.NLBPoolPhasePending
	} else {
		pool.Status.Phase = nlbpov1alpha1.NLBPoolPhasePending
	}

	if err := r.Status().Update(ctx, pool); err != nil {
		logger.Error(err, "Failed to update NLBPool status")
		return err
	}

	logger.Info("Updated NLBPool status",
		"totalNLBs", pool.Status.TotalNLBs, "readyNLBs", pool.Status.ReadyNLBs,
		"totalServices", pool.Status.TotalServices,
		"availableServices", pool.Status.AvailableServices,
		"boundServices", pool.Status.BoundServices,
		"phase", pool.Status.Phase)

	return nil
}

// countBoundServices counts the number of bound services for a pool
func (r *NLBPoolReconciler) countBoundServices(ctx context.Context, pool *nlbpov1alpha1.NLBPool) int {
	svcList := &corev1.ServiceList{}
	if err := r.List(ctx, svcList,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			nlbpov1alpha1.LabelNLBPoolName:   pool.Name,
			nlbpov1alpha1.LabelSvcPoolStatus: nlbpov1alpha1.SvcPoolStatusBound,
		},
	); err != nil {
		return 0
	}
	return len(svcList.Items)
}

// calculatePodsPerNLB calculates how many Pod slots one NLB can support
func calculatePodsPerNLB(spec *nlbpov1alpha1.NLBPoolSpec) int {
	lenRange := int(spec.MaxPort) - int(spec.MinPort) - len(spec.BlockPorts) + 1
	if lenRange <= 0 || spec.PortsPerPod == 0 {
		return 0
	}
	return lenRange / spec.PortsPerPod
}

// calculateRequiredNLBs calculates the number of NLBs needed
func calculateRequiredNLBs(spec *nlbpov1alpha1.NLBPoolSpec, podsPerNLB, boundServices int) int {
	if podsPerNLB <= 0 {
		return 0
	}
	// Need enough NLBs so that (totalNLBs * podsPerNLB - boundServices) >= MinAvailable
	needed := spec.MinAvailable + boundServices
	required := (needed + podsPerNLB - 1) / podsPerNLB
	if required < 1 {
		return 1
	}
	return required
}

// calculatePortsForSlot calculates the ServicePort list for a given slot index
func calculatePortsForSlot(spec nlbpov1alpha1.NLBPoolSpec, slotIdx int) []corev1.ServicePort {
	ports := make([]corev1.ServicePort, spec.PortsPerPod)
	basePort := spec.MinPort

	for i := 0; i < spec.PortsPerPod; i++ {
		portOffset := int32(slotIdx*spec.PortsPerPod + i)
		port := basePort + portOffset

		// Skip blocked ports
		for isBlockedPort(port, spec.BlockPorts) {
			portOffset++
			port = basePort + portOffset
		}

		ports[i] = corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", i),
			Port:       port,
			TargetPort: intstr.FromInt(int(port)),
			Protocol:   spec.Protocols[i%len(spec.Protocols)],
		}
	}
	return ports
}

// isBlockedPort checks if a port is in the blocked ports list
func isBlockedPort(port int32, blockPorts []int32) bool {
	for _, bp := range blockPorts {
		if bp == port {
			return true
		}
	}
	return false
}

// parseZoneMaps parses the ZoneMaps configuration
// Format: "vpc-xxx@zone1:vswitch1,zone2:vswitch2"
func parseZoneMaps(zoneMapsStr string) ([]ZoneInfo, string, error) {
	if zoneMapsStr == "" {
		return nil, "", fmt.Errorf("zoneMaps cannot be empty")
	}

	if !strings.Contains(zoneMapsStr, "@") {
		return nil, "", fmt.Errorf("zoneMaps must include VPC ID in format 'vpc-id@zone:vsw,...', got: %s", zoneMapsStr)
	}

	parts := strings.SplitN(zoneMapsStr, "@", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid zoneMaps format, expected 'vpc-id@zone:vsw,...', got: %s", zoneMapsStr)
	}

	vpcId := strings.TrimSpace(parts[0])
	if vpcId == "" {
		return nil, "", fmt.Errorf("VPC ID cannot be empty in zoneMaps")
	}

	zoneParts := strings.Split(parts[1], ",")
	zones := make([]ZoneInfo, 0, len(zoneParts))

	for _, zp := range zoneParts {
		kv := strings.SplitN(strings.TrimSpace(zp), ":", 2)
		if len(kv) != 2 {
			return nil, "", fmt.Errorf("invalid zoneMap format: %s, expected 'zoneId:vSwitchId'", zp)
		}

		zoneId := strings.TrimSpace(kv[0])
		vSwitchId := strings.TrimSpace(kv[1])

		if zoneId == "" || vSwitchId == "" {
			return nil, "", fmt.Errorf("zoneId and vSwitchId cannot be empty in: %s", zp)
		}

		zones = append(zones, ZoneInfo{
			ZoneId:    zoneId,
			VSwitchId: vSwitchId,
		})
	}

	if len(zones) < 2 {
		return nil, "", fmt.Errorf("at least 2 zone mappings are required, got %d", len(zones))
	}

	return zones, vpcId, nil
}

// protocolsToStrings converts a slice of Protocols to a dash-separated string
func protocolsToStrings(protocols []corev1.Protocol) string {
	strs := make([]string, len(protocols))
	for i, p := range protocols {
		strs[i] = string(p)
	}
	return strings.Join(strs, "-")
}

// listNLBsByPool lists NLB CRs for a given pool and eipIspType
func listNLBsByPool(ctx context.Context, c client.Client, namespace, poolName, eipIspType string) (*nlbv1.NLBList, error) {
	nlbList := &nlbv1.NLBList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		nlbpov1alpha1.LabelNLBPoolName:       poolName,
		nlbpov1alpha1.LabelNLBPoolEipIspType: eipIspType,
	})
	err := c.List(ctx, nlbList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: labelSelector},
	)
	return nlbList, err
}
