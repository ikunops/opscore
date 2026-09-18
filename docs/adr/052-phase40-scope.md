# ADR-052 — Phase 40: Publication Anchoring（发布锚定 / 域外见证）

- **Status**: Proposed (Round 207, Scope stage)
- **Phase**: 40（Publication Anchoring）
- **Base**: Phase 39 CLOSED（R206=A，origin/main = `826d0de1`）
- **Author**: executor（WorkBuddy），方向自拍板授权（用户 2026-08-27）
- **Supersedes**: 无。本 ADR **不修改** P35/P36/P37/P38/P39 的任何冻结判据。

---

## 1. 方向选择（三选一，附淘汰理由）

R207 解冻的三个候选：**SIEM push / HA 多副本 / full-forensic retention**。

| 候选 | 判定 | 理由 |
|---|---|---|
| **full-forensic retention** | ❌ 淘汰 | 它试图解决的冲突（「有限保留」vs「持续可证明」）**已被 P39 用 ledger 解决**：P39 明确论证了「不必全量留存也能持续可证明」。再提全量留存只是把运维成本重新加回来，不是能力缺口，属于**已被前序 Phase 消灭的问题**。 |
| **HA 多副本** | ❌ 淘汰 | 缺口性质不同：它是**部署拓扑/可用性**问题（多副本下 scheduler 互斥、状态一致性），不是证据体系的缺口。且它与现有单机 scheduler 假设（`publication-seq.state`、atomic `os.Link`、Tick 非重入）冲突面极大，工程量集中在**共识**而非**证据**。作为独立 Phase 会与 P30~P39 主线脱节。 |
| **SIEM push** | ✅ **选定** | 但**重新定义为更精确的能力**：不是泛泛的「把日志推给 SIEM」，而是 **Publication Anchoring —— 把「发布曾经发生」这一存在性证据投放到本地信任域之外**。 |

**选定方向**：**Phase 40 = Publication Anchoring（发布锚定）**。

---

## 2. 新问题（Problem Statement）

### 2.1 现有体系能证明什么

P30~P39 建立了五层证据：

```
P35 单快照内部一致（hash/bytes/records）
P36 跨快照 seq 覆盖（gaps / completeness）
P37 单快照来源与防篡改（Ed25519 签名 + 独立 trust anchor）
P38 retained 范围内逐跳发布链（prev_id + prev_digest 双承诺）
P39 retention-independent 的紧凑 predecessor commitment（chain ledger）
```

结论是：**保留下来的历史是自洽的、可验证的、未被改动的**。

### 2.2 现有体系不能证明什么（这就是新问题）

**P35~P39 的全部信任根都位于同一台主机、同一把私钥之下。**

具体地说：`--export-sign-key`（Ed25519 私钥）同时签发 manifest（P37）、链承诺（P38）、ledger 条目（P39）。因此一个取得了该私钥（或能同时重签并重写本地全部文件）的对手可以：

1. 删除 publication #100（含不利记录）及其 manifest/artifact/ledger 条目；
2. 重签 #101，令其 `chain.prev_publication_id = 99`、`prev_manifest_digest = digest(#99)`；
3. 重写 ledger，令 #101 成为 #99 的合法后继；
4. 视需要继续向上/向下修补 #99 之后的所有条目。

修补完成后，本地全部验证**全部通过**：

- `signature = signature_ok`（用真私钥签的）
- `chain = chain_ok`（双承诺自洽）
- `verifiable_from = 99`，`ledger_entries_used > 0`
- P35 `status = ok`、P36 coverage 无 gap（retention 范围内的空洞恒 `indeterminate`，按 P36 冻结条款**不解释为删除**）

**没有任何一个既有维度能观测到 #100 曾经存在。** 更一般地：

> **P35~P39 无法区分「这份链就是唯一被发布过的链」与「这份链是被删改后重签出来的、自洽的链」。**

