# ADR-071 — Phase 48: Runtime Decision Attestation（判定面留痕 / 判定本身是否可断言）

- **Status**: Proposed (Phase 48, Scope stage)
- **Phase**: 48（Runtime Decision Attestation）
- **Base**: Phase 47 CLOSED（HEAD = `df7b73e`）
- **Supersedes**: 无。不修改 P35~P47 的任何冻结判据；判定语义（七守卫顺序 kill → principal_kill → breaker → concurrency → quota → rate → timeout、admit/reject 分类、403/503/429 状态码）逐字沿用；`internal/protection` **零 diff**；P24.2 既有读面（`/decisions`、`/decisions/export`）响应字节不变。

---

## 1. 分工句（第九问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted / P46 whether-witnessed / P47 whether-delivered ⇒
**P48 = whether recorded（判定本身是否被如实留痕）**：前八个 Phase 依次问「证据是谁签的 / 放在哪 / 何时 / 是否验证过 / 为何缺席 / 接受了什么 / 域外是否回照 / 投递义务是否清偿」——**全部关于证据产物与其载体**。**没有一个问过：产生这些产物的那个判定，自己有没有被如实留痕。**

## 2. 新问题（已核对代码，全部带行号）

**事实**：

1. **Gate 每次 Check 恰发一条判定记录**：`gate.go:106-220` 的 `Check` 在七条守卫的每一条（`:114` kill / `:122` principal_kill / `:138` breaker / `:148` concurrency / `:175` quota-unavailable / `:185` quota-exceeded / `:201` rate）与 admit（`:208`）各调用一次 `emitDecision`（定义 `:324-341`）。`g.provenance == nil` 时整条静默（`:325-327`）——**nil 即无留痕**。
2. **唯一配置的 sink 是有界内存环**：`cmd/opscore/main.go:285` `NewRecordingProvenanceSink(4096)`，`:329` 注入 `Provenance: sink`。环是纯内存（`provenance.go:94-112`，**无任何持久化路径**）⇒ **进程重启后判定记录为空**；溢出按序逐出最旧并计数（`:116-126`）。
3. **该读面自己声明它不是取证历史**：`mgmt_obs.go:403-410` 注释原文「the export is a CURRENT RETENTION SNAPSHOT of the bounded in-memory ring — **NOT a complete forensic history**」；envelope 固定 `export_completeness="current-retention-snapshot"`（`:478`，CSV 元数据 `:519`）。
4. **持久面覆盖的是另一个数据集**：`mgmt_obs.go:529-532` 注释原文「the durable counterpart of /decisions/export (which serves the decision-provenance store — a **DIFFERENT dataset**): that endpoint covers provenance; THIS one covers alert transitions」。即：**持久导出的是 alert-transition 边，不是判定。**
5. **那个持久数据集是证据，判定不是**：alert-transition 面有签名（`history_export_scheduler.go:63` `SignKeyPath` / `:67` `SignKeyID`）、链（`snapshot_ledger.go:43` `chain-ledger.jsonl`）、锚定（`snapshot_anchor.go:39` `chain-anchor.jsonl`）、对账（P46）、投递（P47）。**判定本身不在这条链的任何一环上。**
6. **判定确实有一条持久路径，但只是 audit 行**：`cmd/opscore/main.go:327` `Audit: &storageAuditWriter{store: stor.Audit()}` → `protection.AuditWriter`（`types.go:26-28`）/`ProtectionEvent`（`:31-37`，字段仅 Timestamp/Action/CapID/Principal/Detail）。落库形态 `storage.AuditEvent`（`internal/storage/models.go:82-107`）**无 digest、无 prev-hash、无链、无签名**（`internal/storage/audit_query.go` 全文件无任何 hash/chain 概念——已核）。且 `auditWrite` 只在**拒绝**路径被调用（`:113/:121/:137/:147/:174/:184/:200`），**admit 不写 audit** ⇒ audit 面只有拒绝，没有「放行过谁」。
7. **判定面是保护子系统的执行点**：`s.gate.Check` 在 `server.go:660`、`:760`、`:1185` 三处被调用（操作执行路径）⇒ 判定决定了哪些操作被放行。

