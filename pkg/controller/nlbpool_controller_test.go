package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Operator/pkg/apis/nlboperator/v1"
	eipv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbpoolv1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"
	"github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/pkg/provider"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := nlbpoolv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add nlbpool scheme: %v", err)
	}
	if err := nlbv1.SchemeBuilder.AddToScheme(s); err != nil {
		t.Fatalf("add nlb scheme: %v", err)
	}
	if err := eipv1.AddToScheme(s); err != nil {
		t.Fatalf("add eip scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return s
}

// newTestPool builds a v5 NLBPool: 2 lanes (BGP + ChinaTelecom), 2 ports,
// 2 zones, 4 slots per NLB.
func newTestPool(name string) *nlbpoolv1alpha1.NLBPool {
	return &nlbpoolv1alpha1.NLBPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: nlbpoolv1alpha1.NLBPoolSpec{
			Region: "cn-hangzhou",
			VpcId:  "vpc-test",
			ZoneMaps: []nlbpoolv1alpha1.ZoneMapEntry{
				{Zone: "cn-hangzhou-h", VSwitchId: "vsw-aaa"},
				{Zone: "cn-hangzhou-i", VSwitchId: "vsw-bbb"},
			},
			Lanes: []nlbpoolv1alpha1.LaneConfig{
				{Name: "bgp", ISPType: "BGP"},
				{Name: "ctc", ISPType: "ChinaTelecom"},
			},
			Ports: []nlbpoolv1alpha1.PortConfig{
				{Name: "game", Protocol: "TCP", ContainerPort: 7777},
				{Name: "query", Protocol: "UDP", ContainerPort: 7778},
			},
			PortRange:        nlbpoolv1alpha1.PortRange{Min: 10000, Max: 10099},
			SlotsPerNLB:      4,
			MinAvailableNLBs: 1,
		},
	}
}

func newPoolReconciler(t *testing.T, mock *provider.MockNLBClient, objs ...client.Object) (*NLBPoolReconciler, client.Client) {
	t.Helper()
	s := newTestScheme(t)
	builder := fake.NewClientBuilder().WithScheme(s)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
		builder = builder.WithStatusSubresource(objs...)
	}
	fc := builder.Build()
	r := &NLBPoolReconciler{
		Client:    fc,
		Scheme:    s,
		Recorder:  record.NewFakeRecorder(100),
		NLBClient: mock,
	}
	return r, fc
}

func poolRequest(pool *nlbpoolv1alpha1.NLBPool) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Name: pool.Name, Namespace: pool.Namespace,
	}}
}

// =====================================================
// Reconcile: finalizer + prewarm (EIP/NLB/PA creation)
// =====================================================

func TestReconcile_AddsFinalizer(t *testing.T) {
	pool := newTestPool("pool")
	r, fc := newPoolReconciler(t, provider.NewMockNLBClient(), pool)

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := &nlbpoolv1alpha1.NLBPool{}
	if err := fc.Get(context.Background(), poolRequest(pool).NamespacedName, got); err != nil {
		t.Fatalf("get pool: %v", err)
	}
	found := false
	for _, f := range got.Finalizers {
		if f == nlbpoolv1alpha1.FinalizerNLBPool {
			found = true
		}
	}
	if !found {
		t.Errorf("expected finalizer %q on pool, got %v", nlbpoolv1alpha1.FinalizerNLBPool, got.Finalizers)
	}
}