这不是 P38 的已知代价（P38 的代价是「删除会留下痕迹 ⇒ 报 broken」，前提是**攻击者无法重签**）。本问题的前提是**攻击者握有签名权**，P38/P39 的重签名保护在此前提下一律失效。

### 2.3 问题的精确表述

> **缺少域外见证（external witness）**：发布事件的存在性证据全部由被保护对象自己持有和签发，
> 因此「本地从未出现的 publication」与「本地曾经出现但被彻底抹除的 publication」在本地视图内**同形**，
> 且这一同形性**无法**由任何本地密码学手段消除（密钥同域）。

### 2.4 为什么必须由 P40 解决、而不能并入 P39

P39 的 ledger 只是把承诺**压缩**并**解耦 retention**，它仍然与 manifest 由同一把密钥签发、存放在同一目录下。把「外部推送」塞进 P39 会同时违反 P39 的两条冻结：

- 冻结「提交顺序：manifest → 签名 → atomic publish → ledger append」——外部推送是第 5 步，不能挤进 ledger append；
- 冻结「ledger 不提供自身历史完整性证明」——外部见证提供的是**不同性质**的证据（存在性 vs 完整性），混在同一文件里会让两者的语义边界消失。

因此独立成 Phase。

---

## 3. 决策（Scope）

引入 **Publication Anchoring**：每次成功发布后，计算一个**锚定摘要（anchor digest）**并将其**推送**到本地信任域之外的见证端（witness endpoint）。验证时以**调用方提供的见证序列**与本地锚定记录**对账（reconcile）**，从而把「本地缺失」从不可判定升级为**可被外部独立观测**。

**一句话价值**：P38 让删除**留痕**；P40 让删除**可被域外独立证明**——前提是删除发生时该功能已启用且推送已确认。

---

## 4. 能力定义（A1~A9）

### A1 — 锚定摘要（anchor digest）

```
canonicalAnchorPayload(a) = canonicalJSON({
  "publication_id":      int64,
  "anchor_seq":          int64,   // 本地单调递增的锚定序号
  "manifest_digest":     string,
  "prev_publication_id": int64,
  "prev_manifest_digest": string,
  "recorded_at":         string,  // RFC3339Nano
})
anchorDigest = sha256(canonicalAnchorPayload(a))
```

- `canonicalJSON` **完全复用 P37 的规范序列化器**（语义层：键序/空白无关，改值/类型/字段存在性即失败）。不引入第二套 canonical。
- `anchor_seq` 是本地单调递增序号，**独立于 publication_id**。它的唯一作用是在见证侧暴露**推送空洞**（seq 不连续 ⇒ 见证序列不完整），它是**传输完整性**字段，不是发布关系字段，**永不参与 predecessor 裁决**（沿用 P38/R201 的两轴分离铁律）。
- 锚定 payload 由 P37 的签名器签名，使见证端可独立验证来源。

### A2 — 发布顺序（在 P39 冻结之后追加第 5 步）

```
manifest 定稿 → 签名 → atomic publish（唯一成功点）→ ledger append → anchor dispatch
```

- **禁止 anchor-first / anchor-before-publish**：在发布成功之前产生锚定记录会制造 phantom publication evidence（与 P39「禁止 ledger-first」同构）。
- **anchor dispatch 失败不回滚已发布的事实**。atomic publish 是唯一成功点；锚定是发布**之后**的投递行为，其失败只影响「该发布是否被域外见证」，不改变「该发布是否发生」。
- 因此 anchor 面**永远不阻塞**发布路径。

### A3 — 本地锚定日志（`chain-anchor.jsonl`）

因为推送可能失败，必须有一个本地、有界的交付状态载体：

```
{"publication_id":..,"anchor_seq":..,"anchor_digest":..,"state":"anchored|pending|unanchored",
 "attempts":..,"last_error":"..","anchored_at":"..","signature":{...}}
```

