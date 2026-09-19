# ADR-061 · Phase 43 — Evidence Destruction Accountability（销毁留痕）· Architecture

- **Status**: ACCEPTED（R217 裁决 = **A**；Q4/Q5/Q6 全部支持）；实现见 ADR-062
- **Parent**: ADR-060（P43 Scope, commit `6489d58c`）；ADR-059（P42 Implementation, `2cdc2838`）
- **裁决前提**: R216 = **B**（方向与问题定义采纳），两处必修 **R43-1 / R43-2** 闭合后随 Architecture 同轮交付
- **Author**: executor
- **Scope 覆盖**: 本文与 ADR-060 冲突处以本文为准；ADR-060 已就地同步修订

---

## 0. 必修闭合表

| 编号 | judge 要求 | 闭合位置 | 结论 |
|---|---|---|---|
| **R43-1** | 销毁记录的签名权威（方案 A/B 二选一并说明理由）+ 逃逸 A 重写 | §1（选 **A：复用 KAK**，附 DAK 否决论证）、§5（逃逸 A 双路径重写）、§7（`destruction_unauthorized`） | 闭合 |
| **R43-2** | payload/state 字段分离表 + 状态机钉死 + T 判别例 | §2（分离表）、§3（状态机 + 重试语义）、§9（T218~T220） | 闭合 |

judge 采纳项（Q1 锚定不强制 / Q2 policy 不升级为授权判定 / Q3 pre-export drop 列 Phase 45 / I4 / I5 / I6 / T200~T216）**原样继承**，不再复述。

---

## 1. R43-1：销毁记录的签名权威 —— 选**方案 A（复用 KAK）**

### 1.1 决策

**销毁记录由授权密钥（KAK）签发，与导出签名密钥必须不同。**

judge 的逃逸分析成立且我接受其全部前提：现文 §5 逃逸 A 只写了「插链中」一条路，遗漏的「**末端追加**」恰恰是最便宜的一条 —— 新 seq、`prev=当前链头`、持钥签名，是一次**完全合法的追加**，锚定检不出（见证端从未见过「X 未被销毁」的断言）。若不修，`unaccounted_disappearance` 对持钥攻击者形同虚设：P43 的立身之本（无记录的消失可断言）会被一把在线私钥洗掉。

### 1.2 为什么选 A 而不是 B

方案 B（维持同钥 + 如实重写逃逸）在**诚实度**上等价，在**判据强度**上严格更弱：它把核心判据的有效性限制在「攻击者未持有导出私钥」这个子集上，而 P40 §2.2 定义的**主攻击对手模型恰恰就是持有导出私钥者**。用 B，等于宣布核心判据对主攻击无效 —— 那么 P43 相对 P36/P39/P40/P42 的增量就只剩「把沉默整理成一份清单」。

方案 A 的直接收益：**持导出私钥者无法制造一条 `accounted`**。伪造的销毁记录验签不过 ⇒ 不记账 ⇒ 被删证据仍判 `unaccounted_disappearance`（§7、T217）。

### 1.3 为什么不新设 DAK（权限域分离）—— 明确否决

judge 允许「若论证权限域必须分离，则新设 DAK」。我论证**不必分离**，理由三条：

1. **DAK 防的对手已具备等价破坏力** —— 同时持有 KAK 的人已能旋转/吊销全部签名密钥（P41），即已能摧毁 P41 的全部判据。为他单独再设一把钥匙防不住任何**新**东西。
2. **DAK 不产生新维度判据** —— 它只是把「谁能伪造销毁记录」的门槛再抬一级，而 P43 的判据是 `accounted / unaccounted`（销毁是否被记账），不是「谁记的账」。用 R210 击倒 HA 多副本的同一把尺子量：DAK 是**既有维度的加固**，不是新判据 ⇒ 不配占一个密钥位。
3. **仪式成本 ×2** —— 第二把离线私钥 + 第二个独立信任锚 + 第二套 fail-fast 守卫，运维面翻倍，而收益面为零。

⇒ **授权密钥只有一个：KAK。**

### 1.4 P41 冻结表述的扩展（交叉引用）

