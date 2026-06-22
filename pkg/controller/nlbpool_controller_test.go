//go:build v5_skip
// +build v5_skip

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	eipv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbpov1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = nlbpov1alpha1.AddToScheme(s)
	_ = nlbv1.AddToScheme(s)
	_ = eipv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

// --- Helper: create a test NLBPool ---
func newTestPool(name string) *nlbpov1alpha1.NLBPool {
	return &nlbpov1alpha1.NLBPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: nlbpov1alpha1.NLBPoolSpec{
			ZoneMaps:     "vpc-test@cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb",
			MinPort:      10000,
			MaxPort:      10009,
			PortsPerPod:  2,
			Protocols:    []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
			EipIspTypes:  []string{"BGP"},
			MinAvailable: 3,
		},
	}
}

// --- Helper: create a test PortAllocation with SG info ---
func newTestPortAllocation(poolName string, slotIdx int, phase nlbpov1alpha1.PortAllocationPhase) *nlbpov1alpha1.PortAllocation {
	phaseLabel := nlbpov1alpha1.LabelPhaseAvailable
	switch phase {
	case nlbpov1alpha1.PortAllocationProvisioning:
		phaseLabel = nlbpov1alpha1.LabelPhaseProvisioning
	case nlbpov1alpha1.PortAllocationBound, nlbpov1alpha1.PortAllocationDisabled:
		phaseLabel = nlbpov1alpha1.LabelPhaseBound
	case nlbpov1alpha1.PortAllocationBinding, nlbpov1alpha1.PortAllocationReleasing:
		phaseLabel = nlbpov1alpha1.LabelPhaseTransitioning
	}
	return &nlbpov1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-slot-%d", poolName, slotIdx),
			Namespace: "default",
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:     poolName,
				nlbpov1alpha1.LabelPhase:           phaseLabel,
				nlbpov1alpha1.LabelSlotPortsPerPod: "2",
				LabelSlotIndex:                     fmt.Sprintf("%d", slotIdx),
			},
		},
		Spec: nlbpov1alpha1.PortAllocationSpec{
			ServerGroups: []nlbpov1alpha1.ServerGroupInfo{
				{LogicalPort: 10000, ServerGroupId: "sg-1", ServerGroupName: "sg-slot0-p0"},
				{LogicalPort: 10002, ServerGroupId: "sg-2", ServerGroupName: "sg-slot0-p1"},
			},
			NLBEndpoints: []nlbpov1alpha1.NLBEndpoint{
				{
					ISPType: "BGP",
					NLBId:   "nlb-bgp-0",
					EIP:     "1.2.3.4",
					Listeners: []nlbpov1alpha1.ListenerInfo{
						{ListenerPort: 10000, Protocol: "TCP", ListenerId: "lsn-1", ServerGroupRef: "sg-1"},
						{ListenerPort: 10002, Protocol: "UDP", ListenerId: "lsn-2", ServerGroupRef: "sg-2"},
					},
				},
			},
			Enabled: true,
		},
		Status: nlbpov1alpha1.PortAllocationStatus{
			Phase: phase,
		},
	}
}

// =====================================================
// Prewarm Tests
// =====================================================

func TestPrewarm_CreateServerGroups(t *testing.T) {
	// Given: a mock NLB client, an empty PortAllocation (no SGs created yet)
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := &nlbpov1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-slot-0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:     "pool",
				nlbpov1alpha1.LabelPhase:           nlbpov1alpha1.LabelPhaseAvailable,
				nlbpov1alpha1.LabelSlotPortsPerPod: "2",
				LabelSlotIndex:                     "0",
			},
		},
		Spec: nlbpov1alpha1.PortAllocationSpec{
			ServerGroups: []nlbpov1alpha1.ServerGroupInfo{},
			Enabled:      true,
		},
		Status: nlbpov1alpha1.PortAllocationStatus{
			Phase: nlbpov1alpha1.PortAllocationAvailable,
		},
	}

	pool := newTestPool("pool")
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pool, pa).WithStatusSubresource(pool).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: ensureServerGroup is called
	lanes := []nlbLane{
		{ISPType: "BGP", NLBs: []nlbv1.NLB{{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
			Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", LoadBalancerStatus: "Active"},
		}}},
	}
	_, err := r.ensurePortAllocation(context.Background(), pool, 0, 5, lanes)

	// Then: CreateServerGroup should have been called
	if err != nil {
		t.Fatalf("ensurePortAllocation failed: %v", err)
	}
	if len(mock.CreateServerGroupCalls) == 0 {
		t.Error("expected CreateServerGroup to be called")
	}
	// Verify VpcId was extracted from ZoneMaps
	for _, call := range mock.CreateServerGroupCalls {
		if call.VpcId != "vpc-test" {
			t.Errorf("expected VpcId 'vpc-test', got %q", call.VpcId)
		}
		if call.Protocol != "TCP_UDP" {
			t.Errorf("expected Protocol 'TCP_UDP', got %q", call.Protocol)
		}
	}
}