**悖论**：系统能断言「账本被删」（P46 `family_ledger_deleted`）、「从未验证」（P42）、「为何缺席」（P43）、「投递是否清偿」（P47 `converged`）——**唯独不能断言「判定本身被如实留痕」**。今天一条判定记录的三个可能去处全部不合格：内存环（易失 + 有界丢失 + 无签名）、audit 行（只记拒绝 + 无链 + 无签名 + 无锚定）、持久导出（覆盖的是 alert 边，不是判定）。⇒ 「该窗口内的判定记录完整且域外可断言」**在本地无任何面可引用**。

## 3. 决策（Scope）

把判定记录升级为**第六条证据族** `protection_decision`：判定记录持久落盘、按 tick 取摘要、纳入既有签名/链/锚定机制，并注册进 P46 的对账注册表与 P47 的投递分区表 ⇒ **P46/P47 已建成的八个问题面自动覆盖它**。本 Phase 只新增**一个**源特有的判据（诚实丢失），其余判据**逐字复用**。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`decision_attested`** | 该 tick 的判定日志前缀被一条已落盘锚定条目覆盖 ∧ 该窗口 `dropped == 0` ∧ 无结构性 error | 判定记录**完整且域外可断言**——此前给不出的判据（§2 悖论） |
| **`decision_loss`** | 有界队列/日志溢出计数 > 0（R24-7 纪律的**跨进程**版本） | 诚实丢失：**`decision_attested` 必为假**；绝不把「丢了」呈现为「没发生」 |
| `family_intact` / `family_incomplete` / `family_ledger_deleted` / `family_ledger_truncated` / `family_divergent` | **逐字复用 P46**（ADR-068 §3 缺席矩阵 + 三段式窗口） | 对账面免费获得 |
| `pending` / `anchored` / `unanchored` / `pending_retryable` / `converged` / `swept_by` | **逐字复用 P47**（ADR-069 §3） | 投递面免费获得 |

**核心红例（此前给不出的端到端判据）**：手工在判定日志中删除/篡改一条已落盘记录（模拟事后清理）⇒ 内存环无痕、audit 行无链 ⇒ **P35~P47 全部沉默**；P48 在摘要面对账时断言 `family_divergent`（篡改）或 `family_ledger_truncated`（尾部删除）。

**R210 尺子（§8 详述）**：本条产出的是**此前根本给不出**的判据——判定面今天既不可持久断言，也不可域外回照。它不是复述（P46/P47 的判据对象是**证据产物**，本条对象是**判定本身**），不是加固（不是把既有判据再签一遍），不是呈现（不是把既有判据换一种画法）。

## 4. 能力定义（A1~A8）

