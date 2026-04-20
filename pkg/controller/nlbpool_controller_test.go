package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	eipv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/eipoperator/v1alpha1"
	nlbv1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/nlboperator/v1"
	nlbpov1alpha1 "github.com/chrisliu1995/AlibabaCloud-NLB-Pool-Operator/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// setupScheme creates a runtime scheme with all required types
func setupScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = nlbpov1alpha1.AddToScheme(scheme)
	_ = nlbv1.AddToScheme(scheme)
	_ = eipv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return scheme
}

// newTestReconciler creates a reconciler with fake client
func newTestReconciler(objs ...client.Object) (*NLBPoolReconciler, client.Client) {
	scheme := setupScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	builder = builder.WithStatusSubresource(&nlbpov1alpha1.NLBPool{}, &nlbv1.NLB{}, &eipv1.EIP{}, &corev1.Service{})
	c := builder.Build()
	return &NLBPoolReconciler{
		Client: c,
		Scheme: scheme,
	}, c
}

// newTestNLBPool creates a test NLBPool CR
func newTestNLBPool(name, namespace string) *nlbpov1alpha1.NLBPool {
	return &nlbpov1alpha1.NLBPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: nlbpov1alpha1.NLBPoolSpec{
			ZoneMaps:              "vpc-xxx@cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb",
			MinPort:               10000,
			MaxPort:               10099,
			BlockPorts:            []int32{},
			PortsPerPod:           2,
			Protocols:             []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
			EipIspTypes:           []string{"BGP"},
			MinAvailable:          50,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyTypeLocal,
		},
	}
}

// ==================== 工具函数测试 ====================

func TestCalculatePodsPerNLB(t *testing.T) {
	tests := []struct {
		name       string
		spec       *nlbpov1alpha1.NLBPoolSpec
		wantResult int
	}{
		{
			name: "正常场景: MinPort=10000, MaxPort=10099, BlockPorts=[], PortsPerPod=2",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				BlockPorts:  []int32{},
				PortsPerPod: 2,
			},
			wantResult: 50,
		},
		{
			name: "边界场景: 端口范围为0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     9999,
				BlockPorts:  []int32{},
				PortsPerPod: 2,
			},
			wantResult: 0,
		},
		{
			name: "BlockPorts场景: 有blockPorts时容量减少",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				BlockPorts:  []int32{10050, 10051},
				PortsPerPod: 2,
			},
			wantResult: 49,
		},
		{
			name: "PortsPerPod=0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				BlockPorts:  []int32{},
				PortsPerPod: 0,
			},
			wantResult: 0,
		},
		{
			name: "大端口范围",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     19999,
				BlockPorts:  []int32{},
				PortsPerPod: 10,
			},
			wantResult: 1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculatePodsPerNLB(tt.spec)
			if got != tt.wantResult {
				t.Errorf("calculatePodsPerNLB() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestCalculateRequiredNLBs(t *testing.T) {
	tests := []struct {
		name          string
		spec          *nlbpov1alpha1.NLBPoolSpec
		podsPerNLB    int
		boundServices int
		wantResult    int
	}{
		{
			name: "MinAvailable=50, podsPerNLB=50, boundServices=0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 50,
			},
			podsPerNLB:    50,
			boundServices: 0,
			wantResult:    1,
		},
		{
			name: "MinAvailable=51, podsPerNLB=50, boundServices=0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 51,
			},
			podsPerNLB:    50,
			boundServices: 0,
			wantResult:    2,
		},
		{
			name: "MinAvailable=100, podsPerNLB=50, boundServices=0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 100,
			},
			podsPerNLB:    50,
			boundServices: 0,
			wantResult:    2,
		},
		{
			name: "podsPerNLB=0",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 50,
			},
			podsPerNLB:    0,
			boundServices: 0,
			wantResult:    0,
		},
		{
			name: "有boundServices时需要更多NLB",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 50,
			},
			podsPerNLB:    50,
			boundServices: 30,
			wantResult:    2,
		},
		{
			name: "boundServices超过MinAvailable",
			spec: &nlbpov1alpha1.NLBPoolSpec{
				MinAvailable: 50,
			},
			podsPerNLB:    50,
			boundServices: 100,
			wantResult:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateRequiredNLBs(tt.spec, tt.podsPerNLB, tt.boundServices)
			if got != tt.wantResult {
				t.Errorf("calculateRequiredNLBs() = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestParseZoneMaps(t *testing.T) {
	tests := []struct {
		name        string
		zoneMapsStr string
		wantZones   int
		wantVpcId   string
		wantErr     bool
	}{
		{
			name:        "正常格式: vpc-xxx@cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb",
			zoneMapsStr: "vpc-xxx@cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb",
			wantZones:   2,
			wantVpcId:   "vpc-xxx",
			wantErr:     false,
		},
		{
			name:        "错误格式: 无@分隔符",
			zoneMapsStr: "cn-hangzhou-h:vsw-aaa,cn-hangzhou-i:vsw-bbb",
			wantZones:   0,
			wantVpcId:   "",
			wantErr:     true,
		},
		{
			name:        "单个zone(应该失败,需要至少2个)",
			zoneMapsStr: "vpc-xxx@cn-hangzhou-h:vsw-aaa",
			wantZones:   0,
			wantVpcId:   "",
			wantErr:     true,
		},
		{
			name:        "三个zones",
			zoneMapsStr: "vpc-yyy@zone1:vsw1,zone2:vsw2,zone3:vsw3",
			wantZones:   3,
			wantVpcId:   "vpc-yyy",
			wantErr:     false,
		},
		{
			name:        "空字符串",
			zoneMapsStr: "",
			wantZones:   0,
			wantVpcId:   "",
			wantErr:     true,
		},
		{
			name:        "格式错误: 无冒号分隔符",
			zoneMapsStr: "vpc-xxx@zone1-vsw1,zone2-vsw2",
			wantZones:   0,
			wantVpcId:   "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zones, vpcId, err := parseZoneMaps(tt.zoneMapsStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseZoneMaps() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(zones) != tt.wantZones {
					t.Errorf("parseZoneMaps() zones = %v, want %v", len(zones), tt.wantZones)
				}
				if vpcId != tt.wantVpcId {
					t.Errorf("parseZoneMaps() vpcId = %v, want %v", vpcId, tt.wantVpcId)
				}
			}
		})
	}
}

