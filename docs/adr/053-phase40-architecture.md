# ADR-053 — Phase 40: Publication Anchoring — Architecture

- **Status**: Proposed (Round 208, Architecture stage)
- **Phase**: 40（Publication Anchoring）
- **Base**: ADR-052（Scope，R207=B 修订版）
- **Parent commit**: 与 ADR-052 修订同轮交付
- **Supersedes**: 无。不修改 P35/P36/P37/P38/P39 的任何冻结判据。

---

## 0. 与 ADR-052 的关系

本 ADR 只解决**如何实现**。ADR-052 已冻结的语义（A1~A10、§5 结果模型、§6 非目标、§7 证据诚实、§12 必修闭合表）在此**不再重新解释**，只给出落地结构。凡本 ADR 与 ADR-052 冲突，以 ADR-052 为准并回改本 ADR。

R207 的三处必修（R40-1 证据窗口 / R40-2 身份与分歧细分 / R40-3 冻结 re-dispatch）已在 ADR-052 §12 闭合；本 ADR 给出它们的**机制实现**。

---

## 1. 核心实现矛盾与解法（本 ADR 最重要的决策）

### 1.1 矛盾

ADR-052 A3 规定 anchor log 复用 P39/R205 持久化纪律：**真 `O_APPEND` 追加，既有字节永不重写；唯一允许的重写是 prefix compaction 且逐字复制**。

但 anchor 条目携带**可变的投递状态**（`pending → anchored` / `→ unanchored`，`attempts` 递增）。状态变更天然要求改写已有行——**与「既有字节永不重写」直接冲突**。

P39 的 ledger 没有这个矛盾，因为它的条目一旦写入即不可变。

### 1.2 解法：同一 `anchor_seq` 的状态推进 = 追加事件，不是改写

`chain-anchor.jsonl` 建模为**按 seq 分组的事件流**：

```
{"anchor_seq":12, ..., "state":"pending",  "attempts":1, "sig":"…"}   // 第 1 次投递
{"anchor_seq":12, ..., "state":"pending",  "attempts":2, "sig":"…"}   // 重试（同 payload）
{"anchor_seq":12, ..., "state":"anchored", "attempts":2, "ack_id":"…"}// 确认（同 payload）
```

- 读取时：**同一 `anchor_seq` 取最后一条胜出**（同一 seq 内 last-write-wins）。
- 追加**永远**是 O_APPEND，既有字节永不被改写 ⇒ 纪律完全保持。
- 与 P39 的语义同构且可直接复用判据：
  - 同 seq + **同 payload** ⇒ 状态推进，**合法**；
  - 同 seq + **不同 payload** ⇒ **conflict**，fail-closed 拒绝追加（ADR-052 A4/A5 的同 id 不同摘要规则）。

### 1.3 payload 与投递状态严格分离

只有**业务字段 + 身份字段**进入签名 payload；投递状态**永不**被签名（否则状态一变签名就失效）：

```go
type anchorEntry struct {
    // ---- 签名覆盖（canonicalAnchorPayload）----
    PublicationID      int64  `json:"publication_id"`
    AnchorSeq          int64  `json:"anchor_seq"`
    ManifestDigest     string `json:"manifest_digest"`
    PrevPublicationID  int64  `json:"prev_publication_id"`
    PrevManifestDigest string `json:"prev_manifest_digest"`
    RecordedAt         string `json:"recorded_at"`
    KeyID              string `json:"key_id"`     // [R40-2] P37 派生
    StreamID           string `json:"stream_id"`  // [R40-2] 派生

    // ---- 投递状态：不进 payload，可变 ----
    State      string `json:"state"`                 // pending|anchored|unanchored
    Attempts   int    `json:"attempts"`
    AckID      string `json:"ack_id,omitempty"`
    LastError  string `json:"last_error,omitempty"`
    AnchoredAt string `json:"anchored_at,omitempty"`

    Sig string `json:"sig,omitempty"` // 与 P37/P39 同构：序列化前置空
}
```

### 1.4 compaction 推广：以 seq 组为单位（不是以行为单位）