- **A1 判定记录族**：新增主账本 `protection-decision.jsonl`（append-only，有界，诚实丢失计数）+ 锚定流 `decision-anchor.jsonl`（自有 seq 空间，沿 P40 分类器「按 `anchor_seq` 分组故必须分文件」的既有理由，ADR-069 §2 事实 1）。**注册为第六族**：加入 `witnessFamilyRegistry`（`history_export_scheduler.go:1656`）与 `anchorDeliveryStreams`（`snapshot_anchor_delivery.go:93`）。两处注册表都是静态闭表，第六族进入后**既有 P46 POST 路由与 P47 状态面自动覆盖它，无需新路由**（`witnessFamilyKnown` `:1694` 以注册表为准）。
- **A2 零触碰判定路径（承重承诺）**：`internal/protection` **零 diff**。落盘通过**新 sink** 实现 `protection.ProvenanceSink`（`provenance.go:55-57`）并在组装根（`cmd/opscore/main.go:329`）注入——**Gate 的 `emitDecision` 调用点、判定语义、七守卫顺序逐字不变**。
- **A3 非阻塞（R24-4 逐字保留）**：新 sink 的 `Emit` **绝不阻塞判定**：入有界队列即返回；队列满 ⇒ 逐出最旧 + `dropped++` + `truncated=true`（与 `RecordingProvenanceSink:116-126` 同款纪律），**绝不等待、绝不回压、绝不改判定**。落盘由既有 Tick 排空（复用调度器，**不新增常驻组件**）。
- **A4 摘要与链**：每 Tick 对判定日志的**新增前缀**取 head digest，写一条锚定条目（复用 `appendAnchorEntryPathObserved` 形态），纳入 P37 签名 / P39 链 / P40 锚定 / P46 对账 / P47 投递。**不做逐条锚定**（判定是高频率源，逐条签名/锚定不可行——A8-②）。
- **A5 诚实丢失跨进程化**：`dropped`/`truncated` 必须**持久化**并在读面呈现（今天 `ProvenanceStats` 只在内存，`provenance.go:129-138`，重启即归零——把「丢失」洗成「无丢失」）。`dropped > 0` 的窗口**绝不**报 `decision_attested`。
- **A6 正交与零回归**：P35~P47 判据取值零变化；`internal/protection` 零 diff；P24.2 既有读面 `/decisions`（`mgmt_obs.go:31`）与 `/decisions/export`（`:420`）**响应字节逐字节不变**（新 sink 在 `ProvenanceStore` 角色上**委派**既有环，故 `gate.ProvenanceStore()` 返回的仍是同一个环）；`snapshot_anchor.go` 零 diff；`go.mod`/`go.sum` 零改动。**不新增路由**（第六族并入既有状态面与既有 P46/P47 路由）。
- **A7 非目标**：①不改判定语义与七守卫顺序；②不改 `internal/protection`（A2）；③不新增路由 / 不改 P24.2 响应字节；④不做逐条签名/锚定；⑤不拉取 witness（P46 A7-① 仍成立）；⑥不做跨部署；⑦**不追认启用前的判定**（本 Phase 之前的判定不可断言，且**绝不**从 audit 行反推——见 A8-④）；⑧不做实时流式锚定（只做 per-tick 摘要）；⑨不修 P47 A8-⑦ 的 `destructionObserver` 并发缺口。
- **A8 已知代价**：①**自证**：签名者与判定者在同一进程（与全部五族同款，P42/P44 的已知代价同源）——本 Phase **不**声称能防进程内攻击者。②**高频率源的摘要粒度**：判定量远大于生命周期事件（每一次 `Check` 一条），故只做 per-tick 摘要 ⇒ 单条判定的可断言性止于「它落在某个已锚定窗口内」，**不是**逐条可验证（与 P34 快照/边界的既有粒度一致）。③**过载丢失不可与「未发生」区分**：队列满时被逐出的判定在日志里不留痕（只留计数）⇒ 读面必须响亮报 `decision_loss` 且绝不报 `decision_attested`；这是 R24-4（非阻塞）与取证完备性之间的**不可两全**，本 Phase 选择前者并如实登记。④**启用前的判定不可断言**：与 P41 T177 / P42 `verification_absent` 同族；**不从 audit 行反推**（audit 只记拒绝、无链，且并入会造成「两个真相源」）。⑤**判定日志本身可被删除** ⇒ 由 P46 缺席矩阵判 `family_ledger_deleted`（这正是复用 P46 的收益，不是新洞）。⑥**行为变更声明**：第六族的注册会**扩展** P47 的静态分区枚举 ⇒ T277（`snapshot_anchor_delivery_test.go:263-269` 断言 `len(streams) == 5`）与 T283（`:486` 断言三条 sweep 流）必须同步扩到六条——**断言变强而非变弱**，且是 P47 ADR 明示的静态表被合法扩展（A1），本 Phase 显式声明该测试改动。⑦读面成本：多读一条判定日志 + 一条锚定日志（管理读面，非热路径）。⑧判定记录的 `principal_hash` 已在 R24-7 秘密边界内（只哈希、无明文、无密钥）⇒ 新族不扩大秘密面。

