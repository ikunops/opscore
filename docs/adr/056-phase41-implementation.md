# ADR-056 — Phase 41: Signing Key Lifecycle（Implementation）

- **Status**: Proposed (Round 212, Implementation stage)
- **Phase**: 41（Signing Key Lifecycle / 时间受限信任）
- **Base**: ADR-055（Architecture，R211 = **A**）+ ADR-054（Scope，R211 修订版）+ Phase 40 CLOSED（`3c690cf`）
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **Supersedes**: 无。**不修改** P35/P36/P37/P38/P39/P40 的任何冻结判据。

---

## 0. 一句话

把「哪把签名密钥在哪个时间区间被授权」从一份可改写的运行时配置，落为一条 append-only、哈希链 + KAK 授权链的持久化事实，并在 P37 验签之后追加一步授权区间检查。

> **P37 决定 who，P40 决定 where，P41 决定 when。**

---

## 1. 本轮强制随行闭合

### R41-6（`event_seq` 分配与重复拒绝必须是账本导向的）—— 已闭合

judge 指出的崩溃砖死场景：append(seq=N) 成功 → 调用方在确认前崩溃 → 重试时若重复检查只查内存 ⇒ 分配到 seq=N ⇒ conflict ⇒ **之后每一个**事件都永远 conflict ⇒ 账本永久 fail-closed。

| 必修 | 实现 | 证据 |
|---|---|---|
| 1. 重复提交拒绝必须扫描账本本身 | `appendKeyLifecycleEvent` 在写入临界区内 `readLogLines` 后逐行比对 `(key_id, event_type, not_after)`，命中即 `duplicate submission` 拒绝；**不存在任何内存态** | T(R41-6) `TestKeyLifecycleDuplicateSubmissionIsRejectedFromTheLedger` |
| 2. `event_seq` 由账本当前最大值在同一临界区内导出 | `event_seq = maxSeq + 1`，`maxSeq` 来自刚刚读取的同一份行集；Tick 非重入 ⇒ 单写者 | 同上（断言 `first.EventSeq == 2`、`next.EventSeq == 3`） |
| 3. 禁止独立水位文件 | 无 `event-seq.state` 之类文件；测试显式断言 `os.Stat(event-seq.state)` 为 `IsNotExist` | 同上 |
| 4. 判别测试 | 注入「append 成功 → ack 前 crash」= 用同一请求再调一次；断言拒绝 + 字节不动 + 下一事件 seq = N+1 且日志仍可用 | 同上 |

### R41-7（不可解析 `signed_at` 不得保持 `signature_ok`）—— 已闭合

judge 的判据：合法签名路径恒输出 `time.Format(RFC3339Nano)`（snapshot_signature.go:215），不可解析 ⇒ 必非合法路径产物 ⇒ 响亮且无误伤。这与 P38「不可解析 ⇒ 不排序不猜测」不冲突：P38 禁止的是**从坏数据编造断言**，`signature_time_unparseable` 断言的是**时间声明本身有缺陷**。

| 必修 | 实现 | 证据 |
|---|---|---|
| 新增第四个独立 verdict | `sigVerdictTimeUnparseable = "signature_time_unparseable"`，`mismatch` 档上限、独立命名（R41-1 家族） | T(R41-7) `TestKeyLifecycleUnparseableSignedAtIsAssertable` |
| `authorizeByLifecycle` 解析失败时产出它而非保持 ok | 时间解析被提到**区间判定之前**：只要 P41 在发挥作用（`a.Evaluable`）就先解析，失败即返回该 verdict | 同上 |
| 判别面扩到四个新 verdict 互斥 | `TestKeyLifecycleVerdictsAreMutuallyExclusive` 四个输入 ⇒ 四个互异输出（`ok` / `before_activation` / `after_revocation` / `time_unparseable`） | T159 |
| 补红例：`signed_at="not-a-time"` | P37 `signature_ok` ⇒ P41 观测 `signature_time_unparseable`（mismatch），**绝不** ok | T(R41-7) |
| 零回归边界 | P41 **未启用**时（`a.Evaluable == false`）`authorizeByLifecycle` 原样返回，即便时间不可解析也保持 `signature_ok` 且不挂 `validity` | T155 |

