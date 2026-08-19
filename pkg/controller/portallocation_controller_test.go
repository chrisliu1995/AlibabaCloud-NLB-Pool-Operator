package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newBoundTestPA builds a v6 PortAllocation with cloud SG/Listener status
// filled in, in the given phase, bound to game-pod-0 / 10.0.0.1.
func newBoundTestPA(phase nlbpoolv1alpha1.PortAllocationPhase) *nlbpoolv1alpha1.PortAllocation {
	return &nlbpoolv1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-s0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool:  "pool",
				nlbpoolv1alpha1.LabelSlot:  "0",
				nlbpoolv1alpha1.LabelPhase: string(phase),
			},
			Finalizers: []string{nlbpoolv1alpha1.FinalizerPortAllocation},
		},
		Spec: nlbpoolv1alpha1.PortAllocationSpec{
			BoundPod:   "game-pod-0",
			BoundPodIP: "10.0.0.1",
			ServerGroups: []nlbpoolv1alpha1.ServerGroupRef{
				{Name: "game", ServerGroupId: "sg-1"},
				{Name: "query", ServerGroupId: "sg-2"},
			},
			Endpoints: []nlbpoolv1alpha1.LaneEndpoint{
				{
					Lane: "bgp",
					EIP:  "1.2.3.4",
					Ports: []nlbpoolv1alpha1.EndpointPort{
						{Name: "game", ListenerPort: 10000, ContainerPort: 7777, Protocol: "TCP", ListenerId: "lsn-1"},
						{Name: "query", ListenerPort: 10001, ContainerPort: 7778, Protocol: "UDP", ListenerId: "lsn-2"},
					},
				},
			},
		},
		Status: nlbpoolv1alpha1.PortAllocationStatus{
			Phase: phase,
			ServerGroups: []nlbpoolv1alpha1.ServerGroupCloudStatus{
				{Name: "game", ServerGroupId: "sg-1", Phase: "Active"},
				{Name: "query", ServerGroupId: "sg-2", Phase: "Active"},
			},
			Listeners: []nlbpoolv1alpha1.ListenerCloudStatus{
				{PortName: "game", LaneName: "bgp", ListenerId: "lsn-1", ListenerPort: 10000, Phase: "Running"},
				{PortName: "query", LaneName: "bgp", ListenerId: "lsn-2", ListenerPort: 10001, Phase: "Running"},
			},
			SGsTotal: 2, SGsReady: 2, ListenersTotal: 2, ListenersReady: 2,
		},
	}
}

func newClaimedPod(disabled bool) *corev1.Pod {
	annotations := map[string]string{
		nlbpoolv1alpha1.AnnotationNLBPoolName: "pool",
		nlbpoolv1alpha1.AnnotationPAClaim:     "pool-s0",
	}
	if disabled {
		annotations[nlbpoolv1alpha1.AnnotationNetworkDisabled] = "true"
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "game-pod-0",
			Namespace:   "default",
			Annotations: annotations,
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	}
}

func newPAReconciler(t *testing.T, mock *provider.MockNLBClient, objs ...client.Object) (*PortAllocationReconciler, client.Client) {
	t.Helper()
	s := newTestScheme(t)
	builder := fake.NewClientBuilder().WithScheme(s)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...).WithStatusSubresource(objs...)
	}
	fc := builder.Build()
	r := &PortAllocationReconciler{
		Client:    fc,
		Scheme:    s,
		Recorder:  record.NewFakeRecorder(100),
		NLBClient: mock,
	}
	return r, fc
}

func getPA(t *testing.T, fc client.Client, name string) *nlbpoolv1alpha1.PortAllocation {
	t.Helper()
	pa := &nlbpoolv1alpha1.PortAllocation{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, pa); err != nil {
		t.Fatalf("get PA %s: %v", name, err)
	}
	return pa
}

func getPod(t *testing.T, fc client.Client, name string) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, pod); err != nil {
		t.Fatalf("get pod %s: %v", name, err)
	}
	return pod
}

