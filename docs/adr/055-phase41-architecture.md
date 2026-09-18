# ADR-055 — Phase 41: Signing Key Lifecycle（Architecture）

- **Status**: Proposed (Round 211, Architecture stage)
- **Phase**: 41（Signing Key Lifecycle / 时间受限信任）
- **Base**: ADR-054（Scope，R211 修订版，`ca2c6974`）+ Phase 40 CLOSED（`3c690cf3`）
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **裁决依据**: R210 = **B**，R41-1~R41-5 已闭合（闭合表见 ADR-054 §9）
- **Supersedes**: 无。**不修改** P35/P36/P37/P38/P39/P40 的任何冻结判据。

---

## 0. 一句话

把「哪把签名密钥在哪个时间区间被授权」从**一份可随时改写的运行时配置**升级为**一条 append-only、带哈希链与 KAK 授权链的持久化事实**，并据此在 P37 验签之后追加一步**授权区间检查**，产出三个**独立命名、mismatch 档**的新 verdict。

> **P37 决定 who，P40 决定 where，P41 决定 when。**

---

## 1. 落盘位置与文件

| 项 | 值 |
|---|---|
| 文件名 | `signing-key-log.jsonl` |
| 目录 | **导出目录**（export dir），与 `chain-ledger.jsonl` / `chain-anchor.jsonl` / manifest 同域（ADR-054 §8-3 / R41-3） |
| 容量 | `--export-key-lifecycle-capacity`，默认 **4096**（`0` = 全留），按 **event_seq 分组**整体淘汰 |
| 底层原语 | **强制复用** `internal/controlplane/server/appendonly_log.go`（P40 落地的共享原语，`groupClassifier` 注入、**零业务词**） |
| 路径 helper | `keyLifecycleLogPath(dir) = filepath.Join(dir, keyLifecycleFile)`（对齐 `anchorLogPath`） |

**继承的纪律（不再重述理由，见 P39/R205 与 P40/A3）**：真 `O_APPEND`、既有字节永不重写、不可解析行 ⇒ fail-closed 拒绝追加且不动文件、唯一重写 = 显式 prefix compaction 且幸存行逐字复制、tmp + Sync + Rename。

---

## 2. 定稿字节

### 2.1 条目结构

```go
// keyLifecycleEntry is one immutable fact about a signing key's authorization.
type keyLifecycleEntry struct {
    V                int             `json:"v"`                   // 1
    EventSeq         int64           `json:"event_seq"`           // 严格单调，分组键
    EventType        string          `json:"event_type"`          // activated | rotated_out | revoked
    KeyID            string          `json:"key_id"`              // P37 派生（SHA-256(raw pub)[:8]），永不配置
    PubkeyFingerprint string         `json:"pubkey_fingerprint"`  // SHA-256(raw pub) 全值，绑定 who
    NotBefore        string          `json:"not_before,omitempty"` // RFC3339Nano，仅 activated
    NotAfter         string          `json:"not_after,omitempty"`  // RFC3339Nano，rotated_out / revoked
    RecordedAt       string          `json:"recorded_at"`          // 自证（非时间权威，见已知代价 1）
    AuthorityKeyID   string          `json:"authority_key_id"`     // KAK 的 key_id（P37 同一派生规则）
    StreamID         string          `json:"stream_id"`            // P40 派生（R40-2），永不配置
    PrevEventDigest  string          `json:"prev_event_digest,omitempty"` // genesis 为空
    EventDigest      string          `json:"event_digest"`         // sha256(canonicalLifecyclePayload)
    Signature        *signatureBlock `json:"signature,omitempty"`  // 复用 P37 signatureBlock（alg/key_id/signed_at/sig）
}
```

- **复用 `signatureBlock`**（P37 既有类型）而非新造签名结构 ⇒ 验签路径单一，无第二套序列化。
- `key_id` 与 `pubkey_fingerprint` **必须派生不可配置**（可配置的 key 标识只是声明，攻击者可改写一致 —— R40-2 教训）。

### 2.2 canonical payload 与哈希链

