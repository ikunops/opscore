# ADR-054 — Phase 41: Signing Key Lifecycle（签名密钥生命周期 / 时间受限信任）

- **Status**: Proposed (Round 210, Scope stage)
- **Phase**: 41（Signing Key Lifecycle）
- **Base**: Phase 40 CLOSED（R209=A，origin/main = `3c690cf3`）
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **Supersedes**: 无。本 ADR **不修改** P35/P36/P37/P38/P39/P40 的任何冻结判据。

---

## 1. 方向选择

### 1.1 候选淘汰表

| 候选 | 判定 | 理由 |
|---|---|---|
| **HA 多副本**（R210 点名要求重新论证） | ❌ 仍淘汰 | R207 的淘汰理由是「与主线和单机 scheduler 不变量脱节」。按你的要求补**新的主线接口论证**：P30~P40 的主线接口是**「单节点产出的证据是否可验证」**，每一环的产出都是一个**可断言的判据**（status / coverage / signature / chain / ledger / anchor）。HA 的产出是「多副本内容一致」——**一致性不是证据性**：同一攻击者同时控制两个副本时，两副本完全一致且全部本地检查通过。它不产生任何新的可断言判据，只把现有判据复制 N 份。要让它产生新判据就必须引入**域外或跨域比较**，而那条路 P40 已经用更低的成本走通（anchor 到域外）。此外它要求改动的单机不变量（`publication-seq.state`、atomic `os.Link`、Tick 非重入、`O_EXCL` sentinel）全部位于**冻结面**，工程量集中在共识而非证据。**结论：仍淘汰。** |
| **full-forensic retention** | ❌ 淘汰 | 与 R207 同：其冲突已被 P39 ledger 解决，属被前序 Phase 消灭的问题。 |
| **Signing Key Lifecycle（轮换 / 有效期 / 吊销）** | ✅ **选定** | 见 §2。它是**唯一**一个「本地判据集合本身缺一维」的候选：P37 的信任判定是 `key_id → bool`，**没有时间参数**，由此产生一个在运营上必然发生、而在模型里无解的悖论（§2.2）。 |

### 1.2 P22 时代遗留候选自查（R210 点名）

| 遗留候选 | 现状 | 结论 |
|---|---|---|
| **每能力资源配额** | `internal/controlplane/server/mgmt_quota.go`（`handleProtectionQuotas` / `QuotaSet` / `QuotaClear`）+ `protection_quota_test.go` + ADR-051（Phase 23 Quota Protection） | **已闭合，不再开放** |
| **Dashboard-UI kill 翻转面** | `internal/controlplane/server/mgmt_kill.go` 已落地 `handleProtectionKill` / `handleProtectionRelease`（kill/release 翻转），并含 P22-9 的 httpOnly+SameSite=Strict 会话 cookie 与 `sameOriginOrFail` CSRF fail-closed | **已闭合，不再开放** |

⇒ 两个遗留候选均不开放，R210 无需解冻。

---

## 2. 新问题（Problem Statement）

### 2.1 现状事实（已核对代码，非推断）

P37 的信任锚是一个**静态、无时间维度**的公钥集合（`internal/controlplane/server/snapshot_signature.go`）：

```go
type exportTrustStore struct {
	keys map[string]ed25519.PublicKey   // key_id → pub，来自 --export-trust-keys
}
```

验签五态 `signature_ok / signature_invalid / signature_absent / signature_malformed / key_unknown` 的判定链为：
`结构完整 → key_id ∈ keys → canonical payload → ed25519.Verify`。

其中 `signatureBlock.SignedAt` **只被检查「非空」**（`sb.SignedAt == ""` ⇒ malformed），**从未与任何值比较**——既不与账本比较，也不与相邻 manifest 比较，也不与任何授权区间比较。它是 payload 的一部分（因此被签名绑定），但它是**签名者自证的时间**，不是**被验证的时间**。

相邻 Phase 也都不使用时间：P38 只在 `publication_id` 轴取前驱（冻结：不可解析 ⇒ 不排序、不猜测、fail-closed，更不可能用时间戳排序）；P39 ledger 的 `recorded_at` 同为自证；P40 anchor 只见证「启用之后**成功推送**的发布」。

### 2.2 由此产生的悖论：吊销 vs 保史（核心新问题）

设签名密钥 K1 在时刻 T 被确认为泄露。运营者今天只有两个动作可选：