// driveToBound runs handleBinding until the PA reaches Bound.
func driveToBound(t *testing.T, r *PortAllocationReconciler, fc client.Client) {
	t.Helper()
	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationBound {
			return
		}
		if _, err := r.handleBinding(context.Background(), fresh); err != nil {
			t.Fatalf("handleBinding #%d failed: %v", i, err)
		}
	}
	t.Fatalf("PA did not reach Bound")
}

// newBindingMockNLBClient completes AddServers immediately.
func newBindingMockNLBClient() *provider.MockNLBClient {
	mock := provider.NewMockNLBClient()
	added := map[string]bool{}
	mock.AddServersToServerGroupFunc = func(ctx context.Context, req *provider.AddServersRequest) (*provider.AddServersResponse, error) {
		added[req.ServerGroupId] = true
		return &provider.AddServersResponse{}, nil
	}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if added[sgId] {
			return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
		}
		return nil, nil
	}
	return mock
}

// =====================================================
// Provisioning: SG + Listener creation via cloud API
// =====================================================

func TestProvisioning_CreatesServerGroupsAndListeners(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pool := newTestPool("pool")
	nlbBGP := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
		Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", LoadBalancerStatus: "Active", DNSName: "bgp.dns"},
	}
	nlbCTC := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-ctc-0", Namespace: "default"},
		Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-ctc-0", LoadBalancerStatus: "Active", DNSName: "ctc.dns"},
	}
	pa := &nlbpoolv1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-s0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: "pool",
				nlbpoolv1alpha1.LabelSlot: "0",
			},
			Finalizers: []string{nlbpoolv1alpha1.FinalizerPortAllocation},
		},
		Status: nlbpoolv1alpha1.PortAllocationStatus{
			Phase: nlbpoolv1alpha1.PortAllocationProvisioning,
		},
	}
	r, fc := newPAReconciler(t, mock, pool, nlbBGP, nlbCTC, pa)

	// Drive handleProvisioning to completion (SG create -> Active -> Listener
	// create -> Running -> Available). Bounded loop.
	for i := 0; i < 20; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationAvailable {
			break
		}
		if _, err := r.handleProvisioning(context.Background(), fresh); err != nil {
			t.Fatalf("handleProvisioning #%d failed: %v", i, err)
		}
	}

	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationAvailable {
		t.Fatalf("expected phase Available, got %q (msg: %s)", final.Status.Phase, final.Status.Message)
	}

	// 2 ports => 2 SGs; 2 ports x 2 lanes => 4 listeners.
	if len(mock.CreateServerGroupCalls) != 2 {
		t.Errorf("expected 2 CreateServerGroup calls, got %d", len(mock.CreateServerGroupCalls))
	}
	if len(mock.CreateListenerCalls) != 4 {
		t.Errorf("expected 4 CreateListener calls, got %d", len(mock.CreateListenerCalls))
	}
	for _, call := range mock.CreateServerGroupCalls {
		if call.VpcId != "vpc-test" {
			t.Errorf("expected VpcId vpc-test, got %q", call.VpcId)
		}
		if call.ClientToken == "" {
			t.Error("expected non-empty ClientToken on CreateServerGroup")
		}
	}
	for _, call := range mock.CreateListenerCalls {
		if call.ServerGroupId == "" {
			t.Error("expected non-empty ServerGroupId on CreateListener")
		}
		if call.ClientToken == "" {
			t.Error("expected non-empty ClientToken on CreateListener")
		}
	}

	// Spec must be filled for the binding phase.
	if len(final.Spec.ServerGroups) != 2 {
		t.Errorf("expected 2 SG refs in spec, got %d", len(final.Spec.ServerGroups))
	}
	if len(final.Spec.Endpoints) != 2 {
		t.Errorf("expected 2 lane endpoints in spec, got %d", len(final.Spec.Endpoints))
	}
}

// =====================================================
// Binding
// =====================================================