## 5. 测试契约（T297~T316，20 例）

| # | 断言 |
|---|---|
| T297 | 冻结面零 diff + `internal/protection` 零 diff + `go.mod`/`go.sum` 零改动 + P35~P47 判据取值零变化 |
| T298 | **P24.2 读面零回归**：`/decisions` 与 `/decisions/export`（json+csv）在启用第六族前后**逐字节相同**；`gate.ProvenanceStore()` 仍返回既有环（`provenance.go:141-150` 语义） |
| T299 | **核心红例（篡改）**：手工改判定日志中一条已落盘记录 ⇒ 摘要面对账判 `family_divergent`；内存环与 audit 面对此**零反应**（证明此前给不出） |
| T300 | **核心红例（删除）**：手工截断判定日志尾部 ⇒ `family_ledger_truncated`（沿 P46 无宽免层） |
| T301 | **核心红例（整删）**：删主账本 + 锚定流 ⇒ `family_ledger_deleted`（P46 缺席矩阵） |
| T302 | **`decision_attested` 成立**：正常 tick 后窗口被锚定 ∧ `dropped == 0` ⇒ `decision_attested == true` |
| T303 | **`decision_loss` 压制 attested**：制造队列溢出 ⇒ `dropped > 0` ∧ `decision_attested == false` ∧ `truncated == true` |
| T304 | **丢失跨进程诚实**：溢出后重启（新建 sink）⇒ `dropped` **不归零**（持久化），绝不把丢失洗成无丢失 |
| T305 | **非阻塞（R24-4）**：scripted 慢/满 sink 下判定延迟与判定结果**不受影响**（admit/reject 与状态码逐字不变） |
| T306 | **判定语义零变化**：七守卫顺序与 403/503/429 分类逐字不变（对照 P21 既有用例） |
| T307 | **投递面复用**：第六族 `pending/anchored/unanchored/converged/swept_by` 取值正确；`swept_by == delivery_sweep` |
| T308 | **分区扩展（A8-⑥）**：`anchorDeliveryStreams()` 为六条、各恰有一个 sweeper、无重复覆盖（T277 扩展后的断言） |
| T309 | **投递唯一性**：第六族在 tick 内 attempts 恰 +1（T283 同款，扩到四条 sweep 流） |
| T310 | **对账复用**：第六族走 P46 三段式窗口 + 缺席矩阵（min/max/outside 边界） |
| T311 | **零副作用**：读面不落盘、不写 audit、不发网络 |
| T312 | **fail-closed**：判定日志含不可分类行 / 冲突 seq ⇒ 该族整条不 sweep、不落盘、字节零变化、响亮报错，其余五族不受影响 |
| T313 | **`principal_hash` 秘密边界**：新族输出只含哈希与 advisory 字段，无明文/无密钥（R24-7 边界沿用） |
| T314 | **不追认（A7-⑦）**：启用前存在 audit 拒绝行 ⇒ 新族**不**据此生成任何判定记录，读面报 `not_enabled` 而非伪造历史 |
| T315 | **压缩活性**：第六族 stranded pending 收敛后压缩恢复（P47 T288 同款） |
| T316 | 变异 MU1~MU6（MU1 摘落盘 / MU2 摘 `dropped` 持久化（⇒ T304 必红）/ MU3 把丢失并入 `decision_attested`（⇒ T303 必红）/ MU4 摘 fail-closed / MU5 破分区 / MU6 让新 sink 替换而非委派 `ProvenanceStore`（⇒ T298 必红））⇒ 对应用例必红，sha256 还原 |

## 6. 与既有 Phase 的关系