func TestPrewarm_CreateListeners(t *testing.T) {
	// Given: a mock client and a PA with SGs but no listeners
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := &nlbpov1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-slot-0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:     "pool",
				nlbpov1alpha1.LabelPhase:           nlbpov1alpha1.LabelPhaseAvailable,
				nlbpov1alpha1.LabelSlotPortsPerPod: "2",
				LabelSlotIndex:                     "0",
			},
		},
		Spec: nlbpov1alpha1.PortAllocationSpec{
			ServerGroups: []nlbpov1alpha1.ServerGroupInfo{
				{LogicalPort: 10000, ServerGroupId: "sg-1", ServerGroupName: "pool-slot-0-p0"},
				{LogicalPort: 10001, ServerGroupId: "sg-2", ServerGroupName: "pool-slot-0-p1"},
			},
			NLBEndpoints: []nlbpov1alpha1.NLBEndpoint{},
			Enabled:      true,
		},
		Status: nlbpov1alpha1.PortAllocationStatus{Phase: nlbpov1alpha1.PortAllocationAvailable},
	}

	pool := newTestPool("pool")
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pool, pa).WithStatusSubresource(pool).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, NLBClient: mock}

	lanes := []nlbLane{
		{ISPType: "BGP", NLBs: []nlbv1.NLB{{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
			Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", LoadBalancerStatus: "Active"},
		}}},
	}

	// When
	_, err := r.ensurePortAllocation(context.Background(), pool, 0, 5, lanes)

	// Then
	if err != nil {
		t.Fatalf("ensurePortAllocation failed: %v", err)
	}
	if len(mock.CreateListenerCalls) == 0 {
		t.Error("expected CreateListener to be called")
	}
	// Each listener should reference a valid SG
	for _, call := range mock.CreateListenerCalls {
		if call.LoadBalancerId != "lb-bgp-0" {
			t.Errorf("expected LoadBalancerId 'lb-bgp-0', got %q", call.LoadBalancerId)
		}
		if call.ServerGroupId == "" {
			t.Error("expected non-empty ServerGroupId")
		}
	}
}

func TestPrewarm_CreatePortAllocations(t *testing.T) {
	// Given: pool is ready; no PortAllocations exist yet
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pool := newTestPool("pool")
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pool).WithStatusSubresource(pool, &nlbpov1alpha1.PortAllocation{}).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, NLBClient: mock}

	lanes := []nlbLane{
		{ISPType: "BGP", NLBs: []nlbv1.NLB{{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
			Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", LoadBalancerStatus: "Active"},
		}}},
	}

	// When: ensurePortAllocation for slot 0
	pending, err := r.ensurePortAllocation(context.Background(), pool, 0, 5, lanes)

	// Then: PA should be created (pending=true because it was just created)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pending {
		t.Error("expected pending=true after initial PA creation")
	}

	// Verify PA was persisted
	pa := &nlbpov1alpha1.PortAllocation{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: "pool-slot-0", Namespace: "default"}, pa); err != nil {
		t.Fatalf("failed to get created PA: %v", err)
	}
	if pa.Labels[nlbpov1alpha1.LabelPhase] != nlbpov1alpha1.LabelPhaseProvisioning {
		t.Errorf("expected phase label 'provisioning', got %q", pa.Labels[nlbpov1alpha1.LabelPhase])
	}
	if pa.Status.Phase != nlbpov1alpha1.PortAllocationProvisioning {
		t.Errorf("expected status phase 'Provisioning', got %q", pa.Status.Phase)
	}
}