- P41（ADR-055 §7.1 / 实现注释）表述为「KAK 只签生命周期事件」。**P43 将其显式扩展并收窄为**：

  > **KAK 签「授权事件类」；授权事件类 = 生命周期事件 ∪ 销毁事件。**
  > 二者同阶：一个改变**信任的时间边界**（when），一个改变**证据的存在集合**（why absent）；都是对证据平面的**授权行为**，都不是导出内容，因此都不该由导出签名密钥签发。

- R41-2「自签 ⇒ 重基线攻击不可检测」的论证对销毁**逐字成立**：若销毁记录可用导出密钥签，则「删除证据的能力」与「声称删除合法的能力」同归于一把在线密钥 —— 那是 §2.3 攻击的一部分，不是它的解药。
- **实现复用零改动**：`keyAuthority`（`snapshot_key_lifecycle.go:195`）在同包内被 `snapshot_destruction.go` 直接复用，**P41 文件除一行语义注释外零改动**（注释不改行为，语义扩展以本文为准）。不重命名、不改字段、不动 `writable()/verifiable()`。

### 1.5 三条构造守卫（fail-fast，P41 I1 血统）

| | 条件 | 结果 |
|---|---|---|
| **G1** | `--export-destruction-log` 启用 而 `--export-key-authority` 未配 | 构造失败（账本不可写 ⇒ 有记录能力的假象） |
| **G2** | 启用 而 `--export-key-authority-trust` 为空 / KAK 不在信任集 | 构造失败（**能签不能验 = 有授权的假象**，同 P37 T79b） |
| **G3** | KAK key_id == 导出签名 key_id | 构造失败（**已存在**：`history_export_scheduler.go:339`，P43 自动继承，同 P41 T173） |

三条都在**构造期**失败，运行期不退化：P43 不提供「先启用、后补钥匙」的窗口 —— 那段时间里写下的记录无人能验。

**未启用时逐字节不变**（P41/P42 同款纪律）：`--export-destruction-log=false` ⇒ 不建文件、不挂钩、不改任何既有响应（T216b 式零回归断言）。

---

## 2. R43-2：payload / state 字段分离表

judge 指出 I1 与两阶段顺序**字面自相矛盾**（第二行 `state` 必然不同，按 I1 字面即 conflict）。我在复用 P40 模型时漏了 ADR-053 §1.3 的那一步：**显式划分签名区与状态区**。补齐如下（`destructionEntry`）：

| 字段 | 区域 | 可变性 | 说明 |
|---|---|---|---|
| `v` | **payload** | 不可变 | 1 |
| `destruction_seq` | **payload** | 不可变 | 组键，从账本自身导出（R41-6，无水位文件） |
| `kind` | **payload** | 不可变 | `snapshot_retention` / `ledger_compaction` / `anchor_compaction` / `key_lifecycle_compaction` / `verification_compaction` / `self_compaction` |
| `targets[]` | **payload** | 不可变 | 二形态（§4.2） |
| `destroyed_count` | **payload** | 不可变 | 与 `targets` 长度一致（日志面为被裁行数） |
| `policy` | **payload** | 不可变 | 声明性策略标识（`retain=96` / `capacity=4096`），**非授权结论**（Q2） |
| `recorded_at` | **payload** | 不可变 | 自证时间，非时间权威（非目标 5） |
| `authority_key_id` | **payload** | 不可变 | KAK key id（P37 派生） |
| `stream_id` | **payload** | 不可变 | P40 R40-2 派生，防异域（同 P41 T176） |
| `prev_digest` | **payload** | 不可变 | 行级哈希链（§2.2），首行为空 |
| `entry_digest` | 派生 | — | `sha256(canonicalDestructionPayload)`，**不在** payload 内 |
| `signature` | 派生 | — | 复用 P37 `signatureBlock`，覆盖 payload |
| **`state`** | **STATE** | **可变** | `intended` / `completed` / `aborted` —— **永不进 payload、永不被签名** |

### 2.1 canonical 序列化（禁第二套）

`destructionSigned`（= 上表 payload 区，字段顺序固定）经**同一个 `json.Marshal` canonical 家族**序列化（与 P37 manifest / P40 anchor / P41 lifecycle / P42 report 同源）。`canonicalDestructionPayload(e)` 即 KAK 签名覆盖的字节。