| 动作 | 后果 |
|---|---|
| **保留 K1 于 `--export-trust-keys`** | K1 在 T **之后**被伪造的任何 manifest 仍然 `signature_ok` —— **伪造能力无法被撤销** |
| **把 K1 移出信任集** | K1 在 T **之前**签发的**全部历史** manifest 立即变 `key_unknown` ⇒ P37 状态降至 `unknown`；再按 P39「recovery 只能从 `signature_ok` 的 manifest 生成」与回溯式验证，**整条链的可验证性在一次吊销中整体归零** |

> **「撤销一把密钥的伪造能力」与「保留它签发过的历史证据」，在当前模型下不可兼得。**

这不是运维取舍困难，而是**模型缺一维**：信任判定是 `key_id → bool`，把两个本应分离的概念压成了同一个比特——

- **这把密钥是不是我们认识的（身份 / who）**；
- **这把密钥在那个时刻是否被授权（时间 / when）**。

删除公钥这一动作，同时表达「不再认识它」和「不再承认它」，而它实际想表达的只是后者。

### 2.3 两个派生的同形性

**S1 — 回填同形（时间自证的直接后果）**
用泄露的 K1 在 T+30d 补签一份 `signed_at = T-1d` 的 manifest：本地验签 `signature_ok`；P38 在 id 轴重接即 `chain_ok`；P39 ledger 的 `recorded_at` 同样可回填；P40 只见证发布**成功推送**的产物，而补签物不走发布路径 ⇒ 无 anchor 条目 ⇒ P40 观测不到（其非目标第 10 条「不区分从未推送与推送后被丢弃」进一步放大了这点）。
⇒ **「这份 manifest 是当时签的」与「这是后来用同一把已泄露密钥补签、回填时间的」在本地不可区分。**

**S2 — 轮换与吊销同形**
轮换（K1 退役、K2 启用）与吊销（K1 泄露、不再被承认）在当前模型里是**同一个动作**（改一次配置）。因此系统无法回答一个合法且常见的问题：**「这条链上 #49 由 K1 签、#50 由 K2 签，是正常轮换，还是有人引入了一把并行密钥？」** 二者同形。

### 2.4 问题的精确表述

> 缺少**密钥授权的时间维度及其留痕**：密钥的启用 / 轮换 / 吊销是**影响全部历史证据如何被解释**的事实，
> 但这些事实今天**完全只存在于一份可被随时改写的运行时配置中，且不留任何痕迹**。
> 因此「签名在授权期内」与「签名在授权期外（或密钥已泄露）」在本地视图内**同形**。

### 2.5 为什么必须由 P41 解决、不能并入 P37 或 P40

- **并入 P37** 会违反 P37 两条冻结：①信任锚必须是独立配置源、永不从被验对象派生（P41 引入的账本是一个**新的持久化证据对象**，不是配置）；②P37 的五个 verdict 取值与降级表已冻结。P41 是**给 P37 的判定补一个时间参数**，而不是重写 P37。
- **并入 P40** 不成立：P40 解决的是**空间**同形（本地缺失 vs 从未存在，靠域外见证），其 payload 不包含、也不应包含「密钥在何时被授权」这一语义；且 P40 只覆盖启用后成功推送的发布。P41 解决的是**时间**同形（授权期内 vs 授权期外），两者正交。
- 一句话分工：**P37 决定 who，P40 决定 where，P41 决定 when。**

---

## 3. 决策（Scope）

引入 **Signing Key Lifecycle（签名密钥生命周期）**：

1. 新增 **append-only 的密钥生命周期账本** `signing-key-log.jsonl`，把「哪把密钥在什么时间区间被授权」从**配置状态**升级为**带哈希链与授权链的持久化事实**；
2. 把 **轮换（rotated_out）与吊销（revoked）拆成两个不同事件**——这是消解 §2.2 悖论的mechanism；
3. 验签在**既有 P37 判定之后**追加一步**授权区间检查**，产出一个**新的、可断言的** verdict，用于撤销已泄露密钥对**其吊销时刻之后**产物的伪造能力，同时**完整保留**其吊销之前的历史为 `signature_ok`。

**一句话价值**：P38 让删除**留痕**，P40 让删除**可被域外证明**，P41 让**泄露的密钥在其泄露之后不再被承认，且不摧毁它泄露之前签下的历史**。

---

## 4. 能力定义（A1~A8）

### A1 — 生命周期账本（持久化纪律零新建）

文件 `signing-key-log.jsonl`（bounded，默认 4096，复用 P39/P40 的 capacity flag 惯例）。

**强制复用 P40 落地的共享原语 `appendonly_log.go`**（语义无关、分组由 `groupClassifier` 注入），并继承 P39/R205 + P40/A3 全部纪律：

