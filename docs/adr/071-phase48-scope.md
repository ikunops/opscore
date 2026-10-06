# ADR-071 — Phase 48: Runtime Decision Attestation（判定面留痕 / 判定本身是否可断言）

- **Status**: Proposed (Phase 48, Scope stage) · Revision 2（评审 5 项 major 全部闭合，见 §10）
- **Phase**: 48（Runtime Decision Attestation）
- **Base**: Phase 47 CLOSED（HEAD = `df7b73e`）
- **Supersedes**: 无。不修改 P35~P47 的任何冻结判据；判定语义（七守卫顺序 kill → principal_kill → breaker → concurrency → quota → rate → timeout、admit/reject 分类、403/503/429 状态码）逐字沿用；`internal/protection` **零 diff**；`snapshot_anchor.go` **零 diff**（§4 A1 的载体决策）；P24.2 既有读面（`/decisions`、`/decisions/export`）响应字节不变。

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
6. **判定确实有一条持久路径，但只是 audit 行**：`cmd/opscore/main.go:327` `Audit: &storageAuditWriter{store: stor.Audit()}` → `protection.AuditWriter`（`types.go:26-28`）/`ProtectionEvent`（`:31-37`）。落库形态 `storage.AuditEvent`（`internal/storage/models.go:82-107`）**无 digest、无 prev-hash、无链、无签名**（`internal/storage/audit_query.go` 全文件无任何 hash/chain 概念——已核）。且 `auditWrite` 只在**拒绝**路径被调用（`:113/:121/:137/:147/:174/:184/:200`），**admit 不写 audit** ⇒ audit 面只有拒绝，没有「放行过谁」。
7. **判定面是保护子系统的执行点**：`s.gate.Check` 在 `server.go:660`、`:760`、`:1185` 三处被调用（操作执行路径）⇒ 判定决定了哪些操作被放行。

**悖论**：系统能断言「账本被删」（P46 `family_ledger_deleted`）、「从未验证」（P42）、「为何缺席」（P43）、「投递是否清偿」（P47 `converged`）——**唯独不能断言「判定本身被如实留痕」**。今天一条判定记录的三个可能去处全部不合格：内存环（易失 + 有界丢失 + 无签名）、audit 行（只记拒绝 + 无链 + 无签名 + 无锚定）、持久导出（覆盖的是 alert 边，不是判定）。⇒ 「该窗口内的判定记录完整且域外可断言」**在本地无任何面可引用**。

**评审 M1 修正的机制边界（承重，不可省略）**：**P46 的对账并不检查主账本内容**。`snapshot_witness_reconcile.go:175` 的 `ledgerAbsent := !witnessFilePresent(f.LedgerPath(dir))` 是全仓对 `LedgerPath` 的唯一用法之一（另一处 `:384` 同为存在性探针）——**主账本只参与缺席矩阵**；`:244-282` 的三段式窗口比对的是 `digestOfLocalAnchor(&e)`（**本地锚定条目**的 `anchorDigestOf`，`snapshot_anchor.go:665-672`）与 witness 投影的 `AnchorDigest`，而 `f.LocalDigest` 由 `history_export_scheduler.go:1706-1721` 的 `witnessAnchorDigestFunc` 构造、读的同样是 **AnchorPath**。⇒ **篡改/截断主判定日志对 P46 完全不可见**（改一行 ⇒ 锚定流不变 ⇒ `family_intact`；截断尾部 ⇒ 无 seq 高于本地锚定窗口上界 ⇒ 同样 `family_intact`）。**故 P48 的核心判据不能靠复用 P46 得到**（§3、§4 A4）。

## 3. 决策（Scope）

把判定记录升级为**第六条证据族** `protection_decision`，它由**两半**构成，且两半的检测能力**必须分别声明**：