func TestBinding_AddServersToServerGroup(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// After AddServers, report the server as present so Adding -> Bound.
	added := map[string]bool{}
	mock.AddServersToServerGroupFunc = func(ctx context.Context, req *provider.AddServersRequest) (*provider.AddServersResponse, error) {
		added[req.ServerGroupId] = true
		return &provider.AddServersResponse{}, nil
	}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if added[sgId] {
			return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
		}
		return nil, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBinding)
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)

	// Drive handleBinding to completion (AddServers -> Adding -> Bound).
	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationBound {
			break
		}
		if _, err := r.handleBinding(context.Background(), fresh); err != nil {
			t.Fatalf("handleBinding #%d failed: %v", i, err)
		}
	}

	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationBound {
		t.Fatalf("expected phase Bound, got %q", final.Status.Phase)
	}

	// One AddServers call per SG with the pod IP and container port.
	if len(mock.AddServersToServerGroupCalls) != 2 {
		t.Fatalf("expected 2 AddServers calls, got %d", len(mock.AddServersToServerGroupCalls))
	}
	for _, call := range mock.AddServersToServerGroupCalls {
		if len(call.Servers) != 1 {
			t.Fatalf("expected 1 server per call, got %d", len(call.Servers))
		}
		if call.Servers[0].ServerIp != "10.0.0.1" {
			t.Errorf("expected server IP 10.0.0.1, got %q", call.Servers[0].ServerIp)
		}
		if call.Servers[0].ServerType != "Ip" {
			t.Errorf("expected server type Ip, got %q", call.Servers[0].ServerType)
		}
		if call.ClientToken == "" {
			t.Error("expected non-empty ClientToken for idempotency")
		}
	}

	// ExternalAddresses filled for kruise-game.
	if len(final.Status.ExternalAddresses) != 1 {
		t.Fatalf("expected 1 external address, got %d", len(final.Status.ExternalAddresses))
	}
	if final.Status.ExternalAddresses[0].IP != "1.2.3.4" {
		t.Errorf("expected external IP 1.2.3.4, got %q", final.Status.ExternalAddresses[0].IP)
	}
}

func TestBinding_ThrottlingIsRetryableNotFatal(t *testing.T) {
	mock := provider.NewMockNLBClient()
	mock.AddServersToServerGroupFunc = func(ctx context.Context, req *provider.AddServersRequest) (*provider.AddServersResponse, error) {
		return nil, fmt.Errorf("Throttling: request rate exceeded")
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBinding)
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)

	fresh := getPA(t, fc, "pool-s0")
	result, err := r.handleBinding(context.Background(), fresh)
	if err != nil {
		t.Fatalf("throttling must be handled gracefully, got error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue backoff on throttling")
	}

	// Phase must stay Binding (retryable, not a failure).
	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationBinding {
		t.Errorf("expected phase to remain Binding on throttling, got %q", final.Status.Phase)
	}
}

// =====================================================
// Release
// =====================================================

func TestRelease_RemovesServersAndReturnsToAvailable(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// First List returns the bound server; after RemoveServers it is gone.
	removed := map[string]bool{}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if removed[sgId] {
			return nil, nil
		}
		return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
	}
	mock.RemoveServersFromServerGroupFunc = func(ctx context.Context, req *provider.RemoveServersRequest) (*provider.RemoveServersResponse, error) {
		removed[req.ServerGroupId] = true
		return &provider.RemoveServersResponse{}, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationReleasing)
	// Mark servers as bound so the release loop has work to do.
	pa.Status.ServerGroups[0].ServerStatus = "Bound"
	pa.Status.ServerGroups[1].ServerStatus = "Bound"
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)

	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationAvailable {
			break
		}
		if _, err := r.handleReleasing(context.Background(), fresh); err != nil {
			t.Fatalf("handleReleasing #%d failed: %v", i, err)
		}
	}

	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationAvailable {
		t.Fatalf("expected phase Available after release, got %q", final.Status.Phase)
	}
	if final.Spec.BoundPod != "" || final.Spec.BoundPodIP != "" {
		t.Errorf("expected BoundPod/IP cleared, got %q/%q", final.Spec.BoundPod, final.Spec.BoundPodIP)
	}
	if len(mock.RemoveServersFromServerGroupCalls) == 0 {
		t.Error("expected RemoveServers to be called during release")
	}

	// Pod claim annotation must be cleared for rebinding.
	freshPod := &corev1.Pod{}
	if err := fc.Get(context.Background(), types.NamespacedName{Name: "game-pod-0", Namespace: "default"}, freshPod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if claim := freshPod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]; claim != "" {
		t.Errorf("expected pod claim cleared, got %q", claim)
	}
}

