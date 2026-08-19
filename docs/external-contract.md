# 外部只读契约（External Read Contract）

> **SSOT**：`internal/external`（Phase 11.1，ADR-024）。稳定、版本化、只读的 Public Contract：
> `external/v1`。

## 1. 不变式（ADR-024 MUST-0..6）

- **MUST-0**：无新执行入口；不成为 Control Plane；永不包装 Executor。
- **MUST-1**：只读契约；只读取 `platformview` / `correlation`。
- **MUST-2**：复用既有 ID；不铸造 ExternalID / APITokenID-as-entity。
- **MUST-3**：无命令面；仅 GET/query/view（`Get*` 方法）。
- **MUST-4**：契约归属 —— 拥有 DTO 契约，不拥有 Domain/Event/Runtime 模型。
- **MUST-5**：external/v1 DTO 只组合门面，绝不重定义核心模型。
- **MUST-6**：冻结、版本化契约，置于 `external/v1`。

## 2. HTTP 路由（全部 GET，只读）

| 方法 + 路径 | 返回 DTO | 底层读源 |
|-----------|---------|---------|
| `GET /external/v1/execution/{id}` | `ExecutionView` | `platformview.GetExecutionOverview` |
| `GET /external/v1/host/{ref}` | `HostView` | `platformview.GetHostPolicyStatus` + `GetClusterPlacementView` |
| `GET /external/v1/policy/{id}` | `PolicyView` | `platformview.GetGovernanceSummary` |
| `GET /external/v1/correlation?kind=&ref=` | `CorrelationView` | `correlation.Correlate` |

- 契约版本常量：`external.ContractVersion = "external/v1"`。
- 认证：`Authenticator` 接口；默认 `NoAuthAuthenticator`（单租户/无认证 stub，MUST-5 认证 seam）。
- 空/非法输入 → `ErrInvalidID` / `ErrInvalidInput`；无数据 → `404 not found`
  （不返回 `200 null`）。

## 3. DTO 溯源（Provenance）

每个 DTO 携带 `DTOViewMeta`：
- `viewVersion`：`"external/v1"`
- `sourceView`：`"platform/view/v1"` | `"correlation/view/v1"`
- `sourceRefs`：来源引用（排序后的确定性列表）
- `correlatedAt`：关联时间（RFC3339，仅 correlation）

## 4. 确定性

所有切片均拷贝后排序（`sortedCopy`），同一输入产生逐字节一致的 DTO ——
镜像 `correlation` SHOULD-4 的确定性。

## 5. 如何演进

- 演进契约：**升到 `external/v2`**，绝不编辑 `external/v1`
  （SHOULD-2，Public Contract Compatibility：v1+v2 共存，无静默破坏性变更）。
- 本 Phase 16 **不扩展** external/v1（P16-3 硬边界）。

## 6. 边界

`external.Server` 不拥有数据、不持有缓存；每次调用在请求时命中门面查询 API，
返回全新、版本化的 DTO。它永不执行、变异，或越过门面进入冻结系统（MUST-0）。
