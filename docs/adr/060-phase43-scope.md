# ADR-060 · Phase 43 — Evidence Destruction Accountability（销毁留痕）· Scope

- **Status**: PROPOSED（R216 投递，待 judge 裁决）
- **Parent**: ADR-059（P42 Implementation, commit `2cdc2838`）
- **裁决前提**: R215 = A，Phase 42 CLOSED；P30~P42 十三连 CLOSED
- **Author**: executor（方向自拍板，依据 R216 授权）

---

## 1. 分工句

| Phase | 回答的问题 |
|---|---|
| P37 Provenance | **who** — 谁签的 |
| P40 Anchoring | **where** — 域外何处见证 |
| P41 Key Lifecycle | **when** — 时间边界内吗 |
| P42 Attestation | **whether** — 验证发生过吗 |
| **P43 Destruction** | **why absent** — **不在的那段，证据去哪了** |

P36 问「这段区间被覆盖了吗」。**P43 问「没被覆盖的那段，是合法销毁，还是无记录的消失」**——把 P36/P39/P40/P42 四处被显式声明为「不可区分、绝不断言」的地方，变成**可断言**。

---

## 2. 新问题（已核对代码）

### 2.1 五条销毁路径，零记录

| # | 销毁动作 | 位置 | 销毁对象 |
|---|---|---|---|
| 1 | `prune()` | `history_export_scheduler.go:1097`（`os.Remove` at `:1135`；调用点 `:565`、`:599`） | 快照文件 + manifest（retention） |
| 2 | `compactLedgerPrefix` | `snapshot_ledger.go:267` → `appendonly_log.go:123` | 最旧 ledger 组 |
| 3 | `compactAnchorPrefixPath` | `snapshot_anchor.go:565/577` → 同上 | 最旧 anchor 组 |
| 4 | key lifecycle compaction | `snapshot_key_lifecycle.go:803` → 同上 | 最旧生命周期事件组 |
| 5 | `compactVerificationPrefix` | `snapshot_verification.go:978/979` → 同上 | 最旧验证报告组 |

五条路径中**四条汇聚于 `compactLogPrefixGroups`(`appendonly_log.go:123`) 一个原语**，第五条是 `prune`。**没有任何一处写下「销毁了什么」**：全仓 `destruction` 零命中（仅 3 处注释里出现 destroy 一词，均非记录）。

`compactLogPrefixGroups` 的注释甚至把这件事写成了美德：`kept` 之外的组被直接 `rewriteLogLines` 丢弃，幸存行逐字复制——**字节纪律完美，语义信息为零**。

### 2.2 核心悖论（销毁让两种过去字节不可区分）

> **合法销毁** 与 **攻击性删除** 产生**同一个字节状态**：文件不存在。
>
> 「这段证据按策略被销毁了」与「这段证据被握钥者抹掉了」在今日模型中**不可区分**。

这不是假设，是**四个 Phase 各自在文档里承认过**的：

- **P39**（ADR-050 §）：`eviction 与 prefix deletion 不可区分，二者都不得报为篡改`；prefix eviction 只定义 `verifiable_from` 边界，**不得断言过去状态**。
- **P40 R40-1**：合法 prefix compaction 后，orphan 与「合法裁掉」同形 ⇒ 窗口外**不判** broken。
- **P36**：`publication_id` 空洞**恒 `indeterminate`**，**不解释为删除**。
- **P41 T177**：删掉账本 ⇒ `after_revocation` 退回 `signature_ok`，**如实钉住残留弱点，不美化**。
- **P42**：gap 在窗口非 full-history 时 `gap_indeterminate=true`，进 `not_assertable[]`。

**每一处单独看都是正确的保守取舍。合起来看是一个系统性盲区：本系统对「证据消失」这件事，只能说「不知道」。**

### 2.3 攻击复现（P40 主攻击的变体，本地全绿）

持私钥者想抹掉第 k 份 publication：

1. `os.Remove` 掉第 k 份 manifest + artifacts；
2. 顺手 `compact` / 删掉 ledger、anchor 前缀中指向它的条目；
3. 结果：coverage `indeterminate`、chain 因 ledger 缺失走 `verifiable_from` 收缩、anchor 窗口外不判、verification 无报告 ⇒ `unattested`。

**本地全绿。** P40 只能靠域外见证端抓；P42 只能说「没验证过」。**没有任何一个本地判据能说「那份东西曾经在，现在没了，且没人记账」。**

---

## 3. 候选比选（含对 R215 judge 提示的答复）

judge 在 R215 给出「验证者独立性」的初步意见并授权比选。三个候选：