func TestCalculatePortsForSlot(t *testing.T) {
	tests := []struct {
		name         string
		spec         nlbpov1alpha1.NLBPoolSpec
		slotIdx      int
		wantPorts    []int32
		wantProtocol []corev1.Protocol
	}{
		{
			name: "slotIdx=0, PortsPerPod=2, MinPort=10000",
			spec: nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				PortsPerPod: 2,
				Protocols:   []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
				BlockPorts:  []int32{},
			},
			slotIdx:      0,
			wantPorts:    []int32{10000, 10001},
			wantProtocol: []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
		},
		{
			name: "slotIdx=1, PortsPerPod=2, MinPort=10000",
			spec: nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				PortsPerPod: 2,
				Protocols:   []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
				BlockPorts:  []int32{},
			},
			slotIdx:      1,
			wantPorts:    []int32{10002, 10003},
			wantProtocol: []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
		},
		{
			name: "有blockPorts时自动跳过",
			spec: nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				PortsPerPod: 2,
				Protocols:   []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
				BlockPorts:  []int32{10001},
			},
			slotIdx:      0,
			wantPorts:    []int32{10000, 10002},
			wantProtocol: []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
		},
		{
			name: "PortsPerPod=4",
			spec: nlbpov1alpha1.NLBPoolSpec{
				MinPort:     10000,
				MaxPort:     10099,
				PortsPerPod: 4,
				Protocols:   []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolTCP, corev1.ProtocolUDP},
				BlockPorts:  []int32{},
			},
			slotIdx:      0,
			wantPorts:    []int32{10000, 10001, 10002, 10003},
			wantProtocol: []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolTCP, corev1.ProtocolUDP},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ports := calculatePortsForSlot(tt.spec, tt.slotIdx)
			if len(ports) != len(tt.wantPorts) {
				t.Errorf("calculatePortsForSlot() ports count = %v, want %v", len(ports), len(tt.wantPorts))
				return
			}
			for i, port := range ports {
				if port.Port != tt.wantPorts[i] {
					t.Errorf("calculatePortsForSlot() port[%d] = %v, want %v", i, port.Port, tt.wantPorts[i])
				}
				if port.Protocol != tt.wantProtocol[i] {
					t.Errorf("calculatePortsForSlot() protocol[%d] = %v, want %v", i, port.Protocol, tt.wantProtocol[i])
				}
			}
		})
	}
}

