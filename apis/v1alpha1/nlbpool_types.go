package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NLBPoolPhase defines the phase of NLBPool
type NLBPoolPhase string

const (
	NLBPoolPhasePending NLBPoolPhase = "Pending"
	NLBPoolPhaseReady   NLBPoolPhase = "Ready"
	NLBPoolPhaseFailed  NLBPoolPhase = "Failed"
)

// NLBPoolSpec defines the desired state of NLBPool
type NLBPoolSpec struct {
	// ZoneMaps defines VPC and zone mapping
	// Format: "vpc-xxx@zone1:vswitch1,zone2:vswitch2"
	ZoneMaps string `json:"zoneMaps"`

	// MinPort is the minimum port for NLB Listener
	MinPort int32 `json:"minPort"`

	// MaxPort is the maximum port for NLB Listener
	MaxPort int32 `json:"maxPort"`

	// BlockPorts is the list of ports to skip
	// +optional
	BlockPorts []int32 `json:"blockPorts,omitempty"`

	// PortsPerPod is the number of ports required per Pod
	PortsPerPod int `json:"portsPerPod"`

	// Protocols is the list of protocols for each port
	Protocols []corev1.Protocol `json:"protocols"`

	// EipIspTypes is the list of EIP ISP types
	// e.g.: BGP, BGP_PRO, ChinaTelecom, ChinaUnicom, ChinaMobile
	// +optional
	EipIspTypes []string `json:"eipIspTypes,omitempty"`

	// MinAvailable is the minimum number of available (unbound) Services to maintain
	// When available count drops below this value, Controller automatically creates new NLBs
	MinAvailable int `json:"minAvailable"`

	// ExternalTrafficPolicy defines the traffic policy for Service
	// +optional
	ExternalTrafficPolicy corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`

	// HealthCheck defines NLB health check configuration
	// +optional
	HealthCheck *NLBHealthCheckConfig `json:"healthCheck,omitempty"`

	// SecurityProtectionTypes defines EIP security protection types
	// +optional
	SecurityProtectionTypes []string `json:"securityProtectionTypes,omitempty"`
}

// NLBHealthCheckConfig defines health check configuration
type NLBHealthCheckConfig struct {
	// Flag is the health check switch (on/off)
	Flag string `json:"flag,omitempty"`
	// Type is the health check type (tcp/http)
	Type string `json:"type,omitempty"`
	// ConnectPort is the health check port
	ConnectPort string `json:"connectPort,omitempty"`
	// ConnectTimeout is the connection timeout
	ConnectTimeout string `json:"connectTimeout,omitempty"`
	// Interval is the check interval
	Interval string `json:"interval,omitempty"`
	// Uri is the HTTP health check path
	Uri string `json:"uri,omitempty"`
	// Domain is the HTTP health check domain
	Domain string `json:"domain,omitempty"`
	// Method is the HTTP health check method
	Method string `json:"method,omitempty"`
	// HealthyThreshold is the healthy threshold
	HealthyThreshold string `json:"healthyThreshold,omitempty"`
	// UnhealthyThreshold is the unhealthy threshold
	UnhealthyThreshold string `json:"unhealthyThreshold,omitempty"`
}

// NLBPoolStatus defines the observed state of NLBPool
type NLBPoolStatus struct {
	// Phase is the current phase of NLBPool
	Phase NLBPoolPhase `json:"phase,omitempty"`

	// TotalNLBs is the total number of NLB instances
	TotalNLBs int `json:"totalNLBs"`

	// ReadyNLBs is the number of ready NLB instances
	ReadyNLBs int `json:"readyNLBs"`

	// TotalServices is the total number of pre-warmed Services
	TotalServices int `json:"totalServices"`

	// AvailableServices is the number of available (unbound) Services
	AvailableServices int `json:"availableServices"`

	// BoundServices is the number of bound Services
	BoundServices int `json:"boundServices"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="NLBs",type=integer,JSONPath=`.status.totalNLBs`
// +kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableServices`
// +kubebuilder:printcolumn:name="Bound",type=integer,JSONPath=`.status.boundServices`

// NLBPool is the Schema for the nlbpools API
type NLBPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NLBPoolSpec   `json:"spec,omitempty"`
	Status NLBPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NLBPoolList contains a list of NLBPool
type NLBPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NLBPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NLBPool{}, &NLBPoolList{})
}
