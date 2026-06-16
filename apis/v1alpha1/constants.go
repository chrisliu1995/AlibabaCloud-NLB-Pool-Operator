package v1alpha1

const (
	// Labels
	LabelPool  = "nlbpool.alibabacloud.com/pool"
	LabelSlot  = "nlbpool.alibabacloud.com/slot"
	LabelPort  = "nlbpool.alibabacloud.com/port"
	LabelLane  = "nlbpool.alibabacloud.com/lane"
	LabelPhase = "nlbpool.alibabacloud.com/phase"

	// Pod Annotations (由上游workload写入)
	AnnotationNLBPoolName     = "alibabacloud.com/nlb-pool-name"
	AnnotationNetworkDisabled = "alibabacloud.com/nlb-network-disabled"
	// PA Controller 写入的乐观锁
	AnnotationPAClaim = "alibabacloud.com/nlb-pa-claim"

	// Finalizers
	FinalizerNLBPool        = "nlbpool.alibabacloud.com/cleanup"
	FinalizerPortAllocation = "nlbpool.alibabacloud.com/cleanup"
)
