# NLBPool 删除安全边界

删除 NLBPool CR 会触发 finalizer 驱动的级联删除，销毁真实云资源
（NLB、EIP、ServerGroup、Listener）。giant 规模场景下一次误删可级联
销毁 4 NLB x 8 EIP x 1600 Listener 并产生计费影响。本文档描述两个
默认关闭的安全门与回滚/恢复流程。

## 安全门

两个安全门都通过 NLBPool 注解开启，默认关闭——不加注解时删除行为与
原有 GitOps 语义完全一致。

### 1. 删除保护（审批门）

```bash
kubectl annotate nlbpool <name> nlbpool.alibabacloud.com/deletion-protection=true
```

- 该注解为 `true` 时，即使 CR 被 `kubectl delete`，级联删除也不会执行；
- 控制器每 30 秒发出 `DeletionBlocked` Warning 事件，内容包含待删除资源计划；
- 审批放行：移除注解后级联删除继续。

```bash
kubectl annotate nlbpool <name> nlbpool.alibabacloud.com/deletion-protection-
```

建议对生产环境的 NLBPool 默认加上该注解，把移除注解作为删除审批动作。

### 2. Dry-run（删除计划预览）

```bash
kubectl annotate nlbpool <name> nlbpool.alibabacloud.com/deletion-dry-run=true
kubectl delete nlbpool <name> --wait=false
kubectl describe nlbpool <name>   # 查看 DeletionDryRun 事件中的删除计划
```

- 该注解为 `true` 时，删除请求只产生 `DeletionDryRun` Normal 事件，
  列出将被级联删除的资源清单（NLB/EIP/SG/Listener/PA 数量与云端 NLB ID），
  不执行任何删除；
- 确认计划无误后，移除注解即执行真实删除。

## 删除中止（回滚）

级联删除分三阶段：Phase 1 删除 NLB/EIP CR；Phase 2 等待云端 NLB 级联
完成；Phase 3 删除 PortAllocation。在 Phase 1 尚未开始前（即被删除保护
或 dry-run 阻断期间），删除可以完全中止：

1. finalizer 仍在，CR 未真正删除；
2. 恢复方式：由于 CR 已带 DeletionTimestamp，Kubernetes 不允许撤销删除。
   需要从备份/GitOps 源重新 apply 一份等价 CR（新名称或先等旧 CR 移除），
   见下节恢复流程。

一旦删除保护被移除且 Phase 1 开始执行，云资源删除不可回滚，只能走重建。

## 恢复流程（重建）

云 NLB/EIP 被删除后无法原样找回（EIP 公网 IP 会变化）。重建步骤：

1. 从 GitOps 仓库或备份中取出 NLBPool 的 spec；
2. `kubectl apply` 重新创建 NLBPool CR；
3. Operator 自动重建 EIP -> NLB -> PortAllocation -> SG/Listener 全链路；
4. 注意：新 EIP 的公网 IP 与原来不同，依赖固定 IP 的外部配置
   （DNS、防火墙白名单、客户端硬编码）需要同步更新；
5. 使用 `kubectl get nlbpool <name> -o wide` 观察 Phase 到 Ready，
   PortAllocation 全部 Available 后由 kruise-game 插件重新绑定 Pod。

## 事前预防清单

- 生产 NLBPool 常驻 `deletion-protection=true` 注解；
- NLBPool spec 纳入 GitOps 仓库管理，保证可重建；
- 删除前先用 dry-run 预览计划并留存事件输出；
- 记录当前 EIP 公网 IP 清单，评估外部依赖影响面。