不降级任何判据。P46 的对账词汇与 P47 的投递词汇**被复用而非重造**——P48 的工程量集中在「新源 + 新 sink + 两处注册」，这正是把 P46/P47 的抽象用在第九个问题上。**P48 补的是它们共同的上游**：五族账本记录的产生都发生在判定面之后，而判定面自身此前不在证据链上。

## 7. 顺路清偿登记债（本 Phase 内）

> P47 §7 登记的 D1~D4（`ADR-057 §8.1` / `§8-8` 四处错误节号 + `ADR-064` I1 措辞过强 + V2 漏列第 ⑥ 项）**已核实全部清偿**（`docs/adr/063-phase44-scope.md:22/:63/:120/:141/:158` 与 `docs/adr/064-phase44-architecture.md:101/:124/:136` 现均为修正后口径）。本轮盘点**未发现新的登记债**；下表是本 Phase 自己产生、必须随实现一并处理的两笔。

| # | 债务 | 处置 |
|---|---|---|
| D5 | `mgmt_obs.go:403-410` 的诚实边界（「NOT a complete forensic history」）在 P48 后**只对内存环成立**；不区分会让读者把「环是快照」误读为「判定面无取证路径」 | 注释改写为「环是保留快照；**判定面的取证历史见第六族 `protection_decision`**」，**保留 `export_completeness` 取值与响应字节不变**（A6 承重承诺） |
| D6 | P47 的两条静态枚举用例（`snapshot_anchor_delivery_test.go:263-269` T277 断言五条流、`:486` T283 断言三条 sweep 流）在第六族注册后必然为假 | 随 A1 同步扩到六条/四条；**只加枚举数量，不改断言强度**（A8-⑥ 已声明） |

## 8. 证据体系收敛判断（R210 尺子）与方向候选裁定表

本轮对证据体系做了显式盘点：八个问题面（who/where/when/whether/why-absent/what-accepted/whether-witnessed/whether-delivered）各有可断言面；P47 §7 的四笔登记债已核实清偿；五族账本的承重载体（投递）已由 P47 补齐 5/5。

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① **判定面留痕**（本 Phase） | **采纳** | 真实缺口（§2 事实 2/3/4/6 逐行核实，且 `mgmt_obs.go:404-408` 的注释**自认**不是取证历史）；新判据 `decision_attested`/`decision_loss` 对**判定面**此前给不出（§2 悖论）；机制复用 P46/P47 已建成的抽象（§3 表） |
| ② 呈现/运维面整合 | **淘汰（仍）** | 不产生此前给不出的判据（R210；同 P47 §9 裁定） |
| ③ HA 多副本 | **淘汰（仍）** | 「一致性不是证据性」——不产生新可断言判据，只把现有判据复制 N 份（ADR-054 §1.1） |
| ④ 外部时间权威（TSA/RFC3161） | **淘汰（仍）** | 判据类别仍是「该 digest 是否被域外记录」＝ P40 第二实例（ADR-057 §1.1） |
| ⑤ 见证端拉取（P46 的逆操作） | **淘汰** | 不产生新判据：`family_incomplete` 已表达「本地有、域外无」；拉取只是同一判据的第二种取数方式 |
| ⑥ P47 A8-⑦ 的 `destructionObserver` 并发缺口 | **不独立成 Phase** | 是既有不变量的**加固**（既有维度），不产生新判据 ⇒ 被 R210 尺子击倒；且工程量远小于一个 Phase |

⇒ **判断**：存在且**仅存在一个**符合 R210 尺子的候选（①）。其余候选全部被同一把尺子击倒（②③④）或属既有维度加固（⑥）。**本 Phase 选择最小闭环**：复用 P46/P47 的词汇与机制，只新增「持久判定源 + 非阻塞 sink + 两处注册 + 一个诚实丢失判据」。

## 9. 评审闭合表（本轮为空）

本 ADR 为 Scope 首稿；评审发现将在修订轮以同结构表格追加（沿 ADR-067 §9 / ADR-069 §10 先例）。
