# ADR-054 — Phase 41: Signing Key Lifecycle（签名密钥生命周期 / 时间受限信任）

- **Status**: Proposed (Round 211, Scope stage — **revised per R210 verdict B**，R41-1~R41-5 已闭合)
- **Phase**: 41（Signing Key Lifecycle）
- **Base**: Phase 40 CLOSED（R209=A，origin/main = `3c690cf3`）；本 ADR 提交 `ca2c6974`
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **Supersedes**: 无。本 ADR **不修改** P35/P36/P37/P38/P39/P40 的任何冻结判据。
- **修订记录**：R210=B ⇒ 三处取舍已由 judge 裁决（见 §8，改写为已裁决口径）+ 追加必修 R41-4 / R41-5 闭合（分别落 §6-6 与 §4-A2/A3）。**A2/A3/A4/§6/§7 为修订面；§1/§2/§3/§5 未动。**
- **配套**：Architecture ADR-055（`055-phase41-architecture.md`，同轮交付）。

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
| `rotated_out` | key_id 自 `not_after` 起**不再用于新签名**；**不是否定** | **仍 `signature_ok`（完整有效）** | **`signature_after_rotation`（可断言）** —— R41-5 补入，见 A3 |
| `revoked` | key_id 自 `not_after` 起**整体不被承认**（泄露） | **仍 `signature_ok`（历史保住）** | **`signature_after_revocation`（可断言）★核心产出** |

**关键**：运维上吊销**不再通过删除公钥表达**，而是通过写 `revoked` 事件表达。公钥应**永久保留**在信任集中（它回答 who），时间边界由账本回答（when）。§2.2 的悖论由此消解。

### A3 — 授权区间检查与新 verdict（只增不减）

验签顺序（在 P37 **既有判定全部保持不变之后**追加）：

```
P37 verdict == signature_ok
  ∧ 该 key_id 在账本中存在授权区间
  ∧ 该 key_id 的全部生命周期事件均在窗口内（§6-4）
  ∧ signed_at ∉ [not_before, not_after)
        ⇒ 降级为 signature_before_activation
                | signature_after_rotation
                | signature_after_revocation
```

**三个新 verdict（R41-1 / R41-5 裁决：独立命名 + 响亮档）**

| verdict | 触发条件 | 命名的审计语义 |
|---|---|---|
| `signature_before_activation` | `signed_at < not_before` | 授权期**开始前** |
| `signature_after_rotation` | `signed_at ≥ not_after`，终态事件为 `rotated_out` | 授权期**已轮换结束** |
| `signature_after_revocation` | `signed_at ≥ not_after`，终态事件含 `revoked` | 授权期**已吊销终止** |

- **响度放在状态上限**：三者经 `applySignatureVerdict` 一律降级为 **`mismatch`**（与 `signature_invalid` 同档）。理由采纳 judge R41-1：可判定的问题判 `unknown` 是谎报无知，且会把 R40-2 刚消灭的「模糊桶」重新挖开。
- **类别放在 verdict 名**：三者**绝不并入** P37 的 `signature_invalid` 或 `key_unknown`——审计者看到的是「授权期外」，不是「字节被改」，也不是「无法判定」。`unknown` 只留给**不可判定**态（`key_unknown` / `malformed` / `absent`）。
- `signature_after_revocation` 是**本 Phase 的核心产出**（P35~P40 给不出）；`signature_after_rotation` 是 R41-5 的悬空引用补齐（否则 rotated_out 的字面定义「不再用于新签名」完全不被执行）。

**既有 P37 五态取值、语义与降级表零改动**（`invalid ⇒ mismatch`，`key_unknown/malformed ⇒ 至多 unknown`，签名永不升级状态）。新 verdict **只增不减**：今天会输出 `signature_ok` 的输入，在**未启用账本**时仍输出 `signature_ok`（A6）。

### A4 — 账本自身的可验证性（哈希链 + 授权链）

- **哈希链**：`event_digest = sha256(canonicalEventPayload)`，`prev_event_digest` 串联 ⇒ 事件顺序不可重排、删除留痕（同构 P38 逐跳双承诺的思路，但对象不同）。
- **授权链**：**全部**事件（含 genesis）一律由 **KAK（key-authority key）** 签发 —— 一把独立于 `--export-sign-key`、可离线保存的 Ed25519 密钥，**只签生命周期事件，不签 manifest**。**无自签特例**（R41-2 裁决，见 §8-2）。
  - ⇒ 攻击者仅持有泄露的 K1 **无法伪造、也无法无痕删除**一条由 KAK 签发的 `revoked` 事件（P40 启用时该事件还会被域外锚定，A5）。
  - ⇒ 「bootstrap 信任来自配置」的立场不变，但它落在 **KAK 公钥本身属于启用时配置**（`--export-key-authority-trust`，与 `--export-trust-keys` 同一个信任根），而不是落在一条自签特例上。验证路径保持单一规则，无特例。
  - **KAK 与签名密钥必须不同**：`authority_key_id == signature.key_id` ⇒ 构造失败（fail-closed），防止自证。