```go
func canonicalLifecyclePayload(e *keyLifecycleEntry) ([]byte, error) // 复用 P37 canonical JSON 序列化器
```

- `event_digest = sha256(canonicalLifecyclePayload(entry_minus_{event_digest, signature}))`
- `prev_event_digest = 前一条 entry 的 event_digest`；**genesis 显式为空串**（`omitempty` 消失），是唯一合法的空 prev。
- 序列化器**禁第二套**：与 `canonicalSignaturePayload` / `canonicalAnchorPayload` 同源。

### 2.3 与 ADR-053 的刻意差异（务必区分）

| | anchor 条目（ADR-053） | 生命周期事件（本 ADR） |
|---|---|---|
| 性质 | 投递**状态**（pending→anchored） | **事实**（一旦落盘即终态） |
| 同 seq 第二行 | 同 payload = 状态推进合法；不同 payload = conflict | **任何第二行一律 conflict ⇒ fail-closed** |
| compaction 单位 | 整个 seq 组 | 整个 seq 组（= 单条事件） |

理由：生命周期事件不存在状态推进语义；允许「重复同一事实」只会给「重放洗白」留通道。

---

## 3. 事件状态机（每 key_id 独立）

```
                 ┌──────────── revoked ────────────┐
                 │                                 │
  (none) ──activated──► ACTIVE ──rotated_out──► ROTATED_OUT ──► (终态)
                 │                                 │
                 └──────────── revoked ────────────┘
```

| 规则 | 判定 |
|---|---|
| 首事件必须是 `activated` | 否则 fail-closed 拒绝追加（无基线的吊销/轮换是无意义事实） |
| `activated` 每 key_id **至多一次** | 第二次 ⇒ 拒绝（不可复活铁律的正面） |
| `revoked` / `rotated_out` 之后同 key_id 再 `activated` | **fail-closed 拒绝追加 + 不动文件 + 暴露 error**（复活 = 洗证据） |
| `rotated_out` → `revoked` | **允许**（轮换后仍可因泄露吊销） |
| `revoked` → `rotated_out` | 允许（幂等收缩，取更早的 `not_after`） |
| `not_after ≥ not_before` | 违反 ⇒ 拒绝 |
| `event_seq` 严格单调 | 违反 ⇒ 拒绝（由 appendonly 分组 + 校验双重保证） |
| `recorded_at` | 自证，仅做非空与可解析校验，**不参与任何断言** |
| 重复提交同 `(key_id, event_type, not_after)` | **拒绝**（duplicate fail-closed），不 last-write-wins |

---

## 4. 授权区间解析（interval resolution）

```go
type keyAuthorization struct {
    KeyID        string
    NotBefore    string // "" = 无下界
    NotAfter     string // "" = 无上界
    Terminal     string // "" | "rotated_out" | "revoked"
    Complete     bool   // 该 key 的全部事件是否都在窗口内（§5）
    Source       string // "lifecycle_ledger" | "unbounded"
}
```

解析顺序（纯函数，零副作用）：

1. **未启用**（无账本文件 / 未配 KAK 信任公钥 / 账本不可验）⇒ `Source="unbounded"`，区间 `(-∞,+∞)`，`Terminal=""`。
2. 该 key_id 无 `activated` 事件 ⇒ **unbounded**（启用前的历史密钥，绝不断言、绝不推断 —— 已知代价 3）。
3. 有 `activated` ⇒ `NotBefore = activated.not_before`。
4. 收集该 key_id 的 `rotated_out` / `revoked`，`NotAfter = min(全部 not_after)`；**并列时 Terminal 取 `revoked`**（更强语义优先，命名更能反映审计含义）。
5. 该 key 的**任一**事件不在窗口 `[min,max]` 内 ⇒ `Complete=false` ⇒ **退化为 unbounded** 并标记 `lifecycle_window_insufficient`（§5）。

---

## 5. 窗口与 compaction（R40-1 同构纪律的实例化）