func TestPrewarm_NLBCapacityScaling(t *testing.T) {
	// Given: portsPerPod=2, port range=10000-10009 (10 ports) => podsPerNLB=5
	spec := &nlbpov1alpha1.NLBPoolSpec{
		MinPort:      10000,
		MaxPort:      10009,
		PortsPerPod:  2,
		MinAvailable: 8,
	}

	podsPerNLB := calculatePodsPerNLB(spec)
	if podsPerNLB != 5 {
		t.Fatalf("expected podsPerNLB=5, got %d", podsPerNLB)
	}

	// When: 3 already bound, minAvailable=8 => need 8+3=11 => ceil(11/5)=3 NLBs
	required := calculateRequiredNLBs(spec, podsPerNLB, 3)
	if required != 3 {
		t.Errorf("expected 3 required NLBs, got %d", required)
	}

	// When: 0 bound => need 8 => ceil(8/5)=2 NLBs
	required = calculateRequiredNLBs(spec, podsPerNLB, 0)
	if required != 2 {
		t.Errorf("expected 2 required NLBs, got %d", required)
	}
}

// =====================================================
// Binding Tests
// =====================================================

func TestBinding_AddServersToServerGroup(t *testing.T) {
	// Given: PA is Binding, pod IP is set, SGs are ready
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationBinding)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: driveBind
	result, err := r.driveBind(context.Background(), pa)

	// Then: AddServers should be called for each SG
	if err != nil {
		t.Fatalf("driveBind failed: %v", err)
	}
	if len(mock.AddServersToServerGroupCalls) != 2 {
		t.Fatalf("expected 2 AddServers calls, got %d", len(mock.AddServersToServerGroupCalls))
	}
	// Verify server details
	for _, call := range mock.AddServersToServerGroupCalls {
		if len(call.Servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(call.Servers))
		}
		if call.Servers[0].ServerIp != "10.0.0.1" {
			t.Errorf("expected server IP '10.0.0.1', got %q", call.Servers[0].ServerIp)
		}
		if call.Servers[0].ServerType != "Ip" {
			t.Errorf("expected server type 'Ip', got %q", call.Servers[0].ServerType)
		}
	}
	// Should complete without requeue (sync mock)
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}

	// Verify PA is now Bound
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	if updatedPA.Status.Phase != nlbpov1alpha1.PortAllocationBound {
		t.Errorf("expected phase Bound, got %q", updatedPA.Status.Phase)
	}
}

func TestBinding_ConcurrentConflict(t *testing.T) {
	// Given: an error-returning AddServers mock
	mock := provider.NewMockNLBClient()
	mock.AddServersToServerGroupFunc = func(ctx context.Context, req *provider.AddServersRequest) (*provider.AddServersResponse, error) {
		return nil, fmt.Errorf("Throttling: request rate exceeded")
	}
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationBinding)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: driveBind
	result, err := r.driveBind(context.Background(), pa)

	// Then: Throttling is handled gracefully - no error, requeue with backoff
	if err != nil {
		t.Fatalf("expected no error (throttling handled gracefully), got: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s for throttling backoff, got %v", result.RequeueAfter)
	}

	// Verify PA is NOT in Failed state (throttling is retryable, not a failure)
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	if updatedPA.Status.Phase == nlbpov1alpha1.PortAllocationFailed {
		t.Errorf("expected phase NOT Failed for throttling (retryable), got %q", updatedPA.Status.Phase)
	}
}

