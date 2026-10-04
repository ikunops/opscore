# ADR-069 — Phase 47: Anchor Delivery Reliability（锚定投递可靠性 / 投递面全流化）

- **Status**: Proposed (Phase 47, Scope stage) · Revised per review（评审 5 项 major 全部闭合，见 §10）
- **Phase**: 47（Anchor Delivery Reliability）
- **Base**: Phase 46 CLOSED（HEAD = `9aaab99`）
- **Supersedes**: 无。不修改 P35~P46 的任何冻结判据；P40 的 delivery 状态机语义（pending/anchored/unanchored、attempts 上限、conflict 不落行）逐字沿用；`snapshot_anchor.go` 零 diff。

---

## 1. 分工句（第七问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted / P46 whether-witnessed ⇒
**P47 = whether-delivered（投递义务是否被清偿）**：前七个 Phase 依次问「证据是谁签的 / 放在哪 / 何时 / 是否验证过 / 为何缺席 / 接受了什么 / 域外是否回照」，**没有一个问过「这条锚定投递自己结清了没有」**。P40 建了第一条锚定流，P41/P42/P43/P45 各加一条，共五条；但 crash 恢复面只有两条。P47 把恢复面补齐到五条（每条流恰有一个 sweeper），并把「投递状态」从**不可见**变成**可断言**。

## 2. 新问题（已核对代码，全部带行号）

**事实**：

1. **五条锚定流**：`chain-anchor.jsonl`（`snapshot_anchor.go:39`，路径 `:1550`）、`key-lifecycle-anchor.jsonl`（`:46`）、`verification-anchor.jsonl`（`:49`）、`destruction-anchor.jsonl`（`:52`）、`acceptance-anchor.jsonl`（`:60`）。五条各有独立 seq 空间（共享分类器按 `anchor_seq` 分组，故必须分文件）。
2. **pending 行先于联系见证端落盘（R40-6）**，五条流一致：`anchorDestructionEntry` `:1072-1075`、`anchorAcceptanceEntry` `:1113-1116`、`anchorKeyLifecycleEvent` `:1143-1147`、`anchorVerificationReport`（`snapshot_verification.go:1338-1344`）、publication 流经 `dispatchAnchor`/`dispatchAnchorPath` `:944-948`。
3. **crash 恢复只有两处**（Tick 内）：
   - `chain-anchor` ← `anchorHousekeeping`（`:1154-1181`；`loadAnchorState` 即默认流 `:473-474`；调用点 `history_export_scheduler.go:781`）；
   - `acceptance-anchor` ← `dispatchAcceptancePending`（`snapshot_acceptance.go:698-733`，显式 `loadAnchorStatePath(acceptanceAnchorPath)` `:702-703`；调用点 `history_export_scheduler.go:798`）。
4. **另外三条流只有 inline 派发，无任何持久恢复**：
   - lifecycle：`history_export_scheduler.go:672`（在 `AppendKeyLifecycleEvent` 内）；
   - verification：`snapshot_verification.go:1342`（内联）；
   - destruction：`snapshot_destruction.go:1334` → `destructionConfig.anchorDispatch`（`:1327-1336`）→ `dispatchDestructionPending`（`:849-858`，只吃**当次调用收集到的内存队列**）；Tick 尾的 `drainDestructionAnchorQueue`（`snapshot_acceptance.go:819-831`）drain 的也是**内存**队列。
   ⇒ **crash 落在「pending 行已落盘、状态推进未落盘」之间 ⇒ 该条永久 pending，永不重投**（三条流均无「扫 `pending` 并重投」的路径：非测试代码里按路径加载它们各自锚定流的点只有三处，**无一是重投**——P46 的 digest 探针 `history_export_scheduler.go:1678-1690`（只返回 digest、绝不派发）、`snapshot_anchor.go:543-545`（`nextAnchorSeqPath` 的 seq 分配）、`:628-629`（`compactAnchorPrefixPathObserved` 的压缩判据）；`snapshot_acceptance.go:703` 是 acceptance sweep、`snapshot_witness_reconcile.go:196/389` 是 P46 只读对账）。