P39 的「只裁最旧**整行**」在分组模型下必须推广，否则会出现**状态回退**：裁掉 seq=12 的 `anchored` 行而留下它的 `pending` 行 ⇒ 该条目从已锚定回退成待推送，**等于把确认过的证据洗掉**。

> **I3**：prefix compaction 的最小单位是**一个完整的 `anchor_seq` 组**（该 seq 的全部行一起裁掉，或一起保留）；幸存行**逐字复制**。
> 这是 P39「只裁最旧整行」的同构推广——P39 中一行即一条目，两者在原语义下等价。

容量 `--export-anchor-capacity`（默认 4096）计量单位是 **seq 组数**，不是行数。

---

## 2. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_anchor.go` | **新增** | `anchorEntry`、canonical payload、签名/验签、持久化、窗口计算、对账算法、dev witness 后端 |
| `internal/controlplane/server/snapshot_anchor_test.go` | **新增** | T125~T145 |
| `internal/controlplane/server/appendonly_log.go` | **新增** | 共享原语：`readRawLines` / `appendLine` / `compactPrefixGroups` |
| `internal/controlplane/server/snapshot_ledger.go` | **修改（迁移）** | 改用共享原语（见 §3，含等价性证明） |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | anchor 配置字段、dispatch/retry housekeeping、`anchor_error` 状态 |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | `handleHistoryExportAnchor` / `handleHistoryExportAnchorReconcile` |
| `internal/controlplane/server/server.go` | **修改** | 两条路由注册（admin-only） |
| `cmd/opscore/main.go` | **修改** | 4 个 flag |
| `history_export_coverage.go` | **零 diff** | 承诺（T141） |

---

## 3. 持久化层：共享原语与 ledger 迁移决策

### 3.1 决策（显式，非悬置）

**提取共享原语 `appendonly_log.go`，并让 P39 的 ledger 同轮迁移到它。**

理由：R205 的教训是「实现与冻结语义不一致」。若 anchor 复制一份实现，同一条纪律会有两份代码，未来必然漂移——这正是 R205 会再次发生的土壤。单一实现 + 单一测试是唯一可靠防线。

### 3.2 原语签名

```go
// readRawLines 逐行读取；返回每行的原始字节与解析状态。不可解析行原样保留
// 在结果中，绝不静默丢弃（fail-closed 的输入）。
func readRawLines(path string) ([][]byte, bool /*ok*/, error)

// appendLine 真 O_APPEND 追加。调用方必须先自行完成 fail-closed 检查：
// 存在不可解析行时不得调用本函数。既有字节永不被重写。
func appendLine(path string, line []byte) error

// compactPrefixGroups 按「组」裁剪最旧的若干组，幸存行逐字复制。
// groups 由调用方按行内容给出组号（ledger: 每行一组；anchor: 按 anchor_seq 分组）。
func compactPrefixGroups(path string, keepGroups int, groupOf func([]byte) (int64, bool)) error
```

### 3.3 ledger 迁移的等价性证明（可验证）

迁移后必须同时满足：

1. **T104~T124 全绿**（P39 全部既有测试，尤其是 T123 真追加、T124 逐字复制）；
2. **字节等价测试（新增 T124b）**：对同一组输入事件序列，旧实现与新实现产出的 `chain-ledger.jsonl` **逐字节相同**；
3. **T123b**：注入一个不可解析行后，ledger 与 anchor **都**拒绝追加且文件字节不变（证明两条路径共用同一 fail-closed 分支）。

若 judge 认为不应触碰已 CLOSED 的 P39 持久化代码，回退成本极低：仅 revert `snapshot_ledger.go` 一个文件，anchor 继续直接使用原语（行为不变）。**此为显式备选，不悬置。**

---

## 4. 发布端接线（代码级）

### 4.1 插入点

`history_export_scheduler.go` `publishManifest` 尾部（现第 703~708 行）：