func TestPrewarm_CreatesEIPsPerLaneAndZone(t *testing.T) {
	pool := newTestPool("pool")
	pool.Finalizers = []string{nlbpoolv1alpha1.FinalizerNLBPool}
	r, fc := newPoolReconciler(t, provider.NewMockNLBClient(), pool)

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 2 lanes x 2 zones = 4 EIP CRs for group 0.
	eipList := &eipv1.EIPList{}
	if err := fc.List(context.Background(), eipList, client.InNamespace("default")); err != nil {
		t.Fatalf("list EIPs: %v", err)
	}
	if len(eipList.Items) != 4 {
		t.Fatalf("expected 4 EIP CRs (2 lanes x 2 zones), got %d", len(eipList.Items))
	}

	// Charge type must be ISP-aware: BGP -> PayByTraffic, ChinaTelecom -> PayByBandwidth.
	for _, eip := range eipList.Items {
		switch eip.Spec.ISP {
		case "BGP":
			if eip.Spec.InternetChargeType != "PayByTraffic" {
				t.Errorf("BGP EIP %s: expected PayByTraffic, got %s", eip.Name, eip.Spec.InternetChargeType)
			}
		case "ChinaTelecom":
			if eip.Spec.InternetChargeType != "PayByBandwidth" {
				t.Errorf("CTC EIP %s: expected PayByBandwidth, got %s", eip.Name, eip.Spec.InternetChargeType)
			}
			if eip.Spec.Bandwidth != "200" {
				t.Errorf("CTC EIP %s: expected default bandwidth 200, got %q", eip.Name, eip.Spec.Bandwidth)
			}
		default:
			t.Errorf("unexpected ISP %q on EIP %s", eip.Spec.ISP, eip.Name)
		}
		// Owned by the pool.
		if len(eip.OwnerReferences) == 0 {
			t.Errorf("EIP %s missing owner reference", eip.Name)
		}
	}

	// NLB CRs must NOT exist yet (EIPs have no AllocationID).
	nlbList := &nlbv1.NLBList{}
	if err := fc.List(context.Background(), nlbList, client.InNamespace("default")); err != nil {
		t.Fatalf("list NLBs: %v", err)
	}
	if len(nlbList.Items) != 0 {
		t.Errorf("expected 0 NLB CRs before EIPs ready, got %d", len(nlbList.Items))
	}
}

func TestPrewarm_CreatesNLBsAfterEIPsReady(t *testing.T) {
	pool := newTestPool("pool")
	pool.Finalizers = []string{nlbpoolv1alpha1.FinalizerNLBPool}

	// Pre-create all 4 EIP CRs with AllocationIDs already assigned.
	objs := []client.Object{pool}
	for _, lane := range pool.Spec.Lanes {
		for zIdx := range pool.Spec.ZoneMaps {
			eip := &eipv1.EIP{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s-0-z%d", pool.Name, lane.Name, zIdx),
					Namespace: "default",
					Labels: map[string]string{
						nlbpoolv1alpha1.LabelPool: pool.Name,
						nlbpoolv1alpha1.LabelLane: lane.Name,
					},
				},
				Status: eipv1.EIPStatus{
					AllocationID: fmt.Sprintf("eip-%s-z%d", lane.Name, zIdx),
				},
			}
			objs = append(objs, eip)
		}
	}
	r, fc := newPoolReconciler(t, provider.NewMockNLBClient(), objs...)

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// One NLB CR per lane, each zone mapping carries its EIP AllocationId.
	nlbList := &nlbv1.NLBList{}
	if err := fc.List(context.Background(), nlbList, client.InNamespace("default")); err != nil {
		t.Fatalf("list NLBs: %v", err)
	}
	if len(nlbList.Items) != 2 {
		t.Fatalf("expected 2 NLB CRs (one per lane), got %d", len(nlbList.Items))
	}
	for _, nlb := range nlbList.Items {
		if nlb.Spec.VpcId != "vpc-test" {
			t.Errorf("NLB %s: expected VpcId vpc-test, got %s", nlb.Name, nlb.Spec.VpcId)
		}
		if len(nlb.Spec.ZoneMappings) != 2 {
			t.Fatalf("NLB %s: expected 2 zone mappings, got %d", nlb.Name, len(nlb.Spec.ZoneMappings))
		}
		for zIdx, zm := range nlb.Spec.ZoneMappings {
			if zm.AllocationId == "" {
				t.Errorf("NLB %s zone %d: missing EIP AllocationId", nlb.Name, zIdx)
			}
		}
	}
}