5. **且不可见**：五家族各自的 status 组（`keyLifecycleStatusSummary` `snapshot_key_lifecycle.go:816`、`verificationStatusSummary` `snapshot_verification.go:1402`、`destructionStatusSummary` `snapshot_destruction.go:995`、`inputIntegrityStatusSummary` `snapshot_acceptance.go:1033`）**没有任何锚定投递字段**；顶层 `PendingCount`（`history_export_scheduler.go:162`）只由 `refreshAnchorStatus`（`snapshot_anchor.go:1187-1199`）用 `loadAnchorState`（默认 `chain-anchor` 流）填充。P46 的 GET 面（`witnessReconcileLocalView` `snapshot_witness_reconcile.go:360-401`）只报存在性/窗口，也不含 pending。⇒ **三条流（lifecycle/verification/destruction）的投递欠账在本地没有任何表示**：既不自愈，也数不出来。**但 ledger 流不是这样（评审 M2 修正，本 ADR 原稿在此处过度概括）**：ledger 流的 pending/unanchored 本地**已有两处可读面**——顶层 `PendingCount`/`UnanchoredCount`（如上，由 chain-anchor 流填充）与 P40 对账结果的 `PendingIDs`/`UnanchoredIDs`（`snapshot_anchor.go:1272-1273`，在 `len(seq)==0` 早退**之前**用 `st.byID` 填充 `:1320-1332`，经 admin GET 面 `mgmt_obs.go:976` `ReconcileAnchor(nil)` 直接返回）。故本 Phase 的**可见性增量是 3/5 条流**；ledger 流上它的增量只是「与其余四流同形 + attempts 分级 + 每流 owner 可断言」，不是「首次可见」。
6. **后果升级为存储欠账**：`compactAnchorPrefixPathObserved`（`:628-641`）**拒绝逐出未确认组**——`if oldest.State != anchorStateAnchored { return error }`（`:636-639`）。一条 stranded pending 会让该流「超容量且永不可压缩」，欠账随新条目无界增长。
7. **重投在本系统内是安全的、已冻结的**：`classifyAnchorResponse`（`:809-838`）的 duplicate 分支（`:823-825`）把 `409 + reason="duplicate"` 判为 **anchored**（幂等，T129 冻结）；只有 `reason="conflict"`（同 seq 不同 payload）才 refuse-to-record（`:974-978`，ADR-052 A4/A5）。P45 的 acceptance sweep 已经在使用这条 at-least-once 语义。
8. **P46 是镜子不是修复路径**：`family_incomplete`（ADR-068 §3「本地条目 seq 无投影覆盖 ⇒ missing」）把「本地有、域外没有」暴露出来，但 A7-1 明确不做 witness 拉取 ⇒ 它不自愈。**P47 治的正是这面镜子照出来的那一半：本地侧的投递欠账。**

**悖论**：系统已经能断言「域外没有」（P46 `family_incomplete`）、「账本被删」（P46 `family_ledger_deleted`）、「从未验证」（P42）、「为何缺席」（P43）——**唯独不能对 lifecycle/verification/destruction 三条流断言「这条锚定投递已经被清偿」**（ledger 流有 P40 的 `PendingIDs`/`UnanchoredIDs` 可读，见事实 5 修正；其余四流都没有面）。这三条流的 pending 欠账在本地既不可见、也不自愈，而它的代价（不可压缩、无界增长）是即时的。

## 3. 决策（Scope）