- **P46 复用面（整删 + 锚定侧）**：注册进 `witnessFamilyRegistry`（`history_export_scheduler.go:1656`）与 `anchorDeliveryStreams`（`snapshot_anchor_delivery.go:93`）后，第六族**免费获得** P46 的缺席矩阵（两文件双双缺席 ⇒ `family_ledger_deleted`）、锚定流分歧/尾部截断、身份毒化；以及 P47 的投递面（`pending/anchored/unanchored/pending_retryable/converged/swept_by`）。
- **新增本地重算面（判定日志内容）**：**P46 不覆盖主账本内容**（§2 评审 M1 修正），故本 Phase 新增一条**本地重算判据**——判定日志本身是**哈希链**（每条记录带 `seq` 与 `prev_digest`），锚定条目携带**锚定时刻的链头摘要**；读面**重走链并逐条校验链接**、再把重算出的链头与锚定条目所记比较。这是 `decision_attested` 的**非空泛**来源。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`decision_attested`** | ①**本地重算**：重走判定日志，每条记录的 `prev_digest` 与前一条的 `digest` 逐字节相等 ∧ 重算出的**链头摘要 == 锚定条目所记的链头摘要**；②该锚定条目已 `anchored`；③本窗口 `queue_dropped == 0`；④无结构性 error | 判定记录**完整且域外可断言**——此前给不出的判据（§2 悖论）。**四个合取项缺一不可**：缺 ① 则退化为「锚定条目存在」（锚定条目每 tick 必写 ⇒ 判据近乎恒真，评审 M2） |
| **`decision_log_divergent`** | ① 为假（链断裂，或链头 ≠ 锚定头） | 判定日志**内容被改动**（篡改/尾部删除）——**P46 对账面对此沉默**，这是本 Phase 新增的检测面 |
| **`decision_log_truncated`** | ① 中链头短于锚定头（当前链头是锚定头的**真前缀**） | 尾部被删（与「合法前缀压缩」可判别：后者链头**不变**，§4 A4） |
| **`decision_loss`** | 本窗口 `queue_dropped > 0`（**仅指从未落盘的丢失**，不含合法前缀压缩——评审 M4） | 诚实丢失：**`decision_attested` 必为假**；绝不把「丢了」呈现为「没发生」 |
| `family_intact` / `family_incomplete` / `family_ledger_deleted` / `family_ledger_truncated` / `family_divergent` | **逐字复用 P46**（ADR-068 §3 缺席矩阵 + 三段式窗口） | 对账面免费获得——但**只覆盖整删与锚定侧**（§2 机制边界） |
| `pending` / `anchored` / `unanchored` / `pending_retryable` / `converged` / `swept_by` | **逐字复用 P47**（ADR-069 §3） | 投递面免费获得 |

**核心红例（此前给不出的端到端判据）**：手工改判定日志中一条已落盘记录 ⇒ 重算链断裂/链头不符 ⇒ `decision_log_divergent` ∧ `decision_attested == false`；**同一用例显式断言 P46 对账面对此报 `family_intact`**（证明这是新增机制，不是复用）。截断尾部 ⇒ `decision_log_truncated`。整删两文件 ⇒ P46 `family_ledger_deleted`（这才是被 P46 覆盖的那一半）。

**R210 尺子（§9 详述）**：本条产出的是**此前根本给不出**的判据——判定面今天既不可持久断言，也不可域外回照。它不是复述（P46/P47 的判据对象是**证据产物**，本条对象是**判定本身**），不是加固（不是把既有判据再签一遍），不是呈现（不是把既有判据换一种画法）。

## 4. 能力定义（A1~A8）