### 2.2 两条摘要：`entry_digest` 与 `line_digest`（R43-2 的直接推论）

`state` 不在签名内 ⇒ 若直接把 `entry_digest` 当链值，`intended` 行与 `completed` 行**链值相同** ⇒ 「删掉 `completed` 行」与「从未 completed」在链上不可区分，重复追加也检测不到。故：

```
entry_digest = sha256(canonicalDestructionPayload(e))         // 内容承诺；两行相同
line_digest  = sha256(prev_digest + ":" + entry_digest + ":" + state)   // 链值；含 state
prev_digest(第 n 行) = line_digest(第 n-1 行)                 // 首行为空（I3 豁免）
```

- `entry_digest` 落盘（对外承诺、锚定携带）；`line_digest` **验算时现算、不落盘**（无新字段、无新序列化）。
- 于是：同 payload 异 state 的两行 **entry_digest 相同（合法推进）+ line_digest 不同（链可区分）**，I1' 与链完整性同时成立。记为 **I9**。

### 2.3 I1 修订（**I1'**，取代 ADR-060 §4.2 I1）

> 销毁记录是**事实 + 一次执行状态推进**的复合体。判据以 `(destruction_seq, payload)` 为准：
> - 同 seq 第二行 **payload 相同且 state 合法推进**（`intended → completed|aborted`）⇒ **合法**；
> - 同 seq 第二行 **payload 任一字段不同**（`targets`/`destroyed_count`/`policy`/`kind`/`prev_digest`/`recorded_at`…）⇒ **conflict** ⇒ fail-closed；
> - **终态之后出现任何第三行**（无论 payload 是否相同）⇒ **conflict**（I8）。

判别器直接照抄 `anchorPayloadEqual`（`snapshot_anchor.go:191`）的形状：`destructionPayloadEqual(a,b) == (destructionSignedFields(a) == destructionSignedFields(b))`。

---

## 3. 状态机（钉死）

```
            ┌──────────────► completed （终态）
intended ───┤
            └──────────────► aborted   （终态）
completed / aborted ⇒ 终态，永不再迁移（第三行 = conflict）
```

- **重试语义：`aborted` 之后的重试 = 新 `destruction_seq`，禁止在原 seq 上继续。** 理由：原 seq 的 `targets`/`entry_digest` 是对**那一次尝试**的承诺；复用会把两次销毁压成一条记录，`destroyed_count` 与 `targets` 的语义同时崩坏，且给「一次声明、多次销毁」留门。
- **崩溃后只有 `intended` 的组永不自动推进**（无自动收敛 —— 自动收敛必然是洗白通道，已知代价 4 原样保留）。
- **推进不需要 KAK 在线**：因 `state` 不在 payload 内，`completed`/`aborted` 行**逐字复用 `intended` 行的 signature 字节**（payload 相同 ⇒ 签名仍有效）。这与 KAK 离线的运维模型一致，也避免「推进失败 ⇒ 记录永远停在 intended ⇒ 满屏 `destruction_unconfirmed`」的自伤。记为 **I8**。

---

## 4. 机制（沿用 ADR-060，本节只写 Architecture 轮的增量）

### 4.1 文件与身份

`destruction-log.jsonl`，与 export dir **同域**（P41 R41-3：不扇出、逐目录执行）；`stream_id` 派生同 P40。容量默认 4096（`--export-destruction-capacity`，0 = 全留）。

### 4.2 targets 二形态（不变）

- publication 面：`[{publication_id, manifest_digest}]` —— digest 取自销毁前仍可读的 manifest；**manifest 已缺失/不可解析时 digest 为空，按 `publication_id` 精确匹配记账**，并在条目上标 `target_digest_unavailable: ["<id>"]`（不静默、不猜）。空 digest **不是通配符**，它只表示「销毁时摘要不可得」，匹配退化为 id 精确匹配。
- 日志面：`[{from_seq, to_seq, prefix_digest}]` —— 被裁前缀在 compaction 时正在内存，`prefix_digest` 当场可算。

### 4.3 顺序铁律（意图先行，不变）

```
1. append(state=intended) + fsync      ← 摘要在内容仍存在时捕获
2. 执行销毁（os.Remove / rewrite）
3. append(state=completed|aborted)     ← 同 seq 组，复用同一 signature 字节
4. 锚定派发（仅当 P40 anchor 启用；复用 dispatchAnchorPath）
```