- 真 `O_APPEND` 追加，**既有字节永不重写**；
- 存在**不可解析行 ⇒ fail-closed**：拒绝追加、不动文件、暴露 `key_lifecycle_error`；**绝不**借 append 重建「干净」文件把坏证据洗掉；
- 唯一允许的重写 = **显式 prefix compaction**，幸存行**逐字复制**；
- 失败路径清理句柄，tmp + Sync + Rename 原子重写。

条目（架构 ADR 定稿字节，此处只定语义）：

```
{event_seq, event_type, key_id, pubkey_fingerprint,
 not_before, not_after, recorded_at,
 authority_key_id, prev_event_digest, event_digest, signature}
```

### A2 — 三类事件，语义严格分离（核心）

| event_type | 含义 | 对 `signed_at < not_after` 的历史 | 对 `signed_at ≥ not_after` 的产物 |
|---|---|---|---|
| `activated` | key_id 自 `not_before` 起被授权签发 | — | — |
| `rotated_out` | key_id 自 `not_after` 起**不再用于新签名**；**不是否定** | **仍 `signature_ok`（完整有效）** | 由 `activated` 的继任者签发；若出现该 key 的签名 ⇒ 属 `signature_before_activation` 之外的新信号（见 A3） |
| `revoked` | key_id 自 `not_after` 起**整体不被承认**（泄露） | **仍 `signature_ok`（历史保住）** | **`signature_after_revocation`（可断言）★核心产出** |

**关键**：运维上吊销**不再通过删除公钥表达**，而是通过写 `revoked` 事件表达。公钥应**永久保留**在信任集中（它回答 who），时间边界由账本回答（when）。§2.2 的悖论由此消解。

### A3 — 授权区间检查与新 verdict（只增不减）

验签顺序（在 P37 **既有判定全部保持不变之后**追加）：

```
P37 verdict == signature_ok
  ∧ 该 key_id 在账本中存在授权区间
  ∧ signed_at ∉ [not_before, not_after)
        ⇒ 降级为 signature_before_activation | signature_after_revocation
```

- `signature_before_activation`：`signed_at < not_before`。
- `signature_after_revocation`：`signed_at ≥ not_after`（`revoked` 事件）。**这是本 Phase 的核心产出：P35~P40 给不出。**

**既有 P37 五态取值、语义与降级表零改动**（`invalid ⇒ mismatch`，`key_unknown/malformed ⇒ 至多 unknown`，签名永不升级状态）。新 verdict **只增不减**：今天会输出 `signature_ok` 的输入，在**未启用账本**时仍输出 `signature_ok`（A6）。

### A4 — 账本自身的可验证性（哈希链 + 授权链）

- **哈希链**：`event_digest = sha256(canonicalEventPayload)`，`prev_event_digest` 串联 ⇒ 事件顺序不可重排、删除留痕（同构 P38 逐跳双承诺的思路，但对象不同）。
- **授权链**：事件由 `authority_key_id` 签名。默认由 **KAK（key-authority key）** 签——一把独立于 `--export-sign-key`、可离线保存的 Ed25519 密钥，**只签生命周期事件，不签 manifest**。genesis `activated` 允许**自签**（bootstrap，诚实标注 `authority=bootstrap`）。
  - ⇒ 攻击者仅持有泄露的 K1 **无法伪造、也无法无痕删除**一条由 KAK 签发的 `revoked` 事件（P40 启用时该事件还会被域外锚定，A5）。
- **不可复活铁律**：`revoked` / `rotated_out` 之后，同 key_id 再出现 `activated` ⇒ **fail-closed 拒绝追加 + 不动文件 + 暴露 error**（复活 = 洗证据）。
- 同一 key_id 的 `activated` 至多一次；`not_after ≥ not_before`；`event_seq` 严格单调。
- 账本不可解析 / 哈希链断裂 / 事件签名不可验 ⇒ 暴露 `key_lifecycle_error`，并**退化为「无账本」语义（区间全开）**，**绝不静默**（同构 P39 `ledger_error` 纪律）。

### A5 — 与 P40 的关系（正交，不互推）

- 生命周期事件**在 anchor 启用时同样进入 P40 的 dispatch 通道**（复用其 payload 规范与 ack 语义），使「K1 于 T 吊销」这一事实本身可被域外见证。
- **禁止**用 anchor 结果反推密钥有效性（P40 只见证存在性，不承载时间语义）；**禁止**用生命周期账本填补 anchor 空洞（两者是不同性质的证据）。
- anchor 的 `key_id` / `stream_id` 派生规则**零改动**。

