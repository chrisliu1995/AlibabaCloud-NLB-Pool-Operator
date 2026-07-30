# AGENTS.md

面向 coding agent 的项目入口。改动前先读本文件，再按"变更-检查映射"执行验证。

## 项目定位

Go / controller-runtime 实现的 Kubernetes Operator，管理阿里云 NLB/EIP 资源池，
为游戏服务器 Pod 提供预热的公网四层接入（NLB + EIP + ServerGroup + Listener）。
架构与领域细节见 [README.md](README.md)。

## 目录职责

| 路径 | 职责 | 可否手改 |
|------|------|----------|
| `apis/v1alpha1/` | NLBPool / PortAllocation CRD 类型与常量 | 可改类型，改后必须 `make generate manifests` |
| `apis/v1alpha1/zz_generated.deepcopy.go` | controller-gen 生成的 deepcopy | 禁止手改，由 `make generate` 生成 |
| `apis/eipoperator/`、`apis/nlboperator/` | 外部 Operator 的 API 副本（scheme 注册用） | 跟随上游，不做业务改动 |
| `config/crd/bases/` | 生成的 CRD 清单 | 禁止手改，由 `make manifests` 生成 |
| `config/rbac/role.yaml` | 生成的 RBAC | 禁止手改，改控制器 `+kubebuilder:rbac` 注释后 `make manifests` |
| `pkg/controller/` | NLBPool 与 PortAllocation 两个调谐器 | 核心业务逻辑，高风险区 |
| `pkg/provider/` | 阿里云 NLB API 封装、限流器、mock | 改接口需同步 mock 与限流包装 |
| `main.go` | 入口：云凭证读取、限流器装配、controller 注册 | 高风险区（凭证处理） |
| `Dockerfile` | 权威构建入口（多阶段） | 其余 Dockerfile.* 变体为特定场景，见文件头注释 |

## 高风险区

1. **破坏性云删除**：`pkg/controller/nlbpool_controller.go` 的
   `deleteInfrastructure` / `deletePortAllocations` / `hasCloudNLBs` 是
   finalizer 驱动的级联删除路径，直接销毁真实云资源。改动该路径前必读
   [docs/deletion-safety.md](docs/deletion-safety.md)，不得移除
   deletion-protection / dry-run 两个安全门。
2. **云凭证**：`main.go` 从环境变量 `ACCESS_KEY_ID` / `ACCESS_KEY_SECRET` /
   `REGION_ID` 读取 FullAccess 凭证。禁止将凭证写入代码、日志、事件或测试。
3. **限流器**：`main.go` 为每个 NLB 写接口装配独立 token-bucket。新增云 API
   调用时必须在 `pkg/provider/rate_limiter.go` 增加对应 limiter 并在
   `main.go` 暴露 QPS flag，否则会触发云端 Throttling。
4. **PA 状态机**：`pkg/controller/portallocation_controller.go` 的
   Provisioning/Available/Binding/Bound/Releasing/Disabled 状态机使用
   PA CAS + Pod CAS 双乐观锁，改动绑定逻辑必须保持双 CAS 语义。

## 变更-检查映射

| 改了什么 | 必须执行 |
|----------|----------|
| `apis/**/*_types.go`（字段/注释） | `make generate manifests` 然后 `make verify` |
| 控制器 `+kubebuilder:rbac` 注释 | `make manifests` 然后 `make verify` |
| `pkg/controller/`、`pkg/provider/` 逻辑 | `make test`（控制器与限流器均有单测） |
| 任何 Go 代码 | `make vet test` |
| 提交前 | `make vet test verify`（与 CI 合并门一致） |

## 常用命令

```bash
make doctor      # 自检：go、云环境变量、kubeconfig、依赖 CRD
make test        # 单元测试（控制器调谐/删除/绑定/限流/幂等均覆盖）
make verify      # 生成物漂移检查（CI 强制）
make run         # 本地运行（需 .env.example 中的环境变量 + kubeconfig）
make scan        # gitleaks 密钥扫描 + govulncheck 依赖扫描
```

## 禁止事项

- 禁止手改 `zz_generated.deepcopy.go`、`config/crd/bases/`、`config/rbac/role.yaml`；
- 禁止提交任何真实凭证（CI 有 gitleaks 门）；
- 禁止给测试文件加构建标签使其脱离 `make test` 默认测试面；
- 禁止绕过或删除删除安全门（deletion-protection / dry-run 注解）；
- 禁止在未加限流器的情况下新增云写 API 调用。

## 外部依赖

- 运行时依赖 NLB Operator 与 EIP Operator 及其 CRD（`make doctor` 会检查）；
- 集群要求 Kubernetes 1.28+、Terway eniip CNI（Pod IP 需 VPC 可路由）。