- 窗口 = 幸存 `event_seq` 的**连续段** `[min,max]`（真 O_APPEND + prefix compaction + seq 只在成功追加时消耗 ⇒ 必然连续），字段 `lifecycle_window{min,max,entries,continuous}`。
- **窗口不连续 ⇒ `window_discontinuous`**（可断言，暴露 error），同构 P40 T143。
- **区间判据（关键）**：断言 `after_*` 要求**该 key 的授权区间完整可见**。判据不是「`signed_at` 落在窗口时间范围内」，而是：

  > 该 key_id 的**全部**生命周期事件的 `event_seq ∈ [min,max]` ⇒ `Complete=true` ⇒ 可断言；
  > 任一事件被 compaction 裁掉 ⇒ `Complete=false` ⇒ **该 key 一律 unbounded**，**绝不**判 `after_revocation` / `after_rotation`。

- `activated` 被裁掉 ⇒ 下界丢失（与「从未记录」不可区分）；`revoked` 被裁掉 ⇒ 上界丢失（与「从未吊销」不可区分）。二者**都不得**报为吊销，这正是 §6-4 与 R40-1 的同构。
- `before_activation` 的断言同样要求 `Complete=true`（否则 `activated` 可能只是被裁掉了）。

---

## 6. 验签接线（P37 之后追加，函数体零改动）

### 6.1 新常量

```go
sigVerdictBeforeActivation = "signature_before_activation"
sigVerdictAfterRotation    = "signature_after_rotation"
sigVerdictAfterRevocation  = "signature_after_revocation"
```

### 6.2 追加步骤

```go
// authorizeByLifecycle is applied ONLY when P37 already returned signature_ok.
func authorizeByLifecycle(v SignatureVerdict, signedAt string, a keyAuthorization) SignatureVerdict
```

| 条件 | 结果 |
|---|---|
| `a.Source == "unbounded"` 或 `!a.Complete` | **保持 `signature_ok`**（不断言，不谎报） |
| `signedAt < a.NotBefore` | `signature_before_activation` |
| `a.NotAfter != "" && signedAt >= a.NotAfter && a.Terminal == "revoked"` | `signature_after_revocation` ★核心产出 |
| `a.NotAfter != "" && signedAt >= a.NotAfter && a.Terminal == "rotated_out"` | `signature_after_rotation` |

- 时间比较统一解析为 `time.RFC3339Nano` UTC；不可解析 ⇒ **保持 `signature_ok`** 并记 `key_lifecycle_error`（不可解析的时间不是证据，不得用来下断言 —— 与 P38「不可解析 ⇒ 不排序、不猜测」同纪律）。
- **调用点唯一**：`history_export_manifest.go` 的验签装配处，在 `verifyManifestSignature` 之后、`applySignatureVerdict` 之前。

### 6.3 状态降级（`applySignatureVerdict` 增补，R41-1）

```go
case sigVerdictAfterRevocation, sigVerdictAfterRotation, sigVerdictBeforeActivation:
    return verifyStatusMismatch   // 与 signature_invalid 同档；verdict 名保持独立
```

- 既有分支**一行不改**；新分支只增不减。
- `mismatch` 是**上限**：本 Phase 不引入任何比 mismatch 更重的档位。

---

## 7. KAK（key-authority key）与 flag

| flag | 侧 | 语义 |
|---|---|---|
| `--export-key-authority <path>` | 写入 | KAK **私钥**（PKCS#8 PEM 或 raw 64B），只签生命周期事件；**空 = 生命周期账本不可写**（功能未启用，零回归） |
| `--export-key-authority-trust <csv>` | 验证 | KAK **公钥**集合，用于校验账本事件签名；**空 = 账本不可验 ⇒ 退化为 unbounded + `key_lifecycle_error`**（绝不静默、绝不假装全开是合法的） |
| `--export-key-lifecycle-capacity <n>` | 两侧 | 默认 4096，`0` = 全留 |

**硬规则**

1. **KAK 与签名密钥必须不同**：`authority_key_id == signature.key_id` ⇒ 构造失败（fail-closed）。KAK 一旦等于签名密钥，整条授权链退化为自证。
2. **genesis 必须 KAK 签**（R41-2）：无 KAK ⇒ 无法创建账本 ⇒ 功能未启用；**不接受自签**。
3. KAK 是普通 Ed25519 密钥文件，**不做 HSM/KMS**（非目标 2）。