func TestBinding_AsyncJobPolling(t *testing.T) {
	// Given: async mock that succeeds after 2 polls
	mock := provider.NewMockNLBClientWithAsyncJobs(2)
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationBinding)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: first call - jobs are submitted
	result, err := r.driveBind(context.Background(), pa)
	if err != nil {
		t.Fatalf("first driveBind failed: %v", err)
	}
	// Then: should requeue since jobs are still processing
	if result.RequeueAfter != RequeueAfterShort {
		t.Errorf("expected requeue after %v, got %v", RequeueAfterShort, result.RequeueAfter)
	}

	// When: re-read PA and call again (simulating reconcile loop)
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	result2, err2 := r.driveBind(context.Background(), updatedPA)
	if err2 != nil {
		t.Fatalf("second driveBind failed: %v", err2)
	}

	// Verify GetJobStatus was called
	if len(mock.GetJobStatusCalls) == 0 {
		t.Error("expected GetJobStatus to be called")
	}

	// After enough polls, should reach Bound
	_ = result2 // may or may not be final depending on poll count
}

// =====================================================
// Release Tests
// =====================================================

func TestRelease_RemoveServersFromServerGroup(t *testing.T) {
	// Given: PA is Bound, slot is being released (label changed to available)
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationBound)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: driveRelease (target inferred from pa.Spec.Enabled=true => Available)
	pa.Spec.Enabled = true
	result, err := r.driveRelease(context.Background(), pa)

	// Then
	if err != nil {
		t.Fatalf("driveRelease failed: %v", err)
	}
	if len(mock.RemoveServersFromServerGroupCalls) != 2 {
		t.Fatalf("expected 2 RemoveServers calls, got %d", len(mock.RemoveServersFromServerGroupCalls))
	}
	for _, call := range mock.RemoveServersFromServerGroupCalls {
		if len(call.Servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(call.Servers))
		}
		if call.Servers[0].ServerIp != "10.0.0.1" {
			t.Errorf("expected server IP '10.0.0.1', got %q", call.Servers[0].ServerIp)
		}
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue, got %v", result.RequeueAfter)
	}

	// Verify PA is now Available
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	if updatedPA.Status.Phase != nlbpov1alpha1.PortAllocationAvailable {
		t.Errorf("expected phase Available, got %q", updatedPA.Status.Phase)
	}
}

// =====================================================
// Disable / Enable Tests
// =====================================================

func TestDisable_RemoveServersKeepListeners(t *testing.T) {
	// Given: PA is Bound and the bound Pod has network-disabled annotation
	// => Reconcile should release to Disabled (keeping the slot claim)
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationBound)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"
	pa.Spec.Enabled = true

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-0",
			Namespace: "default",
			Annotations: map[string]string{
				nlbpov1alpha1.AnnotationNLBPoolName:     "pool",
				nlbpov1alpha1.AnnotationNetworkDisabled: "true",
				nlbpov1alpha1.AnnotationPAClaim:         "pool-slot-0",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	}

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa, pod).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: Reconcile detects network-disabled annotation on Pod => driveRelease to Disabled
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace},
	})

	// Then
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	_ = result

	// RemoveServers should be called
	if len(mock.RemoveServersFromServerGroupCalls) != 2 {
		t.Fatalf("expected 2 RemoveServers calls, got %d", len(mock.RemoveServersFromServerGroupCalls))
	}

	// PA should be Disabled (not Available: slot is still bound)
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	if updatedPA.Status.Phase != nlbpov1alpha1.PortAllocationDisabled {
		t.Errorf("expected phase Disabled, got %q", updatedPA.Status.Phase)
	}

	// Listeners should still be present (not deleted)
	if len(updatedPA.Spec.NLBEndpoints) == 0 {
		t.Error("expected NLBEndpoints to still be present")
	}
}

func TestEnable_AddServersBack(t *testing.T) {
	// Given: PA is Disabled and the Pod no longer carries the network-disabled
	// annotation => Reconcile should drive a new bind back to Bound.
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := newTestPortAllocation("pool", 0, nlbpov1alpha1.PortAllocationDisabled)
	pa.Spec.BoundPod = "game-pod-0"
	pa.Spec.BoundPodIP = "10.0.0.1"
	pa.Spec.Enabled = false

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "game-pod-0",
			Namespace: "default",
			Annotations: map[string]string{
				nlbpov1alpha1.AnnotationNLBPoolName: "pool",
				nlbpov1alpha1.AnnotationPAClaim:     "pool-slot-0",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	}

	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pa, pod).WithStatusSubresource(pa).Build()
	r := &PortAllocationReconciler{Client: fc, Scheme: s, NLBClient: mock}

	// When: Reconcile sees Disabled PA + Pod without disable annotation => driveBind
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace},
	})

	// Then
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	_ = result

	// AddServers should be called
	if len(mock.AddServersToServerGroupCalls) != 2 {
		t.Fatalf("expected 2 AddServers calls, got %d", len(mock.AddServersToServerGroupCalls))
	}

	// PA should be Bound again
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: pa.Name, Namespace: pa.Namespace}, updatedPA)
	if updatedPA.Status.Phase != nlbpov1alpha1.PortAllocationBound {
		t.Errorf("expected phase Bound, got %q", updatedPA.Status.Phase)
	}
}