**禁 destroy-then-record**；**禁 record-only**。

---

## 5. 逃逸分析（R43-1 要求重写）

伪造「X 已被合法销毁」只有两条路，方案 A 下逐一过审：

### 逃逸 A-1：插在链中（伪装成早期写的）

必须重写其后**全部**行（行级链 + 签名绑定 prev_digest）⇒ 若其后条目已锚定 ⇒ P40 reconcile `divergent`（双信号，T145 式冗余）。**锚定未启用 ⇒ 本地不可检测。**

### 逃逸 A-2：末端追加（新 seq、prev=当前链头、签名）

- **持导出私钥者（= P40 主攻击的对手模型）**：签不出 KAK 签名 ⇒ 条目 `destruction_unauthorized` ⇒ **不记账** ⇒ X 仍判 `unaccounted_disappearance`。**这正是方案 B 下不成立的那一条。**（T217）
- **持 KAK 者**：可完成一次合法追加 ⇒ 洗白成立。**本地与域外均不可检测**：见证端只见过销毁账本的摘要历史，从未见过「X 未被销毁」的断言，故无矛盾可发现。

### 逃逸 B：连销毁账本一起删

⇒ `destruction_absent`，与「从未启用」本地不可区分（同 P42 `verification_absent` / P41 T177 同族）；域外可检测（见证端见过它）。

### 关于「P42 锚定的验证报告能否反向证明 X 曾存在」

不能形成判定，理由要说清：报告 `items[].identity` 确实在见证端留下了「X 在 T1 仍存在」的断言，但**销毁记录的 `recorded_at` 是自证时间**（非目标 5，同 P41/P42）⇒ 攻击者把伪造记录写成 T2 > T1 即无矛盾。**P43 不是时间戳证明** —— 这条限制不因锚定而消失。

### 诚实的结论

方案 A **没有消除**伪造销毁记录的可能性，它做的是**收缩与分层**：

> 伪造 `accounted` 的能力，从「任何持有在线导出私钥者」收缩到「持有离线 KAK 者」。
> 前者是 P40/P41 反复建模的日常对手（RCE、备份泄露、密钥同域）；后者已经能吊销并旋转全部签名密钥。

**新增已知代价第 1 条**（见 §10）照此如实书写，不美化。

---

## 6. 挂钩与接线（Architecture 定稿）

### 6.1 共享原语：observed 变体（P41 路径参数化模式的再次实例化）

`appendonly_log.go`：

```go
func compactLogPrefixGroups(path string, keepGroups int, classify groupClassifier) error {
    return compactLogPrefixGroupsObserved(path, keepGroups, classify, nil)
}

func compactLogPrefixGroupsObserved(path string, keepGroups int, classify groupClassifier,
    observe func(path string, droppedGroups []int64, droppedLines []logLine) error) error { ... }
```

- 原函数变**薄封装**（与 P41 `loadAnchorStatePath`/`dispatchAnchorPath` 同手法），既有 4 个消费方**零改动**。
- `observe` 在**计算完 drop 集之后、rewrite 之前**调用；返回 error ⇒ **compaction 拒绝执行**（fail-closed：宁可不裁，也不无记账地裁）。
- 4 个消费方（ledger / anchor / key-lifecycle / verification）在 scheduler 处各传 `s.destructionObserver()` ⇒ **一处挂钩，四条路径自动继承**。

### 6.2 自指闭合 I5：销毁账本自己的 compaction 不走 observed 版本

若销毁账本自身的 compaction 也装 observer ⇒ 无限递归。**递归终止由结构保证**：

- 销毁账本的 compaction 用**专用路径**：`readLogLines` → 计算 drop 集 → **append `self_compaction` intended**（新组）→ `rewriteLogLines(幸存行 + 刚追加的行)` → append `completed`。
- 专用路径**不装 observer**，因此不递归；且「记录先于销毁」的顺序铁律在自指情形下依然成立。
- 递归终止条件写成一句话：**每个销毁动作的记录者，是比它更高一层的调用者；销毁账本自己的记录者是自己写入的显式序列，不再触发观察。**