| | 候选 | 新可断言判据 | 判据量 | 判定 |
|---|---|---|---|---|
| **C1** | 验证者独立性（judge 提示） | 「报告由非证据持有方密钥签发、信任锚独立」 | 小 | **不选，并入 backlog** |
| **C2** | 输入面完整性（durable 记录本身防篡改） | 「导出内容与库内记录一致」 | 很大 | **不选，排序在后** |
| **C3** | **销毁留痕** | **「消失是记账的 / 无记录的」** | **中且确定** | **✅ 选为 Phase 43** |

**C1 处置（正面答复 judge）**：judge 的判断我接受并进一步收窄——它的**可断言残差真实存在**（报告验签走独立信任锚、与 manifest 信任锚互斥可达，同 P41 的 KAK≠签名密钥），但：
1. 它**不产生新维度的证据**，只把 P37 的 `who` 延伸到验证面 —— 用 R210 击倒 HA 的同一把尺子量：它是**既有维度的加固**，不是新判据；
2. 「验证者跑在独立主机」是**部署拓扑**，代码断言不了（judge 已自指此点，与 T177 / KAK 同域代价同构）；
3. 工程量确实不足以撑一个完整 Phase。

⇒ **不并入 P43**（避免 Scope 膨胀），**登记为 Phase 44 第一候选**。且如实声明：**P43 的销毁记录同样由同一把签名密钥签发，C1 的残差弱点在 P43 中原样存在**（见 §9 已知代价）。

**C2 处置**：真实且重大（攻击者改 durable 记录后重导出，全链合法）。但它需要「记录级标识 + 摘要」基础设施，且会侵入 storage 平面。**P43 的销毁账本正好提供记录消失的语义层** ⇒ 顺序上先 P43 更优。登记为 Phase 44/45 候选，**本轮非目标**。

---

## 4. 机制：销毁账本 `destruction-log.jsonl`

**强制复用 `appendonly_log.go` 共享原语，零新建纪律**（P40/P41/P42 已证伪「为每个家族新建一套」的做法）。

### 4.1 条目

```
{
  destruction_seq,                    // 单调，从账本自身导出（无水位文件，R41-6 纪律）
  kind,                               // snapshot_retention | ledger_compaction |
                                      // anchor_compaction | key_lifecycle_compaction |
                                      // verification_compaction | self_compaction
  targets: [{ id, digest }] | [{ from_seq, to_seq, prefix_digest }],
  destroyed_count,
  policy,                             // 声明性策略标识（如 "retain=96" / "capacity=4096"）
  recorded_at,
  state,                              // intended | completed | aborted（分组状态推进）
  prev_digest,                        // 哈希链
  signature                           // 复用 P37 signatureBlock，禁第二套序列化器
}
```

- **targets 形态二选一**：publication 面记 `{publication_id, manifest_digest}`（摘要在销毁前**已存在**于 manifest/ledger，无需保留内容）；日志面记 `{from_seq, to_seq, prefix_digest}`（被裁前缀在 compaction 时**正在内存中**，`prefix_digest` 当场可算）。
- **摘要必须在销毁前可得**——这是本机制成立的**物理前提**，也是 §4.3 两阶段顺序的根本理由。

### 4.2 不变量

- **I1 事实性**：销毁记录是**事实不是状态机**。同 `destruction_seq` 出现任何**第二行且 payload 不同** ⇒ `conflict` ⇒ fail-closed（与 ADR-055 §2.3、P42 报告纪律一致）；同 payload 第二行 = 状态推进合法（`intended`→`completed`，ADR-053 分组模型）。
- **I2 拒绝追加**：账本不可验（不可解析行 / 签名不验 / 链断裂 / 异域 stream）⇒ **拒绝追加**（P41 I2 推广）。**绝不借 append 重建干净账本**。
- **I3 首行豁免**：哈希链首行 `prev_digest` 指向已被合法裁掉的条目 ⇒ 不判篡改（P41 I3 同构，否则合法 prefix compaction 自伤）。
- **I4 窗口纪律（继承 R40-1）**：账本有界 ⇒ 结果**强制携带** `destruction_window{min,max,entries,continuous}`；窗口外**绝不**断言 `accounted`（下界丢失与「从未记账」不可区分）；段不连续 ⇒ `destruction_window_discontinuous`。
- **I5 自指闭合**：销毁账本**自身**被 compaction 时，必须先为自己记一条 `self_compaction` 再裁。递归终止于「记录先于销毁」这条顺序铁律。
- **I6 锚定是出口不是输入**：销毁状态**绝不参与**其它任何 verdict 的合流（P42 I7 推广）。锚定失败不回滚、不阻塞。