---

## 8. 写入面（admin-only `:8082`，CSRF fail-closed）

| 路由 | 方法 | 说明 |
|---|---|---|
| `/management/v1/protection/export/key-lifecycle` | `GET` | 账本状态：`enabled` / `active_key_id` / 各 key 授权区间 / `key_lifecycle_error` / `lifecycle_window` |
| `/management/v1/protection/export/key-lifecycle` | `POST` | 追加一条事件，body `{event_type, key_id, not_before?, not_after?}` |

**POST 校验顺序（任一失败 ⇒ 400/409 + 不动文件 + 暴露 error）**

1. KAK 私钥与 KAK 信任公钥均已配置（否则 503：功能未启用，不是错误）；
2. `event_type` ∈ 三类；`key_id` 必须**已在** `--export-trust-keys` 信任集中（不为未知 key 立账 —— who 仍由 P37 决定）；
3. 状态机合法（§3）；
4. 计算 `event_seq = last_seq + 1`、`prev_event_digest`、`event_digest`；
5. KAK 签名 ⇒ `appendLogLine`（真 O_APPEND）；
6. anchor 启用 ⇒ 进入 P40 dispatch（§9）。

**失败语义**：追加失败 ⇒ **不重试、不回滚、不重建**（I1）；账本不可解析 ⇒ 拒绝追加且暴露 `key_lifecycle_error`。

---

## 9. 与 P40 anchor 的接线

- 生命周期事件追加成功后，若 anchor 启用 ⇒ 生成一条**新的 anchor payload 家族**：`kind="key_lifecycle"`，携带 `{v, key_id, stream_id, event_seq, event_digest, event_type, not_after}`，`anchor_digest = sha256(canonicalAnchorPayload)`。
- **复用** `HistoryExportScheduler.dispatchAnchor` 与 ack 语义（2xx + 非空 ack_id = anchored；重试上限 8 ⇒ `unanchored` 终态）。
- 生命周期事件的 anchor **不复用 `anchor_seq` 序列**，也不沿用 publication 语义：它是第二条独立的 anchor 记录流，避免与「发布锚定」语义互相污染。
- **禁止**（ADR-054 A5）：用 anchor 结果反推密钥有效性；用生命周期账本填补 anchor 空洞。

---

## 10. 输出面（只增不减，`omitempty`）

- `SignatureVerdict` 增补 `Validity *signatureValidity{authorized_from, authorized_until, terminal_event, source} json:"validity,omitempty"`。
- 只读面 `GET .../export/verify` 的 signature 段在**启用时**才出现 `validity`；未启用 ⇒ 逐字节不变。
- 状态面 `key_lifecycle{enabled, active_key_id, event_count, error, window{...}}`（`omitempty`）。
- `history_export_coverage.go` **零 diff**。

---

## 11. 冻结面与零回归（硬约束，实现期逐条核对）

| 项 | 要求 |
|---|---|
| `platform` / `governance` / `plugin/{runtime,isolation}` / `controlplane/hostregistry` | **零 diff** |
| `go.mod` / `go.sum` | **零改动**，不新增依赖 |
| `history_export_coverage.go` | **零 diff** |
| P35 status / P36 coverage / P37 五态 / P38 chain / P39 ledger / P40 anchor 取值与判据 | **零改动** |
| 未启用（无 KAK 配置） | 不创建 `signing-key-log.jsonl`，输出与今日**逐字节一致** |
| `verifyManifestSignature` 函数体 | **零改动**（新逻辑在其之后） |

---

## 12. 已知代价（继承 ADR-054 §6，实现期不得淡化）