### 6.3 prune 挂钩

`prune()`（`history_export_scheduler.go:1098`）是 retention 删除的**唯一**执行点 ⇒ 只在这里挂钩一次（调用点 :566 / :600 无需改动）：逐 retention unit 计算将被删除的文件集合 → 读 manifest 取 `(publication_id, manifest_digest)` → append intended → `os.Remove` → append completed。

**销毁失败怎么办**：`os.Remove` 部分失败 ⇒ 该组记 `aborted`（不记 completed），`destroyed_count` 为**实际**删除数。绝不因记账而声称删除成功。

### 6.4 其它接线

| 位置 | 改动 |
|---|---|
| `snapshot_destruction.go`（新） | entry / canonical / 签名 / 验签 / load / append / advance / 自指 compaction / 对账 |
| `snapshot_destruction_test.go`（新） | T200~T220 |
| `appendonly_log.go` | 仅加法（observed 变体）；既有函数体逻辑不变 |
| `history_export_scheduler.go` | 构造守卫 G1~G3、observer 注入、`destructionError` 状态字段、anchor 派发 |
| `snapshot_anchor.go` | **第 4 个 anchor family**：`kind=destruction` + `destruction_seq` / `destruction_digest` / `destruction_verdict`（全 `omitempty` ⇒ 前三族字节不变）+ `identityID()` 加一个 case。**这是本轮对 P40 文件的全部改动**，T221 钉住字节等价 |
| `server.go` | `GET /management/v1/protection/alerts/history/export/destruction`（:8082 admin-only，未启用 503） |
| `main.go` | `--export-destruction-log`(false) / `--export-destruction-capacity`(4096)；**KAK 与锚定零新增 flag**（复用 `--export-key-authority*` / `--export-anchor-*`） |

---

## 7. 对账算法与判据（含新增 verdict）

```
曾存在 = ledger.publication_id ∪ chain.prev 引用 ∪ anchor(anchored) ∪ verification report items[].identity
现存   = disk 上可列举的 manifest
缺失   = 曾存在 − 现存
记账   = destruction log 中 state=completed 且签名授权(KAK verified) 且 seq ∈ 窗口 的 targets
未记账 = 缺失 − 记账
```

| verdict | 含义 |
|---|---|
| `accounted` | 缺失项全部被 completed + 已授权的销毁记录覆盖 |
| **`unaccounted_disappearance`** | **至少一个缺失项无任何合法记账**（核心产出） |
| `destruction_unconfirmed` | 存在只有 `intended` 的组（崩溃/中断；响但不指控） |
| **`destruction_unauthorized`** | 条目签名缺失 / 不在 KAK 信任集 / 与 `authority_key_id` 不符 / 对 payload 不验 ⇒ **该条目不记账** + 账本不可验 ⇒ 拒绝追加（**R43-1 新增**） |
| `destruction_conflict` | 同 seq payload 不同，或终态后第三行（I1'/I8） |
| `destruction_chain_broken` | `line_digest` 链不匹配（非首行） |
| `destruction_window_discontinuous` | 幸存 seq 段不连续（I4） |
| `destruction_absent` | 无账本（未启用 / 被整体删除） |

- **窗口纪律 I4 不变**：`destruction_window{min,max,entries,continuous}` 强制携带；窗口外**绝不**断言 accounted ⇒ 相关缺失项进 `not_assertable[]` 并带 reason（**空列表必带说明**）。
- **I6 不变**：销毁 status **绝不参与**任何其它 verdict 的合流（P42 I7 推广）。

---

## 8. 不变量（本文定稿）

| | 内容 |
|---|---|
| **I1'** | `(seq, payload)` 判据（§2.3），取代 ADR-060 I1 |
| **I2** | 账本不可验（不可解析行 / **签名不验（含 unauthorized）** / 链断裂 / 异域 stream）⇒ 拒绝追加，绝不借 append 重建干净账本 |
| **I3** | 首行 `prev_digest` 豁免 |
| **I4** | 窗口纪律（继承 R40-1） |
| **I5** | 自指闭合（§6.2） |
| **I6** | 销毁状态不参与任何合流 |
| **I7** | **payload/state 分离（§2）**：`state` 永不进签名；判别只看 payload |
| **I8** | 终态不可再迁移；`aborted` 后重试 = 新 seq；推进复用同一 signature 字节（KAK 无需在线） |
| **I9** | 链值 `line_digest` 含 `state`，与 `entry_digest` 分离（§2.2） |