// =====================================================
// Disable / Enable
// =====================================================

func TestDisable_RemovesServersKeepsListeners(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// Removal completes immediately: server no longer present after remove.
	removed := map[string]bool{}
	mock.RemoveServersFromServerGroupFunc = func(ctx context.Context, req *provider.RemoveServersRequest) (*provider.RemoveServersResponse, error) {
		removed[req.ServerGroupId] = true
		return &provider.RemoveServersResponse{}, nil
	}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if removed[sgId] {
			return nil, nil
		}
		return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBound)
	pa.Status.ServerGroups[0].ServerStatus = "Bound"
	pa.Status.ServerGroups[1].ServerStatus = "Bound"
	pod := newClaimedPod(true) // network-disabled annotation set
	r, fc := newPAReconciler(t, mock, pa, pod)

	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationDisabled {
			break
		}
		if _, err := r.handleBound(context.Background(), fresh); err != nil {
			t.Fatalf("handleBound #%d failed: %v", i, err)
		}
	}

	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationDisabled {
		t.Fatalf("expected phase Disabled, got %q", final.Status.Phase)
	}
	if len(mock.RemoveServersFromServerGroupCalls) != 2 {
		t.Errorf("expected 2 RemoveServers calls, got %d", len(mock.RemoveServersFromServerGroupCalls))
	}
	// Listeners must be preserved (slot claim retained, only backends removed).
	if len(final.Status.Listeners) != 2 {
		t.Errorf("expected listeners preserved on disable, got %d", len(final.Status.Listeners))
	}
	if final.Spec.BoundPod != "game-pod-0" {
		t.Errorf("expected slot claim retained on disable, got %q", final.Spec.BoundPod)
	}
	if len(mock.DeleteListenerCalls) != 0 {
		t.Errorf("disable must not delete listeners, got %d DeleteListener calls", len(mock.DeleteListenerCalls))
	}
}

func TestEnable_ReturnsToBindingWhenAnnotationRemoved(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationDisabled)
	pod := newClaimedPod(false) // no network-disabled annotation
	r, fc := newPAReconciler(t, mock, pa, pod)

	fresh := getPA(t, fc, "pool-s0")
	if _, err := r.handleDisabled(context.Background(), fresh); err != nil {
		t.Fatalf("handleDisabled failed: %v", err)
	}

	final := getPA(t, fc, "pool-s0")
	if final.Status.Phase != nlbpoolv1alpha1.PortAllocationBinding {
		t.Errorf("expected phase Binding after re-enable, got %q", final.Status.Phase)
	}
}

// =====================================================
// Idempotency
// =====================================================

func TestIdempotency_ProvisioningClientTokenDeterministic(t *testing.T) {
	r := &PortAllocationReconciler{}
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationProvisioning)
	pa.UID = types.UID("fixed-uid")

	t1 := r.provisioningClientToken(pa, "sg", "game")
	t2 := r.provisioningClientToken(pa, "sg", "game")
	if t1 != t2 {
		t.Errorf("expected deterministic token, got %q vs %q", t1, t2)
	}
	t3 := r.provisioningClientToken(pa, "sg", "query")
	if t1 == t3 {
		t.Error("different parts must produce different tokens")
	}
}