- **不可复活铁律**：`revoked` / `rotated_out` 之后，同 key_id 再出现 `activated` ⇒ **fail-closed 拒绝追加 + 不动文件 + 暴露 error**（复活 = 洗证据）。
- 同一 key_id 的 `activated` 至多一次；`not_after ≥ not_before`；`event_seq` 严格单调。
- 账本不可解析 / 哈希链断裂 / 事件签名不可验 ⇒ 暴露 `key_lifecycle_error`，并**退化为「无账本」语义（区间全开）**，**绝不静默**（同构 P39 `ledger_error` 纪律）。该降级的**攻击面含义**见 §6-6（R41-4）。
- **事件是事实不是状态机**（与 ADR-053 的 anchor 条目**刻意不同**）：生命周期事件一旦落盘即不可变，**同一 `event_seq` 出现第二行（无论 payload 是否相同）⇒ conflict ⇒ fail-closed**。不存在「状态推进」这一合法重写语义。

### A5 — 与 P40 的关系（正交，不互推）

- 生命周期事件**在 anchor 启用时同样进入 P40 的 dispatch 通道**（复用其 payload 规范与 ack 语义），使「K1 于 T 吊销」这一事实本身可被域外见证。
- **禁止**用 anchor 结果反推密钥有效性（P40 只见证存在性，不承载时间语义）；**禁止**用生命周期账本填补 anchor 空洞（两者是不同性质的证据）。
- anchor 的 `key_id` / `stream_id` 派生规则**零改动**。
- **账本粒度 = 与 export dir 同域**（R41-3 采纳，不扇出；理由与强制声明见 A7-11 与 §6-7）。每条生命周期事件携带 P40 派生的 `stream_id` ⇒ 域外见证端可以区分事件属于哪个目录，使「目录 A 已吊销、目录 B 未吊销」**在域外可见**（这是把 §6-7 的静默 fail-open 变为**可观测**的唯一缓解）。

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
11. **不做跨目录扇出，也不做跨目录对账**（R41-3 二选一裁决，见 §8-3）：账本粒度 = **一个导出目录一个账本**（与 export dir 同域，与 anchor/ledger 一致）。同钥多目录时生命周期事件**明确不扇出**，「吊销须逐目录执行」定为**运维程序**并显式声明其静默 fail-open 代价（§6-7）。不引入目录清单：目录清单是**配置事实不是证据**，把它塞进证据面等于重造 P37「信任锚永不从被验对象派生」要防的同构风险。

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

6. **腐账 = 撤销洗白攻击面（R41-4 追加必修，四要素齐备）**

   - **①攻击路径（corrupt-to-unrevoke）**：A4 的「账本坏 ⇒ 退化为区间全开」是诚实的本地降级，但它同时是一条**攻击路径**——持 K1 的攻击者（K1 泄露即意味着他可能已在主机上）破坏 `signing-key-log.jsonl`，即可使该 key 的授权区间**回到全开**，`revoked` 的效果**归零**，且系统看到的是「无账本」而非「账本被毁」。本 Phase **不声称能防止此攻击**。
   - **②为何降级仍是本地正确语义（诚实）**：账本损坏后，系统**没有任何可验证的依据**去断言某个 `not_after`。此时若继续输出 `signature_after_revocation`，就等于用一个**不可验证的来源**下断言——那才是谎报。退化为「无从断言」是唯一诚实的本地结论。
   - **③P41 自动路径之外的缓解**（本 Phase 只提供这些，不提供自动修复）：
     - **P40 锚定**：生命周期事件在 anchor 启用时被推送到域外 ⇒ 「K1 于 T 吊销」这一事实**在域外仍有副本**，本地账本被毁不等于该事实消失，运营者可带外取回；
     - **KAK 离线保存**：攻击者持 K1 但**不持 KAK** ⇒ 无法伪造一条「从未吊销」的替代账本（伪造品无 KAK 签名 ⇒ 事件签名不可验 ⇒ 暴露 `key_lifecycle_error`，**不会**静默变成全开）；
     - **自动重建被追加纪律禁止**：账本不可解析 ⇒ fail-closed 拒绝追加且**绝不**重写出一个「干净」账本（I1）。**销毁账本得不到一个看起来正常的账本，只得到一个响亮的 error。**
   - **④`key_lifecycle_error` 必须响亮**：T161 已覆盖（哈希链断裂 ⇒ 暴露 error + 退化为全开，绝不静默、绝不谎报吊销）。R41-4 后该用例同时是**腐账攻击面的红例**：删除/损坏账本 ⇒ `key_lifecycle_error` 出现 ⇒ 运营者可见。T177 进一步钉住「删账本 ⇒ T157 的 `signature_after_revocation` 退回 `signature_ok`」这一**真实的残留弱点**，不做任何美化。