func TestPrewarm_CreatesPortAllocations(t *testing.T) {
	pool := newTestPool("pool")
	pool.Finalizers = []string{nlbpoolv1alpha1.FinalizerNLBPool}

	objs := []client.Object{pool}
	// EIPs ready + NLBs Active so reconcile reaches ensureSlotResources.
	for _, lane := range pool.Spec.Lanes {
		for zIdx := range pool.Spec.ZoneMaps {
			objs = append(objs, &eipv1.EIP{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%s-0-z%d", pool.Name, lane.Name, zIdx),
					Namespace: "default",
					Labels:    map[string]string{nlbpoolv1alpha1.LabelPool: pool.Name},
				},
				Status: eipv1.EIPStatus{AllocationID: fmt.Sprintf("eip-%s-z%d", lane.Name, zIdx)},
			})
		}
		objs = append(objs, &nlbv1.NLB{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s-0", pool.Name, lane.Name),
				Namespace: "default",
				Labels:    map[string]string{nlbpoolv1alpha1.LabelPool: pool.Name},
			},
			Spec: nlbv1.NLBSpec{
				ZoneMappings: []nlbv1.ZoneMapping{
					{ZoneId: "cn-hangzhou-h", VSwitchId: "vsw-aaa", AllocationId: fmt.Sprintf("eip-%s-z0", lane.Name)},
					{ZoneId: "cn-hangzhou-i", VSwitchId: "vsw-bbb", AllocationId: fmt.Sprintf("eip-%s-z1", lane.Name)},
				},
			},
			Status: nlbv1.NLBStatus{LoadBalancerId: "lb-" + lane.Name, LoadBalancerStatus: "Active"},
		})
	}
	r, fc := newPoolReconciler(t, provider.NewMockNLBClient(), objs...)

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// SlotsPerNLB=4, 1 NLB group => 4 PortAllocations.
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	if err := fc.List(context.Background(), paList, client.InNamespace("default")); err != nil {
		t.Fatalf("list PAs: %v", err)
	}
	if len(paList.Items) != 4 {
		t.Fatalf("expected 4 PortAllocations (slotsPerNLB=4), got %d", len(paList.Items))
	}
	for _, pa := range paList.Items {
		if pa.Labels[nlbpoolv1alpha1.LabelPool] != pool.Name {
			t.Errorf("PA %s: missing pool label", pa.Name)
		}
		if pa.Labels[nlbpoolv1alpha1.LabelSlot] == "" {
			t.Errorf("PA %s: missing slot label", pa.Name)
		}
		if len(pa.OwnerReferences) == 0 {
			t.Errorf("PA %s: missing owner reference", pa.Name)
		}
	}
}

// =====================================================
// Expansion logic
// =====================================================

func TestExpansion_ComputeDesiredNLBs(t *testing.T) {
	tests := []struct {
		name      string
		slots     int32
		minNLBs   int32
		total     int32
		available int32
		bound     int32
		expected  int32
	}{
		{"enough headroom keeps current", 4, 1, 4, 4, 0, 1},
		{"available below minFree expands", 4, 1, 4, 1, 3, 2},
		{"all bound expands to cover bound+minFree", 4, 1, 4, 0, 4, 2},
		{"never shrinks below current", 4, 1, 8, 8, 0, 2},
		{"zero state defaults to one group", 4, 1, 0, 0, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := newTestPool("pool")
			pool.Spec.SlotsPerNLB = tt.slots
			pool.Spec.MinAvailableNLBs = tt.minNLBs
			pool.Status.TotalSlots = tt.total
			pool.Status.AvailableSlots = tt.available
			pool.Status.BoundSlots = tt.bound

			r, _ := newPoolReconciler(t, provider.NewMockNLBClient())
			got := r.computeDesiredNLBs(pool)
			if got != tt.expected {
				t.Errorf("computeDesiredNLBs() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// =====================================================
// Deletion cascade
// =====================================================

// newDeletingPool returns a pool marked for deletion with child CRs attached.
func deletionFixture(t *testing.T, pool *nlbpoolv1alpha1.NLBPool, scheme *runtime.Scheme) []client.Object {
	t.Helper()
	now := metav1.Now()
	pool.DeletionTimestamp = &now
	pool.Finalizers = []string{nlbpoolv1alpha1.FinalizerNLBPool}

	nlb := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-bgp-0",
			Namespace: "default",
			Labels:    map[string]string{nlbpoolv1alpha1.LabelPool: pool.Name},
		},
		Status: nlbv1.NLBStatus{LoadBalancerId: "lb-bgp-0"},
	}
	eip := &eipv1.EIP{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-bgp-0-z0",
			Namespace: "default",
			Labels:    map[string]string{nlbpoolv1alpha1.LabelPool: pool.Name},
		},
	}
	pa := &nlbpoolv1alpha1.PortAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool-s0",
			Namespace: "default",
			Labels: map[string]string{
				nlbpoolv1alpha1.LabelPool: pool.Name,
				nlbpoolv1alpha1.LabelSlot: "0",
			},
		},
	}
	// Set pool as controller owner so deleteInfrastructure picks them up.
	for _, obj := range []client.Object{nlb, eip} {
		if err := ctrl.SetControllerReference(pool, obj, scheme); err != nil {
			t.Fatalf("set owner ref: %v", err)
		}
	}
	return []client.Object{pool, nlb, eip, pa}
}