func TestIdempotency_ProvisioningDoesNotDuplicateSGs(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pool := newTestPool("pool")
	nlbBGP := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp-0", Namespace: "default"},
		Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0", DNSName: "bgp.dns"},
	}
	nlbCTC := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-ctc-0", Namespace: "default"},
		Status:     nlbv1.NLBStatus{LoadBalancerId: "lb-ctc-0", DNSName: "ctc.dns"},
	}
	pa := &nlbpoolv1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-s0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: "pool",
				nlbpoolv1alpha1.LabelSlot: "0",
			},
			Finalizers: []string{nlbpoolv1alpha1.FinalizerPortAllocation},
		},
		Status: nlbpoolv1alpha1.PortAllocationStatus{Phase: nlbpoolv1alpha1.PortAllocationProvisioning},
	}
	r, fc := newPAReconciler(t, mock, pool, nlbBGP, nlbCTC, pa)

	// Drive to Available, then call handleProvisioning again: no new SG creation.
	for i := 0; i < 20; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationAvailable {
			break
		}
		if _, err := r.handleProvisioning(context.Background(), fresh); err != nil {
			t.Fatalf("handleProvisioning #%d failed: %v", i, err)
		}
	}
	callsAfterFirst := len(mock.CreateServerGroupCalls)
	if callsAfterFirst != 2 {
		t.Fatalf("expected 2 CreateServerGroup calls after provisioning, got %d", callsAfterFirst)
	}

	fresh := getPA(t, fc, "pool-s0")
	// Force phase back through provisioning path with already-Active SGs.
	fresh.Status.Phase = nlbpoolv1alpha1.PortAllocationProvisioning
	if _, err := r.handleProvisioning(context.Background(), fresh); err != nil {
		t.Fatalf("re-run handleProvisioning failed: %v", err)
	}
	if len(mock.CreateServerGroupCalls) != callsAfterFirst {
		t.Errorf("re-provisioning must not create new SGs: %d -> %d",
			callsAfterFirst, len(mock.CreateServerGroupCalls))
	}
}

// =====================================================
// Pod result annotation (Issue #13)
// =====================================================

const expectedMappingJSON = `[{"lane":"bgp","ip":"1.2.3.4","ports":` +
	`[{"name":"game","listenerPort":10000,"containerPort":7777,"protocol":"TCP"},` +
	`{"name":"query","listenerPort":10001,"containerPort":7778,"protocol":"UDP"}]}]`

func TestBound_WritesPortMappingResultAnnotation(t *testing.T) {
	mock := newBindingMockNLBClient()
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBinding)
	// Drop ContainerPort on "query" to verify the ListenerPort fallback.
	pa.Spec.Endpoints[0].Ports[1].ContainerPort = 0
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)
	r.EnablePodResultAnnotation = true

	driveToBound(t, r, fc)

	want := `[{"lane":"bgp","ip":"1.2.3.4","ports":` +
		`[{"name":"game","listenerPort":10000,"containerPort":7777,"protocol":"TCP"},` +
		`{"name":"query","listenerPort":10001,"containerPort":10001,"protocol":"UDP"}]}]`
	got := getPod(t, fc, "game-pod-0").Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]
	if got != want {
		t.Errorf("annotation mismatch:\n got  %s\n want %s", got, want)
	}
}

func TestBound_AnnotationSyncIsIdempotent(t *testing.T) {
	mock := newBindingMockNLBClient()
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBinding)
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)
	r.EnablePodResultAnnotation = true

	driveToBound(t, r, fc)

	podBefore := getPod(t, fc, "game-pod-0")
	if podBefore.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] != expectedMappingJSON {
		t.Fatalf("unexpected annotation after Bound: %q",
			podBefore.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult])
	}
	rvBefore := podBefore.ResourceVersion

	// handleBound steady-state pass: value unchanged -> no Patch must occur.
	fresh := getPA(t, fc, "pool-s0")
	result, err := r.handleBound(context.Background(), fresh)
	if err != nil {
		t.Fatalf("handleBound failed: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected 30s requeue in steady state, got %v", result.RequeueAfter)
	}
	podAfter := getPod(t, fc, "game-pod-0")
	if podAfter.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] != expectedMappingJSON {
		t.Error("annotation value changed unexpectedly")
	}
	if podAfter.ResourceVersion != rvBefore {
		t.Errorf("expected no pod write (resourceVersion %s -> %s)", rvBefore, podAfter.ResourceVersion)
	}
}