---

## 9. 非目标增补（在 ADR-060 12 条之上）

13. **不做销毁者身份判定**（C1 边界，同 Scope 第 11 条）—— KAK 只回答「这条记录是授权的」，不回答「是谁按的按钮」；
14. **不做「销毁是否被允许」的判定**（Q2 已采纳）—— `policy` 是声明，授权属策略引擎，是新维度；
15. **不因记账失败而阻止销毁**（同 Scope 第 1 条）—— 但**记账失败会让 compaction 拒绝执行**（§6.1）：这是「不阻止销毁」的唯一例外，因为 compaction 是**本系统主动发起**的销毁，而 retention 之外的第三方删除不由本系统控制。此差异在 ADR 中显式声明，避免下轮误读为矛盾。

---

## 10. 已知代价（重写第 1、5 条）

1. **【新第一条】伪造能力收缩而非消除**：持 **KAK** 者可末端追加伪造销毁记录，**本地与域外均不可检测**（§5 逃逸 A-2）。方案 A 把伪造门槛从「在线导出私钥」抬到「离线授权密钥」，但**没有**让它变成不可能。这是 P43 最强的残留弱点，与 P41 T177 / P42 `verification_absent` 同族。
2. `destruction_absent` 大概率是常态（未启用时），宁可说无法断言，不说一切正常。
3. 只覆盖启用之后发生的销毁；历史空洞永远是 `indeterminate`，P43 不追溯。
4. `destruction_unconfirmed` 会长期滞留（无自动收敛）。
5. **【原第 5 条替换】C1 残差按对手分层**：对持导出私钥者，P43 已闭合（T217）；对持 KAK 者，残差原样保留（第 1 条）。
6. pre-export 丢失（ring/file drop）不在本平面，记录从未进入证据链 ⇒ P43 无法记账（Q3，列 Phase 45）。
7. **targets 摘要不可得时退化为 id 匹配**（§4.2）：此时「记账的那一项」与「消失的那一项」只靠 id 关联，强度低于 id+digest。

---

## 11. 测试契约（T200~T221，22 例）

- **T200~T216**：ADR-060 原 17 例原样保留。
- **T217 ★（R43-1 判别）**：持**导出私钥**为已删 publication 末端追加一条伪造销毁记录 ⇒ 验签 `destruction_unauthorized` ⇒ 该条目不记账 ⇒ 结果**仍** `unaccounted_disappearance`。
- **T218 ★（R43-2 合法推进）**：`intended → completed` 同 seq、同 payload、异 state ⇒ 合法；且 completed 行的 `signature` 与 `entry_digest` 与 intended 行**字节相同**（I8/I9）。
- **T219（R43-2 conflict）**：同 seq 第二行 `targets` 不同 ⇒ `destruction_conflict`。
- **T220（终态）**：`completed` 之后再追加第三行 ⇒ conflict（即便 payload 相同）。
- **T221（P40/P41/P42 字节等价）**：启用销毁锚定后，既有三族 anchor 条目**逐字节不变**（`identityID()` 新 case 不回染）。

**变异 M1~M7**（摘判据 ⇒ 对应 T 必红，按 sha256 还原）：

| | 变异 | 必红 |
|---|---|---|
| M1 | 摘 prune 记账 | T211 / T213 |
| M2 | 放行窗口外断言 | T212 |
| M3 | 放行同 seq 第二行（不判 payload） | T219 / T220 |
| M4 | 摘 `line_digest` 链校验 | T213 |
| M5 | 改事后补记（destroy-then-record） | T213 |
| **M6** | **用导出签名密钥签销毁记录（摘 G3）** | **T217** |
| **M7** | **把 `state` 纳入签名 payload（破 I7）** | **T218 / T219** |

---

## 12. 冻结面自证

`appendonly_log.go` 仅加法、P36/P39/P40/P41/P42 响应零改动、`snapshot_anchor.go` 仅 omitempty + 一个 switch case、`go.mod`/`go.sum` 零改动、四冻结包零 diff。