### 4.3 顺序铁律（两阶段，**意图先行**）

```
1. append(state=intended) + fsync     ← 销毁发生前，摘要此时仍可得
2. 执行销毁（os.Remove / compaction）
3. append(state=completed | aborted)  ← 同 seq 组，last-wins
4. 锚定派发（复用 P40 dispatchAnchorPath，仅当 anchor 启用）
```

**为什么是「意图先行」而不是「事后补记」**（两种方向都 fail-closed，须论证）：

| | 崩溃点在 1↔2 之间 | 崩溃点在 2↔3 之间 |
|---|---|---|
| **意图先行** | 记录在、数据还在 ⇒ `destruction_unconfirmed`（**响**） | 已销毁、有 intended ⇒ `destruction_unconfirmed`（**响**） |
| 事后补记 | 数据还在、无记录 ⇒ 静默（无害） | **已销毁、无记录 ⇒ 伪造出 `accounted` 的反面，且与真实攻击同形** ⇒ 假 `unaccounted_disappearance`（**响但冤**） |

决定性理由不是响度（两者都响），而是：**摘要必须在内容仍存在时捕获**；且**意图先行使记录成为「预承诺」——攻击者无法为「已经不在了的东西」追溯性地补出一条能嵌入既有哈希链的记录**（见 §5 逃逸分析）。

**禁 destroy-then-record**；**禁 record-only（声称销毁但实际未销毁）**。

---

## 5. 核心产出：新可断言判据

| verdict | 含义 | 触发 |
|---|---|---|
| **`unaccounted_disappearance`** | **证据已知曾存在，现已不在，且无任何销毁记录为其记账** | **核心产出** |
| `accounted` | 所有缺失项均有 `completed` 销毁记录覆盖 | 合法 retain/compaction |
| `destruction_unconfirmed` | 有 `intended` 无 `completed` | 崩溃/中断（**响，但不指控**） |
| `destruction_conflict` | 同 seq 第二行 payload 不同 | I1 |
| `destruction_chain_broken` | 链摘要不匹配（非首行） | I2/I3 |
| `destruction_window_discontinuous` | 幸存 seq 段不连续 | I4 |
| `destruction_absent` | 无账本（未启用 / 被整体删除） | 启用前 / 攻击 |

### 「已知曾存在」的证据来源（P43 只消费，不重复实现）

P38 链 `prev_manifest_digest` 引用了一个不存在的前驱 / P39 ledger 中 `publication_id` 的 disk 侧缺失 / P40 已锚定条目 / P42 报告中 `items[].identity`。**P43 站在这些既有产出之上做差集**：

> 曾存在（ledger ∪ chain ∪ anchor ∪ report） − 现存（disk） − 已记账销毁（destruction log） = **无记录的消失**

### 逃逸分析（诚实）

- **逃逸 A：攻击者补写一条伪造销毁记录。** 因 seq 单调且后序条目的 `prev_digest` 依赖前序 ⇒ 必须**插在链中间** ⇒ 重写其后全部条目 ⇒ 若那些条目已锚定 ⇒ **P40 reconcile `divergent`（双信号，T145 式冗余）**。**若未启用锚定 ⇒ 本地不可检测**（同 P40 已知代价，见 §9）。
- **逃逸 B：连销毁账本一起删。** ⇒ `destruction_absent`；与「从未启用」本地不可区分（同 P42 `verification_absent`、P41 T177 的同一族弱点），**域外可检测**（锚定见证端见过它）。

**两种逃逸都落在「本系统自己保存自己的销毁账本」这一前提上**——这正是 P40 用锚定、P41 用 KAK 反复削弱但未消除的同一前提。P43 不解决它，只把它**从四处分散的沉默收敛为一处显式声明**。

---

## 6. 正交性（绝不改写既有判据）

- **第五/六维独立字段** `destruction_status`，**绝不并入** `chain_broken` / `coverage` / `anchor` / `verification_overall`。
- P36 coverage 响应、**P39 ledger、P40 anchor、P42 verification 响应零改动**（既有响应属冻结面）。
- P42 I7 推广为 I6：**销毁状态不参与任何合流**，否则「账本在不在」会变成判据输入（外部/本地存储状态成信任源，违 A7-6）。

---

## 7. 非目标（12 条）