新增**锚定投递面**：五条锚定流的投递状态统一成为可读、可断言的字段；同时把恢复面从 **2/5 补齐到 5/5**（每条流恰有一个 sweeper，分区冻结）。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`converged`**（每流 + 全局） | 该流 `pending == 0` ∧ 无 `conflicts` ∧ 无 `error` | 投递状态机**静止**。**≠ 全部投递成功**（`unanchored` 是终态也计入收敛，ADR-052 §6-12）——故必须与 `anchored`/`unanchored` 计数同面呈现（A8-1） |
| **`pending_retryable`** | pending ∧ attempts < max | 尚有重试余量，sweep 会推进它 |
| **`pending_exhausted`** | pending ∧ attempts ≥ max | 与投递状态机**自相矛盾**（`dispatchAnchorPath:993` 在 attempts 达上限时直接落 `unanchored`）⇒ 下一轮 sweep 终态化。读面必须**显式单列**，绝不并入 pending（否则「自相矛盾」被静默） |
| **`swept_by`**（每流一个 owner） | 静态分区 | ledger→`anchor_housekeeping`；acceptance→`acceptance_pending`；key_lifecycle / verification / destruction→`delivery_sweep`。**分区可断言**（ADR-070 I1/T277） |
| **崩溃-重启收敛**（核心红例） | 手工落一条 pending 行（模拟 crash，无派发）⇒ 构造新 scheduler ⇒ Tick | 三条流**此前无任何恢复路径**，该条在 ≤ `anchorMaxAttempts()` 轮内转终态，且 attempts **恰好** +1/轮（单 sweeper，无双派发）——此前给不出的端到端判据。**判据的精确边界（评审 M4 修正）**：「+1/轮」对**本 tick 开始时已 pending** 的条目成立（sweep 只重投这类义务，且它在 tick 尾的 in-tick 生产者**之前**运行，见 ADR-070 I10）；本 tick 内**新产生**的义务归其生产者，sweep 不碰（A8-⑧） |
| **`conflicts`**（每流） | 该流锚定日志存在同 seq 不同 payload 的条目（`loadAnchorStatePath` 记入 `st.conflicts` 并**把该 seq 移出 `latest`**，`:496-505`） | **该流整条不 sweep**（fail-closed，沿 P46 纪律 `snapshot_witness_reconcile.go:208-222`），读面**响亮报错且绝不报 `converged`**——评审 M1/M5 修正：原稿误以为 `loadAnchorStatePath` 会为冲突返回 error，实际它只静默剔除该 seq，不检查就会把「被静默剔除的 seq」呈现为「已收敛」 |
| **压缩活性** | stranded pending 使该流超容量不可压缩（`:636-639`）⇒ sweep 收敛后压缩恢复 | 把「投递欠账 ⇒ 存储欠账」的因果钉死（T288） |

## 4. 能力定义（A1~A8）