- **A1 判定记录族与载体（评审 M3 闭合）**：新增主账本 `protection-decision.jsonl`（哈希链，每条记录含 `seq`/`prev_digest`/`digest`）+ 锚定流 `decision-anchor.jsonl`（自有 seq 空间，沿 P40 分类器「按 `anchor_seq` 分组故必须分文件」的既有理由，ADR-069 §2 事实 1）。**注册为第六族**：加入 `witnessFamilyRegistry`（`history_export_scheduler.go:1656`）、`anchorDeliveryStreams`（`snapshot_anchor_delivery.go:93`）、`witnessFamilyEnabled`（`:1742-1760` 的 5-case switch——**漏了它第六族会恒报 `not_enabled`**）。**锚定条目的摘要载体**：`snapshot_anchor.go` 属铁律冻结面，其 `anchorEntry`/`anchorSigned`（`:100-177`）**没有通用 payload 摘要字段**，只有按族分配的槽位（`ManifestDigest`/`EventDigest`/`ReportDigest`/`DestructionDigest`/`AcceptanceDigest`）；故第六族**复用两个既有槽位**：`Kind = "protection_decision"`（新常量定义在新文件——`anchorKind*` 常量块在冻结文件 `:65-78`，不得改）+ `ManifestDigest = <判定链头摘要>`。**语义重载显式声明**：`ManifestDigest` 对 publication 族是 manifest 摘要，对非 publication 族历史上恒为 `""`（`anchorDestructionEntry` `:1054`、`anchorAcceptanceEntry` `:1098`、`anchorKeyLifecycleEvent` `:1132` 均不设它）；第六族把它用作判定链头摘要——**字段名不改**（改即破坏冻结面与既有五族的签名 payload）。`identityID()`（`:208-217`）对第六族回退到 `PublicationID`（0），与 **P42 verification 族今日处境完全相同**（该 switch 无 verification 分支）⇒ 既有先例，实现期须以用例钉住（T302）。
- **A2 零触碰判定路径（承重承诺）**：`internal/protection` **零 diff**。落盘通过**新 sink** 实现 `protection.ProvenanceSink`（`provenance.go:55-57`）并在组装根（`cmd/opscore/main.go:329`）注入——**Gate 的 `emitDecision` 调用点、判定语义、七守卫顺序逐字不变**。
- **A3 非阻塞（R24-4 逐字保留）**：新 sink 的 `Emit` **绝不阻塞判定**：入有界队列即返回；队列满 ⇒ 逐出最旧 + `queue_dropped++`（与 `RecordingProvenanceSink:116-126` 同款纪律），**绝不等待、绝不回压、绝不改判定**。落盘由既有 Tick 排空（复用调度器，**不新增常驻组件**）。
- **A4 链、摘要与两种「减少」的判别（评审 M1/M4 闭合）**：①每条判定记录携带 `seq` 与 `prev_digest`（前一条的 `digest`），构成哈希链；②每 Tick 取**当前链头摘要**写入一条锚定条目（复用 A1 的载体），纳入 P37 签名 / P39 链 / P40 锚定 / P46 对账 / P47 投递；③**不做逐条锚定**（判定是高频率源，逐条签名/锚定不可行——A8-②）；④**两种「记录变少」必须分开**：
  - **合法前缀压缩**（有界保留）：判定日志沿用五条主账本的同款纪律——`compactLogPrefixGroupsObserved`（`appendonly_log.go:140`）+ 本族分组函数 `decisionGroupOf`（定义在新文件，同 `keyLifecycleGroupOf`/`verificationGroupOf`/`acceptanceGroupOf` 先例）+ 本族观察者；**压缩只裁前缀，链头不变** ⇒ `decision_attested` 不受影响，且**绝不**计入 `queue_dropped`。
  - **尾部删除/篡改**：链头与锚定头不符 ⇒ `decision_log_truncated` / `decision_log_divergent`。
  ⇒ 二者的判别判据是「**链头是否仍在锚定头上**」，T315 钉死。