### A6 — 默认零回归（硬约束）

- 未启用（无账本文件 / 未配置）⇒ **授权区间 = (-∞, +∞)**，验签结论与今天**逐字节一致**，状态面新字段全部 `omitempty` 消失，不创建任何文件、不改变任何输出。
- `history_export_coverage.go` 零 diff。
- **冻结包零 diff**：`platform` / `governance` / `plugin/{runtime,isolation}` / `controlplane/hostregistry`。
- `go.mod` / `go.sum` 零改动，不新增依赖。

### A7 — 非目标（重新冻结清单）

1. **不做 PKI**：不引入 CA / X.509 证书链 / CRL / OCSP / 证书路径验证。
2. **不做密钥托管**：不引入 HSM / KMS / 密钥生成与分发；KAK 只是一把普通的离线 Ed25519 密钥文件。
3. **不提供时间权威（重要诚实声明）**：不引入 TSA / RFC3161 / 可信时间戳；`signed_at` **仍是签名者自证值**。本 Phase 只约束**授权区间**，**不证明绝对时刻**。⇒ 已知代价见 §7。
4. **不做自动轮换策略**：只提供显式事件记录入口（scheduler/只读面）与查询，不做「每 90 天自动轮换」。
5. **不修改** P35 status / P36 coverage / P37 既有五态 / P38 chain / P39 ledger / P40 anchor 的任何取值与判据。
6. **不记录吊销原因**：账本只记事实（what/when），不记叙事（why），避免把运营主观描述写成证据。
7. **不从账本恢复或补写** manifest / ledger / anchor（同构 P39「recovery 只能从 `signature_ok` 的 manifest 生成」）。
8. **不在验证路径发起网络调用**：reconcile 严格只读、零副作用（沿用 P40/A6）。
9. **不解决 KAK 同域问题**：KAK 若与签名密钥同主机同攻击者控制，则退化为自证——**本 Phase 不检测也不解决**（同构 P40「见证端独立性」的已知代价）。
10. **不新增常驻组件**：复用 scheduler tick，Tick 非重入纪律不变。

### A8 — 结果模型

- `SignatureVerdict` 增补可选字段 `validity{authorized_from, authorized_until, source}`，`omitempty`。
- 只读面新增（仅 `:8082` admin-only，CSRF fail-closed）：
  - `GET /management/v1/protection/export/key-lifecycle` —— 账本状态：事件数 / 当前 active key / 各 key 授权区间 / `key_lifecycle_error` / 窗口。
- 状态面新增 `key_lifecycle{enabled, active_key_id, event_count, error}`（`omitempty`）。

---

## 5. 不变量（I1~I8）

| # | 不变量 |
|---|---|
| I1 | 真 `O_APPEND`；既有字节永不重写；不可解析行 ⇒ fail-closed 拒绝追加 + 不动文件 + 暴露 error |
| I2 | 唯一重写 = 显式 prefix compaction，幸存行逐字复制；compaction 拒绝不可分类行 |
| I3 | `event_seq` 严格单调；`prev_event_digest` 链不可重排 |
| I4 | `revoked`/`rotated_out` 之后同 key_id 的 `activated` 一律 fail-closed 拒绝（不可复活） |
| I5 | `revoked` **只收缩** `not_after`，**永不影响** `not_after` 之前的历史 ⇒ `signature_ok` 保持 |
| I6 | 未启用 ⇒ 区间全开，输出与今日逐字节一致（零回归） |
| I7 | P37 既有五态取值/语义/降级表零改动；新 verdict 只增不减 |
| I8 | 冻结包零 diff；`go.mod`/`go.sum` 零改动；不新增依赖；P36 coverage 零 diff |

---

## 6. 已知代价（诚实声明）

1. **时间仍是自证的**（A7-3）：攻击者若同时持有泄露密钥**且**能操纵系统时钟/回填 `signed_at`，仍可构造落在授权区间内的伪造签名。**P41 不是时间戳证明。** 本 Phase 的价值是把「密钥不再被承认」这一**运营事实**固化为可验证证据，而不是证明某个绝对时刻。
2. **安全性上界 = KAK 的独立性**（同构 P40）：KAK 与签名密钥同域 ⇒ 退化为自证。价值由部署拓扑决定，本 Phase 不检测。
3. **只覆盖启用之后记录的事件**：启用前已存在的历史密钥**没有授权区间** ⇒ 一律按**全开**处理，**绝不**推断、也**绝不**据此判 `after_revocation`。
4. **账本有界 ⇒ 早期事件可能消失**：compaction 之后，早期密钥的授权区间**下界丢失**，与「账本从未记录」**不可区分** ⇒ 一律按全开处理，**绝不**判 `after_revocation`。
   > 这一条是 R40-1（窗口外 witness 不判 broken）的同构风险，本轮**主动先行闭合**，不等裁决提必修：结果强制携带 `lifecycle_window{min,max,entries,continuous}`；`signed_at` 落在窗口**外**的事件**不参与** `after_revocation` 断言；窗口不连续 ⇒ `window_discontinuous` 可断言。