func TestRelease_ClearsPortMappingResultAnnotation(t *testing.T) {
	mock := provider.NewMockNLBClient()
	removed := map[string]bool{}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if removed[sgId] {
			return nil, nil
		}
		return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
	}
	mock.RemoveServersFromServerGroupFunc = func(ctx context.Context, req *provider.RemoveServersRequest) (*provider.RemoveServersResponse, error) {
		removed[req.ServerGroupId] = true
		return &provider.RemoveServersResponse{}, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationReleasing)
	pa.Status.ServerGroups[0].ServerStatus = "Bound"
	pa.Status.ServerGroups[1].ServerStatus = "Bound"
	pod := newClaimedPod(false)
	pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] = expectedMappingJSON
	r, fc := newPAReconciler(t, mock, pa, pod)
	r.EnablePodResultAnnotation = true

	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationAvailable {
			break
		}
		if _, err := r.handleReleasing(context.Background(), fresh); err != nil {
			t.Fatalf("handleReleasing #%d failed: %v", i, err)
		}
	}

	freshPod := getPod(t, fc, "game-pod-0")
	if _, ok := freshPod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; ok {
		t.Error("expected port-mapping result annotation removed after release")
	}
	if claim := freshPod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim]; claim != "" {
		t.Errorf("expected pod claim cleared, got %q", claim)
	}
}

func TestDeletion_ClearsPortMappingResultAnnotation(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// Listeners already gone (cascade), SG deletion succeeds.
	mock.GetListenerAttributeFunc = func(ctx context.Context, listenerId string) (*provider.ListenerAttribute, error) {
		return nil, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBound)
	pod := newClaimedPod(false)
	pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] = expectedMappingJSON
	r, fc := newPAReconciler(t, mock, pa, pod)
	r.EnablePodResultAnnotation = true

	// fake client sets deletionTimestamp (finalizer present) instead of deleting.
	if err := fc.Delete(context.Background(), getPA(t, fc, "pool-s0")); err != nil {
		t.Fatalf("delete PA: %v", err)
	}
	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if _, err := r.handleDeletion(context.Background(), fresh); err != nil {
			t.Fatalf("handleDeletion #%d failed: %v", i, err)
		}
		if err := fc.Get(context.Background(), types.NamespacedName{Name: "pool-s0", Namespace: "default"}, &nlbpoolv1alpha1.PortAllocation{}); err != nil {
			break // PA fully deleted
		}
	}

	freshPod := getPod(t, fc, "game-pod-0")
	if _, ok := freshPod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; ok {
		t.Error("expected port-mapping result annotation removed after deletion")
	}
}