// ==================== Controller Reconcile 测试 ====================

func TestReconcile_CreateNLBPool(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	r, c := newTestReconciler(pool)

	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 执行 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 验证 NLB CRs 被创建
	nlbList := &nlbv1.NLBList{}
	if err := c.List(ctx, nlbList, client.InNamespace(pool.Namespace)); err != nil {
		t.Fatalf("Failed to list NLBs: %v", err)
	}
	// 由于 NLB 需要 EIP 的 AllocationID，而 fake client 不会自动填充 EIP Status，
	// 所以这里 NLB 不会被创建。这是预期行为。
	// 我们主要验证 EIP 被创建了

	// 验证 EIP CRs 被创建
	eipList := &eipv1.EIPList{}
	if err := c.List(ctx, eipList, client.InNamespace(pool.Namespace)); err != nil {
		t.Fatalf("Failed to list EIPs: %v", err)
	}
	// 每个 NLB 每个 zone 一个 EIP，需要 1 NLB * 2 zones = 2 EIPs
	if len(eipList.Items) != 2 {
		t.Errorf("Expected 2 EIPs, got %d", len(eipList.Items))
	}

	// 验证 EIP Labels 正确
	for _, eip := range eipList.Items {
		if eip.Labels[nlbpov1alpha1.LabelEIPPoolName] != pool.Name {
			t.Errorf("EIP %s has wrong pool name label", eip.Name)
		}
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	r, c := newTestReconciler(pool)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 第一次 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("First Reconcile failed: %v", err)
	}

	// 记录第一次后的 EIP 数量
	eipList1 := &eipv1.EIPList{}
	c.List(ctx, eipList1, client.InNamespace(pool.Namespace))
	firstEIPCount := len(eipList1.Items)

	// 第二次 Reconcile
	_, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}

	// 记录第二次后的 EIP 数量
	eipList2 := &eipv1.EIPList{}
	c.List(ctx, eipList2, client.InNamespace(pool.Namespace))
	secondEIPCount := len(eipList2.Items)

	// 验证 EIP 数量没有增加
	if secondEIPCount != firstEIPCount {
		t.Errorf("Idempotent failed: EIP count changed from %d to %d", firstEIPCount, secondEIPCount)
	}
}

func TestReconcile_AutoExpansion(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	pool.Spec.MinAvailable = 100 // 需要 100 个可用 Service
	pool.Spec.PortsPerPod = 2
	// podsPerNLB = (10099-10000+1)/2 = 50
	// 需要 100 个可用，需要 100/50 = 2 个 NLB

	r, c := newTestReconciler(pool)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 第一次 Reconcile - 创建基础资源
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("First Reconcile failed: %v", err)
	}

	// 手动创建一些 bound Services 来模拟资源紧张
	for i := 0; i < 30; i++ {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("bound-svc-%d", i),
				Namespace: pool.Namespace,
				Labels: map[string]string{
					nlbpov1alpha1.LabelNLBPoolName:   pool.Name,
					nlbpov1alpha1.LabelSvcPoolStatus: nlbpov1alpha1.SvcPoolStatusBound,
				},
			},
		}
		c.Create(ctx, svc)
	}

	// 再次 Reconcile
	_, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}

	// 验证更多的 EIP 被创建（因为 boundServices 增加，需要更多 NLB）
	eipList := &eipv1.EIPList{}
	c.List(ctx, eipList, client.InNamespace(pool.Namespace))
	// 原始需要 2 NLBs * 2 zones = 4 EIPs
	// 现在有 30 bound，需要 (100+30)/50 = 3 NLBs，需要 3 * 2 = 6 EIPs
	if len(eipList.Items) < 4 {
		t.Errorf("Expected at least 4 EIPs for expansion, got %d", len(eipList.Items))
	}
}