1. 不阻止任何销毁（**P43 不是防删除**）；
2. 不改变 retention / capacity 策略本身；
3. **不判定销毁是否被授权**（`policy` 是声明不是授权结论 —— 授权是策略引擎，属新维度，超范围）；
4. 不恢复已销毁数据；
5. 不提供时间权威（`recorded_at` 自证，**P43 不是时间戳证明**，同 P41/P42）；
6. 不引入外部依赖；
7. 不新建第二套 append-only / canonical / 签名实现；
8. 不回写既有只读面任何字段；
9. 不做「销毁未记账 ⇒ 停止导出」（**反向 fail-open**，同 P42）；
10. 不覆盖 pre-export 的 ring/file drop（见 §9/Q3）；
11. 不做销毁者身份判定（**who 属 P37，本轮不延伸** —— 即 C1 的边界）；
12. 不做跨目录/跨域销毁对账（同 P41 R41-3：逐目录执行。

---

## 8. 已知代价（如实）

1. **上界 = 账本自身独立性**：同域保存 ⇒ 逃逸 A/B 在未启用锚定时本地不可检测（§5）；
2. **`destruction_absent` 大概率是常态**（未启用时），**宁可说无法断言，不说一切正常**；
3. 只覆盖**启用之后**发生的销毁 —— 历史遗留的空洞**永远是 `indeterminate`**，P43 **不追溯**；
4. `destruction_unconfirmed` 会在崩溃恢复后**长期滞留**（无自动收敛，避免自动收敛成为洗白通道）；
5. **C1 残差原样保留**：销毁记录由同一把签名密钥签发 ⇒ 持钥者可伪造销毁记录（逃逸 A）。**P43 让伪造必须破坏锚定，但不让伪造不可能**；
6. pre-export 丢失（ring 256 / file 10000 的 `runtime_dropped`/`file_dropped`）**不在本平面**，记录从未进入证据链 ⇒ P43 无法记账。

---

## 9. 三处提请裁决

- **Q1 锚定是否强制？** 我：**不强制**，复用 `--export-anchor-*`（与 P42 一致）。代价：未启用 ⇒ 逃逸 A 本地不可检测。
- **Q2 `policy` 是否升级为授权判定？** 我：**否**。授权需要策略引擎 + 授权者身份（P37 域），会把 Phase 撑成两套。
- **Q3 pre-export drop 是否纳入？** 我：**否**（另一平面，需记录级标识）。列为 Phase 45 候选。

---

## 10. 测试契约（T200~T216，17 例）

- **T200~T205**：账本原语（单调 seq / 事实性 conflict / 拒绝追加 / 首行豁免 / 分组状态推进 / 自指 compaction）。
- **T206~T210**：五条销毁路径各自记账（prune / ledger / anchor / key-lifecycle / verification compaction）。
- **T211**：合法 prune ⇒ `accounted`（**绿例，防误报**）。
- **T212**：合法 compaction 后窗口外 ⇒ 不断言（I4）。
- **T213 ★核心红例**：持钥删 publication + 删 ledger/anchor 前缀 ⇒ **`unaccounted_disappearance`**；**且逐一验证 P35~P42 各自判据仍为「不断言」**（证明它确实是新判据，不是既有判据的复述）。
- **T214 ★non-vacuousness**：M1（摘掉 prune 的记账）⇒ T213/T211 **必红**。
- **T215**：追溯补写销毁记录 ⇒ 链/锚定 `divergent`。
- **T216**：销毁账本整体删除 ⇒ `destruction_absent`，且**不**被美化成 `accounted`。

**变异 M1~M5**：①摘 prune 记账 ②放行窗口外断言 ③放行同 seq 第二行 ④摘 prev_digest 校验 ⑤改事后补记 ⇒ 各自对应用例必红，按 sha256 字节还原。

---

## 11. 文件与接线（Scope 阶段预估，实现轮可微调）

- 新 `snapshot_destruction.go` + `snapshot_destruction_test.go`；
- `appendonly_log.go`：在 `compactLogPrefixGroups` 内**一处**挂钩（4 条路径自动继承）；`prune()` 单独挂钩；
- `history_export_scheduler.go`：顺序接线（意图先行）；
- `server.go`：新路由 `GET /management/v1/protection/alerts/history/export/destruction`（:8082 admin-only，未启用 503）；
- `main.go`：`--export-destruction-log`(false) / `--export-destruction-capacity`(4096)；**锚定零新增 flag**；
- **构造守卫**：启用销毁记账但 signer==nil ⇒ 失败（同 P42）。

冻结面：`appendonly_log.go` 仅**加法**、P36/P39/P40/P41/P42 响应零改动、`go.mod`/`go.sum` 零改动、四冻结包零 diff。
