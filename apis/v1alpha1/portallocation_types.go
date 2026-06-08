package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PortAllocationPhase
type PortAllocationPhase string

const (
	PortAllocationProvisioning PortAllocationPhase = "Provisioning"
	PortAllocationAvailable    PortAllocationPhase = "Available"
	PortAllocationBinding      PortAllocationPhase = "Binding"
	PortAllocationBound        PortAllocationPhase = "Bound"
	PortAllocationReleasing    PortAllocationPhase = "Releasing"
	PortAllocationDisabled     PortAllocationPhase = "Disabled"
)

// ServerGroupRef 引用SG（V6: 不再引用 CR name，直接持有云端 SG ID）
type ServerGroupRef struct {
	// Name 端口名称（对应 pool.spec.ports[].name）
	Name string `json:"name"`
	// ServerGroupId 云端 SG ID
	ServerGroupId string `json:"serverGroupId"`
}

// EndpointPort lane上某端口的信息
type EndpointPort struct {
	// Name 端口名称
	Name string `json:"name"`
	// ListenerPort 监听端口
	ListenerPort int32 `json:"listenerPort"`
	// ContainerPort 容器端口（后端服务器真实监听端口）
	ContainerPort int32 `json:"containerPort"`
	// Protocol 协议
	Protocol string `json:"protocol"`
	// ListenerId 云端 Listener ID（V6: 不再引用 CR name）
	ListenerId string `json:"listenerId,omitempty"`
}

// ServerGroupCloudStatus 记录一个 SG 的云端状态
type ServerGroupCloudStatus struct {
	// Name 对应 pool.spec.ports[].name
	Name string `json:"name"`
	// ServerGroupId 云端 SG ID
	ServerGroupId string `json:"serverGroupId,omitempty"`
	// Phase: Pending / Creating / Active
	Phase string `json:"phase,omitempty"`
}

// ListenerCloudStatus 记录一个 Listener 的云端状态
type ListenerCloudStatus struct {
	// PortName 对应 pool.spec.ports[].name
	PortName string `json:"portName"`
	// LaneName 对应 pool.spec.lanes[].name
	LaneName string `json:"laneName"`
	// ListenerId 云端 Listener ID
	ListenerId string `json:"listenerId,omitempty"`
	// ListenerPort NLB 上的监听端口
	ListenerPort int32 `json:"listenerPort"`
	// Phase: Pending / Creating / Running
	Phase string `json:"phase,omitempty"`
}

// LaneEndpoint 每条lane的接入端点
type LaneEndpoint struct {
	// Lane lane名称
	Lane string `json:"lane"`
	// EIP 公网IP
	EIP string `json:"eip"`
	// Ports 该lane上分配的监听端口
	Ports []EndpointPort `json:"ports"`
}

// PortAllocationSpec defines the desired state of PortAllocation
type PortAllocationSpec struct {
	// BoundPod 当前绑定的Pod名称（PA Controller写入）
	BoundPod string `json:"boundPod,omitempty"`
	// BoundPodIP Pod IP
	BoundPodIP string `json:"boundPodIP,omitempty"`
	// ServerGroups 引用SG CR列表（NLBPool Controller填充）
	ServerGroups []ServerGroupRef `json:"serverGroups,omitempty"`
	// Endpoints 每条lane的接入端点（NLBPool Controller填充）
	Endpoints []LaneEndpoint `json:"endpoints,omitempty"`
}

// ExternalAddressPort 公网地址端口
type ExternalAddressPort struct {
	// Name 端口名称
	Name string `json:"name"`
	// Port 端口号
	Port int32 `json:"port"`
	// Protocol 协议
	Protocol string `json:"protocol"`
}

// ExternalAddress 公网地址（供kruise-game读取）
type ExternalAddress struct {
	// Lane lane名称
	Lane string `json:"lane"`
	// IP 公网IP
	IP string `json:"ip"`
	// Ports 端口列表
	Ports []ExternalAddressPort `json:"ports"`
}

// PortAllocationStatus defines the observed state of PortAllocation
type PortAllocationStatus struct {
	// Phase 当前阶段
	Phase PortAllocationPhase `json:"phase,omitempty"`
	// ExternalAddresses Bound时填充，供kruise-game读取
	ExternalAddresses []ExternalAddress `json:"externalAddresses,omitempty"`
	// Message 附加诊断信息
	Message string `json:"message,omitempty"`
	// ServerGroups 记录每个端口对应的云端 SG 信息（V6: PA 直管云资源）
	ServerGroups []ServerGroupCloudStatus `json:"serverGroups,omitempty"`
	// Listeners 记录每个 (port, lane) 对应的云端 Listener 信息
	Listeners []ListenerCloudStatus `json:"listeners,omitempty"`
	// RegisteredSGs 记录已成功调用 AddServer 的 SG ID 集合（Binding 阶段跳过已注册的 SG）
	RegisteredSGs []string `json:"registeredSGs,omitempty"`
	// SGsReady 已就绪的 SG 数量
	SGsReady int32 `json:"sgsReady,omitempty"`
	// SGsTotal 总 SG 数量
	SGsTotal int32 `json:"sgsTotal,omitempty"`
	// ListenersReady 已就绪的 Listener 数量
	ListenersReady int32 `json:"listenersReady,omitempty"`
	// ListenersTotal 总 Listener 数量
	ListenersTotal int32 `json:"listenersTotal,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="BoundPod",type=string,JSONPath=`.spec.boundPod`
// +kubebuilder:printcolumn:name="SGs",type=string,JSONPath=`.status.sgsReady`
// +kubebuilder:printcolumn:name="SGsTotal",type=string,JSONPath=`.status.sgsTotal`
// +kubebuilder:printcolumn:name="Listeners",type=string,JSONPath=`.status.listenersReady`
// +kubebuilder:printcolumn:name="ListenersTotal",type=string,JSONPath=`.status.listenersTotal`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PortAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PortAllocationSpec   `json:"spec,omitempty"`
	Status PortAllocationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PortAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PortAllocation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PortAllocation{}, &PortAllocationList{})
}