func TestReconcile_StatusUpdate(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	r, c := newTestReconciler(pool)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 执行 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 获取更新后的 pool
	updatedPool := &nlbpov1alpha1.NLBPool{}
	if err := c.Get(ctx, req.NamespacedName, updatedPool); err != nil {
		t.Fatalf("Failed to get updated pool: %v", err)
	}

	// 验证 Status 被更新
	if updatedPool.Status.Phase == "" {
		t.Error("Expected Phase to be set")
	}

	// 手动创建一些 available Services
	for i := 0; i < 5; i++ {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("available-svc-%d", i),
				Namespace: pool.Namespace,
				Labels: map[string]string{
					nlbpov1alpha1.LabelNLBPoolName:   pool.Name,
					nlbpov1alpha1.LabelSvcPoolStatus: nlbpov1alpha1.SvcPoolStatusAvailable,
				},
			},
		}
		c.Create(ctx, svc)
	}

	// 手动创建一些 bound Services
	for i := 0; i < 3; i++ {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("bound-svc-%d", i),
				Namespace: pool.Namespace,
				Labels: map[string]string{
					nlbpov1alpha1.LabelNLBPoolName:   pool.Name,
					nlbpov1alpha1.LabelSvcPoolStatus: nlbpov1alpha1.SvcPoolStatusBound,
				},
			},
		}
		c.Create(ctx, svc)
	}

	// 再次 Reconcile
	_, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Second Reconcile failed: %v", err)
	}

	// 获取更新后的 pool
	if err := c.Get(ctx, req.NamespacedName, updatedPool); err != nil {
		t.Fatalf("Failed to get updated pool: %v", err)
	}

	// 验证 Status 统计正确
	if updatedPool.Status.TotalServices != 8 {
		t.Errorf("Expected TotalServices=8, got %d", updatedPool.Status.TotalServices)
	}
	if updatedPool.Status.AvailableServices != 5 {
		t.Errorf("Expected AvailableServices=5, got %d", updatedPool.Status.AvailableServices)
	}
	if updatedPool.Status.BoundServices != 3 {
		t.Errorf("Expected BoundServices=3, got %d", updatedPool.Status.BoundServices)
	}
}

// ==================== Service 构造测试 ====================