```go
if s.signer != nil {
    if rerr := s.recordLedgerEntry(&manifest, generatedAt); rerr != nil {
        s.setLedgerError(rerr.Error())
    }
}
// ↓ Phase 40 新增（第 5 步，严格在 atomic publish 与 ledger append 之后）
if s.anchor != nil {
    if aerr := s.recordAnchorEntry(&manifest, generatedAt); aerr != nil {
        s.setAnchorError(aerr.Error())   // 永不回滚已发布事实
    }
}
return "", ""
```

- `os.Link` 成功（第 689 行）才是唯一成功点；anchor 在其后，故 **anchor dispatch 失败不可能影响发布**（I6）。
- 测试注入沿用既有惯例：结构体加函数字段 `beforeAnchorDispatch func(dir string, seq int64)`。

### 4.2 `anchor_seq` 分配

- 由 `chain-anchor.jsonl` 当前最大 seq + 1 得出；**只在成功追加时消耗**（与 P35 publication id「失败尝试零消耗」同构）⇒ 幸存 seq 必然连续（I4）。
- 空文件 ⇒ 从 1 开始。

### 4.3 Housekeeping：重试与状态推进

每个 tick 在发布之后扫描：**最后状态为 `pending` 且 `attempts < max`** 的条目 ⇒ 重推：

| 结果 | 动作 |
|---|---|
| 2xx + 非空 `ack_id` | append `state=anchored`（同 payload） |
| 2xx 无 `ack_id` / 不可解析响应 | append `state=pending, attempts+1` |
| 5xx / 超时 / 网络错误 | append `state=pending, attempts+1`（指数退避） |
| 4xx（非 409） | append `state=unanchored` + `last_error` |
| 409 duplicate（同 id 同摘要） | append `state=anchored`（幂等） |
| 409 conflict（同 id 不同摘要） | **不追加**，置 `anchor_error`（A4/A5） |
| `attempts >= max` | append `state=unanchored`（终态，永久暴露） |

**`unanchored` 之后不再重试**（ADR-052 §6 第 12 条：无 re-dispatch，冻结为非目标）。

### 4.4 容量满的处理（不洗证据）

seq 组数达到 capacity ⇒ **拒绝对新条目分配 seq**（新发布得 `unanchored` + `anchor_error`），**绝不**通过丢弃最旧组来腾空间（I2/A5）。

---

## 5. 见证端契约（最小，非厂商特定）

### 5.1 请求

```json
{"v": 1,
 "key_id": "…", "stream_id": "…",
 "publication_id": 100, "anchor_seq": 12,
 "anchor_digest": "…", "manifest_digest": "…",
 "prev_publication_id": 99, "prev_manifest_digest": "…",
 "recorded_at": "…",
 "sig": "<base64: Ed25519(canonicalAnchorPayload)>"}
```

`POST <endpoint>`，`Content-Type: application/json`，超时 `--export-anchor-timeout`（默认 5s）。

### 5.2 响应

| 响应 | 判定 |
|---|---|
| 2xx + body `{"ack_id":"<非空>"}` | `anchored`（唯一可确认态） |
| 2xx 但 `ack_id` 缺失/空/body 不可解析 | `pending`（**响应码 ≠ 持久化**） |
| 409 + `{"reason":"duplicate"}` | `anchored`（幂等） |
| 409 + `{"reason":"conflict"}` | **conflict ⇒ `anchor_error`** |
| 其它 4xx | `unanchored` + `last_error` |
| 5xx / 超时 / 网络错误 | `pending` + `attempts+1` |

### 5.3 dev witness 后端（测试专用，不联网）

`--export-anchor-endpoint` 支持 `file://<dir>`：把每条请求**逐字追加**到 `<dir>/witness.jsonl` 并返回 `{"ack_id":"<seq>-<n>"}`。用于 T125~T145 全程离线自测，同时天然提供 reconcile 所需的见证序列。

---

## 6. 证据窗口计算（[R40-1] 机制）

```go
func loadAnchorState(dir string, trust *exportTrustStore) (*anchorState, error) {
    lines, ok, err := readRawLines(path)
    if err != nil { return nil, err }
    if !ok { return nil, errUnparseableAnchorLine }   // fail-closed，返回 error 而非跳过
    // 分组：同一 anchor_seq 的最后一行为准；同 seq 不同 payload ⇒ conflict
    // 窗口：
    //   seqs = 排序后的组号
    //   continuous = seqs[i] == seqs[i-1]+1 对所有 i 成立
    //   window = [seqs[0], seqs[len-1]]
}
```