- **A5 诚实丢失跨进程化**：`queue_dropped` 必须**持久化**并在读面呈现（今天 `ProvenanceStats` 只在内存，`provenance.go:129-138`，重启即归零——把「丢失」洗成「无丢失」）。`queue_dropped > 0` 的窗口**绝不**报 `decision_attested`。
- **A6 正交与零回归**：P35~P47 判据取值零变化；`internal/protection` 与 `snapshot_anchor.go` 零 diff；P24.2 既有读面 `/decisions`（`mgmt_obs.go:31`）与 `/decisions/export`（`:420`）**响应字节逐字节不变**（新 sink 在 `ProvenanceStore` 角色上**委派**既有环，故 `gate.ProvenanceStore()` 返回的仍是同一个环）；`go.mod`/`go.sum` 零改动。**不新增路由**（第六族并入既有状态面与既有 P46/P47 路由）。
- **A7 非目标**：①不改判定语义与七守卫顺序；②不改 `internal/protection`（A2）；③不改 `snapshot_anchor.go`（A1：载体复用而非加字段）；④不新增路由 / 不改 P24.2 响应字节；⑤不做逐条签名/锚定；⑥不拉取 witness（P46 A7-① 仍成立）；⑦不做跨部署；⑧**不追认启用前的判定**（本 Phase 之前的判定不可断言，且**绝不**从 audit 行反推——见 A8-④）；⑨不做实时流式锚定（只做 per-tick 链头摘要）；⑩不修 P47 A8-⑦ 的 `destructionObserver` 并发缺口。
- **A8 已知代价**：①**自证**：签名者与判定者在同一进程（与全部五族同款，P42/P44 的已知代价同源）——本 Phase **不**声称能防进程内攻击者。②**高频率源的摘要粒度**：判定量远大于生命周期事件（每一次 `Check` 一条），故只做 per-tick 链头摘要 ⇒ 单条判定的可断言性止于「它落在某个已锚定窗口内且其后的链未被改动」，**不是**逐条可验证。③**过载丢失不可与「未发生」区分**：队列满时被逐出的判定在日志里不留痕（只留计数）⇒ 读面必须响亮报 `decision_loss` 且绝不报 `decision_attested`；这是 R24-4（非阻塞）与取证完备性之间的**不可两全**，本 Phase 选择前者并如实登记。④**启用前的判定不可断言**：与 P41 T177 / P42 `verification_absent` 同族；**不从 audit 行反推**（audit 只记拒绝、无链，且并入会造成「两个真相源」）。⑤**判定日志与锚定流都可被删除** ⇒ 两文件双双缺席由 P46 缺席矩阵判 `family_ledger_deleted`；只删主账本由本地重算判 `decision_log_divergent`。⑥**行为变更声明（评审 M5 修正，穷举）**：第六族注册使**三条既有用例的枚举为假**，必须同步扩展——`snapshot_anchor_delivery_test.go:268`（T277 `len(streams) != 5`）、`:486`（T283 断言三条 sweep 流）、**`snapshot_witness_reconcile_test.go:418`（T259 `len(res.Families) != 5`）**；且 `history_export_scheduler.go:1742-1760` 的 `witnessFamilyEnabled` 是 5-case switch（`default false`），**必须新增 case**，否则 P46 GET 探针（`snapshot_witness_reconcile.go:386` `NotEnabled: !witnessFamilyEnabled(...)`）对第六族恒报 `not_enabled=true`，与「生产者一直在跑」自相矛盾。**全部只加枚举数量/新增 case，不改断言强度。** ⑦读面成本：多读一条判定日志 + 一条锚定日志 + 一个持久化计数（管理读面，非热路径）。⑧判定记录的 `principal_hash` 已在 R24-7 秘密边界内（只哈希、无明文、无密钥）⇒ 新族不扩大秘密面。⑨**`ManifestDigest` 语义重载**（A1）是冻结面逼出来的，不是偏好；一旦将来解冻 `snapshot_anchor.go`，应换成专有字段并迁移。

## 5. 测试契约（T297~T316，20 例）

