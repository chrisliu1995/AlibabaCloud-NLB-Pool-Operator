package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NLBPoolPhase
type NLBPoolPhase string

const (
	NLBPoolPending      NLBPoolPhase = "Pending"
	NLBPoolProvisioning NLBPoolPhase = "Provisioning"
	NLBPoolReady        NLBPoolPhase = "Ready"
	NLBPoolDeleting     NLBPoolPhase = "Deleting"
	NLBPoolFailed       NLBPoolPhase = "Failed"
)

// LaneConfig 每条线路配置
type LaneConfig struct {
	// Name lane名称，用于CR命名
	Name string `json:"name"`
	// ISPType 线路类型: BGP, BGP_PRO, ChinaTelecom, ChinaUnicom, ChinaMobile
	ISPType string `json:"ispType"`
	// Bandwidth EIP 带宽峰值，单位 Mbps。
	// BGP/BGP_PRO（PayByTraffic）：未填时云端默认 5 Mbps 峰值。
	// 单线 ISP（PayByBandwidth）：未填时默认 200 Mbps。
	// +optional
	Bandwidth string `json:"bandwidth,omitempty"`
	// BandwidthPackageId 共享带宽包 ID（CommonBandwidthPackage）。
	// BGP lane：NLB 跳过 EIP CR，自动建 BGP PayByTraffic EIP 加入带宽包（纯 B 路径）。
	// 单线 ISP lane：Operator 先建独立 EIP CR（PayByBandwidth），NLB 同时传
	//   AllocationId + BandwidthPackageId，由 NLB 将 EIP 加入带宽包（A+B 混合路径）。
	// 带宽包的 ISP 必须与 lane.ISPType 一致。
	// +optional
	BandwidthPackageId string `json:"bandwidthPackageId,omitempty"`
	// SecurityProtectionTypes EIP 安全防护类型，例如 ["AntiDDoS_Enhanced"]。
	// +optional
	SecurityProtectionTypes []string `json:"securityProtectionTypes,omitempty"`
}

// PortConfig 每Pod暴露的逻辑端口
type PortConfig struct {
	// Name 端口名称，用于CR命名
	Name string `json:"name"`
	// Protocol 协议: TCP, UDP, TCPSSL
	Protocol string `json:"protocol"`
	// ContainerPort 容器暴露的端口（后端服务器真实监听端口）
	ContainerPort int32 `json:"containerPort,omitempty"`
}

// PortRange Listener端口范围
type PortRange struct {
	// Min 最小端口
	Min int32 `json:"min"`
	// Max 最大端口
	Max int32 `json:"max"`
}

// ZoneMapEntry 可用区映射
type ZoneMapEntry struct {
	// Zone 可用区ID
	Zone string `json:"zone"`
	// VSwitchId 交换机ID
	VSwitchId string `json:"vswitchId"`
}

// NLBHealthCheckConfig 健康检查配置
type NLBHealthCheckConfig struct {
	// Enabled 是否启用
	Enabled bool `json:"enabled"`
}

// NLBPoolSpec defines the desired state of NLBPool
type NLBPoolSpec struct {
	// Region 阿里云区域
	Region string `json:"region"`
	// VpcId VPC ID
	VpcId string `json:"vpcId"`
	// ZoneMaps NLB需要的可用区和交换机
	ZoneMaps []ZoneMapEntry `json:"zoneMaps"`
	// Lanes 每条线路配置（取代eipIspTypes数组）
	Lanes []LaneConfig `json:"lanes"`
	// Ports 每Pod暴露的逻辑端口（取代portsPerPod + protocols）
	Ports []PortConfig `json:"ports"`
	// PortRange Listener端口范围
	PortRange PortRange `json:"portRange"`
	// SlotsPerNLB 每个NLB实例承载的slot数
	SlotsPerNLB int32 `json:"slotsPerNLB"`
	// MinAvailableNLBs 最小空闲NLB实例数（以NLB为粒度预热）
	MinAvailableNLBs int32 `json:"minAvailableNLBs"`
	// HealthCheck NLB健康检查配置
	HealthCheck *NLBHealthCheckConfig `json:"healthCheck,omitempty"`
}

// LaneStatus 每条lane的状态
type LaneStatus struct {
	// Name lane名称
	Name string `json:"name"`
	// NLBId 云端NLB ID
	NLBId string `json:"nlbId,omitempty"`
	// EIP 公网IP
	EIP string `json:"eip,omitempty"`
	// Ready 是否就绪
	Ready bool `json:"ready"`
}

// NLBPoolStatus defines the observed state of NLBPool
type NLBPoolStatus struct {
	// Phase 当前阶段
	Phase NLBPoolPhase `json:"phase,omitempty"`
	// Lanes 每条lane的状态
	Lanes []LaneStatus `json:"lanes,omitempty"`
	// NLBsPerLane 每条lane当前的NLB数
	NLBsPerLane int32 `json:"nlbsPerLane,omitempty"`
	// TotalSlots 总slot数
	TotalSlots int32 `json:"totalSlots,omitempty"`
	// AvailableSlots 可用slot数
	AvailableSlots int32 `json:"availableSlots,omitempty"`
	// BoundSlots 已绑定slot数
	BoundSlots int32 `json:"boundSlots,omitempty"`
	// Message 附加信息
	Message string `json:"message,omitempty"`
	// CloudNLBIds stores cloud NLB instance IDs collected before deletion,
	// used to verify cloud-side cascade deletion is complete before removing PAs.
	CloudNLBIds []string `json:"cloudNLBIds,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.totalSlots`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableSlots`
// +kubebuilder:printcolumn:name="Bound",type=integer,JSONPath=`.status.boundSlots`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NLBPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NLBPoolSpec   `json:"spec,omitempty"`
	Status NLBPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type NLBPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NLBPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NLBPool{}, &NLBPoolList{})
}