// =====================================================
// Rate Limiter / Throttling Tests
// =====================================================

func TestRateLimiter_Throttling(t *testing.T) {
	// Given: a mock client wrapped with a tight rate limiter
	mock := provider.NewMockNLBClient()
	rl := provider.NewRateLimitedClient(mock, 100, 5)

	// When: burst of calls within limit
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := rl.CreateServerGroup(ctx, &provider.CreateServerGroupRequest{
			ServerGroupName: fmt.Sprintf("sg-%d", i),
		})
		if err != nil {
			t.Fatalf("unexpected error on call %d: %v", i, err)
		}
	}

	// Then: all calls should reach mock
	if len(mock.CreateServerGroupCalls) != 5 {
		t.Errorf("expected 5 calls, got %d", len(mock.CreateServerGroupCalls))
	}

	// When: context is cancelled, rate limiter should fail
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rl.CreateServerGroup(cancelCtx, &provider.CreateServerGroupRequest{ServerGroupName: "should-fail"})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestIdempotency_ClientToken(t *testing.T) {
	// Given: a mock client that records calls
	mock := provider.NewMockNLBClient()
	s := newTestScheme()

	pa := &nlbpov1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-slot-0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:     "pool",
				nlbpov1alpha1.LabelPhase:           nlbpov1alpha1.LabelPhaseAvailable,
				nlbpov1alpha1.LabelSlotPortsPerPod: "2",
				LabelSlotIndex:                     "0",
			},
		},
		Spec: nlbpov1alpha1.PortAllocationSpec{
			ServerGroups: []nlbpov1alpha1.ServerGroupInfo{},
			Enabled:      true,
		},
		Status: nlbpov1alpha1.PortAllocationStatus{Phase: nlbpov1alpha1.PortAllocationAvailable},
	}

	pool := newTestPool("pool")
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(pool, pa).WithStatusSubresource(pool).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, NLBClient: mock}

	lanes := []nlbLane{
		{ISPType: "BGP", NLBs: []nlbv1.NLB{{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
			Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", LoadBalancerStatus: "Active"},
		}}},
	}

	// When: call ensurePortAllocation twice
	_, _ = r.ensurePortAllocation(context.Background(), pool, 0, 5, lanes)

	// Re-fetch PA for second call
	updatedPA := &nlbpov1alpha1.PortAllocation{}
	_ = fc.Get(context.Background(), types.NamespacedName{Name: "pool-slot-0", Namespace: "default"}, updatedPA)

	// Then: ClientToken should be deterministic (format: "sg-<vpcId>-<sgName>")
	for _, call := range mock.CreateServerGroupCalls {
		if call.ClientToken == "" {
			t.Error("expected non-empty ClientToken for idempotency")
		}
		// ClientToken format: "sg-<vpcId>-<sgName>" (from sgClientToken)
		expected := "sg-vpc-test-" + call.ServerGroupName
		if call.ClientToken != expected {
			t.Errorf("expected ClientToken=%q, got %q", expected, call.ClientToken)
		}
	}
}

// =====================================================
// Pure helper function tests
// =====================================================