- **A1 投递面格式**：`HistoryExportStatus` 追加**末位** `omitempty` 组 `anchor_delivery`（nil 当且仅当 anchoring 关闭 ⇒ 默认部署状态文档逐字节不变，沿 P41/P42/P43/P45 的追加纪律）。每流一行：`{family, present, swept_by, anchored, unanchored, pending, pending_retryable, pending_exhausted, conflicts[], oldest_pending_recorded_at, converged, error?}`；另给全局 `converged`（**当且仅当五流皆 `converged` ∧ 皆无 `conflicts` ∧ 皆无 `error`**）。**不新增路由**（最小闭环：既有 `GET .../export/scheduler` 已承载全部家族状态）。
- **A2 分区（sweeper 所有权）**：五条流各恰有一个 sweeper；分区为**静态表**并冻结：`anchor_housekeeping`（chain-anchor）、`acceptance_pending`（acceptance-anchor）、`delivery_sweep`（其余三条）。**禁止两条 sweep 覆盖同一条流**（P40 起「同一 tick 内绝不重复派发一条」的纪律，`history_export_scheduler.go:779-781`）。**精确表述（评审 M4 修正）**：sweep 只重投「本 tick 开始时已 pending」的义务，且在 tick 尾的 in-tick 生产者（`drainDestructionAnchorQueue`）**之前**运行 ⇒ 对任何 anchor_seq，**同一 Tick 内至多 +1 次 sweep 投递**（T277/T283/T295 以此判别）；本 tick 新产生的义务由其生产者投递，若首投失败则下一 tick 才被 sweep 重投。
- **A3 sweep 语义**：对每条属 `delivery_sweep` 的流，`loadAnchorStatePath(该流路径, dir, s.trust)`（与既有两处 sweep 同款：`anchorHousekeeping` 用 `s.trust`、`dispatchAcceptancePending` 用 `s.trust`）⇒ 逐条 `pending`：`attempts ≥ max` ⇒ 终态化 `unanchored`（与 `anchorHousekeeping:1166-1172`、`dispatchAcceptancePending:712-718` 逐字同款，含 `last_error="attempts exhausted (N)"`）；否则重投。**`unanchored` 永不复活**（ADR-052 §6-12 冻结）。
- **A4 fail-closed（评审 M1/M5 修正）**：**两类**不可用状态都必须整条不 sweep（不落盘、不改字节）+ 响亮报错：①**不可分类行**——`loadAnchorStatePath` 自己返回 error（`:486-489`「skipping is how a tampered prefix disappears」，注释 `:470-471`）；②**冲突 seq**——`loadAnchorStatePath` **不返回 error**：它只把该 seq 记入 `st.conflicts` 并从 `latest` 删除（`:496-505`），故 sweep 与读面**必须显式检查 `len(st.conflicts) > 0`**，否则会把「被静默剔除的 seq」呈现为已收敛（这正是 P46 对账面已有的纪律：`snapshot_witness_reconcile.go:208-222` 判 `unverifiable`，理由是「被删边界会让窗口位移并误判」）。**流间隔离**：一条流的失败不阻断其余四条（T286/T293）。**术语区分（防混淆）**：A4 的「冲突 seq」= **日志冲突**（同一 anchor_seq 出现不同 payload，`st.conflicts`）；与 A7-④/A8-② 的**投递冲突**（witness 返回 `409 reason="conflict"`，`snapshot_anchor.go:974-978`）是两回事——前者是本地日志自相矛盾（整条不 sweep），后者是域外拒收（本地只记 error、条目仍 pending）。
- **A5 读面零副作用**：投递面**只读**（`os.Stat` + 既有 load），不落盘、不写 audit、不发网络、**不派发**（T290）。sweep 只在 Tick 内发生。
- **A6 正交与零回归**：P40~P46 的判据取值零变化；五家族既有 status 组、顶层 `PendingCount`/`AnchoredCount`/`UnanchoredCount`、P46 的 GET/POST 两面**逐字节不变**；`snapshot_anchor.go` 零 diff；`go.mod`/`go.sum` 零改动。**错误状态隔离**：sweep 的错误只进新的 `anchor_delivery` 组（`s.deliveryError`），**不写** `AnchorError`/`destruction.error` 等既有判据面（见 ADR-070 §4）。
- **A7 非目标**：①不拉取 witness（不做 P46 的逆操作）；②不改 `family_incomplete` 语义（它继续只描述「域外缺」）；③不新增路由/不改响应字节；④不修 witness-conflict 语义（`409 reason=conflict` 仍 refuse-to-record，`snapshot_anchor.go:974-978`）；⑤不做跨部署；⑥不新增常驻组件；⑦不修 `destructionObserver` 路径的既有并发缺口（`snapshot_destruction.go:1365-1400` 经 `destructionConfig:1327-1336` 内联派发，**不**受 `destructionDispatchMu` 保护——本 Phase 显式不碰，列为已知代价 7）；⑧不引入重投上限之外的退避/抖动策略（沿用既有 attempts 语义）。
- **A8 已知代价**：①**`converged` ≠ 投递成功**：`unanchored`（放弃投递）也收敛 ⇒ 读面必须同面给出 `anchored`/`unanchored`，否则「放弃」会被误读为「完成」。②**witness-conflict 的 pending 既不可收敛也不可区分**：conflict 路径**不落任何行**（ADR-052 A4/A5），日志里它与普通 pending 完全同形且 attempts 不增长 ⇒ sweep 每轮都会重投它一次（与 `anchorHousekeeping` 今日行为一致），读面只能报 `pending_retryable` + 家族 error 字符串；**P47 不声称它能收敛**。③只治本地投递欠账，**不治见证端丢失**（P46 `family_incomplete` 若源于 witness 保留策略，sweep 不能自愈）。④sweep 的派发与 `dispatchAnchorPath` 是**同义实现**（冻结面无法给该函数加 observer 参数，见 ADR-070 §3），靠差分用例钉住不漂移。⑤**行为变更声明**：lifecycle/verification 此前没有锚定派发互斥，P47 为 sweep 引入 per-stream 派发互斥并包裹生产者调用点——收敛了既有并发暴露面，同时改变了并发时序。⑥读面成本：状态面每次多读五条锚定日志（原已读 3 个家族状态 + chain-anchor）。⑦`destructionObserver` 路径的既有并发缺口不修（A7-7）。⑧**本 tick 新产生义务的重投残差**：三条流的锚定 seq 由冻结面分配（`anchorKeyLifecycleEvent`/`anchorVerificationReport`/`anchorDestructionEntry` 各自 `nextAnchorSeqPath`），生产者调用点**拿不到该 seq**，故无法建立「本 tick 已投递 seq」集合；结论：本 tick 内由**生产者**新建、且 sweep 的加载点之前已落 pending 行的条目，可能在同一 tick 内被 sweep 追加一次尝试（attempts ≤ 2）。**不修的理由与代价边界**：重复投递是幂等的（`409 duplicate` ⇒ `anchored`，事实 7/T129）；收敛上界（≤ max 轮）不受影响；受影响的只是「+1/轮」这一**测试级**断言，A2 已把它精确限定为「本 tick 开始时已 pending」的条目。⑨**新增锁依赖**：lifecycle/verification 的 sweep 持 L1（新 per-stream 派发互斥）期间，其派发引发的压缩会同步调用 `destructionObserver` → `completeDestruction` → `dispatchDestructionPending` → 取 L2（`destructionDispatchMu`）⇒ 存在 L1→L2 的锁依赖（ADR-070 I8 已给出无环证明）；该依赖是 P47 新引入的。

