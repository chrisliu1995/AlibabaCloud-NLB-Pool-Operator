package v1alpha1

const (
	// NLBPool related Labels
	LabelNLBPoolName        = "game.kruise.io/nlb-pool-name"
	LabelSvcPoolStatus      = "game.kruise.io/svc-pool-status"
	LabelSvcPoolPortsPerPod = "game.kruise.io/svc-pool-ports-per-pod"
	LabelSvcPoolProtocols   = "game.kruise.io/svc-pool-protocols"
	LabelSvcPoolBoundPod    = "game.kruise.io/svc-pool-bound-pod"
	LabelSvcPoolBoundGss    = "game.kruise.io/svc-pool-bound-gss"
	LabelNLBPoolIndex       = "game.kruise.io/nlb-pool-index"
	LabelNLBPoolEipIspType  = "game.kruise.io/nlb-pool-eip-isp-type"
	LabelEIPPoolName        = "game.kruise.io/eip-pool-name"
	LabelEIPPoolIndex       = "game.kruise.io/eip-pool-index"
	LabelEIPPoolEipIspType  = "game.kruise.io/eip-pool-eip-isp-type"

	// Service Pool status values
	SvcPoolStatusAvailable = "available"
	SvcPoolStatusBound     = "bound"

	// Dummy Selector (placeholder selector when Service is unbound)
	LabelNLBPoolPlaceholder = "game.kruise.io/nlb-pool-placeholder"
	PlaceholderValue        = "none"

	// NLB Annotations
	AnnotationSlbId               = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-id"
	AnnotationSlbListenerOverride = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-force-override-listeners"

	// Health Check Annotations
	AnnotationHealthCheckFlag           = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-flag"
	AnnotationHealthCheckType           = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-type"
	AnnotationHealthCheckConnectPort    = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-connect-port"
	AnnotationHealthCheckConnectTimeout = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-connect-timeout"
	AnnotationHealthCheckInterval       = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-interval"
	AnnotationHealthyThreshold          = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-healthy-threshold"
	AnnotationUnhealthyThreshold        = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-unhealthy-threshold"
	AnnotationHealthCheckDomain         = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-domain"
	AnnotationHealthCheckUri            = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-uri"
	AnnotationHealthCheckMethod         = "service.beta.kubernetes.io/alibaba-cloud-loadbalancer-health-check-method"

	// Service proxy name label (prevent kube-proxy from programming iptables)
	LabelServiceProxyName = "service.kubernetes.io/service-proxy-name"
)