- **bounded**：`--export-anchor-capacity`（默认 4096，0 = 全留）。
- **复用 P39/R205 的持久化纪律（强制）**：
  - 正常 append = **真 `O_APPEND` 追加**，既有字节永不重写；
  - **存在不可解析行 ⇒ fail-closed**：拒绝追加、不动文件、暴露 `anchor_error`，**绝不**借 append 重建「干净」文件把坏证据洗掉；
  - 唯一允许的重写 = **显式 prefix compaction**，只裁最旧整行，幸存行**逐字复制**。
- 该文件的语义边界（必须写进代码注释）：**它是投递状态账本，不是证据**。它不能证明任何事情——它只记录「我们声称推送过什么、对方确认了什么」。

### A4 — 确认语义（关键，防止把「发出去」当成「已锚定」）

| 见证端响应 | 判定 | 理由 |
|---|---|---|
| 2xx 且含明确的持久化确认（`ack_id` 非空） | `anchored` | 唯一可确认为「已锚定」的情形 |
| 2xx 但无 `ack_id` / 响应体不可解析 | `pending` | **响应码不等于持久化**。200 OK 但没落盘是完全可能的 |
| 4xx（除 409 冲突） | `pending`（永久失败转 `unanchored`） | 配置/鉴权错误需人工介入 |
| 5xx / 超时 / 网络错误 | `pending`（重试队列） | 瞬时故障 |
| 409（同 id 同摘要已存在） | `anchored`（幂等，沿用现有 ack） | 重推幂等 |
| 409（同 id **不同**摘要） | **conflict** ⇒ `anchor_error`，fail-closed | 与 P39「publication_id 逻辑唯一」同构，不 last-write-wins |

**绝不**把「发送时无异常」当作「已锚定」——这是本 ADR 最容易被实现偷懒击穿的一条。

### A5 — 有界重试与「永久未锚定」

- 重试：指数退避，上限 `--export-anchor-max-attempts`（默认 8）。
- 超过上限 ⇒ 状态由 `pending` 转为 `unanchored`，**保留记录与 `attempts`/`last_error` 永久暴露**。
- **fail-closed 且不洗证据**：anchor 记录文件满时，**拒绝新条目入队**（新发布 ⇒ `unanchored` + `anchor_error`），**绝不**丢弃最旧条目来腾空间——丢最旧 = 洗掉「我们曾经未能锚定」的证据。
- `unanchored` 是**一等状态**，必须在状态面与验证面原样暴露，不得因「后来网络恢复」而被追溯抹平。

### A6 — 对账面（reconcile）：严格只读，且**不发网络请求**

新增只读端点接收**调用方提供的见证序列**（文本/JSON），本地做对账。

- **系统本身不在验证路径上发起任何网络调用**。理由：
  1. 保持 P35 以来「verify 严格只读」的纪律；
  2. 避免把外部系统变成**新的信任源**——如果 verify 自己去拉，见证端的可用性/可信性就被隐式抬升为验证前提；
  3. 调用方提供序列 ⇒ 该序列的**完整性与来源可靠性由调用方负责**，ADR 明确声明这一点（见 §7.3）。
- 序列格式：`[{publication_id, anchor_seq, anchor_digest, ack_id?}, ...]`。

### A7 — 对齐下界（witness_trust）：不可对齐 ⇒ 不做任何断言

对账前必须先建立「这份序列确实来自我们的同一个发布流」：

| witness_trust | 条件 | 后果 |
|---|---|---|
| `not_provided` | 未提供序列 | 只输出本地 anchor 状态，**不产出**任何对账结论 |
| `untrusted` | 序列中**没有任何**条目的 `(publication_id, anchor_seq, anchor_digest)` 与本地 `anchored` 记录完全一致 | **不做任何断言**。verdict = `anchor_unverifiable`，`orphan`/`missing` 列表**必须为空** |
| `aligned` | 至少存在 1 个上述完全一致条目 | 才允许产出对账结论 |

