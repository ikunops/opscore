# OpsCore 架构（Architecture）

> **SSOT 声明**：本文档描述 `opscore` 仓库当前的真实分层结构，经 R71 核验与实际代码一致。
> 任何与代码不符之处，以代码为准，并在 `docs/operations.md` 的 Drift Log 中记录
> （Fact → Drift → Impact → Future Major Candidate），**绝不静默改冻结代码迁就文档**。

## 1. 分层总览（7 层闭环 + 部署面）

OpsCore 是一个 **Lightweight Control Plane**：核心执行能力在 `internal/core` /
`internal/plugin` / `internal/controlplane`（冻结的 Runtime Core），外围通过四层逐步叠加
（能力 → 持久化 → 集成 → 关联 → 外部 → 部署），最终由 Deployment 面装配。

| 层 | 包（目录） | 职责 | 状态 |
|----|-----------|------|------|
| 0 Runtime Core | `core`, `plugin`(`runtime`/`isolation`), `builtin`, `controlplane` | 执行引擎、插件运行时/隔离、控制平面、内建能力 | 冻结（Runtime Contract 不变） |
| 1 Platform Operations | `observability`, `cluster`(+`clusterprojection`), `enterprise`, `governance` | 四大外围能力（可观测/集群/企业/治理） | 冻结 |
| 2 Governance Policy Persistence | `governancepolicy` | Policy 持久化的唯一所有者（Phase 14） | 冻结（仅描述，不改） |
| 3 Platform Integration | `platformview` | 只读集成门面，组合 4 大能力 | 冻结（仅描述，不改） |
| 4 Event Correlation | `correlation` | 只读事件关联门面 | 冻结（仅描述，不改） |
| 5 External Interface | `external` (external/v1) | 稳定、版本化、只读公共契约 | 冻结（仅描述，不改） |
| 6 Deployment | `harness` + `cmd/opscore-server` | 唯一组合根，装配读图 + 传输 | 冻结（仅描述，不改） |
| — 辅助/预留 | `storage`（多节点 seam 预留）, `platform`（Phase 2.x HostSnapshot resolver，冻结，勿与 platformview 混淆）, `demo` | 见 §4 | — |

> ⚠️ **易混点**：`internal/platform`（Phase 2.x 的 HostSnapshot resolver）与
> `internal/platformview`（Phase 9.1 的读门面）是**两个不同包**，不要混淆。
> 读门面走 `platformview`；`platform` 是更早冻结的 resolver，Phase 9.1 不可复用其路径。

## 2. 依赖方向（Dependency Inversion）

- 每个上层（`platformview` / `correlation` / `external`）只依赖下层提供的 **查询接口**（Reader
  接口），绝不 import 冻结的执行路径（`core/execution`、`plugin/runtime`、`plugin/isolation`、
  `controlplane/*`、`builtin/*`）。
- `governancepolicy` 只 import `governance` 的 **Rule 值类型**，绝不 import 引擎执行路径；
  `governance.Engine.Evaluate` **永不被运营层调用**（B7/B9）。
- `harness` 是唯一组合根：它把所有真实的 Reader 实现注入门面，本身不持有领域状态（只持有引用）。

## 3. SSOT 映射（文档 ↔ 代码）

| 文档主题 | 真源代码 | 关键类型 / 常量 |
|---------|---------|---------------|
| 外部只读契约 | `internal/external` | `external.ContractVersion = "external/v1"`；`Server.GetExecution/GetHost/GetPolicy/GetCorrelation` |
| 平台集成门面 | `internal/platformview` | `Facade.GetExecutionOverview/GetHostPolicyStatus/GetGovernanceSummary/GetClusterPlacementView` |
| 事件关联 | `internal/correlation` | `Correlator.Correlate`（唯一公开方法） |
| Policy 持久化/生命周期 | `internal/governancepolicy` | `Repository`, `Reader`, `Create/Activate/Deactivate/Archive`, `StatusDraft/Active/Archived` |
| 部署/组合根 | `internal/harness` + `cmd/opscore-server` | `HarnessConfig`, `Build`, `Serve`, `Shutdown`；`opscore-server` 是唯一入口 |
| 运营探针 | `internal/harness/probe.go` | `/healthz` `/readyz` `/versionz`（独立 bind） |

## 4. Boundary Matrix（冻结边界）

| 边界 ID | 边界内容 | 允许 | 禁止 | 执行方 / 守卫 |
|--------|---------|------|------|--------------|
| B-Runtime | Runtime Core 冻结 | harness 装配读图 | 开启任何执行入口、改动 Runtime Contract | harness MUST-0；AST import guard |
| MUST-0 | 外部无新执行入口 | 只读查询 | external 包装 Executor / 成为 Control Plane | external 包注释；AST import guard |
| MUST-1 | 外部只读 | 读 platformview/correlation | 越过门面读冻结系统 | `PlatformViewReader`/`CorrelationReader` |
| MUST-3 | 外部无命令面 | GET/query | 任何写/命令方法 | external 仅 `Get*` |
| MUST-4 | 契约归属 | external 拥有 DTO 契约 | 重定义领域/事件/运行时模型 | DTO 仅重排引用 |
| Composition Root | 组合根唯一 | `cmd/opscore-server` 装配 | 其他包成为组合根 | harness 注释 SHOULD-1 |
| Engine.NonInvoke | 治理引擎不调用 | — | 运营层调用 `governance.Engine.Evaluate` | `TestNoEngineEval` guard |
| P16-0 | Phase 16 无运行时效能变化 | 文档/打包 | 任何运行时行为改动 | Phase 16.2 仅文档 |
| P16-3 | 无写/控 API | 文档化只读能力 | 偷偷扩展 external/v1 为写/控 | external MUST-3；R70 硬边界 |
| Drift | 文档与代码漂移 | 记录 Fact→Drift→Impact→Future Major | 静默改冻结代码迁就文档 | `docs/operations.md` Drift Log |

## 5. 演进纪律

任何 Major 演进都必须走 **Scope ADR → Architecture ADR → Implementation** 三级签字
（演进章程 ADR-021 §6），且 **不直接编码** —— 先定范围。详见 `docs/operations.md`。