## 5. 测试契约（T276~T295，20 例）

| # | 断言 |
|---|---|
| T276 | 冻结面零 diff + `go.mod`/`go.sum` 零改动 + P40~P46 既有判据取值零变化（零回归） |
| T277 | **分区**：五条流各恰有一个 sweeper，`swept_by` 逐流命名正确；无流被两条 sweep 覆盖 |
| T278 | **核心红例**：destruction 流手工落一条 pending（模拟 crash，无派发）⇒ 新 scheduler + Tick ⇒ 收敛为 `anchored`，attempts 恰为 1 |
| T279 | 同 T278 对 key_lifecycle 流 |
| T280 | 同 T278 对 verification 流 |
| T281 | **有界性**：witness 持续 503 ⇒ 每 Tick attempts+1，第 max 轮转 `unanchored`（终态）；第 max+1 轮不再派发（attempts 不再增长） |
| T282 | **不复活**：`unanchored` 条目永不被 sweep 重投（witness 无新行、attempts 不变，ADR-052 §6-12） |
| T283 | **零双派发**：一条**本 tick 开始时已 pending** 的条目，在一次 Tick 内 attempts 恰好 +1（不是 +2）——与既有两处 sweep 的分区不重叠（五流各测）；本 tick 新产生义务的边界由 T294/T295 判别 |
| T284 | **可见性**：三条流各落一条 pending ⇒ `anchor_delivery.families[*].pending == 1`、`converged == false`；同时五家族既有 status 组与顶层计数**逐字节不变** |
| T285 | 收敛后 `converged == true` ∧ `pending == 0`，且 `anchored`/`unanchored` 如实（A8-1：`unanchored` 也收敛） |
| T286 | **fail-closed + 隔离**：某流锚定日志含不可分类行 ⇒ 该流**不被 sweep**（字节零变化）且错误响亮；其余四条不受影响 |
| T287 | **矛盾 pending**（手工写 pending ∧ attempts ≥ max）⇒ 按既有纪律终态化 `unanchored`（与 `anchorHousekeeping` 同款），**绝不**留在 pending；读面在 sweep 前把该态单列为 `pending_exhausted`（不与 pending 混淆） |
| T288 | **压缩活性**：stranded pending 使 destruction 锚定流超容量不可压缩（`:636-639`）⇒ sweep 收敛后压缩恢复（组数回落 ≤ capacity） |
| T289 | **序列化**：sweep 与生产者并发派发同一流 ⇒ 每 seq 仅一条终态行、无冲突 seq、无 fork（新 per-stream 派发互斥生效） |
| T290 | **读面零副作用**：调用状态面不改任何文件字节、不写 audit、不发网络（witness 文件零变化） |
| T291 | **差分**：同输入 + 同 scripted witness 响应下，sweep 落的状态推进行与生产者路径落行**逐字节相同**（A8-4 的防漂移判据） |
| T292 | 变异 MU1~MU5（MU1 摘 sweep 调用 / MU2 摘不可分类行的 fail-closed（对不可读流仍 sweep）/ MU3 摘 `unanchored` 不复活 / MU4 破分区（两处 sweep 同流）/ MU5 摘 `st.conflicts` 检查（⇒ T293 必红）⇒ 对应用例必红，sha256 还原）。**编号用 MU 以避开本轮评审发现的 M1~M5** |
| T293 | **冲突 seq fail-closed（评审 M1/M5，新增）**：某流锚定日志含同 seq 不同 payload 的条目 ⇒ 该流**整条不被 sweep**（文件字节零变化）∧ 读面 `conflicts` 非空 ∧ `error` 响亮 ∧ `converged == false`（全局亦然）；与 T286 的「不可分类行」路径判别开 |
| T294 | **本 tick 新产生义务的残差边界（评审 M4，新增）**：本 tick 内由生产者新建且首投失败（503）的条目，**本 tick 内 attempts ≤ 2**（sweep 至多追加一次），下一 tick 起 +1/轮，且 ≤ `anchorMaxAttempts()` 轮内达终态（A8-⑧） |
| T295 | **顺序判别（评审 M4，新增）**：`drainDestructionAnchorQueue` 在本 tick 新建的 destruction 条目，**本 tick 内 attempts 恰好 = 1**——sweep 在 drain **之前**运行故不重投本 tick 新产生的义务；把 sweep 挪到 drain 之后（原稿设计）该断言必红 |