7. **同钥多目录 ⇒ 吊销不跨域（R41-3 强制声明，不可省略）**

   账本与 export dir 同域且**明确不扇出**（A7-11）⇒ **同一把 key 服务多个导出目录时，各目录的账本相互独立**：目录 A 的 `revoked` **不约束**目录 B，且**目录 B 的遗漏在本地不可见**（B 侧验签全绿、`signature_ok`、无任何 error）——这是**静默 fail-open**，不是实现缺陷，是粒度选择的直接代价。
   - **裁决（二选一，已选）**：**明确不扇出** + 把「吊销须**逐目录**执行」定为**运维程序**。
   - **为何不扇出**：写入侧只有一个目录的配置，**没有、也不应有**「全部目录清单」这一概念；扇出需要一个全局注册表，而注册表本身是**声明而非证据**，且扇出的**部分失败**会引入一个新的静默 fail-open（正是本条要声明的东西）。
   - **可观测性缓解（唯一）**：生命周期事件携带 P40 派生的 `stream_id`（A5）⇒ 域外见证端可区分目录，使「A 已吊销、B 未吊销」**在域外可见**；anchor 未启用时此缓解**不存在**，遗漏回到完全静默。T176 钉住该行为（B 侧仍 `signature_ok`，不谎报、不推断）。

---

## 7. 测试契约（T155~T172）

| 编号 | 用例 | 覆盖 |
|---|---|---|
| T155 | 未启用 ⇒ verify/coverage/anchor 输出逐字节不变 | I6 / A6 |
| T156 | `rotated_out(K1)` 且 K1 仍在信任集 ⇒ 其历史 `signature_ok` | A2（悖论消解 · 保史） |
| T157 | `revoked(K1,T)` ⇒ `signed_at ≥ T` ⇒ `signature_after_revocation` | A3（核心产出） |
| T158 | `revoked(K1,T)` ⇒ `signed_at < T` ⇒ 仍 `signature_ok` | I5 |
| T159 | **判别面**：`before_activation` / `after_rotation` / `after_revocation` 三者之间、以及与 `key_unknown` / `signature_invalid` 均可判别（verdict 名与 detail 均不同） | A3 |
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
| T173 | **genesis 必须 KAK 签**：自签 `activated` ⇒ fail-closed 拒绝追加 + 文件字节不动；KAK 签 ⇒ 通过；`authority_key_id == signature.key_id` ⇒ 构造失败 | A4 / R41-2 |
| T174 | `rotated_out(K1,T)` ⇒ `signed_at ≥ T` ⇒ `signature_after_rotation`（且 `signed_at < T` 仍 `signature_ok`）—— **旋转腿的独立红例**，确保 rotated_out 不是咨询性的 | A2 / R41-5 |
| T175 | 状态档位：三个新 verdict 一律把 P35 status 降为 `mismatch`（与 `signature_invalid` 同档），且**不**与 `invalid` 同名 | A3 / R41-1 |
| T176 | **同钥多目录**：目录 A `revoked`、目录 B 无事件 ⇒ B 侧仍 `signature_ok`、无 error（静默 fail-open 如实呈现，不谎报、不推断） | §6-7 / R41-3 |
| T177 | **腐账红例（残留弱点如实钉住）**：删除/损坏账本 ⇒ `key_lifecycle_error` 响亮 + 区间全开 ⇒ T157 的 `signature_after_revocation` 退回 `signature_ok` | §6-6 / R41-4 |

> T170 的 non-vacuousness 红例按 judge 要求**扩展覆盖旋转腿**：K1 被 `rotated_out` 后仍用它签 manifest ⇒ P35~P40 全绿 ⇒ **仅 P41 观测 `signature_after_rotation`**。

---

## 8. 三处取舍（R210 已裁决 — 本节为最终口径，实现期不可变更）

> 原 §8 是「提请你明示的三处取舍」。judge R210 裁决为 **B — Accept with modifications**，三处全部裁定。以下为**采纳后的最终口径**，原提案中被否决的部分一并记录，以便复核。

### 8-1（原 Q1 / R41-1）：三个新 verdict —— **响亮档，独立命名**