func TestDeletion_CascadeDeletesChildrenAndRemovesFinalizer(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// Cloud NLB already gone.
	mock.LoadBalancerExistsFunc = func(ctx context.Context, id string) (bool, error) {
		return false, nil
	}
	pool := newTestPool("pool")
	s := newTestScheme(t)
	objs := deletionFixture(t, pool, s)

	fc := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, Recorder: record.NewFakeRecorder(100), NLBClient: mock}

	// Drive reconcile until finalizer removed (bounded loop).
	for i := 0; i < 10; i++ {
		if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
			t.Fatalf("Reconcile #%d failed: %v", i, err)
		}
		got := &nlbpoolv1alpha1.NLBPool{}
		err := fc.Get(context.Background(), poolRequest(pool).NamespacedName, got)
		if err != nil {
			// Pool fully deleted after finalizer removal — success.
			return
		}
		if len(got.Finalizers) == 0 {
			return
		}
	}

	// Finalizer should be gone; children should have been deleted.
	nlbList := &nlbv1.NLBList{}
	_ = fc.List(context.Background(), nlbList, client.InNamespace("default"))
	if len(nlbList.Items) != 0 {
		t.Errorf("expected all NLB CRs deleted, got %d", len(nlbList.Items))
	}
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	_ = fc.List(context.Background(), paList, client.InNamespace("default"))
	if len(paList.Items) != 0 {
		t.Errorf("expected all PAs deleted, got %d", len(paList.Items))
	}
	t.Fatalf("finalizer not removed after 10 reconciles")
}

func TestDeletion_WaitsForCloudNLBCascade(t *testing.T) {
	mock := provider.NewMockNLBClient()
	// Cloud NLB still exists — cascade not complete.
	mock.LoadBalancerExistsFunc = func(ctx context.Context, id string) (bool, error) {
		return true, nil
	}
	pool := newTestPool("pool")
	pool.Status.CloudNLBIds = []string{"lb-bgp-0"}
	s := newTestScheme(t)
	objs := deletionFixture(t, pool, s)

	fc := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, Recorder: record.NewFakeRecorder(100), NLBClient: mock}

	result, err := r.Reconcile(context.Background(), poolRequest(pool))
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while cloud NLB still exists")
	}

	// PAs must NOT be deleted while the cloud cascade is incomplete
	// (otherwise PA finalizers hit ResourceInUse).
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	_ = fc.List(context.Background(), paList, client.InNamespace("default"))
	if len(paList.Items) != 1 {
		t.Errorf("expected PA preserved while cloud NLBs exist, got %d PAs", len(paList.Items))
	}
}

// =====================================================
// Deletion safety boundary (dry-run + approval gate)
// =====================================================