func TestDisable_ClearsResultAnnotationKeepsClaim_ReEnableRewrites(t *testing.T) {
	// Shared backend state: servers start present; Add/Remove/List all follow it.
	added := map[string]bool{"sg-1": true, "sg-2": true}
	mock := provider.NewMockNLBClient()
	mock.AddServersToServerGroupFunc = func(ctx context.Context, req *provider.AddServersRequest) (*provider.AddServersResponse, error) {
		added[req.ServerGroupId] = true
		return &provider.AddServersResponse{}, nil
	}
	mock.RemoveServersFromServerGroupFunc = func(ctx context.Context, req *provider.RemoveServersRequest) (*provider.RemoveServersResponse, error) {
		added[req.ServerGroupId] = false
		return &provider.RemoveServersResponse{}, nil
	}
	mock.ListServerGroupServersFunc = func(ctx context.Context, sgId string) ([]provider.BackendServer, error) {
		if added[sgId] {
			return []provider.BackendServer{{ServerIp: "10.0.0.1", Port: 7777}}, nil
		}
		return nil, nil
	}

	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBound)
	pa.Status.ServerGroups[0].ServerStatus = "Bound"
	pa.Status.ServerGroups[1].ServerStatus = "Bound"
	pod := newClaimedPod(true) // network-disabled
	pod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult] = expectedMappingJSON
	r, fc := newPAReconciler(t, mock, pa, pod)
	r.EnablePodResultAnnotation = true

	// Drive to Disabled.
	for i := 0; i < 10; i++ {
		fresh := getPA(t, fc, "pool-s0")
		if fresh.Status.Phase == nlbpoolv1alpha1.PortAllocationDisabled {
			break
		}
		if _, err := r.handleBound(context.Background(), fresh); err != nil {
			t.Fatalf("handleBound #%d failed: %v", i, err)
		}
	}
	if got := getPA(t, fc, "pool-s0").Status.Phase; got != nlbpoolv1alpha1.PortAllocationDisabled {
		t.Fatalf("expected phase Disabled, got %q", got)
	}

	// Annotation removed, claim retained for re-enable.
	disabledPod := getPod(t, fc, "game-pod-0")
	if _, ok := disabledPod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; ok {
		t.Error("expected result annotation removed on disable")
	}
	if disabledPod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim] != "pool-s0" {
		t.Errorf("expected claim retained on disable, got %q",
			disabledPod.Annotations[nlbpoolv1alpha1.AnnotationPAClaim])
	}

	// Re-enable: drop the disabled annotation, drive Disabled -> Binding -> Bound.
	disabledPod.Annotations[nlbpoolv1alpha1.AnnotationNetworkDisabled] = ""
	if err := fc.Update(context.Background(), disabledPod); err != nil {
		t.Fatalf("update pod: %v", err)
	}
	if _, err := r.handleDisabled(context.Background(), getPA(t, fc, "pool-s0")); err != nil {
		t.Fatalf("handleDisabled failed: %v", err)
	}
	if got := getPA(t, fc, "pool-s0").Status.Phase; got != nlbpoolv1alpha1.PortAllocationBinding {
		t.Fatalf("expected phase Binding after re-enable, got %q", got)
	}
	driveToBound(t, r, fc)

	if got := getPod(t, fc, "game-pod-0").Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; got != expectedMappingJSON {
		t.Errorf("expected annotation rewritten after re-enable, got %q", got)
	}
}

func TestClearPodResultAnnotation_PodNotFoundSilent(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBound)
	pa.Spec.BoundPod = "ghost-pod" // does not exist
	r, _ := newPAReconciler(t, mock, pa)
	r.EnablePodResultAnnotation = true

	// Must not panic or surface an error (function returns nothing; best-effort).
	r.clearPodResultAnnotation(context.Background(), pa)
}

func TestFeatureDisabled_NoPodAnnotationWrite(t *testing.T) {
	mock := newBindingMockNLBClient()
	pa := newBoundTestPA(nlbpoolv1alpha1.PortAllocationBinding)
	pod := newClaimedPod(false)
	r, fc := newPAReconciler(t, mock, pa, pod)
	// EnablePodResultAnnotation stays false (default).

	driveToBound(t, r, fc)

	freshPod := getPod(t, fc, "game-pod-0")
	if _, ok := freshPod.Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; ok {
		t.Error("feature disabled: result annotation must not be written")
	}

	// Steady-state handleBound must not write either.
	if _, err := r.handleBound(context.Background(), getPA(t, fc, "pool-s0")); err != nil {
		t.Fatalf("handleBound failed: %v", err)
	}
	if _, ok := getPod(t, fc, "game-pod-0").Annotations[nlbpoolv1alpha1.AnnotationPortMappingResult]; ok {
		t.Error("feature disabled: result annotation must not appear in steady state")
	}
}
