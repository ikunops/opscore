# ADR-059 — Phase 42 Implementation: Verification Attestation

- **Status**: Implemented (Round 215)
- **Phase**: 42（Verification Attestation）
- **Base**: `b92970d9`（ADR-057 修订 + ADR-058 Architecture）
- **配套**: Scope `057-phase42-scope.md`（Accepted, R214）／Architecture `058-phase42-architecture.md`（A, R214）
- **分工句**: P37 决定 **who**，P40 决定 **where**，P41 决定 **when**，**P42 决定 whether**。

> 本 ADR 记录实现决策；语义边界（什么算分歧、什么算尺子变了）仍在 ADR-057 §4-A4/A4.1，不可在此变更。

---

## 1. 交付面

| 文件 | 类型 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_verification.go` | **新增** | 报告/合流/账本/窗口/四判据/比较引擎/锚定/HTTP 面 |
| `internal/controlplane/server/snapshot_verification_test.go` | **新增** | T180~T199 |
| `internal/controlplane/server/snapshot_anchor.go` | **仅加法** | `anchorKindVerification` 常量 + `verificationAnchorFile` + `verificationAnchorPath` + 3 个 `omitempty` 字段（`ReportSeq`/`ReportDigest`/`Overall`，进 `anchorEntry`/`anchorSigned`/`anchorSignedFields`） |
| `internal/controlplane/server/history_export_scheduler.go` | 改 | 3 个配置字段 + 3 条构造守卫 + `verificationError` + `runVerify`/`verifyTick` + Status 展开 |
| `internal/controlplane/server/server.go` | 改 | 2 条路由（紧邻既有 anchor 路由） |
| `cmd/opscore/main.go` | 改 | 3 个 flag |
| `appendonly_log.go` / `snapshot_signature.go` / `snapshot_chain.go` / `snapshot_ledger.go` / `history_export_coverage.go` / `history_export_manifest.go` / `snapshot_key_lifecycle.go` | **零改动** | 只读取它们的输出 |

`VerifySnapshotsDetailed`（`history_export_manifest.go:544`）与 `Coverage`（`history_export_coverage.go:99`）函数体零改动；P42 只在其外层组装。

---

## 2. R42-3 闭合（本轮强制随行）

| 条款 | 落地 |
|---|---|
| 判定：compaction 之后核心产出是否被埋葬 | **否**。`fullHistory := (min_seq == 1)` 只决定"无需边界"；`min_seq > 1` ⇒ 进入边界模式，不退回 indeterminate |
| 断言下界 | **发布时间不早于最早幸存报告的 `verified_at`**。实现取 `ReportSeq == window.MinSeq` 那条报告的 `VerifiedAt` |
| 发布时间来源 | `ManifestListEntry.GeneratedAt`（`ListManifests`）——与报告 `verified_at` **同源时钟**（`s.clock()`），与 P41 用 `signed_at` 比较的假设一致 |
| 结果必须暴露下界 | `gap.gap_assertable_from`（RFC3339Nano）；`min==1` 时为空字符串并附"从未裁剪"理由 |
| 边界之下不可断言 | 进 `gap.not_assertable[]`，并**强制** `gap_indeterminate=true` + `reason` 说明条数与边界（绝不静默） |
| 空列表必须带说明 | `reason` 恒非空（三种情形各有措辞） |
| 判别测试 | T198：compaction（capacity=1）后 min=2 ⇒ P3（边界后、未覆盖）⇒ `gaps=[3]`；P1（边界前、未覆盖）⇒ `not_assertable=[1]` |

**为什么边界是充分的（实现侧论证）**：覆盖某 publication 的报告必然建于该 publication 存在之后 ⇒ 其 `verified_at ≥ published_at ≥ 边界` ⇒ 其构建时刻不早于最早幸存报告 ⇒ 由于 `report_seq` 在"读账本 → max+1 → 真追加"的同一临界区内分配，**晚追加者 seq 必更大** ⇒ 该报告 seq ≥ min ⇒ prefix compaction（只裁最旧整组）不可能裁掉它 ⇒ 缺席即真 gap。

**已知代价（如实）**：该推理依赖同一时钟单调（与 P41 对 `signed_at` 的依赖同构，P42 仍**不是**时间权威）。时钟回拨会让边界右移或左移；域外不可检测，本轮冻结对账。

---

## 3. 实现决策（Architecture 未细化处）

1. **Subject 的 publication 来源**：`VerifyResult` 不携带 `publication_id`，因此用 `ListManifests(limit, "")` 建立 `identity → (publication_id, generated_at)` 映射后与 verify 结果按 `Snapshot` 恒等键连接。两个面同一 limit、同一排序域（manifest-bearing），未复制 discover 逻辑。
2. **无法归属的快照组**：计入 `subject.unattributed`，不进 items —— 计入而非静默丢弃。
3. **`lifecycle` 维度逐 publication 求值**（不是报告级）：取值来自 `authorizationFor(signature.key_id)` 的 `Complete && Source==ledger`，即 P41 的"尺子"。无签名（无 key_id）⇒ 该条目该维取值为空 ⇒ 经 §5.2 的 `valueOf != ""` 自动不可比。报告级理由仍标 `scope=ledger`，因为它读的是**同一份账本**。
4. **报告是事实不是状态机**：`loadVerificationState` 与 `appendVerificationReport` 双处检测同 `report_seq` 第二行 ⇒ `verifiable=false` / 拒绝追加（fail-closed）。与 P40 anchor 条目的"状态可推进"**刻意相反**。
5. **签名复用 P37 `signatureBlock`**（不是自定义 `sig` 串）；canonical 复用同一家族 `json.Marshal(结构体)`，无第二套序列化器。
6. **I7 机械保证**：`verificationSigned`（canonical 载荷结构体）**不含** `anchor` 字段 ⇒ 锚定状态在字节层面无法回流（T199 钉住）。
7. **锚定顺序**：`appendLogLine`（落账本）→ `dispatchAnchorPath`（派发）；`beforeAnchorDispatch` 钩子在 T196 中钉住"派发时 pending 已落盘"。

---

## 4. 判据与派生

| 判据 | 触发 |
|---|---|
| `verification_absent` | 账本无条目（含被删） |
| `verification_gap` | 可观测 publication 未被任何幸存报告覆盖，且落在断言边界之上 |
| `verification_divergent` | 相邻两次报告：可比维取值分歧（①）／尺子相同而 overall 分歧（③，S3 fail-closed） |
| `verification_scope_changed` | 仅可用性向量变化（带 `changed_dimensions[]`、from/to overall、后者 reasons[]） |
| `window_discontinuous` | 幸存 `report_seq` 有空洞 ⇒ **不产出任何断言**（gap 转 indeterminate） |
| 账本不可验 | 签名/摘要/链/异域/重复 seq 任一命中 ⇒ `Error` + 零断言（fail-closed） |

---

## 5. 门禁与自证（R215）

- `GOTOOLCHAIN=local GOSUMDB=off GOPROXY=off`：`go build ./...` / `go vet ./...` rc=0；`go test ./... -count=1` 全绿（server 包 84.6s）。
- **冻结面零 diff**（基线 `b92970d9`，逐 blob 比对）：`appendonly_log.go`、`snapshot_signature.go`、`snapshot_chain.go`、`snapshot_ledger.go`、`history_export_coverage.go`、`history_export_manifest.go`、`snapshot_key_lifecycle.go`、`go.mod`、`go.sum` ⇒ **FROZEN TOUCHED = NONE**；四冻结包（`platform`/`governance`/`plugin/{runtime,isolation}`/`controlplane/hostregistry`）零改动。
- **P40/P41 锚定字节不变**（T196b）：golden 串比对 `canonicalAnchorPayload` —— P40 条目无 `kind`/`report_*`/`overall`，P41 条目保留 `kind:"key_lifecycle"` 且无 `report_*`；序列化条目同样逐字段核对。
- **变异 M1~M7 全红后按 sha256 还原**（`_p42_mutation.py`）：M1 摘 gap／M2 摘 divergent／M3 放行同 seq 第二行／M4 放行签名不可验条目／M5 摘锚定／M6 摘归一支／M7 退回 pre-R42-3 规则。

## 6. 非目标确认

告警、修复、时间权威、验证者身份判定、witness 侧 verification 对账（A7-11 冻结）、为第三家族新增 reconcile 路由、re-dispatch —— 全部未做。
