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
	// AnnotationPortMappingResult 由 PA Controller 在 Bound 后写入绑定 Pod 的
	// 端口映射结果（JSON），供容器经 Downward API 消费（Issue #13）。
	// 释放 / 禁用时由控制器清理。
	AnnotationPortMappingResult = "alibabacloud.com/nlb-port-mapping-result"

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