- **不可解析行 ⇒ 整个 load 失败**（`anchor_error` + `trust_reason=no_local_window`，**不做任何断言**）——沿用 A3/R205：绝不 skip-and-continue。
- **`continuous == false` ⇒ `window_discontinuous` ⇒ `anchor_broken`**：合法操作（真 append + 按组 prefix compaction）不可能产生 seq 空洞。这是与 `orphan_witness` 同级的强信号。
- 窗口**必须**随结果输出：`anchor_window{min_seq,max_seq,entries,continuous}`。

---

## 7. 对账算法（[R40-1] + [R40-2] 合并）

```
reconcile(local, seq):
  if seq 为空:            return {trust: not_provided}                       // 仅本地状态
  if seq 格式非法:        return {trust: untrusted, reason: malformed_sequence}
  if local 窗口不可用:    return {trust: untrusted, reason: no_local_window}  // + anchor_error

  // ---- 身份（R40-2）----
  if ∃ item.key_id    != local.key_id   : return {trust: untrusted, reason: foreign_key_id}
  if ∃ item.stream_id != local.stream_id: return {trust: untrusted, reason: foreign_stream}

  shared := ids(local.anchored) ∩ ids(seq)
  if shared == ∅: return {trust: untrusted, reason: no_overlap}

  if ∀ id ∈ shared: (anchor_seq, anchor_digest) 无一与本地一致:
      return {trust: divergent, verdict: anchor_broken, divergent_ids: shared}   // 响亮，不静默
  // 否则至少一个完全一致 ⇒ aligned
  trust = aligned

  // ---- A8 遍历并集 ----
  for each id in (local.anchored ∪ seq):
      local 有 anchored ∧ 见证有 ∧ digest 相等        → witnessed
      local 有 anchored ∧ 见证有 ∧ digest 不等        → digest_mismatch      ⇒ broken
      local 有 anchored ∧ 见证无                       → missing_witness      ⇒ 至多 incomplete
      local 无 ∧ 见证有 ∧ seq ∈ [min,max]              → orphan_witness       ⇒ broken  ★核心产出
      local 无 ∧ 见证有 ∧ seq <  min                   → witness_outside_window ⇒ 不判 broken
      local 无 ∧ 见证有 ∧ seq >  max                   → witness_ahead_of_window ⇒ broken
      local pending/unanchored ∧ 见证有                → witnessed_unconfirmed（不参与断言）
  if !local.window.continuous:                          → window_discontinuous ⇒ broken

  verdict = broken 若有任一 ⇒ / incomplete 若有 pending|unanchored|missing|outside / 否则 ok
```

**不变式**：`divergent`、`orphan_witness`、`witness_ahead_of_window`、`digest_mismatch`、`window_discontinuous` 是**仅有的五个可断言 broken 来源**；`missing_witness` 与 `witness_outside_window` **永不** broken（不可区分 ⇒ 不断言，§7.8）。

---

## 8. 只读面与配置

| 项 | 内容 |
|---|---|
| `GET .../export/anchor` | 本地 anchor 状态（无见证序列 ⇒ `not_provided`） |
| `POST .../export/anchor/reconcile` | body = 见证序列；**零副作用**：不落盘、不写 audit、不发网络（ADR-052 §6-13） |
| scheduler 状态新增 | `anchor_enabled` / `anchored_count` / `pending_count` / `unanchored_count` / `anchor_error` / `anchor_window` |
| flags | `--export-anchor-endpoint`（空=关闭）、`--export-anchor-timeout`（5s）、`--export-anchor-max-attempts`（8）、`--export-anchor-capacity`（4096） |
| 位置 | 全部 `:8082` admin-only + CSRF fail-closed；`:8080` 无新路由 |

**默认零回归**：`--export-anchor-endpoint` 为空 ⇒ 不创建文件、不推送、状态面新字段全部 `omitempty` 消失、verify/coverage 输出逐字节不变（T125）。