func TestDeletion_ProtectionAnnotationBlocksCascade(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pool := newTestPool("pool")
	pool.Annotations = map[string]string{
		nlbpoolv1alpha1.AnnotationDeletionProtection: "true",
	}
	s := newTestScheme(t)
	objs := deletionFixture(t, pool, s)

	fc := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
	recorder := record.NewFakeRecorder(100)
	r := &NLBPoolReconciler{Client: fc, Scheme: s, Recorder: recorder, NLBClient: mock}

	result, err := r.Reconcile(context.Background(), poolRequest(pool))
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Error("expected requeue while deletion is blocked")
	}

	// Nothing may be deleted and the finalizer must stay.
	nlbList := &nlbv1.NLBList{}
	_ = fc.List(context.Background(), nlbList, client.InNamespace("default"))
	if len(nlbList.Items) != 1 {
		t.Errorf("expected NLB CR preserved under deletion protection, got %d", len(nlbList.Items))
	}
	paList := &nlbpoolv1alpha1.PortAllocationList{}
	_ = fc.List(context.Background(), paList, client.InNamespace("default"))
	if len(paList.Items) != 1 {
		t.Errorf("expected PA preserved under deletion protection, got %d", len(paList.Items))
	}
	got := &nlbpoolv1alpha1.NLBPool{}
	if err := fc.Get(context.Background(), poolRequest(pool).NamespacedName, got); err != nil {
		t.Fatalf("pool should still exist: %v", err)
	}
	if len(got.Finalizers) == 0 {
		t.Error("finalizer must not be removed while deletion is blocked")
	}

	// A DeletionBlocked event must be emitted.
	select {
	case ev := <-recorder.Events:
		if want := "DeletionBlocked"; !contains(ev, want) {
			t.Errorf("expected %q event, got %q", want, ev)
		}
	default:
		t.Error("expected a DeletionBlocked event")
	}
}

func TestDeletion_DryRunEmitsPlanWithoutDeleting(t *testing.T) {
	mock := provider.NewMockNLBClient()
	pool := newTestPool("pool")
	pool.Annotations = map[string]string{
		nlbpoolv1alpha1.AnnotationDeletionDryRun: "true",
	}
	s := newTestScheme(t)
	objs := deletionFixture(t, pool, s)

	fc := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
	recorder := record.NewFakeRecorder(100)
	r := &NLBPoolReconciler{Client: fc, Scheme: s, Recorder: recorder, NLBClient: mock}

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Children preserved.
	nlbList := &nlbv1.NLBList{}
	_ = fc.List(context.Background(), nlbList, client.InNamespace("default"))
	if len(nlbList.Items) != 1 {
		t.Errorf("dry-run must not delete NLB CRs, got %d remaining", len(nlbList.Items))
	}
	eipList := &eipv1.EIPList{}
	_ = fc.List(context.Background(), eipList, client.InNamespace("default"))
	if len(eipList.Items) != 1 {
		t.Errorf("dry-run must not delete EIP CRs, got %d remaining", len(eipList.Items))
	}

	// DeletionDryRun event contains the resource plan.
	select {
	case ev := <-recorder.Events:
		if !contains(ev, "DeletionDryRun") {
			t.Errorf("expected DeletionDryRun event, got %q", ev)
		}
		if !contains(ev, "1 NLB CR(s)") {
			t.Errorf("expected plan to list 1 NLB CR, got %q", ev)
		}
	default:
		t.Error("expected a DeletionDryRun event")
	}
}

func TestDeletion_NoAnnotationKeepsDefaultBehavior(t *testing.T) {
	// Default off: without annotations the cascade proceeds (GitOps semantics).
	mock := provider.NewMockNLBClient()
	mock.LoadBalancerExistsFunc = func(ctx context.Context, id string) (bool, error) {
		return false, nil
	}
	pool := newTestPool("pool")
	s := newTestScheme(t)
	objs := deletionFixture(t, pool, s)

	fc := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).WithStatusSubresource(objs...).Build()
	r := &NLBPoolReconciler{Client: fc, Scheme: s, Recorder: record.NewFakeRecorder(100), NLBClient: mock}

	if _, err := r.Reconcile(context.Background(), poolRequest(pool)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Infrastructure deletion must have been triggered on the first pass.
	nlbList := &nlbv1.NLBList{}
	_ = fc.List(context.Background(), nlbList, client.InNamespace("default"))
	if len(nlbList.Items) != 0 {
		t.Errorf("expected NLB CR deletion triggered without annotations, got %d remaining", len(nlbList.Items))
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