## 6. 与既有 Phase 的关系

不降级任何判据。P40~P46 的判据取值零变化；本 Phase 补的是它们共同的**承重前提**：五条流作为「域外可检测」的载体，其投递义务此前只有两条被保证会被清偿。P46 的 `family_incomplete` 与 P47 的 `pending_retryable` 是**同一因果链的两端**（本地欠账 → 域外缺），P47 修前者、不碰后者的语义。

## 7. 顺路清偿登记债（本 Phase 内，随 kickoff 提交一并清偿）

| # | 债务 | 处置 |
|---|---|---|
| D1 | `docs/adr/063-phase44-scope.md` 四处引「ADR-057 §8.1」（:22、:45、:63、:120）——ADR-057 的 §8 是「三处取舍（Q1~Q3）」，**不存在 §8.1**；该句原文在 **ADR-057 §6 已知代价 1**（`docs/adr/057-phase42-scope.md:281`，本轮已核对） | 四处节号改为「ADR-057 §6 已知代价 1」（:45 处保留其 `:281` 行号引用） |
| D2 | `docs/adr/064-phase44-architecture.md` I1 措辞（:136）称「三信任锚两两不相交」，**强于实际执行面**：V2 的扫描只遍历 `verifierTrust.keys`（`history_export_scheduler.go:524-535`），故「非 VAK、非 KAK、非 signer 的第三方密钥同时出现在 manifest 锚与 KAK 锚」**不被拒**（本轮已核对：V2 全部检查均命中不了此情形） | I1 措辞对齐实际执行面：改为「verifier 锚与其余两锚逐 key 不相交（`:524-535`）+ 显式成员检查 ②③④⑤（`:504-523`）+ 三私钥两两不同（`:401`/`:485`/`:488`）」，并显式登记 manifest∩KAK 残差 |
| D3 | `docs/adr/064-phase44-architecture.md` §5 的 V2 行（:101）只列 ①~⑤，**漏了实现里真实存在的第 ⑥ 项**：`verifierTrust.keys` 逐 key 扫描 × `kakTrust`（`history_export_scheduler.go:530-535`，代码注释 `:491-502` 自称「①~⑤ 单独会漏掉的残差」） | V2 行补 ⑥ 并与代码注释对齐（同时补全 ①~⑤ 的行号） |
| D4 | **本轮新核实的同族缺陷（不在登记清单内，一并清偿并显式声明）**：`docs/adr/063-phase44-scope.md:141`、`:158` 与 `docs/adr/064-phase44-architecture.md:124` 三处引「ADR-057 §8-8」——ADR-057 的 §8 只有 Q1~Q3，**不存在 §8-8**；所指对象（A7-11「不做 verification 家族的 witness 侧对账」）实际在 **ADR-057 §4 A7 第 11 条**（`docs/adr/057-phase42-scope.md:237`，本轮已核对） | 三处改为「ADR-057 §4 A7-11」 |