| # | 断言 |
|---|---|
| T297 | 冻结面零 diff（**含 `snapshot_anchor.go`**）+ `internal/protection` 零 diff + `go.mod`/`go.sum` 零改动 + P35~P47 判据取值零变化 + **五族既有锚定条目字节不变**（`manifest_digest` 对它们仍为 `""`） |
| T298 | **P24.2 读面零回归**：`/decisions` 与 `/decisions/export`（json+csv）在启用第六族前后**逐字节相同**；`gate.ProvenanceStore()` 仍返回既有环（`provenance.go:141-150` 语义） |
| T299 | **核心红例（篡改）**：改判定日志中一条已落盘记录 ⇒ `decision_log_divergent` ∧ `decision_attested == false`；**同一用例断言 P46 对账面对此报 `family_intact`**（评审 M1：证明这是新增机制，不是 P46 复用） |
| T300 | **核心红例（尾部删除）**：截断判定日志尾部 k 条 ⇒ `decision_log_truncated` ∧ `decision_attested == false`（判据来源是**本地重算**，不是 P46 窗口——同用例断言 P46 报 `family_intact`） |
| T301 | **P46 复用面（整删）**：删判定日志 + 判定锚定流 ⇒ P46 `family_ledger_deleted`（§3 两半的分界） |
| T302 | **`decision_attested` 成立（非空泛）**：正常 tick + 链完整 + 链头 == 锚定头 + `queue_dropped == 0` ⇒ `true`；且第六族锚定条目确以 `kind="protection_decision"` / `manifest_digest=<链头>` 落盘，`identityID()` 回退行为与 verification 族一致（A1） |
| T303 | **`decision_loss` 压制 attested**：制造队列溢出 ⇒ `queue_dropped > 0` ∧ `decision_attested == false` |
| T304 | **丢失跨进程诚实**：溢出后重启（新建 sink）⇒ `queue_dropped` **不归零**（持久化），绝不把丢失洗成无丢失 |
| T305 | **非阻塞（R24-4）**：scripted 慢/满 sink 下判定延迟与判定结果**不受影响**（admit/reject 与状态码逐字不变） |
| T306 | **判定语义零变化**：七守卫顺序与 403/503/429 分类逐字不变（对照 P21 既有用例） |
| T307 | **投递面复用**：第六族 `pending/anchored/unanchored/converged/swept_by` 取值正确；`swept_by == delivery_sweep` |
| T308 | **分区扩展（A8-⑥）**：`anchorDeliveryStreams()` 为六条、各恰有一个 sweeper、无重复覆盖；`witnessFamilyEnabled` 对第六族返回其启用门（**不再恒 `not_enabled`**） |
| T309 | **投递唯一性**：第六族在 tick 内 attempts 恰 +1（T283 同款，扩到四条 sweep 流） |
| T310 | **对账复用**：第六族走 P46 三段式窗口 + 缺席矩阵（min/max/outside 边界） |
| T311 | **零副作用**：读面不落盘、不写 audit、不发网络 |
| T312 | **fail-closed**：判定日志含不可分类行 / 锚定流冲突 seq ⇒ 对应面整条不 sweep、不落盘、字节零变化、响亮报错，其余五族不受影响 |
| T313 | **`principal_hash` 秘密边界**：新族输出只含哈希与 advisory 字段，无明文/无密钥（R24-7 边界沿用） |
| T314 | **不追认（A7-⑧）**：启用前存在 audit 拒绝行 ⇒ 新族**不**据此生成任何判定记录，读面报 `not_enabled` 而非伪造历史 |
| T315 | **两种「记录变少」的判别（评审 M4）**：合法前缀压缩（链头不变）⇒ `decision_attested` 仍为真且 `queue_dropped == 0`；同构造下改成尾部删除（链头变短）⇒ `decision_log_truncated`。二者必不混淆 |
| T316 | 变异 MU1~MU6（MU1 摘本地重算（⇒ T299 必红）/ MU2 摘 `queue_dropped` 持久化（⇒ T304 必红）/ MU3 把丢失并入 `decision_attested`（⇒ T303 必红）/ MU4 摘 fail-closed（⇒ T312 必红）/ MU5 破分区或漏 `witnessFamilyEnabled` case（⇒ T308 必红）/ MU6 让新 sink 替换而非委派 `ProvenanceStore`（⇒ T298 必红））⇒ 对应用例必红，sha256 还原 |