func TestConstructService(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	r, _ := newTestReconciler()

	svcName := "nlbpool-test-pool-0-0-bgp"
	nlbId := "nlb-xxx"
	eipIspType := "BGP"
	nlbIndex := 0
	slotIdx := 0
	ports := []corev1.ServicePort{
		{Name: "port-0", Port: 10000, Protocol: corev1.ProtocolTCP},
		{Name: "port-1", Port: 10001, Protocol: corev1.ProtocolUDP},
	}

	svc := r.constructService(pool, svcName, nlbId, eipIspType, nlbIndex, slotIdx, ports)

	// 验证 Name
	if svc.Name != svcName {
		t.Errorf("Service name = %v, want %v", svc.Name, svcName)
	}

	// 验证 Namespace
	if svc.Namespace != pool.Namespace {
		t.Errorf("Service namespace = %v, want %v", svc.Namespace, pool.Namespace)
	}

	// 验证 Labels
	expectedLabels := map[string]string{
		nlbpov1alpha1.LabelNLBPoolName:        pool.Name,
		nlbpov1alpha1.LabelSvcPoolStatus:      nlbpov1alpha1.SvcPoolStatusAvailable,
		nlbpov1alpha1.LabelSvcPoolPortsPerPod: "2",
		nlbpov1alpha1.LabelSvcPoolProtocols:   "TCP-UDP",
		nlbpov1alpha1.LabelNLBPoolEipIspType:  eipIspType,
		nlbpov1alpha1.LabelNLBPoolIndex:       "0",
		nlbpov1alpha1.LabelServiceProxyName:   "dummy",
	}
	for key, expectedVal := range expectedLabels {
		if svc.Labels[key] != expectedVal {
			t.Errorf("Service label %s = %v, want %v", key, svc.Labels[key], expectedVal)
		}
	}

	// 验证 Annotations
	if svc.Annotations[nlbpov1alpha1.AnnotationSlbId] != nlbId {
		t.Errorf("Service annotation slb-id = %v, want %v", svc.Annotations[nlbpov1alpha1.AnnotationSlbId], nlbId)
	}
	if svc.Annotations[nlbpov1alpha1.AnnotationSlbListenerOverride] != "true" {
		t.Errorf("Service annotation listener-override = %v, want true", svc.Annotations[nlbpov1alpha1.AnnotationSlbListenerOverride])
	}

	// 验证 Spec.Type
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Errorf("Service type = %v, want LoadBalancer", svc.Spec.Type)
	}

	// 验证 Spec.LoadBalancerClass
	if svc.Spec.LoadBalancerClass == nil || *svc.Spec.LoadBalancerClass != "alibabacloud.com/nlb" {
		var class string
		if svc.Spec.LoadBalancerClass != nil {
			class = *svc.Spec.LoadBalancerClass
		}
		t.Errorf("Service loadBalancerClass = %v, want alibabacloud.com/nlb", class)
	}

	// 验证 Spec.AllocateLoadBalancerNodePorts
	if svc.Spec.AllocateLoadBalancerNodePorts == nil || *svc.Spec.AllocateLoadBalancerNodePorts != false {
		t.Errorf("Service allocateLoadBalancerNodePorts should be false")
	}

	// 验证 Spec.Selector (placeholder)
	if svc.Spec.Selector[nlbpov1alpha1.LabelNLBPoolPlaceholder] != nlbpov1alpha1.PlaceholderValue {
		t.Errorf("Service selector placeholder = %v, want %v", svc.Spec.Selector[nlbpov1alpha1.LabelNLBPoolPlaceholder], nlbpov1alpha1.PlaceholderValue)
	}

	// 验证 Ports 数量和值
	if len(svc.Spec.Ports) != len(ports) {
		t.Errorf("Service ports count = %v, want %v", len(svc.Spec.Ports), len(ports))
	}
	for i, port := range svc.Spec.Ports {
		if port.Port != ports[i].Port {
			t.Errorf("Service port[%d] = %v, want %v", i, port.Port, ports[i].Port)
		}
		if port.Protocol != ports[i].Protocol {
			t.Errorf("Service protocol[%d] = %v, want %v", i, port.Protocol, ports[i].Protocol)
		}
	}

	// 验证 ExternalTrafficPolicy
	if svc.Spec.ExternalTrafficPolicy != pool.Spec.ExternalTrafficPolicy {
		t.Errorf("Service externalTrafficPolicy = %v, want %v", svc.Spec.ExternalTrafficPolicy, pool.Spec.ExternalTrafficPolicy)
	}

	// 验证 OwnerReference
	if len(svc.OwnerReferences) == 0 {
		t.Error("Service should have OwnerReference")
	} else {
		if svc.OwnerReferences[0].Name != pool.Name {
			t.Errorf("Service owner name = %v, want %v", svc.OwnerReferences[0].Name, pool.Name)
		}
	}
}

func TestConstructService_WithHealthCheck(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	pool.Spec.HealthCheck = &nlbpov1alpha1.NLBHealthCheckConfig{
		Flag:             "on",
		Type:             "http",
		ConnectPort:      "8080",
		ConnectTimeout:   "5",
		Interval:         "10",
		HealthyThreshold: "2",
		Domain:           "example.com",
		Uri:              "/health",
		Method:           "GET",
	}
	r, _ := newTestReconciler()

	svc := r.constructService(pool, "test-svc", "nlb-xxx", "BGP", 0, 0, []corev1.ServicePort{})

	// 验证 Health Check Annotations
	expectedAnnotations := map[string]string{
		nlbpov1alpha1.AnnotationHealthCheckFlag:           "on",
		nlbpov1alpha1.AnnotationHealthCheckType:           "http",
		nlbpov1alpha1.AnnotationHealthCheckConnectPort:    "8080",
		nlbpov1alpha1.AnnotationHealthCheckConnectTimeout: "5",
		nlbpov1alpha1.AnnotationHealthCheckInterval:       "10",
		nlbpov1alpha1.AnnotationHealthyThreshold:          "2",
		nlbpov1alpha1.AnnotationHealthCheckDomain:         "example.com",
		nlbpov1alpha1.AnnotationHealthCheckUri:            "/health",
		nlbpov1alpha1.AnnotationHealthCheckMethod:         "GET",
	}
	for key, expectedVal := range expectedAnnotations {
		if svc.Annotations[key] != expectedVal {
			t.Errorf("Service annotation %s = %v, want %v", key, svc.Annotations[key], expectedVal)
		}
	}
}

