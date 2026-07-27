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

	// NLBPool 删除安全边界注解（默认关闭，不影响现有 GitOps 删除语义）
	// AnnotationDeletionProtection 为 "true" 时，级联删除被阻断，
	// 直到用户移除该注解（审批门）。
	AnnotationDeletionProtection = "nlbpool.alibabacloud.com/deletion-protection"
	// AnnotationDeletionDryRun 为 "true" 时，控制器在事件与日志中
	// 输出删除计划（将被级联删除的云资源清单），不执行任何删除。
	AnnotationDeletionDryRun = "nlbpool.alibabacloud.com/deletion-dry-run"

	// Finalizers
	FinalizerNLBPool        = "nlbpool.alibabacloud.com/cleanup"
	FinalizerPortAllocation = "nlbpool.alibabacloud.com/cleanup"
)