## 6. 与既有 Phase 的关系

不降级任何判据。P46 的对账词汇与 P47 的投递词汇**被复用**——但**复用范围被显式收窄**（§2 机制边界：P46 只覆盖整删与锚定侧，**不覆盖主账本内容**）。P48 的工程量因此集中在三处：**新源（哈希链判定日志）+ 新 sink（非阻塞）+ 本地重算面**，以及两处注册表 + 一处 enable switch。**P48 补的是它们共同的上游**：五族账本记录的产生都发生在判定面之后，而判定面自身此前不在证据链上。

## 7. 顺路清偿登记债（本 Phase 内）

> P47 §7 登记的 D1~D4（`ADR-057 §8.1` / `§8-8` 四处错误节号 + `ADR-064` I1 措辞过强 + V2 漏列第 ⑥ 项）**已核实全部清偿**（`docs/adr/063-phase44-scope.md:22/:63/:120/:141/:158` 与 `docs/adr/064-phase44-architecture.md:101/:124/:136` 现均为修正后口径）。本轮盘点**未发现新的登记债**；下表是本 Phase 自己产生、必须随实现一并处理的三笔。

| # | 债务 | 处置 |
|---|---|---|
| D5 | `mgmt_obs.go:403-410` 的诚实边界（「NOT a complete forensic history」）在 P48 后**只对内存环成立**；不区分会让读者把「环是快照」误读为「判定面无取证路径」 | 注释改写为「环是保留快照；**判定面的取证历史见第六族 `protection_decision`**」，**保留 `export_completeness` 取值与响应字节不变**（A6 承重承诺） |
| D6 | 三条既有静态枚举用例（`snapshot_anchor_delivery_test.go:268` T277、`:486` T283、`snapshot_witness_reconcile_test.go:418` T259）在第六族注册后必然为假 | 随 A1 同步扩到六条/四条/六族；**只加枚举数量，不改断言强度**（A8-⑥） |
| D7 | `witnessFamilyEnabled`（`history_export_scheduler.go:1742-1760`）是 5-case switch，`default false` ⇒ 第六族会静默恒报 `not_enabled`，与生产者实际在跑矛盾 | 新增第六族 case（返回判定留痕的启用门）；T308 钉死 |

## 8. 证据体系收敛判断

本轮对证据体系做了显式盘点：八个问题面（who/where/when/whether/why-absent/what-accepted/whether-witnessed/whether-delivered）各有可断言面；P47 §7 的四笔登记债已核实清偿；五族账本的承重载体（投递）已由 P47 补齐 5/5。**收敛的只是「问题面」与「载体」——判定面本身仍不在证据链上**（§2 事实 2/3/4/6）。

## 9. 方向候选裁定表（自拍板，R210 尺子）

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① **判定面留痕**（本 Phase） | **采纳** | 真实缺口（§2 事实 2/3/4/6 逐行核实，且 `mgmt_obs.go:404-408` 的注释**自认**不是取证历史）；新判据 `decision_attested`/`decision_log_divergent`/`decision_log_truncated`/`decision_loss` 对**判定面**此前给不出（§2 悖论）；机制复用 P46/P47 已建成的抽象（§3 表），新增面已按 §2 机制边界精确收窄 |
| ② 呈现/运维面整合 | **淘汰（仍）** | 不产生此前给不出的判据（R210；同 P47 §9 裁定） |
| ③ HA 多副本 | **淘汰（仍）** | 「一致性不是证据性」——不产生新可断言判据，只把现有判据复制 N 份（ADR-054 §1.1） |
| ④ 外部时间权威（TSA/RFC3161） | **淘汰（仍）** | 判据类别仍是「该 digest 是否被域外记录」＝ P40 第二实例（ADR-057 §1.1） |
| ⑤ 见证端拉取（P46 的逆操作） | **淘汰** | 不产生新判据：`family_incomplete` 已表达「本地有、域外无」；拉取只是同一判据的第二种取数方式 |
| ⑥ P47 A8-⑦ 的 `destructionObserver` 并发缺口 | **不独立成 Phase** | 是既有不变量的**加固**（既有维度），不产生新判据 ⇒ 被 R210 尺子击倒；且工程量远小于一个 Phase |

