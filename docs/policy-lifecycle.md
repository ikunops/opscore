# Policy 生命周期（Policy Lifecycle）

> **SSOT**：`internal/governancepolicy`（Phase 14，ADR-029/030）。
> 本包是 **Policy 持久化的唯一所有者**（B6）。

## 1. 关键区分

- **`governance.Engine`**（冻结）：无状态 *评估器*，拥有 *评估*，不拥有 *存储*。
- **`governancepolicy`**（持久化）：拥有 *存储* 与 *生命周期状态*，但
  **永不调用 `Engine.Evaluate`**（B7/B9，无执行桥）。

## 2. 生命周期状态（闭集）

| 状态 | 含义 | 评估行为 |
|------|------|---------|
| `draft` | 已创作未激活 | 不参与评估 |
| `active` | 生效中 | 引擎可对其评估 |
| `archived` | 已归档 | 不再评估，仅保留审计 |

转换：`Create → draft`；`Activate → active`；`Deactivate → draft`（等价于未生效）；
`Archive → archived`。

## 3. 操作（纯仓库写，无评估）

- `Create(repo, policyID, rules)` → 以 `draft` 落库首版（rules 复用 `governance.Rule` 原值，
  不复制语义）。
- `Activate` / `Deactivate` / `Archive` → 仅改状态，纯持久化。
- `Repository` 接口：`Save` / `Get` / `List` / `Archive` / `Activate` / `Deactivate` /
  `NextRevision`（持久化动词合法；执行动词被机械守卫禁止）。

## 4. 读面（Reader）

- `QueryRules` → 投射已持久化规则为 `RuleView`，按 `(priority desc, RuleID asc)` 稳定排序；
  未知 policy 返回诚实空（nil）。
- `QueryVerdict` → **诚实返回 nil**（Verdict 需要本层永远不持有的观测 State；
  Governance 拥有评估，不拥有存储，B2/B7）。
- `QueryVerdictRefs` → 返回已持久化 PolicyID（只读、复用既有身份，MUST-2）。

## 5. 文件存储

- `NewFileRepository(dir)`：每个 policy 一个 JSON 文件 `policy-{id}.json`；创建时 `MkdirAll`；
  **空 dir 拒绝（`ErrInvalidID`）** 以避免隐式/歧义状态（B6）。
- `Revision` 是版本属性（B8），每次 Save 既有 PolicyID 时自增。
- `PolicyID` 复用既有 governance 身份（B8），无新全局身份。
- `Repository` **无 `Close`**（基于单次文件读写）—— 刷新用类型断言 `closeIfNeeded`
  安全 no-op（harness 关闭时）。

## 6. 运维须知（重要）

本包只做 *持久化/生命周期*。**让 policy 生效需要 `Activate`**；而「对 active policy 求值」
是 `governance.Engine` 的职责，**不在本包范围内**。

> ⚠️ Phase 16 **不新增** Management API 来外部创建/激活 policy —— 那是
> **方向 A（Management API / Policy Management）**，属于独立 Major，须独立 Scope ADR，
> 且 **不得偷偷扩展 external/v1**（R70 硬边界）。
