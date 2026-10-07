# ADR-073 — Phase 49: Anchor Realization（锚定兑现 / 锚定条目所声称的证据是否真的兑现）

- **Status**: Proposed (Phase 49, Scope stage)
- **Phase**: 49（Anchor Realization）
- **Base**: Phase 48 CLOSED（HEAD = `45a580e`）
- **Supersedes**: 无。不修改 P35~P48 的任何冻结判据；锚定条目结构（`anchorEntry`/`anchorSigned`）、P37 验签决定序、P46 对账词汇与三段式窗口、P47 投递词汇、P43 销毁词汇**逐字沿用**；`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `appendonly_log.go` / 冻结五文件 **零 diff**；`internal/protection` 零 diff；既有读面响应字节不变。

---

## 1. 分工句（第十问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted / P46 whether-witnessed / P47 whether-delivered / P48 whether-recorded ⇒
**P49 = whether realized（锚定条目所声称锚定的那条证据，是否真的在主账本中兑现）**：前九个 Phase 依次问「证据是谁签的 / 放在哪 / 何时 / 是否验证过 / 为何缺席 / 接受了什么 / 域外副本是否一致 / 投递义务是否清偿 / 判定本身是否被如实留痕」。**九个问题的对象都是「证据产物」或「载体自身」——没有一个问过：载体对证据的那句声明，是不是真的。**

## 2. 新问题（已核对代码，全部带行号）

**事实**：

1. **锚定条目是一句断言，不是一份凭据**：每条锚定条目携带它「所锚定证据」的**身份 + 摘要**，来源是主账本记录的**直拷或重算**：
   - lifecycle：`snapshot_anchor.go:1131-1132` `EventSeq: e.EventSeq` / `EventDigest: e.EventDigest`（`e` 是 KAK 签名的账本行）
   - destruction：`:1055-1056` `DestructionSeq: e.DestructionSeq` / `DestructionDigest: e.EntryDigest`
   - acceptance：`:1098-1100` `AcceptanceSeq: e.EntrySeq` / `AcceptanceDigest: e.EntryDigest` / `AcceptanceRecordSeq: e.RecordSeq`
   - verification：`snapshot_verification.go:1330` `dg, derr := reportDigest(r)` → `:1337` `ReportDigest: dg`（`reportDigest` 定义 `:277-283`）
   - publication：`snapshot_anchor.go:1561-1572` `entryForAnchor` → `ManifestDigest = ledgerDigestOf(m)`
2. **锚定条目的验签只回答「谁签的」，不回答「签的是什么」**：`verifyAnchorEntrySignature`（`snapshot_anchor.go:379-411`）只做 P37 决定序 + stream 绑定（`:395-401`）+ Ed25519 验签（`:407-409`）；`loadAnchorStatePath`（`:480-536`）只做分类 / 冲突（`:500-512`）/ 验签（`:522-535`）。**全仓对锚定条目摘要字段的消费只有「写入」没有「核对」**。本轮亲跑两条命令，**逐行如实列出**（评审 M3 修正：原文只报了其中两行，属证据陈述不实）：

- `grep -rn "ReportDigest\|EventDigest\|DestructionDigest\|AcceptanceDigest" internal/controlplane/server/*.go | grep -v _test | grep -v snapshot_anchor.go` ⇒ **16 行**。逐行归类：`snapshot_verification.go:1337` 是**锚定条目的写入**（`:1330` 的 `reportDigest(r)`）；`mgmt_obs.go:1178` 读的是 **`keyLifecycleEntry` 账本行**的 `EventDigest`；其余 **14 行**（`snapshot_key_lifecycle.go:109/110/127/142/157/158/357/358/368/380/704/723/725/729`）**全部是 `keyLifecycleEntry` 自己的账本字段**（结构体定义 / 规范序列化 / load 自校验 / 链头推进）——**与锚定条目无关**。
- `grep -rn "\.ReportDigest\|\.DestructionDigest\|\.AcceptanceDigest" internal/controlplane/server/*.go | grep -v _test` ⇒ **恰 3 行**，全部在 `snapshot_anchor.go:193/196/201` 的 `anchorSignedFields`（**签名覆盖区的规范 payload 构造**）。**没有任何一处把它们与账本记录比对。**

⇒ 结论（不因 M3 而变，但表述精确化）：锚定条目的 `ReportDigest`/`DestructionDigest`/`AcceptanceDigest` **全仓只被签名路径读、从无核对**；`ManifestDigest`/`EventDigest` 的读者只有事实 5/6 那两处，且都不是核对。
3. **P46 的对账看不到主账本内容**：`snapshot_witness_reconcile.go:175` 的 `ledgerAbsent := !witnessFilePresent(f.LedgerPath(dir))` 与 `:384` 同为**存在性探针**；`:244-282` 的三段式窗口比对的是 `digestOfLocalAnchor(&e)`（**本地锚定条目**）与 `items[i].AnchorDigest`（**witness 投影**）。⇒ **P46 是「锚定条目 ↔ 域外副本」，不是「锚定条目 ↔ 证据产物」**。（与 P48 ADR-071 §2 评审 M1 逐字同源——P48 只把这条盲区在**判定族**上补了。）
4. **P47 的投递面与内容无关**：`snapshot_anchor_delivery.go:93-148` 的分区表与 `:386` 的状态面（`:349` 的 `Converged` 字段）只处理 `state/attempts/conflicts/converged/swept_by`——一条内容为假的锚定条目，只要被派发成功，`converged` 照真。
5. **P43 的销毁面对锚定条目只读 publication 族，且把它的声明当真**：`snapshot_destruction.go:1251-1266` 的 `knownPublications` 遍历 `as.latest`，`e.Kind != anchorKindPublication` 即跳过（`:1256-1258`），且 `out[id] = knownPublication{id: id, digest: e.ManifestDigest}`（`:1263`）——**锚定条目的摘要被采信为「曾存在过的出版物的摘要」，从未被核对**。
6. **唯一存在的「摘要 ↔ 账本」比对是「磁盘清单 ↔ 账本」，不是「锚定 ↔ 账本」**：`history_export_manifest.go:633-640` 的 `chainSources` 比的是 `ledgerDigestOf(g.manifest)`（磁盘 manifest）与 `ledgerView.usable[...].ManifestDigest`（账本行）⇒ 锚定条目不在其中。
7. **P46 对下界以下是沉默的**：`snapshot_witness_reconcile.go:245-248` 注释原文「Below the lower bound: legal prefix compaction and deletion are indistinguishable (R40-1) — no assertion, no waiver, silence」；`:268-273` 对**上界以上**判 truncated（R40-6 保证 local max ≥ witness max）。⇒ P46 的窗口是**锚定 seq 轴**、比对对象是**域外投影**；**没有任何面**在**证据身份轴**上做同样的三段式判断。
8. **A-3 是登记在册、跨四个 Phase 无人认领的逃逸通道**：`docs/adr/064-phase44-architecture.md:124` 原文「伪造验证**锚定条目**（`kind=verification`，`Overall=attested` + 任意 `ReportDigest`）……本地对 anchor log 的验签走 manifest 信任锚……⇒ **本地与域外均不可检测**」，处置「**列 Phase 45 候选（与 C2 并案评估）**」；`docs/adr/063-phase44-scope.md:158` 同款（已知代价 7）。**P45 选了 C2（输入面完整性）**（`docs/adr/065-phase45-scope.md:17`）；**P46/P47/P48 的 ADR 全文对 `A-3` 零命中**（本轮亲跑 grep）⇒ A-3 至今无人认领。
9. **为什么它今天全绿**：持导出私钥者写一条**合法签名**的锚定条目（`snapshot_verification.go:1332` 用 `s.signer`）并照常 `dispatchAnchorPath`（`:1345`）⇒ 见证端也收到同一条 ⇒ P37 验签 OK（`loadAnchorStatePath:522-535`）、P46 `family_intact`（`:244-282` 两侧摘要一致）、P47 `converged`（`:349`/`:386`）、P42 面读的是**报告**（`verificationState.entries`）而不是锚定条目、P43 面只看 publication 族 ⇒ **每一个面都绿**。

**悖论**：系统能断言「域外副本与本地载体一致」（P46）、「载体把这条证据投递出去了」（P47）、「判定被如实留痕」（P48）——**唯独不能断言「载体所声称锚定的那条证据，真的存在」**。锚定条目是**断言**，从来不是**凭据**；今天它与主账本的矛盾在本地与域外**都没有任何面可引用**。

## 3. 决策（Scope）

新增**锚定兑现面**——**不新增证据族、不新增写入路径、不新增路由、不新增常驻组件**，只新增一个**读派生判据**：把每条锚定条目的声明拿去**在它自己的主账本里兑现**。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`anchor_realized`** | 该族锚定窗口内**每一条可用（验签通过）锚定条目**所声称的证据都被该族**主账本**中的一条记录兑现（身份相同 ∧ 该族声明的全部内容字段相同 ∧ 摘要按该族**规范摘要函数重算**后逐字节相等）；**或**其缺席被一条**已完成**的 P43 销毁记录记账（`kind` = 该族 compaction kind ∧ `from_seq ≤ 身份 ≤ to_seq`）；且该族非 indeterminate ∧ 无 unusable 条目 | **载体所声称锚定的证据真实存在**——此前给不出的判据（§2 悖论） |
| **`anchor_unrealized`** | 存在一条可用锚定条目，其声称的证据在**主账本保留窗口内**找不到兑现（窗口内无该身份的记录 ∧ 无摘要相符的记录 ∧ 身份高于窗口上界），且不被任何已完成的销毁记录记账 | 锚定条目是**无凭据的断言**：载体说某证据存在，而它不存在、也没人记账。**P46/P47/P43 对此全绿** |
| **`anchor_realization_indeterminate`** | 该族锚定流含不可分类行 / 冲突 seq（`loadAnchorStatePath:500-512`）/ **含验签不通过的条目（unusable）**，或该族主账本 load 失败 / 含不可分类行 / 冲突 seq | **fail-closed**：该族**整条不判**、响亮报错、**绝不**报 `anchor_realized`；**族间隔离**（其余族不受影响）。**unusable 条目必须有落点**（评审 M2）：不得因「不可用就不看」而变绿 |
| **`anchor_compacted`** | 条目身份**低于**主账本保留下界，且被一条已完成的该族销毁记录覆盖 | 前缀被**合法**裁掉（P43 记账）——**不是** unrealized |
| **`anchor_out_of_window`** | 条目身份**低于**主账本保留下界，且**无**销毁记录覆盖（销毁面未启用 / 未记账）；**或**该族**自己的**账本源未启用（根本没有窗口可比，**第二轮评审 M2**） | **不可判**，如实报出（沿用 P46 `:245-248` 的「no assertion, no waiver, silence」纪律），**绝不**算 realized 也**绝不**算 unrealized |
| **`anchor_nothing_assessed`**（族级） | 该族**没有任何一行**被判为 realized，且也无 unrealized/indeterminate（行全为 `anchor_compacted`/`anchor_out_of_window`，**或**该族锚定窗口为空） | **本族在本次读中没有一条声明被实际核对**——如实报出，**绝不**报 realized（空集不构成「全部兑现」） |
| **`anchor_realization_delegated`**（族级） | 该族 = `protection_decision`（P48 哈希链，无签名；其兑现判据由 P48 本地重算产出，本 Phase 不重复，T328） | 本面对该族**不产出** `anchor_realized`/`anchor_unrealized`；**闭表纪律**：注册表恰六行，新增族必须同时决定兑现归属，不得静默漏判 |
| `family_intact` / `family_incomplete` / `family_ledger_deleted` / `family_ledger_truncated` / `family_divergent` | **逐字复用 P46** | 不变 |
| `pending` / `anchored` / `unanchored` / `pending_retryable` / `converged` / `swept_by` | **逐字复用 P47** | 不变 |
| `accounted` / `unaccounted_disappearance` / `destruction_*` | **逐字复用 P43** | 不变（本 Phase 只**读**销毁记录做记账匹配） |

**族级与全局取值的权威定义（Scope 为准；Architecture 不得另立，评审 M1/M2 闭合）**：

- **行级**（每条锚定条目，5 值）：`realized` / `compacted` / `out_of_window` / `unrealized` / `indeterminate`。
- **族级**（每族一个值，**全序首个命中**）：① 任一行 `indeterminate` ⇒ `indeterminate`；② 否则任一行 `unrealized` ⇒ `unrealized`；③ 否则**至少一行** `realized` ⇒ `realized`；④ 否则 ⇒ `anchor_nothing_assessed`。`protection_decision` 族恒为 `anchor_realization_delegated`。
- **全局** `anchor_realization_state`（**标量**，全序首个命中，**不是合取**）：① 任一**非委派**族 `indeterminate` ⇒ `indeterminate`；② 否则任一非委派族 `unrealized` ⇒ `unrealized`；③ 否则任一非委派族 `realized` ⇒ `realized`；④ 否则（全部非委派族皆 `anchor_nothing_assessed`）⇒ `nothing_assessed`。**`delegated` 族不参与以上任何一步。**
- `anchor_realized`（bool）**只是派生便捷量**：`anchor_realized := (anchor_realization_state == "realized")`——**不得另行定义**。⇒「空集为真」在本定义下**不可能**（第 ④ 步给出 `nothing_assessed`，而不是 `realized`）。
- **命名边界（消歧）**：**行级/族级**取值不加 `anchor_` 前缀（`realized` / `unrealized` / `indeterminate` / `compacted` / `out_of_window` / `nothing_assessed` / `delegated`）；带 `anchor_` 前缀的 `anchor_realized` / `anchor_realization_state` / `anchor_realization_claims` 是**全局字段**。上表 `anchor_realized` 一行的条件是**该族**的 realized 条件；**全局**语义以本段第 ③④ 条为准。
- `anchor_realization_claims`（int）= 各非委派族 `checked` 之和 = **本次读实际核对了多少条声明**——非空泛性的见证量；`anchor_realized == true` **蕴含** `claims > 0`。
- 三态判别、区间记账匹配、fail-closed 与零副作用的机制细节见 **ADR-074 §3**。

**核心红例（此前给不出的端到端判据）**：持导出私钥者向 `verification-anchor.jsonl` 追加一条 `kind=verification` / `Overall=attested` / `ReportDigest=<任意>` 的锚定条目（导出钥签名合法）并照常派发 ⇒ 今天：P37 验签 OK、P46 `family_intact`、P47 `converged`、P43/P42 面无异常 —— **全绿**；本 Phase 后：`anchor_unrealized` ∧ `anchor_realized == false`，**同一用例显式断言 P46 仍报 `family_intact` ∧ P47 仍报 `converged`**（证明这是新增机制，不是复用）。**这条同时闭合 A-3 的「持导出私钥」分支**（该对手造不出 VAK 签名的验证账本行来兑现它）。

**各族检测力必须分别声明（承重，不可省略）**：

| 族 | 证据产物的签名权威 | 本 Phase 对该族的检测力 |
|---|---|---|
| `key_lifecycle` | **KAK**（`snapshot_key_lifecycle.go:235` `verifyKeyLifecycleEntrySignature`） | **非空泛**：导出私钥造不出 KAK 签名的账本行 ⇒ 伪造的锚定条目必被兑现面抓到 |
| `destruction` | **KAK**（`snapshot_destruction.go:291` `verifyDestructionEntrySignature`） | **非空泛** |
| `acceptance` | **KAK**（`snapshot_acceptance.go:227` `verifyAcceptanceEntrySignature`） | **非空泛** |
| `verification` | **VAK**（`snapshot_verification.go:757` `verifyVerificationEntrySignatureIn`，独立模式走 `verifierTrust`） | **非空泛**；但兑现需从账本行**重算 `reportDigest`**（`:277-283`，7 字段）——**不是** `verificationEntryDigest`（`:345-350`，11 字段） |
| `ledger`（publication） | **导出签名密钥**（`snapshot_ledger.go:83-90` 注释原文「same key as manifests」） | **空泛**：持导出私钥者两侧同钥，可同时伪造账本行与锚定条目 ⇒ **如实声明**；该族的保护来自 P38/P39 链 + P40 对账，**不是**本 Phase |
| `protection_decision` | 无签名（P48 哈希链） | **P48 的本地重算已覆盖**（`snapshot_decision_attest.go:723-752` 锚定链头 + ADR-072 §3.3 重走链）⇒ 逐字复用，本 Phase **不重复** |

⇒ 本 Phase 对 **4/6 族**（lifecycle / destruction / verification / acceptance）新增此前给不出的检测力；1 族（ledger）**空泛并显式声明**；1 族（decision）已由 P48 覆盖。**不夸大。**

**R210 尺子（§9 详述）**：本条产出的是**此前根本给不出**的判据——今天没有任何面能把锚定条目的声明与它所声称的证据产物比对（§2 事实 1~7 逐行核实；事实 8 是**登记在册、未认领**的 A-3）。它不是复述（P46 的对象是**域外副本**、P47 的对象是**投递义务**、P43 的对象是**消失是否记账**、P48 的对象是**判定**——四者的对象都不是「锚定条目 ↔ 主账本」），不是加固（不是把既有判据再签一遍），不是呈现（不是换一种画法）。它是 P48 ADR-071 §2 评审 M1 揭示的**「P46 不读主账本内容」**这一盲区，在**其余五族**上的补完。

## 4. 能力定义（A1~A8）

- **A1 兑现面的载体（读派生，零新族、零新写入路径）**：per-family 需要四件东西——(a) 锚定流路径、(b) 主账本路径、(c) 该族**从账本记录重算摘要**的函数、(d) 该族的 **compaction kind**。既有 `witnessFamily`（`history_export_scheduler.go:1648-1665`）已给 (a)(b)；**新增两个字段** (c)(d)（`history_export_scheduler.go` **不在冻结面**，改它合法）。各族取数面**全部已存在**（只读调用）：`loadAnchorStatePath`（`snapshot_anchor.go:480`）、`loadLedgerState`（`snapshot_ledger.go:153`，`usable map[int64]ledgerEntry` `:139`）、`loadKeyLifecycleState`（`snapshot_key_lifecycle.go:326`，`entries []keyLifecycleEntry` `:306`）、`loadDestructionState`（`snapshot_destruction.go:412`，`bySeq map[int64]*destructionGroup` `:385`）、`loadVerificationState`（`snapshot_verification.go:619`，`entries []verificationLogEntry` `:587`）、`loadAcceptanceState`（`snapshot_acceptance.go:307`，`entries []acceptanceEntry` `:287`）。
- **A2 零触碰（承重承诺）**：`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `appendonly_log.go` / 冻结五文件 **零 diff**；`internal/protection` 零 diff；**不新增证据族、不新增路由、不新增常驻组件**；P35~P48 判据取值零变化；既有读面响应字节不变（新字段全 `omitempty`，兑现面未启用时整组缺席 ⇒ 默认部署状态文档**逐字节不变**）。
- **A3 零副作用**：兑现面**严格只读**——`os.Stat` + 既有 load + 读销毁账本；不落盘、不写 audit、不发网络、不派发、不压缩（P47 I4 / P46 对账零副作用纪律沿用）。
- **A4 两种「记录变少」的判别（承重）**：本 Phase 的判据必须把**合法前缀压缩**与**尾部删除/伪造**分开，否则合法压缩会被读成伪造：
  - **合法前缀压缩**（P43 已记账）⇒ 身份低于主账本保留下界 ∧ 有已完成的该族销毁记录覆盖 ⇒ `anchor_compacted`（**不是** unrealized）。
  - **尾部删除**（账本尾部被删，锚定条目仍在）⇒ 身份**高于**账本窗口上界 ⇒ `anchor_unrealized`。
  - 判别支点 = **身份是否落在主账本的保留窗口内 ∧ 是否有已完成的销毁记录覆盖**（与 P48 I6 的「链头是否仍在锚定头上」同款纪律，对象从「链头」换成「账本窗口 + P43 记账」）。
- **A5 不可判必须响亮**：`anchor_out_of_window`（销毁面未启用时的前缀）与 `anchor_realization_indeterminate` 都**绝不**计入 realized；读面必须同面给出**计数与原因**（沿用 P47 `last_unanchored_reason` 的「为何」纪律，不用粘滞字段）。
- **A6 正交与零回归**：P35~P48 判据取值零变化；冻结面零 diff（T317）；默认部署（兑现面未启用）状态文档逐字节不变（T318）；`go.mod`/`go.sum` 零改动；**注册表族数不变**（6 族 6 流，T331）。
- **A7 非目标**：①不改锚定语义与锚定条目结构（`anchorEntry`/`anchorSigned` 零字段变更）；②不改任何主账本（零 diff）；③不新增路由 / 不改响应字节；④不做跨部署；⑤不拉取 witness（P46 A7-① 仍成立）；⑥**不新增证据族**；⑦**不做逐条实时核对**（只在管理读面派生，不触碰热路径）；⑧**不闭合 A-3 的「持 VAK/KAK 私钥」分支**（能伪造证据产物本身者必然能兑现它——同 P43 逃逸 A-2 / P41 T177 家族，如实登记）；⑨不修 P47 A8-⑦ 的 `destructionObserver` 并发缺口；⑩不改 P43 的 `knownPublications`（`snapshot_destruction.go:1251-1266` 把锚定条目当真——本 Phase 只**新增**核对面，不改既有采信路径；该采信路径的加固列为非目标并登记）；⑪不做「证据内容为真」的断言（那是 P42/P45 的维度）。
- **A8 已知代价**：
  ① **`ledger` 族空泛**（同钥，§3 表）——如实声明，不夸大；
  ② **依赖销毁面启用**：销毁面关闭时前缀缺席只能报 `anchor_out_of_window`（不可判）；本 Phase **不**因此判 unrealized（否则合法压缩会被读成伪造）；
  ③ **保留窗口不对称**：`--export-anchor-capacity`（默认 4096，`cmd/opscore/main.go:503`）与 `--export-ledger-capacity`（默认 4096，`:497`）是**独立旋钮**，且锚定流有「未确认组永不逐出」规则（`:503` 帮助文本）⇒ 锚定流可能比主账本保留更多 ⇒ 该残差由 `anchor_out_of_window` 表达；
  ④ **不是「证据为真」的断言**：兑现只证明「载体所声称的那条证据产物存在且摘要一致」，**不**证明证据产物的内容为真（P42/P45 的维度）；
  ⑤ **自证**：核对者与被核对者在同一进程（与全部六族同款）；
  ⑥ **行为变更声明（穷举）**：**只加读面字段，不加族、不改枚举**——`witnessFamily` 结构新增两个字段不改变族数，故 `snapshot_anchor_delivery_test.go:268`（T277 六流）/`:294`（四条 sweep 流）/`snapshot_witness_reconcile_test.go:418`（T259 六族）**取值不变**（T331 钉死）；默认部署状态文档逐字节不变（T318）；
  ⑦ **读面成本**：每族多读一条主账本 + 一次销毁账本读（锚定流已读）——管理读面，非热路径；
  ⑧ **A-3 只闭合一半**：持导出私钥分支闭合；持 VAK 私钥分支原样保留（§4 A7-⑧）。
⑨ **本族账本源关闭 ⇒ 不可判（第二轮评审 M2）**：某族**自己的**主账本来源未启用时，该族没有窗口可比 ⇒ 逐条 `anchor_out_of_window`、族级 `anchor_nothing_assessed`，**绝不** `unrealized`。**理由**：destruction 的 load 在开关关闭时返回**静默空态**，零值若被读成「窗口 (0,0)」，一次完全正常的配置变更（关掉 `--export-destruction-log`）就会被读成「尾部被删」。本 Phase 的判据必须只断言**它能真正看到的东西**。
⑩ **空锚定窗口不读账本（第二轮评审 M3）**：`anchor_nothing_assessed` 的判定**不依赖**该族账本是否可读——「窗口为空」是**不需要账本**的事实。否则无 KAK 部署（acceptance 账本 load 报错）会把整个兑现面永久钉成 `anchor_realization_indeterminate`，尽管它从未锚定过任何声明。

## 5. 测试契约（T317~T339，23 例；T336 内 MU1~MU9）

| # | 断言 |
|---|---|
| T317 | 冻结面**逐文件 sha256 钉死**（**含 `snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`appendonly_log.go`**）+ `internal/protection` 零 diff + `go.mod`/`go.sum` **逐字节**零改动 + P35~P48 判据取值零变化（第二轮评审：由「扫描 Phase 49 关键词」升级为字节断言——冻结面永不变更，哈希不匹配即违规） |
| T318 | **默认部署零回归**：兑现面未启用 ⇒ 状态文档**逐字节不变**（新字段全 `omitempty`，整组缺席） |
| T319 | **核心红例（伪造验证锚定条目 = A-3 的持导出私钥分支）**：追加合法导出钥签名 + `Overall=attested` + 任意 `ReportDigest` 的 `kind=verification` 锚定条目并派发 ⇒ `anchor_unrealized` ∧ `anchor_realized == false`；**同一用例断言 P46 报 `family_intact` ∧ P47 报 `converged`**（评审：证明这是新增机制，不是 P46/P47 复用） |
| T320 | **核心红例（同身份异摘要/异内容字段）**：锚定条目的身份在账本中存在，但摘要不符、或 `overall`/`event_type`/`verdict` 等内容字段不符 ⇒ `anchor_unrealized` |
| T321 | **核心红例（尾部删除）**：删主账本尾部 k 条（锚定条目仍在）⇒ 对应锚定条目身份**高于**账本上界 ⇒ `anchor_unrealized`；同用例断言 P46 报 `family_intact`（P46 窗口在锚定 seq 轴上，看不见账本尾部） |
| T322 | **合法前缀压缩不误报（A4）**：销毁面启用 + 账本前缀被合法压缩 ⇒ 对应条目判 `anchor_compacted`（**不是** unrealized）∧ 该族 `anchor_realized` 仍可为真 |
| T323 | **不可判响亮（A5）**：销毁面**关闭** + 前缀缺席 ⇒ `anchor_out_of_window`（**不是** unrealized、**不是** realized），读面同面给出计数与原因 |
| T324 | **fail-closed 与族间隔离（A3/A5）**：锚定流含不可分类行 / 冲突 seq，或主账本 load 失败 / 含不可分类行 / 冲突 seq ⇒ 该族 `anchor_realization_indeterminate`、整条不判、响亮报错，**其余族取值不受影响**，**绝不**报 realized |
| T325 | **各族摘要重算正确性（A1）**：lifecycle/destruction/acceptance 的锚定摘要 == 账本行摘要（直拷路径）；**verification 必须从账本行重算 `reportDigest`（7 字段）**并等于锚定条目的 `ReportDigest`，且**不**等于 `verificationEntryDigest`（11 字段）——两套规范序列化的判别 |
| T326 | **`anchor_realized` 成立（非空泛）与空集不为真（评审 M1）**：(a) 正常 tick + **至少一族**有锚定条目且全部兑现 ⇒ `anchor_realization_state == "realized"` ∧ `anchor_realized == true` ∧ `anchor_realization_claims > 0` ∧ **至少一条**条目被判为「重算摘要逐字节相等」（判据来源是**重算比对**，不是「没找到反例」）；(b) 全部非委派族的锚定窗口皆为空（或行全为 compacted/out_of_window）⇒ `state == "nothing_assessed"` ∧ `anchor_realized == false` ∧ `claims == 0`——**空集绝不判真** |
| T327 | **`ledger` 族空泛声明（A8-①）**：同钥构造（导出钥伪造账本行 + 锚定条目）⇒ 本面报 realized ⇒ **用例断言并登记该空泛性**（不夸大） |
| T328 | **`protection_decision` 族不重复（§3 表）**：该族兑现判据由 P48 本地重算产出，本 Phase 不重复；用例断言两面结论不冲突 |
| T329 | **unusable 条目（A3）**：锚定流中验签失败的条目 ⇒ 计入 unusable、该族**不**报 realized（不得因「不可用就不看」而变绿） |
| T330 | **零副作用**：读面不落盘、不写 audit、不发网络、不派发、不压缩（读前后文件字节与 mtime 不变） |
| T331 | **零新增族/路由（A2/A8-⑥）**：注册表仍 6 族、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条、路由表零新增；T277/T283/T259 三条既有枚举用例取值不变 |
| T332 | **`anchor_compacted` vs `anchor_unrealized` vs `anchor_out_of_window` 三态判别（A4）**：同一构造下仅「是否有已完成销毁记录覆盖」不同 ⇒ 三种取值，必不混淆 |
| T333 | **身份边界**：`身份 < 窗口下界` / `窗口内缺记录` / `窗口内摘要不符` / `身份 > 窗口上界` 四态各自取值（min / max / outside 边界） |
| T334 | **区间记账匹配**：销毁记录 `from_seq..to_seq` 的**闭区间**语义；相邻区间不误覆盖；`kind` 必须与该族 compaction kind 相符（`snapshot_destruction.go:63-69`/`:731-738`）；`state` 必须是 `completed`（`intended`/`aborted` **不**记账） |
| T335 | **畸形输入不 panic**：空锚定流 / 空账本 / 零值条目 / 超长字段 / 非法 JSON / 身份为 0 ⇒ 不 panic，走 fail-closed |
| T336 | 变异 MU1~MU6（MU1 摘掉「重算摘要比对」（⇒ T319/T320 必红）/ MU2 把 `anchor_out_of_window` 并入 unrealized（⇒ T323/T332 必红）/ MU3 摘 fail-closed（⇒ T324 必红）/ MU4 让 verification 用 `verificationEntryDigest` 而非重算 `reportDigest`（⇒ T325 必红）/ MU5 摘 `ledger` 族空泛声明（⇒ T327 必红）/ MU6 让读面产生副作用（⇒ T330 必红））⇒ 对应用例必红，sha256 还原；**MU7/MU8/MU9**（第二轮评审新增：停止承认委派 / 摘掉「本族账本源关闭」守卫 / 摘掉「空窗口不读账本」早退）分别对上 **T337/T338/T339**，均已实跑判红并 sha256 还原 |
| T337 | **委派族不被损坏的判定锚定流拖成 `indeterminate`（第二轮评审 M1 / I11）**：向判定锚定流追加一条不可分类行 ⇒ `protection_decision` 保持 `delegated`、**不进入** indeterminate/unrealized 列表、全局标量不变，且**其余五族逐字节不变** |
| T338 | **本族账本源关闭 ⇒ 不可判（第二轮评审 M2 / I12）**：同一 dir 上以 `DestructionLog=false`（并 `AcceptanceLog=false`，P45 G4）重建调度器 ⇒ destruction 族逐条 `out_of_window`、族级 `nothing_assessed`、带 reason，**绝不** `unrealized`，全局非 `unrealized` |
| T339 | **空锚定窗口不读账本（第二轮评审 M3 / I13）**：无 KAK 部署 ⇒ acceptance 族为 `nothing_assessed`（不是 `indeterminate`）、不进 indeterminate 列表、全局标量非 `indeterminate`，且该族 `checked`/`realized`/`unrealized` 全为 0 |

## 6. 与既有 Phase 的关系

不降级任何判据。P46 的对账词汇、P47 的投递词汇、P43 的记账词汇**被读**但**不改**。P49 的工程量集中在三处：**per-family 兑现索引（身份 → 账本摘要）**、**跨族统一的多态判别（realized / compacted / out_of_window / unrealized / indeterminate）**、**与 P43 销毁记账的区间匹配**。**P49 补的是它们共同的前提**：P46 说「域外副本与本地载体一致」、P47 说「载体投递成功」、P43 说「消失被记账」——**三条都默认「载体说的那句话是真的」**，而这一点此前无人断言。

## 7. 顺路清偿登记债（本 Phase 内）

> P48 §7 的 D5~D7 **已核实全部清偿**：`mgmt_obs.go:413-420` 注释已改写（判定面的取证历史指向第六族）；`snapshot_anchor_delivery_test.go:268` = 6 流、`:294` = 4 条 sweep 流；`snapshot_witness_reconcile_test.go:418` = 6 族；`history_export_scheduler.go:1784-1789` 已有第六族 case。本轮盘点发现**三笔他人登记、至今无人认领**的债务 + **一笔本轮新核实的注释陈旧**：

| # | 债务 | 处置 |
|---|---|---|
| D8 | `docs/adr/064-phase44-architecture.md:124` 与 `docs/adr/063-phase44-scope.md:158` 把 A-3 登记为「列 Phase 45 候选（与 C2 并案评估）」；P45 选了 C2，**P46/P47/P48 全文对 A-3 零命中**（本轮亲跑 grep）⇒ 该登记已陈旧且误导 | 两处更新为「**持导出私钥分支由 P49 兑现面闭合**（`anchor_unrealized`，ADR-073 §3）；**持 VAK 私钥分支原样保留**」——登记与实况对齐 |
| D9 | `docs/adr/060-phase43-scope.md:223` 与 `docs/adr/061-phase43-architecture.md:293` 把 Q3（pre-export drop）登记为「列 Phase 45」；P45 选了 C2 ⇒ 至今未做 | 改写为「**仍未认领**，且**本轮不认领**：其判据已存在（`runtime_dropped`/`file_dropped`/`Truncated` 见 `mgmt_obs.go:265-283`），剩余的是**接入**而非新判据 ⇒ 被 R210 尺子击倒」——不再悬挂为「候选」 |
| D10 | `docs/adr/063-phase44-scope.md:128` 的「不提供 VAK 旋转/吊销机制（列 Phase 45+ 候选）」**已核实未做**（P45~P48 零命中） | 同上：改写为「仍未认领 + 为何本轮不认领（『when』维度在**另一把密钥**上的延伸，需独立威胁模型；且**不解决 A-3**，排序在兑现面之后）」 |
| D11 | **本轮新核实**：`snapshot_anchor_delivery.go:90-92` 注释仍写「the static **five**-family table (ADR-070 §2)」，而该函数在 P48 后已返回**六**条（`:93-148`，本轮亲跑 `grep -c "Family:"` = 6） | 注释改为 six；**只改注释，不改代码**（T331 断言族数不变） |

## 8. 证据体系收敛判断

本轮对证据体系做了显式盘点：九个问题面各有可断言面；P47/P48 的登记债已核实清偿；六族账本都上了证据链（P46 对账 / P47 投递）。**收敛的只是「问题面」与「载体」——「载体那句声明是否为真」仍无人断言**（§2 事实 1~7），且它对应的正是一条**登记在册、跨四个 Phase 无人认领**的逃逸通道（事实 8）。故本轮**不收敛**。

## 9. 方向候选裁定表（自拍板，R210 尺子）

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① **锚定兑现**（本 Phase） | **采纳** | 真实缺口（§2 事实 1~9 逐行核实；A-3 是登记在册、四个 Phase 无人认领的通道）；新判据 `anchor_realized`/`anchor_unrealized` 对**锚定条目 ↔ 主账本**此前给不出（§2 悖论）；机制**零新族、零新写入路径**，纯读派生；对 4/6 族非空泛（§3 表），1 族空泛**已显式声明**，1 族已由 P48 覆盖 |
| ② VAK 旋转/吊销（ADR-063 §8-9） | **不淘汰，但本轮不选** | 「when」维度在**另一把密钥**上的延伸：需独立威胁模型与 KAK 授权的第二账本，工程量 ≥1 Phase；且**不解决 A-3**（能吊销 VAK 者仍可伪造锚定条目）⇒ 排序在 ① 之后（D10 已登记） |
| ③ pre-export drop 记账（ADR-060 §9 Q3） | **淘汰** | 判据已存在（`runtime_dropped`/`file_dropped`/`Truncated`，`mgmt_obs.go:265-283`）⇒ 不产生此前给不出的判据，只是**接入**（R210 击倒；D9 已登记） |
| ④ 呈现/运维面整合 | **淘汰（仍）** | 不产生新判据（R210；同 P47/P48 裁定） |
| ⑤ HA 多副本 | **淘汰（仍）** | 「一致性不是证据性」（ADR-054 §1.1） |
| ⑥ 外部时间权威（TSA/RFC3161） | **淘汰（仍）** | 判据类别仍是「该 digest 是否被域外记录」＝ P40 第二实例（ADR-057 §1.1） |
| ⑦ 见证端拉取（P46 逆操作） | **淘汰（仍）** | `family_incomplete` 已表达「本地有、域外无」；拉取只是同一判据的第二种取数方式 |
| ⑧ P47 A8-⑦ 的 `destructionObserver` 并发缺口 | **不独立成 Phase（仍）** | 既有不变量的**加固**（既有维度），不产生新判据（同 P48 §9 ⑥） |

⇒ **判断**：存在且**仅存在一个**符合 R210 尺子的候选（①）。②被「不解决 A-3 且更重」降序；③④⑤⑥⑦被同一把尺子击倒；⑧属既有维度加固。

## 10. 评审闭合表（第一轮，3 项 major）

| 发现 | 级别 | 闭合 |
|---|---|---|
| **M1** 全局 `anchor_realized` 语义自相矛盾（074 的 JSON 注释写「非委派且非空」，紧接的正文写「任一非委派族 `no_anchors` ⇒ 假」）；两种读法在「某族本次 tick 无锚定条目」下结论相反，而 T326 要求「各族兑现完整 ⇒ true」，按正文读法多数部署不可达 | major | ① 全局取值由**合取**改为**标量 `anchor_realization_state`（全序首个命中）**，`anchor_realized` 降为**派生 bool**，定义**唯一**在 **ADR-073 §3**；② 删去 `no_anchors` 族级取值，改为 **`anchor_nothing_assessed`**（族级第 ④ 步）——空集**既不**判真、**也不**判 unrealized；③ **T326 重写**为 (a) 至少一族有声明且全兑现 ⇒ `state == "realized"` ∧ `anchor_realized` ∧ `claims > 0`、(b) 全部非委派族无声明 ⇒ `state == "nothing_assessed"` ∧ `anchor_realized == false` ∧ `claims == 0`——可达、非空泛、且「空集为真」不可能 |
| **M2** Scope 与 Architecture 的判据集合不一致：074 单方面引入 073 从未定义的 `anchor_realization_delegated`、`no_anchors` 与全局合取；且 073 的 `anchor_realization_indeterminate` 行未给 unusable 条目落点 | major | ① 两个族级取值（`anchor_nothing_assessed` 取代 `no_anchors`；`anchor_realization_delegated`）**写入 073 §3 闭表**，并在 073 §3 新增「**族级与全局取值的权威定义**」段（含命名边界消歧）；② 073 的 `anchor_realization_indeterminate` 行条件**显式加入 unusable 条目**；③ 074 全文改为**引用** 073 §3（§2.2 / §3 伪码 / §4 状态面 / I5 / I7 同步），不再另立任何取值 |
| **M3** 073 §2 事实 2 的「本轮亲跑」证据陈述与实况不符（声称只命中 2 行，实跑 16 行） | major | 该段**重写**为两条命令的**逐行如实归类**：主命令 **16 行** = 1 行锚定条目写入（`snapshot_verification.go:1337`）+ 1 行读**账本行**字段（`mgmt_obs.go:1178`）+ **14 行** `keyLifecycleEntry` 自有账本字段（`snapshot_key_lifecycle.go:109/110/127/142/157/158/357/358/368/380/704/723/725/729`）；补一条精确命令 `grep -rn "\.ReportDigest\|\.DestructionDigest\|\.AcceptanceDigest" internal/controlplane/server/*.go | grep -v _test` ⇒ **恰 3 行**，全在 `snapshot_anchor.go:193/196/201` 的 `anchorSignedFields`（**签名覆盖区的 payload 构造**），**无一处与账本记录比对**。结论不变、表述精确化 |

## 11. 评审闭合表（第二轮：实现轮对抗评审）

实现 `353286a` 经独立对抗评审，发现并闭合 **3 项 major**（委派判定顺序 / 本族账本源关闭被读成尾部删除 / 空锚定窗口仍读账本），另有 2 项轻量修正（ADR-074 §1 的 `mgmt_obs.go` 行、T317 升级为字节钉死）。**逐条闭合细节、可达路径、回归用例与变异体见 ADR-074 §9**；本表只登记结论：三条都是**判定先后顺序**把一类输入送进了错误分支，均以「先判定、后加载」的顺序修正，且各有 T337/T338/T339 与 MU7/MU8/MU9。Scope 层相应新增 A8-⑨（本族账本源关闭 ⇒ 不可判）与 A8-⑩（空窗口不读账本），并把 `anchor_out_of_window` 的行条件扩写为「**或**该族自己的账本源未启用」。**Scope 的三处核心取值定义（行级 / 族级 / 全局全序）未变**——三条修正都只是让实现回到 Scope 已经写下的规则。