> 这是**对齐的密码学下界，不是完整性证明**。攻击者若能伪造整份序列，他也能伪造对齐——P40 不解决「见证端本身被攻陷」，见 §8 已知代价。
> 但**没有**这条下界会更糟：任意无关系统的日志都会被当成我们的见证，从而把「外部有、本地没有」批量误报成删除。

### A8 — 对账判据（仅在 `aligned` 下计算）

遍历**并集**（本地 anchored ∪ 见证序列）：

| 情形 | 结论 | 语义 |
|---|---|---|
| 本地 `anchored` ∧ 见证存在 ∧ digest 相等 | `witnessed` | 最好的情形 |
| 本地 `anchored` ∧ 见证存在 ∧ **digest 不等** | `anchor_broken` | 内容被替换（本地或见证侧之一被改），**确凿的不一致** |
| 本地 `anchored` ∧ 见证**缺失** | `missing_witness` ⇒ verdict 至多 `anchor_incomplete` | **见证端裁剪与见证端丢失不可区分**（与 P39「eviction 与 prefix deletion 不可区分」同构）。**绝不**断言为篡改 |
| 本地**无**该 publication_id ∧ 见证存在 | **`orphan_witness` ⇒ `anchor_broken`** | **本 Phase 的核心产出**：外部见证了一个本地根本不存在的发布。这是 P35~P39 给不出的结论 |
| 本地 `pending` / `unanchored` ∧ 见证存在 | `witnessed_unconfirmed` | 见下 |

**`pending`/`unanchored` 条目不参与「已确认」类断言**（沿用 A4）：它们尚未被确认送达，既不能当 `witnessed`，其「见证缺失」也不能当 `missing_witness`（否则「还没推送成功」会被读成「见证端把它丢了」）。

### A9 — 第四维正交

anchor 是**新增的第四个正交维度**：

```
P35 status  ×  P37 signature  ×  P38 chain  ×  P40 anchor
```

- anchor **不修改** P35 的 `status`、P37 的 `signature.verdict`、P38 的 `chain.verdict`、P39 的 `verifiable_from` / `ledger_entries_used` 的任何取值逻辑。
- `history_export_coverage.go`（P36）**零 diff**（沿用 P37/P38/P39 的既有承诺）。
- 冻结包零 diff；`go.mod` / `go.sum` 零改动。

---

## 5. 结果模型

per-publication：

```json
{"publication_id": 100,
 "anchor_state": "anchored|pending|unanchored|unknown",
 "anchor_seq": 12,
 "anchor_digest": "…",
 "anchored_at": "…",
 "attempts": 1,
 "witness": "witnessed|digest_mismatch|missing_witness|orphan_witness|witnessed_unconfirmed|not_evaluated"}
```

aggregate：

```json
{"enabled": true,
 "witness_trust": "not_provided|untrusted|aligned",
 "verdict": "anchor_absent|anchor_ok|anchor_incomplete|anchor_broken|anchor_unverifiable",
 "orphan_witness_ids": [100],
 "missing_witness_ids": [98],
 "digest_mismatch_ids": [],
 "pending_ids": [101],
 "unanchored_ids": [102],
 "local_anchor_entries": 12,
 "anchor_error": "…"}
```

verdict 语义（**严格区分「不可证明」与「检测到问题」**）：

| verdict | 含义 |
|---|---|
| `anchor_absent` | 功能未启用，无任何本地 anchor 记录（与 P38 `chain_absent` 同构：允许「没有」这一态） |
| `anchor_ok` | aligned 且并集内无 mismatch / 无 orphan / 无 missing |
| `anchor_incomplete` | 存在 `pending` / `unanchored` / `missing_witness` / 未对齐所需的样本 ⇒ **不可完整证明**，但**未检测到不一致** |
| `anchor_broken` | 出现 `orphan_witness` 或 `digest_mismatch` |
| `anchor_unverifiable` | `witness_trust = untrusted` ⇒ 不做断言 |

---

## 6. 边界与非目标（重新冻结清单）