---

## 2. 落盘与文件清单（ADR-055 §14 对照）

| 文件 | 变化 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_key_lifecycle.go` | **新增** | `keyLifecycleFile` / `keyLifecycleEntry` / `canonicalLifecyclePayload` / `loadKeyLifecycleState` / `resolveKeyAuthorization`（= `authorizationFor`）/ `appendKeyLifecycleEvent` / `compactKeyLifecyclePrefix` / `authorizeByLifecycle` / `keyLifecycleSummary` / `keyAuthority` |
| `internal/controlplane/server/snapshot_signature.go` | 只增 | `SignatureVerdict.Validity`（`omitempty`）；`applySignatureVerdict` **增一个 case**（四个新 verdict ⇒ `mismatch`），既有分支一行未改；`verifyManifestSignature` **函数体零改动** |
| `internal/controlplane/server/history_export_manifest.go` | 只增 | `VerifySnapshotsDetailed` 内 **唯一调用点**：`verifyManifestSignature` 之后、`applySignatureVerdict` 之前；新增 `signedAtOfManifest` |
| `internal/controlplane/server/history_export_scheduler.go` | 只增 | 3 个 config 字段、2 个 struct 字段、`keyLifecycleConfig()` / `lifecycleStreamID()` / `AppendKeyLifecycleEvent` / `KeyLifecycleStatus` / 状态字段 `KeyLifecycle`（`omitempty`）+ 构造期两条 fail-fast |
| `internal/controlplane/server/snapshot_anchor.go` | 只增 | 第二 anchor 家族：`keyLifecycleAnchorFile` / `anchorKindKeyLifecycle` / `anchorEntry.{Kind,EventSeq,EventDigest,EventType,NotAfter}`（全 `omitempty`）/ `identityID()` / 路径参数化的 `loadAnchorStatePath`・`nextAnchorSeqPath`・`appendAnchorEntryPath`・`compactAnchorPrefixPath`・`dispatchAnchorPath` / `anchorKeyLifecycleEvent` |
| `internal/controlplane/server/server.go` | 只增 | 2 条路由（同一 handler 的 GET/POST） |
| `internal/controlplane/server/mgmt_obs.go` | 只增 | `handleHistoryExportKeyLifecycle` |
| `cmd/opscore/main.go` | 只增 | 3 个 flag |
| `internal/controlplane/server/appendonly_log.go` | **零改动** | 仅复用（`readLogLines` / `appendLogLine` / `compactLogPrefixGroups`） |
| `internal/controlplane/server/history_export_coverage.go` | **零改动** | — |

---

## 3. 关键实现决策（实现期新增，请 judge 复核）

### I1. KAK 写密钥无 KAK 信任锚 ⇒ **构造期 fail-fast**（严于 ADR-055 §7）

ADR-055 §7 原文把「空 trust」描述为运行期退化为 unbounded + `key_lifecycle_error`。实现改为**构造即拒绝**：能签却不能验 ⇒ 会造出一本永远无法评估的账本，那是「有时间边界的假象」，比没有更危险。此处与 P37 的 T79b 同构（签名密钥不在信任集中 ⇒ 构造失败）。

保留的运行期退化路径：trust 已配置但账本不可验（不可解析行 / 签名不验 / 哈希链断裂 / 异域 stream / 窗口不连续）⇒ `verifiable=false` ⇒ 全键 unbounded + `key_lifecycle_error` 响亮。

### I2. 账本不可验 ⇒ **拒绝追加**

ADR-055 §8 只写了「账本不可解析 ⇒ 拒绝追加」。实现把它推广到**任何不可验**：不可解析行、某条 KAK 签名不验、某条属于异域 stream —— 三者都在 `appendKeyLifecycleEvent` 里 fail-closed。理由：往一本无法评估的账本后面追加，等于用新事实给腐败背书（R205 教训）。

### I3. 哈希链首行豁免

存活的第一行**不校验** `prev_event_digest`：合法 prefix compaction 之后，首行的 prev 指向一个已被合法裁掉的条目，把它判为篡改会自伤（与 R40-1 同构）。`i > 0` 才做严格链接校验。

### I4. 异域 stream ⇒ 整本账本不可验（而非只忽略那一行）

R41-3「同钥多目录」的静默 fail-open 在这里被强制声明：只要有一行属于别的 stream，整本账本不参与断言（T176）。「忽略看不懂的行」正是 R40-1 要消灭的动作。

### I5. 生命周期 anchor 走**独立文件**而非并入 `chain-anchor.jsonl`

ADR-055 §9 要求「第二条独立的 anchor 记录流，不复用 `anchor_seq` 序列」。若两个家族共用文件，共享原语的分组键（`anchor_seq`）会把 publication 的 seq 与 lifecycle 的 seq 当成同一组，从而触发 ADR-053 I5 的 conflict 判定 —— 语义污染。实现选择独立文件 `key-lifecycle-anchor.jsonl`，复用同一 `anchorEntry` 结构与同一 dispatch/ack 语义，只换路径。两条禁令原样继承：不用 anchor 结果反推密钥有效性；不用生命周期账本填补 anchor 空洞。

### I6. `event_seq` 无水位文件（R41-6.3）

P35 的 `publication-seq.state` 是为**证据身份**设计的（先持久化后发布）。照搬到这里会在「水位已消耗、行未落盘」处制造 seq 空洞 ⇒ 触发 `window_discontinuous` 自伤。此处 seq 只由账本自身导出。

### I7. 状态机落点

`validateLifecycleTransition` 在**写入临界区内**执行（与重复检测共用同一次账本读取），因此「检查—写入」之间不存在可被并发利用的窗口：

- 首事件必须是 `activated`；
- `activated` 每 key 至多一次；终态后再 `activated` ⇒ 拒绝（不可复活）；
- `rotated_out` → `revoked` 允许；`revoked` → `rotated_out` 允许（幂等收缩，解析时取 `min(not_after)`）；
- `not_after` 不得早于该 key 的 `not_before`；
- `key_id` 必须在 P37 信任集中（who 仍由 P37 决定）。

---

## 4. 输出面（只增不减）

- `SignatureVerdict.validity{authorized_from, authorized_until, terminal_event, source}` —— `omitempty`，未启用时**逐字节不变**。
- `HistoryExportStatus.key_lifecycle{enabled, writable, event_count, error, window, authorizations[], active_key_id}` —— 未启用时整个字段消失。
- 路由：`GET|POST /management/v1/protection/export/key-lifecycle`（:8082，admin-only，POST 另需 `sameOriginOrFail`）。
- flag：`--export-key-authority` / `--export-key-authority-trust` / `--export-key-lifecycle-capacity`（默认 4096）。

POST 失败语义：未启用 ⇒ 503；重复提交 / 非法状态机 / 账本不可验 ⇒ 409（**字节不动**）；其余 400。

---

## 5. 已知代价（继承并逐条落到代码）

1. 时间仍是**自证**的 —— P41 不是时间戳证明（`recorded_at` 不参与任何断言）。
2. 安全性上界 = **KAK 独立性**（同域 ⇒ 退化为自证，不检测）。
3. 只覆盖**启用之后**记录的事件；启用前密钥 ⇒ unbounded。
4. 账本有界 ⇒ compaction 后区间丢失 ⇒ unbounded（T163 已钉）。
5. 吊销需要**及时性**（运营属性）。
6. **腐账 = 撤销洗白攻击面**：删掉账本 ⇒ after_* 全部退回 `signature_ok`。**T177 如实钉住这条残留弱点，不美化**；唯一缓解是 P40 域外副本 + KAK 离线 + 禁止自动重建 + `key_lifecycle_error` 响亮。
7. **同钥多目录 ⇒ 吊销不跨域**：明确不扇出，逐目录执行是运维程序；事件携带 `stream_id` 使遗漏**域外可见**，本地静默（T176）。

---

## 6. 测试映射

| 编号 | 用例 | 落点 |
|---|---|---|
| T155 / T169 | 未启用零回归 + 不创建文件 | `TestKeyLifecycleDisabledCreatesNothingAndAssertsNothing` |
| T156 / T158 | `revoked` / `rotated_out` 的历史保住 | `TestKeyLifecyclePreservesHistoryBeforeTheBound`（双 leg） |
| T157 / T174 | 两个 `after_*` 断言 + 独立命名、互不混淆 | `TestKeyLifecycleAfterVerdictsAreIndependentlyNamed` |
| T159 | 判别面（四 verdict 互斥） | `TestKeyLifecycleVerdictsAreMutuallyExclusive` |
| T175 | mismatch 档位 | `TestKeyLifecycleVerdictsDegradeToMismatch` |
| T160 | fail-closed（不可解析行，字节不动） | `TestKeyLifecycleFailClosedOnUnreadableLine` |
| T161 | 哈希链 | `TestKeyLifecycleHashChainIsVerified` |
| T162 | 不可复活 | `TestKeyLifecycleResurrectionIsRefused` |
| T163 | 窗口：activation 被裁 ⇒ unbounded | `TestKeyLifecycleLostActivationNeverMeansRevoked` |
| T164 | 窗口不连续 ⇒ 全部停用 | `TestKeyLifecycleDiscontinuousWindowDisablesAssertions` |
| T165 | anchor 接线 | `TestKeyLifecycleAnchorWiring` |
| T166 | 跨维零干扰（P37 五态不变） | `TestKeyLifecycleDoesNotDisturbPhase37Verdicts` |
| T167 / T168 | 冻结面零 diff + go.mod/go.sum 零改动 | 门禁外核对（逐文件 blob 哈希，见 SUBMISSION 证据段） |
| T170 | non-vacuousness 红例（吊销腿 + 旋转腿） | `TestKeyLifecycleAfterVerdictsAreIndependentlyNamed` 双 leg + T177 |
| T171 / T172 | 变异判别（数据层 M1~M3） | `TestKeyLifecycleAssertionsAreDiscriminating` |
| T173 | genesis 必须 KAK 签 + KAK ≠ 签名密钥 | `TestKeyLifecycleGenesisRequiresTheAuthorityKey` |
| T176 | 同钥多目录 | `TestKeyLifecycleDoesNotCrossExportDirectories` |
| T177 | 腐账红例（删账本 ⇒ 退回 ok） | `TestKeyLifecycleDeletedLedgerRevertsToSignatureOK` |
| **T178（R41-7）** | 不可解析 `signed_at` ⇒ `signature_time_unparseable` | `TestKeyLifecycleUnparseableSignedAtIsAssertable` |
| **T179（R41-6）** | crash-retry 重复拒绝 + seq 从账本导出 + 无水位文件 | `TestKeyLifecycleDuplicateSubmissionIsRejectedFromTheLedger` |

代码层变异（M1~M4）在门禁外现取红证据：分别摘掉 ①`NotAfter` 判据 ②`Terminal` 分派 ③`applySignatureVerdict` 新 case ④R41-7 的时间解析前置 —— 对应用例必红，恢复后转绿（按 sha256 校验字节还原）。

---

## 7. 请求裁决

Implementation 定稿如上。请裁决 **A（CLOSED）/ B（含必修）/ C**。

- 若 **A** ⇒ Phase 41 CLOSED，我起 Phase 42 Scope。
- 若 **B** ⇒ 请列必修条款编号（R41-8 起），我闭合后同轮投递修订版 ADR-056。
- 若 **C** ⇒ 请指出方向性否定点。

**三处请 judge 特别复核**：
1. **I1**（KAK 写密钥无 trust ⇒ 构造期 fail-fast）严于 ADR-055 §7 原文，是否接受；
2. **I5**（生命周期 anchor 走独立文件 `key-lifecycle-anchor.jsonl`）是否为 §9 的正确实例化；
3. **R41-7 的零回归边界**（未启用时不可解析时间仍保持 `signature_ok`）是否符合 judge 对「未启用 ⇒ 逐字节一致」的理解。