---

## 9. 不变量（I1~I9）

| # | 不变量 |
|---|---|
| I1 | 真 `O_APPEND` 追加；既有字节永不重写 |
| I2 | 存在不可解析行 ⇒ 拒绝追加 + 不动文件 + 暴露 `anchor_error`；**绝不**借 append 重建「干净」文件 |
| I3 | 唯一允许的重写 = 按 **seq 组**的 prefix compaction，幸存行逐字复制 |
| I4 | `anchor_seq` 只在成功追加时消耗 ⇒ 幸存 seq 连续；不连续 ⇒ `window_discontinuous`（可断言） |
| I5 | 同 seq 不同 payload ⇒ conflict，fail-closed；同 seq 同 payload ⇒ 状态推进（合法） |
| I6 | 发布顺序：`atomic publish` → `ledger append` → `anchor dispatch`；anchor 失败不回滚已发布事实 |
| I7 | reconcile 零副作用：不落盘、不写 audit、不发网络 |
| I8 | 第四维正交：不修改 P35 status / P37 signature / P38 chain / P39 ledger / P36 coverage 任何取值 |
| I9 | 未配置 ⇒ 零文件、零输出、零行为变化 |

---

## 10. 测试映射

| ADR-052 用例 | 本节机制 |
|---|---|
| T125 零回归 | §8 默认关闭分支 |
| T126/T127 确认语义 | §5.2 响应表 + §4.3 |
| T128 队列满不洗证据 | §4.4（拒绝新 seq，不裁旧组） |
| T129/T130 幂等与 conflict | §1.2 + I5 |
| T131 不可解析行 fail-closed | §3.2 + I2 |
| T132 顺序与不回滚 | §4.1 + I6 |
| T133/T134 身份与未对齐 | §7 身份段 |
| T135~T138 A8 判据 | §7 遍历段 |
| T139 pending 不参与断言 | §7 末行 |
| T140~T142 跨维零干扰 | I8 + 冻结包零 diff |
| **T143 窗口外 witness-only 不判 broken** | §6 + §7（`seq < min` 分支；用例须同时覆盖窗口内 orphan ⇒ broken） |
| **T144 共享 id 分歧可断言** | §7 `divergent` 分支；须与 `no_overlap` / `foreign_stream` 的静默 untrusted 可判别 |
| **T145 non-vacuousness 红例** | §5.3 dev witness：P39 面重签删档全绿 ⇒ P40 观测为 broken |
| **T124b / T123b（新增）** | §3.3 ledger 迁移字节等价 + 双路径 fail-closed 一致 |

---

## 11. 交付顺序（每步跑门禁）

1. `appendonly_log.go` + ledger 迁移 → **T104~T124 必须全绿** + T124b 字节等价 + T123b 双路径一致
2. `snapshot_anchor.go`：结构 / canonical / 签名 / 持久化 / 窗口
3. scheduler 接线：dispatch + housekeeping + `anchor_error`（含 I6 顺序测试）
4. 只读面：两条路由 + 状态字段（含 I7 零副作用测试）
5. `main.go` 4 个 flag（含 T125 零回归）
6. 对账算法 + T143/T144/T145
7. 三道门禁（`go build` / `go vet` / `go test ./...`）+ 冻结包零 diff + `go.mod`/`go.sum` 零改动 → mktree 提交

---

## 12. 未决（提请教官确认，非悬置）

1. **ledger 迁移**：本文选「同轮迁移到共享原语」（§3.1）。若 judge 倾向不动 P39 持久化代码，回退为「anchor 独占原语 + ledger 保持原样 + T123b 守护一致性」，仅需 revert 一个文件。
2. **`stream_id` 的必要性**：我判定**需要**（同一把 key 用于多个 export dir 时，仅靠 `key_id` 会把「另一个导出流的序列」误判成 `divergent` ⇒ 误报 broken）。`stream_id = SHA-256(raw pub ‖ 0x00 ‖ abs_export_dir)[:8]`，**派生非配置**。若 judge 认为多导出目录场景不在 scope，可去掉该字段，判据退化为仅 `key_id`。