1. **不实现任何厂商 SIEM 协议**。只定义一个最小的锚定提交契约（HTTPS POST + JSON + Ed25519 签名 + `ack_id` 语义），并提供一个 `file://` / 内置 dev witness 后端用于测试与自托管。**适配 Splunk/QRadar/Elastic 等属于后续工作，本 Phase 明确排除。**
2. **不做双向同步**：绝不从见证端拉回数据用于重建、恢复或补写本地任何文件（否则见证端成为新的信任源，且违反 P39「recovery 只能从 `signature_ok` 的 manifest 生成」）。
3. **不做 HA / 多副本 / 共识**（本轮淘汰方向的残余，明确冻结）。
4. **不改 retention**：artifact / manifest / ledger 的保留策略与 prune 逻辑不变；anchor 不参与任何 retention 决策，也不触发删除。
5. **不修改 P35/P36/P37/P38/P39 的任何既有判据、状态取值与输出字段**；`history_export_coverage.go` 零 diff。
6. **不把 anchor 提升为完整性证明**。它提供的是**存在性证据**与**本地缺失的可观测性**，不是「历史完整无缺」的证明。`anchor_ok` **不等于**「没有发布被删除」——它只等于「并集内未发现不一致」（与 P39「`chain_ok` ≠ 全局无删除」同构）。
7. **不在验证路径发起网络调用**（A6）。
8. **默认零回归**：未配置 `--export-anchor-endpoint` ⇒ 不创建 `chain-anchor.jsonl`、不产生任何推送、verify 与 coverage 输出**逐字节不变**、状态面不出现任何新字段（空值字段一律 omitempty）。
9. **不解释见证端的裁剪/保留策略**：`missing_witness` 与见证端裁剪不可区分，二者都**不得**报为篡改（A8）。
10. **不推断「从未推送」与「推送后被见证端丢弃」的区别**——二者在本地视图同形，输出 `missing_witness`，不细分。
11. `go.mod` / `go.sum` 零改动；冻结包零 diff；不新增依赖。

---

## 7. 证据诚实条款（沿用并强化历届纪律）

1. **「发成功」≠「已锚定」**（A4）。
2. **「不可对齐」≠「有删除」**（A7）——`untrusted` 时 orphan/missing 列表必须为空。
3. **「见证缺失」≠「篡改」**（A8）——与 P39 eviction/deletion 不可区分同构。
4. **「不可完整证明」≠「检测到不一致」**——`anchor_incomplete` 与 `anchor_broken` 必须分离，不得合并成单一「有问题」。
5. **「没有该功能」≠「该功能失败」**——`anchor_absent`（未启用）与 `unanchored`（启用但未送达）不得混淆。
6. **不可解析的本地 anchor 行 ⇒ fail-closed + 暴露 `anchor_error`**，绝不静默跳过或重建清洗（A3，直接沿用 P39/R205 教训）。
7. **两轴分离**：`anchor_seq` 只用于传输空洞检测，**永不参与** predecessor / 发布关系裁决（沿用 P38/R200/R201）。

---

## 8. 已知代价与未解决问题（诚实声明）

1. **安全性上界 = 见证端的独立性**。若见证端与本地处于同一信任域/同一攻击者可控范围，P40 退化为「自证」，本 Phase **不检测也不解决**这种情况。ADR 明确声明：anchor 的价值由部署拓扑决定，不由本实现决定。
2. **时间窗**：推送有延迟。刚发布、尚未确认的条目为 `pending`，这是**正常态**而非故障；验证者不得以「当前 pending」推断异常。
3. **覆盖起点**：P40 只能见证**启用之后**的发布。启用之前的历史仍然只有 P35~P39 的保护——`verifiable_from` 与 anchor 的起点是**两个不同的边界**，不得互相推导。
4. **见证端自身的可信性、可用性与保留策略**全部不在本 Phase 范围内（§6.1、§6.9）。
5. **新增持久化文件仅 1 个**（`chain-anchor.jsonl`，有界）；新增 CLI flag 若干；**不新增**任何后台常驻组件（推送复用 scheduler tick，Tick 非重入纪律不变）。