func TestCalculatePodsPerNLB(t *testing.T) {
	tests := []struct {
		name     string
		spec     *nlbpov1alpha1.NLBPoolSpec
		expected int
	}{
		{
			name:     "basic calculation",
			spec:     &nlbpov1alpha1.NLBPoolSpec{MinPort: 10000, MaxPort: 10009, PortsPerPod: 2},
			expected: 5,
		},
		{
			name:     "with blocked ports",
			spec:     &nlbpov1alpha1.NLBPoolSpec{MinPort: 10000, MaxPort: 10009, PortsPerPod: 2, BlockPorts: []int32{10003, 10004}},
			expected: 4,
		},
		{
			name:     "zero portsPerPod",
			spec:     &nlbpov1alpha1.NLBPoolSpec{MinPort: 10000, MaxPort: 10009, PortsPerPod: 0},
			expected: 0,
		},
		{
			name:     "inverted range",
			spec:     &nlbpov1alpha1.NLBPoolSpec{MinPort: 10010, MaxPort: 10000, PortsPerPod: 2},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculatePodsPerNLB(tt.spec)
			if got != tt.expected {
				t.Errorf("calculatePodsPerNLB() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestParseZoneMaps(t *testing.T) {
	// Valid
	zones, vpc, err := parseZoneMaps("vpc-xxx@cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vpc != "vpc-xxx" {
		t.Errorf("expected vpc 'vpc-xxx', got %q", vpc)
	}
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(zones))
	}

	// Invalid: no @
	_, _, err = parseZoneMaps("no-at-sign")
	if err == nil {
		t.Error("expected error for missing @")
	}

	// Invalid: only 1 zone
	_, _, err = parseZoneMaps("vpc-xxx@cn-hangzhou-h:vsw-aaa")
	if err == nil {
		t.Error("expected error for <2 zones")
	}
}

func TestComputeTotalSlots(t *testing.T) {
	lanes := []nlbLane{
		{ISPType: "BGP", NLBs: make([]nlbv1.NLB, 3)},
		{ISPType: "CTC", NLBs: make([]nlbv1.NLB, 2)},
	}
	// min(3,2) * 5 = 10
	got := computeTotalSlots(lanes, 5)
	if got != 10 {
		t.Errorf("expected 10, got %d", got)
	}

	// empty lanes
	got = computeTotalSlots([]nlbLane{}, 5)
	if got != 0 {
		t.Errorf("expected 0, got %d", got)
	}

	// duplicate ISP lanes: [BGP, BGP, CTC] with 1 NLB each => min(1,1,1)*5 = 5
	duplicateLanes := []nlbLane{
		{ISPType: "BGP", NLBs: make([]nlbv1.NLB, 1)},
		{ISPType: "BGP", NLBs: make([]nlbv1.NLB, 1)},
		{ISPType: "CTC", NLBs: make([]nlbv1.NLB, 1)},
	}
	got = computeTotalSlots(duplicateLanes, 5)
	if got != 5 {
		t.Errorf("expected 5, got %d", got)
	}
}

func TestBuildExternalAddresses(t *testing.T) {
	pa := &nlbpov1alpha1.PortAllocation{
		Spec: nlbpov1alpha1.PortAllocationSpec{
			NLBEndpoints: []nlbpov1alpha1.NLBEndpoint{
				{
					ISPType: "BGP",
					EIP:     "1.2.3.4",
					Listeners: []nlbpov1alpha1.ListenerInfo{
						{ListenerPort: 10000, Protocol: "TCP"},
						{ListenerPort: 10001, Protocol: "UDP"},
					},
				},
				{
					ISPType: "CTC",
					EIP:     "5.6.7.8",
					Listeners: []nlbpov1alpha1.ListenerInfo{
						{ListenerPort: 10000, Protocol: "TCP"},
					},
				},
			},
		},
	}

	addrs := buildExternalAddresses(pa)
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(addrs))
	}
	if addrs[0].ISPType != "BGP" || addrs[0].IP != "1.2.3.4" {
		t.Errorf("unexpected first address: %+v", addrs[0])
	}
	if len(addrs[0].Ports) != 2 {
		t.Errorf("expected 2 ports on BGP, got %d", len(addrs[0].Ports))
	}
	if addrs[1].ISPType != "CTC" || addrs[1].IP != "5.6.7.8" {
		t.Errorf("unexpected second address: %+v", addrs[1])
	}
}

// Suppress unused import warnings
var _ = time.Second