5. **吊销需要及时性**：`revoked.not_after` 应尽可能接近实际泄露时刻；写晚了，中间窗口内的伪造品仍是 `signature_ok`。这是运营属性，不是实现可消除的。

---

## 7. 测试契约（T155~T172）

| 编号 | 用例 | 覆盖 |
|---|---|---|
| T155 | 未启用 ⇒ verify/coverage/anchor 输出逐字节不变 | I6 / A6 |
| T156 | `rotated_out(K1)` 且 K1 仍在信任集 ⇒ 其历史 `signature_ok` | A2（悖论消解 · 保史） |
| T157 | `revoked(K1,T)` ⇒ `signed_at ≥ T` ⇒ `signature_after_revocation` | A3（核心产出） |
| T158 | `revoked(K1,T)` ⇒ `signed_at < T` ⇒ 仍 `signature_ok` | I5 |
| T159 | `after_revocation` 与 `key_unknown` 可判别（不是同一 verdict，detail 不同） | A3 |
| T160 | 账本含不可解析行 ⇒ fail-closed 拒绝追加 + 文件字节不动 + 暴露 error | I1 |
| T161 | 哈希链断裂 ⇒ 暴露 error + 退化为全开（绝不静默、绝不谎报吊销） | A4 |
| T162 | `revoked` 后同 key 再 `activated` ⇒ fail-closed 拒绝 | I4 |
| T163 | compaction 后早期事件消失 ⇒ 该 key 区间全开，**不**判 `after_revocation` | §6-4 |
| T164 | 窗口外 `signed_at` 不参与断言；窗口不连续 ⇒ `window_discontinuous` | §6-4 |
| T165 | 生命周期事件在 anchor 启用时被推送，reconcile 可观测 | A5 |
| T166 | 双密钥轮换链 K1→K2：跨 key 的 P38 链仍 `chain_ok`，输出零 diff | I8 |
| T167 | 跨维零干扰：P35 status / P36 coverage / P38 chain / P39 ledger / P40 anchor 取值零变化 | I8 |
| T168 | `go.mod`/`go.sum` 零改动 + 冻结包零 diff | I8 |
| T169 | 默认零回归：不创建 `signing-key-log.jsonl` | I6 |
| T170 | **non-vacuousness 红例**：持泄露 K1 在吊销后重签 manifest ⇒ P35~P40 全绿（status ok / signature_ok / chain_ok / ledger 无 error / anchor 未见异常）⇒ **仅 P41 观测 `signature_after_revocation`** | §2.2 |
| T171 | 变异判别 M1：摘掉 `signed_at ≥ not_after` 判据 ⇒ T157 必红（按 sha256 字节恢复） | A3 |
| T172 | 变异判别 M2：把 `revoked` 当成 `rotated_out` 处理 ⇒ T170 必红 | A2 |

---

## 8. 提请你明示的取舍（三处）

1. **`signature_after_revocation` 的降级档位**：我选**保守**——与 `key_unknown` 同档（至多 `unknown`），理由是「签名是真的、授权过期了」不该被说成篡改（`invalid ⇒ mismatch`）。但反向论证同样有力：吊销意味着「此密钥之后签的一概不承认」，判成 `unknown` 等于给伪造品留下「无法判定」的模糊空间，与你在 P40 采纳的「orphan_witness ⇒ broken」响亮原则同构。**请裁决是否升为 `mismatch`。**
2. **genesis `activated` 自签**是否可接受（bootstrap 不可自证，其信任来自配置）。替代方案：要求 KAK 签 genesis —— 代价是首次启用必须先存在 KAK，且启用前历史密钥仍然没有区间。
3. **账本粒度**：一个导出目录一个账本（与 export dir 同域），还是全局一个（与 P40 `stream_id` 的「同 key 多目录」问题同构）。我倾向**与 export dir 同域**，与 anchor/ledger 保持一致，避免引入新的跨域歧义。

请裁决。若需 Architecture ADR 我按你的口径续写 R211。