---

## 9. 只读面与配置

| 项 | 内容 |
|---|---|
| 状态 | `GET .../alerts/history/export/scheduler` 增加 `anchor_enabled` / `anchored_count` / `pending_count` / `unanchored_count` / `anchor_error` |
| 对账 | `GET .../alerts/history/export/anchor`（本地状态，无序列 ⇒ `not_provided`）；`POST .../alerts/history/export/anchor/reconcile`（body = 见证序列，admin-only + CSRF fail-closed） |
| 配置 | `--export-anchor-endpoint`（空 = 关闭）、`--export-anchor-timeout`、`--export-anchor-max-attempts`（默认 8）、`--export-anchor-capacity`（默认 4096） |
| 位置 | 全部位于 `:8082` admin-only 面，`:8080` 不新增任何路由 |

---

## 10. 测试契约（T125~T142，暂定）

| # | 断言 |
|---|---|
| T125 | 默认关闭 ⇒ 无 `chain-anchor.jsonl`、verify/coverage 输出逐字节不变（零回归） |
| T126 | 发布成功且推送确认 ⇒ `anchored`，`anchor_seq` 单调 |
| T127 | 推送 5xx ⇒ `pending`，发布已完成且**不回滚**，队列保留 |
| T128 | 队列满 ⇒ 新条目 `unanchored` + `anchor_error`，**最旧条目不被丢弃** |
| T129 | 重推同 id 同摘要 ⇒ 幂等，单条记录 |
| T130 | 同 id **不同**摘要 ⇒ conflict + `anchor_error`，不 last-write-wins |
| T131 | 本地 anchor 文件含不可解析行 ⇒ 拒绝追加 + `anchor_error`，文件字节不变（P39/R205 纪律） |
| T132 | 顺序：publish 先于 anchor；anchor 失败不影响已发布事实 |
| T133 | reconcile 未提供序列 ⇒ `not_provided`，不产出任何对账结论 |
| T134 | 序列无匹配项 ⇒ `untrusted`，`orphan`/`missing` 列表为空 |
| T135 | aligned 且并集全匹配 ⇒ `anchor_ok` |
| T136 | aligned 且外部有本地不存在的 id ⇒ `orphan_witness` ⇒ `anchor_broken`（核心价值） |
| T137 | aligned 且本地已锚定但外部缺 ⇒ `missing_witness` ⇒ 至多 `anchor_incomplete`，**不**判 broken |
| T138 | digest 不等 ⇒ `anchor_broken` |
| T139 | `pending` 条目不参与 witnessed/missing 断言 |
| T140 | anchor 不改变 P35 status / P37 signature / P38 chain / P39 ledger 任何取值 |
| T141 | `history_export_coverage.go` 零 diff |
| T142 | 冻结包零 diff、`go.mod`/`go.sum` 零改动 |

---

## 11. 与既有 Phase 的关系

```
P35 内部一致 ─┐
P36 序列覆盖 ─┤
P37 来源/防篡改 ─┼─ 全部信任根：同机 + 同私钥  ⇒  P40 缺口
P38 发布链 ────┤
P39 ledger ────┘

P40 锚定：把「发布曾经发生」投放到域外 ⇒ 使「本地缺失」可被独立观测
```

- **不替代** P38/P39：删除留痕（P38）与持续可证明（P39）在**无密钥泄露**前提下依然成立；P40 只在「密钥/主机同域失守」这一更强前提下提供额外一层。
- **不降级** P38/P39 的任何判据：逐跳双承诺、`verifiable_from` 边界、ledger 提交顺序全部原样保留。
- **不触碰** P36：`indeterminate` 空洞在本 Phase 内**不升级**（§6.5）。P36 coverage 输出零变化。若 judge 认为「空洞 + orphan_witness 应当联动升级 P36 的 `indeterminate`」，请在裁决中明示——本 ADR 选择保守，把联动留给后续 Phase。