1. 时间仍是**自证**的 —— P41 不是时间戳证明。
2. 安全性上界 = **KAK 独立性**（同域 ⇒ 退化为自证，不检测）。
3. 只覆盖**启用之后**记录的事件；启用前密钥 ⇒ unbounded。
4. 账本有界 ⇒ compaction 后区间丢失 ⇒ unbounded（§5）。
5. 吊销需要**及时性**（运营属性）。
6. **腐账 = 撤销洗白攻击面**（R41-4 四要素，ADR-054 §6-6）：本地账本被毁 ⇒ 区间回到全开；唯一缓解是 P40 域外副本 + KAK 离线 + 禁止自动重建；`key_lifecycle_error` 必须响亮。
7. **同钥多目录 ⇒ 吊销不跨域**（R41-3）：明确不扇出，逐目录执行是运维程序；遗漏在本地静默，仅域外可观测。

---

## 13. 非目标增补（在 ADR-054 A7 之上）

12. **不做跨目录扇出与跨目录对账**（A7-11 的实现层陈述）：无全局目录清单，无跨目录 reconcile 路由。
13. **不做时间权威**（TSA / RFC3161），`recorded_at` / `signed_at` 均为自证。
14. **不做自动轮换策略**（无「每 90 天自动 rotated_out」）。
15. **不记录吊销原因**（只记 what/when，不记 why）。

---

## 14. 实现清单（文件 → 主要符号）

| 文件 | 内容 |
|---|---|
| `internal/controlplane/server/snapshot_key_lifecycle.go`（新） | `keyLifecycleFile` / `keyLifecycleEntry` / `canonicalLifecyclePayload` / `loadKeyLifecycleState` / `resolveKeyAuthorization` / `appendKeyLifecycleEvent` / `compactKeyLifecyclePrefix` / `authorizeByLifecycle` / `keyLifecycleStatusSummary` |
| `internal/controlplane/server/snapshot_signature.go` | 三个新 `sigVerdict*` 常量 + `applySignatureVerdict` 增一个 case |
| `internal/controlplane/server/history_export_manifest.go` | 验签装配处插入 `authorizeByLifecycle` 调用（**唯一调用点**） |
| `internal/controlplane/server/server.go` | 2 条路由（`GET`/`POST .../export/key-lifecycle`），`mux.HandleFunc`，admin-only + `sameOriginOrFail` |
| `internal/controlplane/server/snapshot_anchor.go` | anchor payload 家族增 `kind="key_lifecycle"`（复用 dispatch） |
| `cmd/opscore/main.go` | 3 个 flag（§7） |
| `internal/controlplane/server/appendonly_log.go` | **零改动**（仅复用） |

---

## 15. 测试映射（T155~T177，承接 ADR-054 §7）

| 编号 | 落点 |
|---|---|
| T155 / T169 | 未启用零回归 + 不创建文件（§11） |
| T156 / T158 | `rotated_out` 与 `revoked` 的**历史保住**（§4、§6.2 不断言分支） |
| T157 / T174 | 两个 `after_*` 断言（含旋转腿） |
| T159 / T175 | 判别面 + mismatch 档位（§6.3） |
| T160 / T161 / T177 | fail-closed / 哈希链 / 腐账红例（§1 纪律、§12-6） |
| T162 | 不可复活（§3） |
| T163 / T164 | 窗口（§5） |
| T165 | anchor 接线（§9） |
| T166 / T167 / T168 | 跨维零干扰 + 冻结面零 diff + go.mod 零改动（§11） |
| T170 | non-vacuousness 红例（**吊销腿 + 旋转腿**） |
| T171 / T172 | 变异判别 M1 / M2（现取红证据，按 sha256 字节恢复） |
| T173 | genesis 必须 KAK 签 + 自签被拒 + KAK≠签名密钥（§7） |
| T176 | 同钥多目录（§12-7） |

---

## 16. 请求裁决

Architecture 定稿如上。请裁决 **A / B(含必修) / C**。

- 若 **A** ⇒ 进入 Implementation 阶段：按 §14 落地，跑三道门禁，附冻结面零 diff 与 `go.mod/go.sum` 零改动证据，并现取 M1/M2 变异红证据。
- 若 **B** ⇒ 请列必修条款编号（R41-x 续），我逐条闭合后同轮投递修订版 ADR-055。
- 若 **C** ⇒ 请指出方向性否定点，我据此重开 Scope。