⇒ **判断**：存在且**仅存在一个**符合 R210 尺子的候选（①）。其余候选全部被同一把尺子击倒（②③④）或属既有维度加固（⑥）。

## 10. 评审闭合表（第一轮，5 项 major）

| 发现 | 级别 | 闭合 |
|---|---|---|
| **M1** 核心红例 T299/T300 指向主判定日志，但 P46 的 `family_divergent`/`family_ledger_truncated` 只从**锚定流**取值（`snapshot_witness_reconcile.go:175` 的 `LedgerPath` 仅用于存在性；`:244-282` 比对 `digestOfLocalAnchor` 与 witness 投影），故改/截主账本对 P46 不可见 ⇒ 红例无机制支撑 | major | **§2 新增「机制边界」段**（逐行列出 `:175`/`:384` 的存在性用法与 `:244-282` 的比对对象）+ **§3 拆成两半**（P46 复用面 = 整删/锚定侧；新增本地重算面 = 主账本内容）+ **§4 A4 定义哈希链与链头重算** + **T299/T300 重写**并**显式断言 P46 报 `family_intact`** + 变异 MU1 |
| **M2** `decision_attested` 定义不校验判定日志内容与锚定摘要是否一致 ⇒ 「覆盖」退化为「锚定条目存在」（每 tick 必写）⇒ 判据近乎恒真，§2 悖论未被回答 | major | **§3 表把定义写成四合一合取**（①本地重算 ②已 anchored ③`queue_dropped==0` ④无结构性 error），并注明「缺 ① 即退化为恒真」+ **T302 断言非空泛**（含载体字段与 `identityID` 回退）+ 变异 MU1 |
| **M3** per-tick 链头摘要无处存放：`anchorEntry`/`anchorSigned`（`snapshot_anchor.go:100-177`）无通用摘要字段，而该文件属冻结面 | major | **§4 A1 给出载体决策**：复用 `Kind`（新常量定义在新文件）+ `ManifestDigest`（非 omitempty，非 publication 族历史上恒为 `""`——`:1054`/`:1098`/`:1132` 均不设它）；**显式声明语义重载**并列入 A8-⑨；`identityID()`（`:208-217`）回退与 P42 verification 族同先例；T297/T302 钉死 |
| **M4** 主判定日志同时被描述为「append-only」与「有界（逐出最旧）」，`decision_loss` 指向不明 ⇒ 若压缩计入则稳态恒假（与 T302 冲突），若不计入则「诚实计数」不成立 | major | **§4 A4 拆成两种「记录变少」**：合法前缀压缩（`compactLogPrefixGroupsObserved` `appendonly_log.go:140` + `decisionGroupOf` + 观察者；**链头不变**，不计入 `queue_dropped`）vs 尾部删除/篡改（链头不符）；`decision_loss` 只指 `queue_dropped`；**T315 钉死二者判别** |
| **M5** A8-⑥/D6 声称已穷举测试改动但漏了 `snapshot_witness_reconcile_test.go:418`（T259 `len(res.Families) != 5`）；机制必需的 `witnessFamilyEnabled` 新 case（`history_export_scheduler.go:1742-1760`）被漏掉，且被 ADR-072 里一句错误的「Status() 追加第六族行」替代（该行其实由 `snapshot_anchor_delivery.go:353` 遍历 `anchorDeliveryStreams()` 自动产生） | major | **A8-⑥ 改为穷举三条用例** + **新增 D7**（`witnessFamilyEnabled` case，T308 钉死）+ **ADR-072 §1 文件清单改正**（删去「Status() 追加第六族行」，改列 `witnessFamilyEnabled`）+ 变异 MU5 |