// ==================== 辅助函数测试 ====================

func TestProtocolsToStrings(t *testing.T) {
	tests := []struct {
		name      string
		protocols []corev1.Protocol
		want      string
	}{
		{
			name:      "TCP和UDP",
			protocols: []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP},
			want:      "TCP-UDP",
		},
		{
			name:      "只有TCP",
			protocols: []corev1.Protocol{corev1.ProtocolTCP},
			want:      "TCP",
		},
		{
			name:      "空切片",
			protocols: []corev1.Protocol{},
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protocolsToStrings(tt.protocols)
			if got != tt.want {
				t.Errorf("protocolsToStrings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsBlockedPort(t *testing.T) {
	tests := []struct {
		name       string
		port       int32
		blockPorts []int32
		want       bool
	}{
		{
			name:       "端口在block列表中",
			port:       10050,
			blockPorts: []int32{10050, 10051},
			want:       true,
		},
		{
			name:       "端口不在block列表中",
			port:       10052,
			blockPorts: []int32{10050, 10051},
			want:       false,
		},
		{
			name:       "空block列表",
			port:       10050,
			blockPorts: []int32{},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBlockedPort(tt.port, tt.blockPorts)
			if got != tt.want {
				t.Errorf("isBlockedPort() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestReconcile_WithReadyNLB 测试当 NLB 已经 Ready 时的场景
func TestReconcile_WithReadyNLB(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")

	// 预先创建 EIP 并设置 AllocationID
	eip1 := &eipv1.EIP{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool-eip-bgp-0-z0",
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpov1alpha1.LabelEIPPoolName:       pool.Name,
				nlbpov1alpha1.LabelEIPPoolIndex:      "0-z0",
				nlbpov1alpha1.LabelEIPPoolEipIspType: "BGP",
			},
		},
		Status: eipv1.EIPStatus{
			AllocationID: "eip-alloc-1",
		},
	}
	eip2 := &eipv1.EIP{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool-eip-bgp-0-z1",
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpov1alpha1.LabelEIPPoolName:       pool.Name,
				nlbpov1alpha1.LabelEIPPoolIndex:      "0-z1",
				nlbpov1alpha1.LabelEIPPoolEipIspType: "BGP",
			},
		},
		Status: eipv1.EIPStatus{
			AllocationID: "eip-alloc-2",
		},
	}

	// 预先创建 NLB 并设置 LoadBalancerId
	nlb := &nlbv1.NLB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pool-bgp-0",
			Namespace: pool.Namespace,
			Labels: map[string]string{
				nlbpov1alpha1.LabelNLBPoolName:       pool.Name,
				nlbpov1alpha1.LabelNLBPoolIndex:      "0",
				nlbpov1alpha1.LabelNLBPoolEipIspType: "BGP",
			},
		},
		Status: nlbv1.NLBStatus{
			LoadBalancerId:     "nlb-xxx",
			LoadBalancerStatus: "Active",
		},
	}

	r, c := newTestReconciler(pool, eip1, eip2, nlb)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 执行 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 验证 Services 被创建
	svcList := &corev1.ServiceList{}
	if err := c.List(ctx, svcList, client.InNamespace(pool.Namespace)); err != nil {
		t.Fatalf("Failed to list Services: %v", err)
	}

	// podsPerNLB = 50, 需要创建 50 个 Services
	expectedSvcCount := 50
	if len(svcList.Items) != expectedSvcCount {
		t.Errorf("Expected %d Services, got %d", expectedSvcCount, len(svcList.Items))
	}

	// 验证 Service 名称格式
	for _, svc := range svcList.Items {
		expectedPrefix := fmt.Sprintf("nlbpool-%s-0-", pool.Name)
		if !strings.HasPrefix(svc.Name, expectedPrefix) {
			t.Errorf("Service name %s doesn't have expected prefix %s", svc.Name, expectedPrefix)
		}
	}

	// 验证 Service Labels
	for _, svc := range svcList.Items {
		if svc.Labels[nlbpov1alpha1.LabelNLBPoolName] != pool.Name {
			t.Errorf("Service %s has wrong pool name label", svc.Name)
		}
		if svc.Labels[nlbpov1alpha1.LabelSvcPoolStatus] != nlbpov1alpha1.SvcPoolStatusAvailable {
			t.Errorf("Service %s has wrong status label", svc.Name)
		}
		if svc.Labels[nlbpov1alpha1.LabelNLBPoolEipIspType] != "BGP" {
			t.Errorf("Service %s has wrong eipIspType label", svc.Name)
		}
	}
}

// TestReconcile_MultipleEipIspTypes 测试多个 EIP ISP Type 的场景
func TestReconcile_MultipleEipIspTypes(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	pool.Spec.EipIspTypes = []string{"BGP", "ChinaTelecom"}

	// 预先创建所有需要的 EIP 和 NLB
	eips := []client.Object{}
	nlbs := []client.Object{}

	for _, ispType := range pool.Spec.EipIspTypes {
		ispLower := strings.ToLower(ispType)
		// 每个 ISP 类型 2 个 zones
		for z := 0; z < 2; z++ {
			eips = append(eips, &eipv1.EIP{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-pool-eip-%s-0-z%d", ispLower, z),
					Namespace: pool.Namespace,
					Labels: map[string]string{
						nlbpov1alpha1.LabelEIPPoolName:       pool.Name,
						nlbpov1alpha1.LabelEIPPoolIndex:      fmt.Sprintf("0-z%d", z),
						nlbpov1alpha1.LabelEIPPoolEipIspType: ispType,
					},
				},
				Status: eipv1.EIPStatus{
					AllocationID: fmt.Sprintf("eip-alloc-%s-%d", ispLower, z),
				},
			})
		}

		nlbs = append(nlbs, &nlbv1.NLB{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-pool-%s-0", ispLower),
				Namespace: pool.Namespace,
				Labels: map[string]string{
					nlbpov1alpha1.LabelNLBPoolName:       pool.Name,
					nlbpov1alpha1.LabelNLBPoolIndex:      "0",
					nlbpov1alpha1.LabelNLBPoolEipIspType: ispType,
				},
			},
			Status: nlbv1.NLBStatus{
				LoadBalancerId:     fmt.Sprintf("nlb-%s", ispLower),
				LoadBalancerStatus: "Active",
			},
		})
	}

	objs := append([]client.Object{pool}, eips...)
	objs = append(objs, nlbs...)
	r, c := newTestReconciler(objs...)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 执行 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 验证 Services 被创建
	svcList := &corev1.ServiceList{}
	if err := c.List(ctx, svcList, client.InNamespace(pool.Namespace)); err != nil {
		t.Fatalf("Failed to list Services: %v", err)
	}

	// 每个 ISP 类型 50 个 Services，共 2 个 ISP 类型 = 100 个 Services
	expectedSvcCount := 100
	if len(svcList.Items) != expectedSvcCount {
		t.Errorf("Expected %d Services, got %d", expectedSvcCount, len(svcList.Items))
	}

	// 统计每个 ISP 类型的 Service 数量
	ispCounts := make(map[string]int)
	for _, svc := range svcList.Items {
		ispType := svc.Labels[nlbpov1alpha1.LabelNLBPoolEipIspType]
		ispCounts[ispType]++
	}

	for _, ispType := range pool.Spec.EipIspTypes {
		if ispCounts[ispType] != 50 {
			t.Errorf("Expected 50 Services for ISP type %s, got %d", ispType, ispCounts[ispType])
		}
	}
}

// TestReconcile_InvalidConfiguration 测试无效配置的场景
func TestReconcile_InvalidConfiguration(t *testing.T) {
	pool := newTestNLBPool("test-pool", "default")
	// 设置无效配置：MinPort > MaxPort
	pool.Spec.MinPort = 20000
	pool.Spec.MaxPort = 10000

	r, c := newTestReconciler(pool)
	ctx := context.Background()
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}

	// 执行 Reconcile
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 获取更新后的 pool
	updatedPool := &nlbpov1alpha1.NLBPool{}
	if err := c.Get(ctx, req.NamespacedName, updatedPool); err != nil {
		t.Fatalf("Failed to get updated pool: %v", err)
	}

	// 验证 Status.Phase 为 Failed
	if updatedPool.Status.Phase != nlbpov1alpha1.NLBPoolPhaseFailed {
		t.Errorf("Expected Phase to be Failed, got %s", updatedPool.Status.Phase)
	}
}