- **裁定**：状态降级上限 = **`mismatch` 档**（与 `signature_invalid` 同档）；但**绝不**并入 P37 的 `signature_invalid` 或 `key_unknown`——`signature_before_activation` / `signature_after_rotation` / `signature_after_revocation` 是**独立命名**的新 verdict。
- **采纳理由（judge）**：①该发现**可断言**——两个都被认证的工件（K1 真实验签的 manifest + KAK 签发的生命周期事件）之间存在确定性不一致，与 P40 `digest_mismatch` 同构：不知道谁在撒谎，但矛盾是真的；`unknown` 只留给**不可判定**态，可判定的问题判 `unknown` 是谎报无知。②保守档会重造 R40-2 刚消灭的洞：持钥伪造品若未回填时间，将藏进「unknown」的模糊桶里与 `malformed` 无从区分——P41 的全部意义是撤销伪造能力，其唯一产出 verdict 却与「解析失败」同权重，等于白做。③我方原论据「签名是真的、不该说成篡改」**混淆了响度与类别**——独立命名已保全该关切。④时钟偏斜导致真实记录被响亮标记是**正确**行为：自证时间就是系统唯一的时间契约，偏斜是应当被暴露的运维缺陷。
- **我方原提案（保守 unknown 档）已被否决**，不再保留。落点：A3 表格 + `applySignatureVerdict` + T175。

### 8-2（原 Q2 / R41-2）：genesis **必须 KAK 签发，不接受自签**

- **裁定**：我的替代方案（要求 KAK 签 genesis）被采纳，且我提出的「代价」论证**不成立**——「首次启用必须先有 KAK」不是代价：A4 规定除 genesis 外**所有**事件都要 KAK 签，启用时刻运营者必然持有 KAK，genesis 用 KAK 签**零额外成本**。
- **决定性理由（judge）**：自签 genesis 使**重基线攻击（re-baseline）**不可检测——攻击者（持 K1、在主机上）删除账本后自写一份 `genesis(K1, 自签)`，系统看到的是一份**干净的**账本、K1 全开区间，洗掉吊销且**不留任何异常**。KAK 签发下：重写/新建账本无法产出任何有效事件（无 KAK），删除账本则 `key_lifecycle_error` 响亮可见（T161）。
- **bootstrap 信任来源的重新定位**（我方原立场保留但移位）：信任来自配置这一点不变，但它落在 **KAK 公钥本身属于启用时配置**（`--export-key-authority-trust`，与 `--export-trust-keys` 同一个信任根），**而不是**落在一条自签特例上。验证路径保持单一规则，无特例。落点：A4 + T173。

### 8-3（原 Q3 / R41-3）：**与 export dir 同域采纳**，附强制声明（不扇出）

- **裁定**：与 anchor/ledger/`stream_id` 家族一致，**采纳**「一个导出目录一个账本」。但必须声明同构于 R40-5 的代价：同一把 key 服务多个导出目录时，各目录账本独立，**目录 A 的 `revoked` 不约束目录 B，且 B 的遗漏不可见（静默 fail-open）**。
- **Architecture 层二选一裁决（不得悬置）**：**明确不扇出** + 把「吊销须逐目录执行」定为**运维程序**。理由：写入侧只有一个目录的配置，扇出需要一个全局目录清单，而清单是**声明不是证据**；且扇出的部分失败会引入一个新的静默 fail-open。
- **可观测性缓解（唯一）**：事件携带 P40 派生 `stream_id` ⇒ 域外见证端可区分目录，使遗漏**在域外可见**（anchor 未启用时此缓解不存在）。落点：A5 / A7-11 / §6-7 / T176。

---

## 9. R210 必修闭合表

| 编号 | 条款 | 闭合位置 | 状态 |
|---|---|---|---|
| R41-1 | 两 verdict 响亮档（mismatch 上限）+ 独立命名 | §8-1 / A3 / T159 / T175 | ✅ |
| R41-2 | genesis 必须 KAK 签发，禁自签 | §8-2 / A4 / T173 | ✅ |
| R41-3 | 与 export dir 同域 + 强制声明（不扇出，运维程序） | §8-3 / A5 / A7-11 / §6-7 / T176 | ✅ |
| R41-4 | 腐账 = 撤销洗白攻击面，四要素已知代价 | §6-6 / T161 / T177 | ✅ |
| R41-5 | 悬空引用：补 `signature_after_rotation` 第三 verdict | A2 / A3 / T174 / T170 旋转腿 | ✅ |

**架构 ADR**：`055-phase41-architecture.md`（R211 同轮交付），含定稿字节、事件状态机、区间解析、验签接线、窗口规则、KAK flag、写入面与实现清单。