## 8. 证据体系收敛判断（用户明示偏好：战线收敛）

七个 Phase 的**问题面**已经闭合（who/where/when/whether/why-absent/what-accepted/whether-witnessed 各有一面可断言），本轮对**证据主线的真实缺口**做了显式核对：

- 未发现「产生新可断言判据」的第八个问题面缺口——第八问若强行开（例如运行时判定面留痕，候选②），不是缺口的补完而是**新维度**：它要新建签名/账本/锚定家族并重立威胁模型（≥1 Phase），且与「投递义务是否清偿」这一承重前提无关（候选②裁定：**不淘汰，但延后**；理由见下）。
- 候选③（呈现面整合）**被 R210 击倒**：它不产生此前给不出的判据，只是把既有判据换一种画法。
- 候选①**通过了 R210 尺子**（评审 M2 修正后的精确版）：它产出的是此前**根本给不出**的判据——对 **lifecycle/verification/destruction 三条流**，「这条锚定投递已被清偿」在本地无任何面可引用（§2 事实 5：四个家族 status 组无任何投递字段、P46 GET 只报存在性/窗口），且这三条流连 pending 都数不出来。**不夸大**：ledger 流此前已有 `PendingCount`/`UnanchoredIDs` 等面（事实 5 修正），故对 ledger 流的增量是「与其余四流同形 + attempts 分级 + owner 可断言」，不是首次可见。

⇒ **判断**：证据主线的**问题面**已收敛，剩余的是**承重面**（载体自身的可靠性）。P47 选择最小闭环——**不加新判据维度、不加路由、不改响应字节**，只把既有五条流的投递状态补齐为可断言面 + 把恢复面从 2/5 补到 5/5，并顺路清偿 D1~D4 四笔登记债（D4 为本轮新核实的同族缺陷，已显式声明）。**候选②延后**：它是合法的新 Phase 候选，但需独立立项与独立威胁模型，本轮不并入。

## 9. 方向候选裁定表（自拍板，R210 尺子）

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① Anchor Delivery Reliability | **采纳** | 真实缺口（§2 事实 4/5/6 逐行核实）；新判据（`converged`/`pending_retryable`/`pending_exhausted`/`conflicts`/`swept_by`/崩溃-重启收敛/压缩活性）对 **3/5 条流**此前给不出（ledger 流已有面，见 §2 事实 5 修正）；机制复用已冻结的 at-least-once 语义（§2 事实 7，T129）；工程量小而确定 |
| ② 运行时判定面留痕（protection kill/decision → 证据体系） | **延后（不淘汰）** | 判定面已有 provenance 面（`Gate.emitDecision` `internal/protection/gate.go:324-341`、`ProvenanceStore` `:347-351`），把它接进**签名证据体系**是新维度（新家族 + 新威胁模型），≥1 Phase，且不属承重面收敛 |
| ③ 呈现面整合 | **淘汰** | 不产生此前给不出的判据（R210） |

## 10. 评审闭合表（双镜头合并，本轮）

| 发现 | 级别 | 闭合 |
|---|---|---|
| **M1/M5（同根因，合并）** A4/I3 声称「冲突 seq ⇒ 整条不 sweep」并引 `:471-478`，但 `loadAnchorStatePath` 对冲突**不返回 error**（只记 `st.conflicts` 并从 `latest` 删除该 seq，`snapshot_anchor.go:496-505`）⇒ 含冲突的流会被照常 sweep，读面可报 `converged=true ∧ error=""`，把被静默剔除的 seq 呈现为已收敛 | major | **A4 重造**：拆成两类——①不可分类行（load 自己返回 error，`:486-489`）；②冲突 seq（**必须显式检查 `len(st.conflicts)>0`**，纪律取自 P46 `snapshot_witness_reconcile.go:208-222`）。A1 加 `conflicts[]` 字段；全局 `converged` 追加「皆无 conflicts」；§3 表新增 `conflicts` 判据行；新增 **T293**；变异 **MU5** |
| **M2** §9/R210 论证的绝对句「『这条锚定投递已被清偿』在本地无任何面可引用」为假：ledger 流已有顶层 `PendingCount/UnanchoredCount` 与 P40 对账的 `PendingIDs/UnanchoredIDs`（admin GET） | major | **事实 5 修正**（显式列出这两处面与行号 `snapshot_anchor.go:1272-1273`/`:1320-1332`、`mgmt_obs.go:976`）＋**§2 悖论收窄**为「三条流」＋**§8/§9 收窄**为「可见性增量 3/5 条流；ledger 流的增量只是同形化 + attempts 分级」 |
| **M3** I8 把 `destructionDispatchMu` 列为 per-stream 锁之一，又断言「绝不嵌套两个 per-stream 锁」；但 lifecycle/verification 的 sweep 持 L1 期间的压缩会同步走 `destructionObserver → completeDestruction → dispatchDestructionPending → destructionDispatchMu`，必然嵌套；且 I8 的锁序漏掉 `destructionDispatchMu` | major | **I8 重写**：给出完整偏序 L1（keyLifecycle/verification dispatchMu）→ L2（`destructionDispatchMu`）→ L3（`destructionWriteMu`）→ L4（`s.mu`）＋无环证明（不存在 L2→L1 路径）＋允许且仅允许 L1→L2 的嵌套；**A8-⑨** 登记该新锁依赖；§3 伪码注释同步改正 |
| **M4** sweep 接在 `drainDestructionAnchorQueue`（`:799`）之后 ⇒ 同一 Tick 内 destruction 条目被派发两次（drain 先投一次，sweep 再把该 pending 重投），attempts 1→2，违反 A2/§3/I1/T283 | major | **I10 重排**：sweep 移到 `dispatchAcceptancePending`（`:798`）与 `drainDestructionAnchorQueue`（`:799`）**之间**（in-tick 生产者之前）；**A2/§3 精确化**为「本 tick 开始时已 pending 的义务 +1/轮」；新增 **T295**（顺序判别）；**A8-⑧** 显式登记「本 tick 新产生义务」的残差与其边界（seq 由冻结面分配，生产者拿不到，无法建 per-seq tick 集合；重复投递幂等，收敛上界不受影响）＋ **T294** |

**修订面**：§2 事实 5/悖论、§3 表、A1/A2/A4/A8、§5（+T293/T294/T295）、§8、§9、§10 为本轮修订面；§1、§2 事实 1~4/6~8、A3/A5/A6/A7、§6、§7 未动（事实 4 的括注与事实 7 的行号在上一提交已修正）。
