# ADR-075 — Phase 50: Verifier Authority Lifecycle（验证者授权生命周期 / 「when」面在第二把签名身份上的补完）

- **Status**: Proposed (Phase 50, Scope stage)
- **Phase**: 50（Verifier Authority Lifecycle）
- **Base**: Phase 49 CLOSED（HEAD = `8188879`）
- **Supersedes**: 无。不修改 P35~P49 的任何冻结判据；P41 生命周期账本的**结构、规范序列化与既有判定**（`signature_before_activation` / `signature_after_rotation` / `signature_after_revocation` / `signature_time_unparseable`）逐字沿用；P44 的验证者判定（`verification_unauthorized` / `key_unknown`）逐字沿用；`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `history_export_manifest.go` / `appendonly_log.go` / 冻结五文件 / `internal/protection` **零 diff**；既有读面响应字节不变。

---

## 1. 分工句（第十一问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted / P46 whether-witnessed / P47 whether-delivered / P48 whether-recorded / P49 whether-realized ⇒
**P50 = whether the observer was in authority（签发这条验证证据的那把身份，在它签发的时刻，是否处在被授权的区间内）**：前十问依次问「证据是谁签的 / 放在哪 / 何时 / 是否验证过 / 为何缺席 / 接受了什么 / 域外副本是否一致 / 投递义务是否清偿 / 判定是否被留痕 / 锚定声明是否兑现」。**其中「何时」（P41）只覆盖了**一把**密钥——manifest 的签名密钥；P44 引入的**第二把签名身份（VAK，验证报告的签发者）至今没有任何时间维度**，而它签的正是「whether」那一面本身的证据。**

## 2. 新问题（已核对代码，全部带行号）

**事实**：

1. **P44 引入了第二把签名身份，并**当场**把它的时间维度留成空白**：`history_export_scheduler.go:248-253` 的 `verifierSigner`/`verifierTrust`（注释 `:116-122` 原文「the identity that signs verification reports, deliberately a **different** key」）；`docs/adr/063-phase44-scope.md:128` 末句「不提供 VAK 旋转/吊销机制（静态锚，同 manifest trust 先例）」；已知代价 2（`:153`）「**VAK 无生命周期**：静态锚。VAK 泄露 = 持续伪造能力，直至人工换锚 + 换目录」。**这不是「没接线」，是「不可表示」**：见事实 2。
2. **生命周期账本在写入面**结构性地**拒绝**为验证者记账**：`appendKeyLifecycleEvent`（`snapshot_key_lifecycle.go:636`）在 `:648-654` 只接受 `c.signingTrust.keys[req.KeyID]`（P37 的 **manifest 签名**信任集）——`keyLifecycleConfig.signingTrust` 字段注释 `:278` 原文「P37 trust set: **WHO** may ever be given a window」。本轮亲跑 `grep -n "signingTrust" internal/controlplane/server/*.go | grep -v _test` ⇒ **恰 4 行**，逐行归类：`history_export_scheduler.go:645`（**配置装配**，把 P37 信任集传给生命周期配置）+ `snapshot_key_lifecycle.go:278`（**字段定义**）+ `:648` / `:651`（**写入面主体判据**，两行都在 `appendKeyLifecycleEvent` 内）。⇒ **读面一处也不查主体集**（`loadKeyLifecycleState:326` 与 `keyLifecycleSummary:828-860` 都没有该符号）；库里任何一个 `KeyID` 都必须先是 manifest 签名密钥，VAK 的 key_id 连一行授权事实都写不进去。
3. **没有任何面把授权区间用在验证报告上**：`authorizeByLifecycle`（`snapshot_key_lifecycle.go:534`）的**唯一**调用点是 `history_export_manifest.go:596-597`（`a := lifecycleState.authorizationFor(v.KeyID)` / `v = authorizeByLifecycle(v, signedAtOfManifest(m), a)`），且注释原文「the **ONE** call site」。验证报告的入口 `loadVerificationState`（`snapshot_verification.go:619`）对每条记录只做 `verifyVerificationEntrySignatureIn`（`:757`）——P44 的静态信任锚判定；**从不解析区间**：本轮亲跑 `grep -n "authorizationFor\|lifecycleRulerOf" internal/controlplane/server/*.go | grep -v _test` ⇒ **恰 8 行**（其中 **2 行是注释**：`snapshot_key_lifecycle.go:424`、`snapshot_verification.go:972`），逐行归类：`history_export_manifest.go:596`（**唯一的跨面消费点**：manifest 签名）、`snapshot_key_lifecycle.go:428`（定义）/`:853`（**P41 状态面自身**的 roll-up）、`snapshot_verification.go:852`（报告里 `lifecycle` 维度的赋值）/`:975`（`lifecycleRulerOf` 定义）/`:979`（其对 `authorizationFor` 的调用）——后三者服务的都是 **manifest 签名者**（事实 4）。⇒ **没有一处以验证报告的签发者为对象**。
4. **验证报告已有的 `lifecycle` 维度指的是**别人**：`snapshot_verification.go:852` `it.Lifecycle = lifecycleRulerOf(in.lifecycle, sigKeyIDOf(res))`，而 `sigKeyIDOf`（`:965-971`）取的是 `res.Signature.KeyID`——**manifest 签名者**。⇒ P42 报告里那个叫「lifecycle」的维度，对「验证者是否在位」一个字也没说；`lifecycleRulerOf`（`:975-985`）也只是 ruler（`bounded`/`unbounded`），V44 加了一把密钥却没有给这一维加行。
5. **P44 的判定是「认识它 / 不认识它」，不是「它当时有没有权」**：`verifyVerificationEntrySignatureIn`（`:757-790`）的分支只有 absent / malformed / `verification_unauthorized`（`:584`，异族已知钥）/ `key_unknown` / invalid / ok。**没有时间参数**——与 ADR-054 §2.1 对 P37 的原话逐字同构。
6. **P49 兑现面对这条通道也不设防**：`snapshot_anchor_realization.go` 的 verification 族核对的是「锚定条目声称的 `ReportDigest` 是否能由账本行**重算**得到」（ADR-074 §2.1 第 4 行）；它**不核对账本行的签名者是否被授权**。⇒ 持 VAK 私钥者在边界之后伪造的报告一旦落账，P49 报 `realized`（锚定条目所称的 digest 确实在账本里）。ADR-073 §3 的表把 verification 族标为「非空泛」，其论据是「导出私钥造不出 VAK 签名的账本行」——**对持 VAK 私钥者不成立**（本条 Phase 是那句声明的补完，不是否定：导出私钥分支仍由 P49 闭合）。
7. **吊销与保史的悖论在 VAK 上完整重演**（ADR-054 §2.2 的对象是 manifest 签名密钥 K1，此处对象换成 VAK）：今天运营者只有两个动作，逐条对应代码——
   - **旧 VAK 留在 `--export-verifier-trust`**：它在泄露**之后**被伪造的报告仍然 `signature_ok`；P42/P44/P47/P49 四个面**无一**能拒绝（事实 3/4/5/6）⇒ 伪造能力不可撤销。
   - **把旧 VAK 移出 verifier trust**：`verifyVerificationEntrySignatureIn:780` 落 `key_unknown` ⇒ `loadVerificationState`（`:657-658`）把 `st.verifiable = false`，该账本**整体不可验**⇒ 一次轮换把**整段历史验证记录的可验证性归零**。
8. **【首轮评审 M1 补入】验证账本**缺少**「记录自称的 `key_id`」与「签名块的 `key_id`」的绑定校验——本家族其余三位都有**：本轮亲跑 `grep -rn "disagrees" internal/controlplane/server/*.go | grep -v _test` ⇒ 命中 **恰 7 行**，其中**三行是本家族的绑定校验**（`snapshot_acceptance.go:251`「acceptance authority_key_id disagrees with the signing key id」、`snapshot_destruction.go:315` 同款、`snapshot_key_lifecycle.go:257` 同款），其余 **4 行**为 `history_export_scheduler.go:65`、`:350`、`snapshot_chain.go:38`、`snapshot_witness_reconcile.go:63` 的注释/其他语义；**verification 侧一行也没有**。核实结论：`snapshot_verification.go:775` 的信任查表用的是 `trust.keys[sb.KeyID]`（**签名块**的 key_id），`:757-790` 全部分支只用 `sb.*`；入队点 `:692`（`st.entries = append(st.entries, e)`）前后**没有任何** `e.KeyID != sb.KeyID` 检查；`e.KeyID` 虽然在被签载荷里（`verificationSignedEntry.KeyID`，`:318`/`:333`）**但它只是签名覆盖的一个可任填字段，不与签名者身份绑定**。诚实写入路径两者同源（`:729` `e.Signature.KeyID = s.keyID`、`:1084` `KeyID: signingKey.keyID`），所以**正常部署永不触发**；但**手工构造**的账本行可以 `key_id = <区间内的另一把 key>` + `signature.key_id = <已吊销的 A>`，签名用 A 的私钥（载荷里的 `key_id` 由攻击者任选）⇒ `:775` 查到 A（仍在 trust）⇒ `sigVerdictOK` ⇒ 落进 `st.entries`。⇒ **本 Phase 的判据必须把「签发者」锚在 `signature.key_id` 上，并把两者的错配并入 fail-closed**（§4 A9）；否则本 Phase 锁定的对手（持已吊销 VAK 私钥者）可一步绕过整个机制（§3 表第 1 行）。
9. **为什么它今天全绿**：持 VAK 私钥者写一条**合法签名**的验证记录（P44 信任锚内，`signature_ok`）并照常锚定、派发 ⇒ P37/P44 OK、P42 面无异常、P47 `converged`、P49 该族 `realized`、P46 `family_intact` ⇒ **每一个面都绿**，而「这个签发者在签发时刻是否在位」没有任何面可以引用。

**悖论**：系统能断言「这条验证报告是谁签的」（P44）、「它验证了什么」（P42）、「它被投递/锚定/兑现了」（P47/P49）——**唯独不能断言「签它的那把身份，在签它的时候有权签」**。这一维在 P41 已经为 manifest 签名密钥建成，**P44 引入第二把签名身份时把它落下了**（事实 1/2），而 P44 自己的已知代价 2 与 ADR-063 Q1 都把它**登记在册**。

## 3. 决策（Scope）

新增**验证者授权面**——**不新增账本、不新增密钥类型、不新增证据族、不新增写入路径、不新增路由、不新增常驻组件**，只新增一个**读派生判据**：把每条验证记录的**签发者 × 签发时刻**拿去**在生命周期账本里兑现**。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`verifier_authorized`** | **签发者 = `verificationLogEntry.Signature.KeyID`**（**不是** `e.KeyID`——首轮评审 M1，§4 A9）**且** `e.KeyID == e.Signature.KeyID`；该签发者在生命周期账本中有一条**可断言**（`Source=lifecycle_ledger ∧ Complete`，`snapshot_key_lifecycle.go:477-483`）的区间，且该条记录的签发时刻落在区间内（`not_before ≤ t < not_after`，逐字沿用 `authorizeByLifecycle:552-580` 的比较语义） | **签发时刻该身份在位**——此前给不出的判据（§2 悖论） |
| **`verifier_authority_violated`** | 存在一条可用验证记录，其签发者区间**可断言**而签发时刻在区间之外——`before_activation`（`t < not_before`）/ `after_rotation` / `after_revocation`（`t ≥ not_after`，终局取值 `revoked > rotated_out`） | **签发时刻该身份不在位**：证据来自一个当时无权签它的身份。P44/P42/P47/P49 对此全绿（事实 3~6） |
| **`verifier_authority_unbounded`** | 该签发者在账本中**没有**可断言的区间：账本未启用 / 该 key 无任何事件 / 事件落在保留下界之外（`Complete=false`）/ 账本不可验 | **不可判**，如实报出（沿用 P41「每一个『我们不知道』的路径都返回 unbounded 而不是猜测」的纪律，`:424-427`），**绝不算 authorized，也绝不算 violated** |
| **`verifier_authority_indeterminate`** | 验证账本或生命周期账本 load 失败 / 含不可分类行 / digest 不符 / prev 断链 / **同一 `key_id` 同时出现在两个信任锚**（域歧义，§4 A6）/ **`e.KeyID != e.Signature.KeyID`**（身份错配，§4 A9）/ `signed_at` 不可解析 | **fail-closed**：响亮报错、整面不判、**绝不**报 `authorized` |
| **`verifier_authority_nothing_assessed`**（全局） | 本次读**没有任何一条**记录被真正核对（验证账本为空 / 全部记录落 `unbounded`） | **本次读没有核对过任何东西**——如实报出，**绝不**报 `authorized`（空集不构成「全部在位」） |
| **`verifier_authority_state`**（全局标量） | 全序首个命中：① 任一 key 行 `indeterminate` ⇒ `indeterminate`；② 否则任一 key 行 `violated` ⇒ `violated`；③ 否则**至少一行** `authorized` ⇒ `authorized`；④ 否则 ⇒ `nothing_assessed` | 权威取值唯一在本 §3；Architecture 不得另立 |
| `verifier_authorized`（bool） | `= (verifier_authority_state == "authorized")`——**派生便捷量，不得另行定义** | 「空集为真」在本定义下**不可能** |
| `verifier_authority_claims`（int） | 各 key 行 `checked` 之和 = 本次读**实际核对过区间**的记录条数 | **非空泛性的见证量**；`authorized == true` **蕴含** `claims > 0` |
| `signature_before_activation` / `signature_after_rotation` / `signature_after_revocation` / `signature_time_unparseable` | **逐字复用 P41**（`snapshot_key_lifecycle.go:73-82`） | 不变（本 Phase 只**复用**其语义，且**只在**新面上产生同名取值，绝不改动 P41 在 manifest 面的取值） |
| `verification_unauthorized` / `key_unknown` | **逐字复用 P44** | 不变（**绝不折叠**，见 A5） |
| P42/P46/P47/P48/P49 全部取值 | **逐字复用** | 不变 |

**全局取值的权威定义（Scope 为准；Architecture 不得另立）**：

- **行级**（每个签发者 key，**全序首个命中**——首轮评审 M2）：① 该 key 的任一记录落 fail-closed（`signed_at` 不可解析 / `e.KeyID != e.Signature.KeyID` 身份错配 / 既定语义矛盾）⇒ `indeterminate`；② 否则违纪计数之和 > 0（`before_activation` + `after_rotation` + `after_revocation`）⇒ `violated`；③ 否则 `authorized > 0` ⇒ `authorized`；④ 否则区间不可断言（`Source ≠ lifecycle_ledger ∨ ¬Complete`）⇒ `unbounded`；⑤ 否则 ⇒ `nothing_assessed`（**按构造不可达**，见 ADR-076 §3 ⑤；仅作防御性兜底）。**行级取值必须由这五步「全序首个命中」产出，不得由顺序赋值产出**——否则循环内先置的 `indeterminate` 会被后续赋值覆盖，方向是 fail-open（首轮评审 M2 的缺陷）。
- **全局** `verifier_authority_state`（**标量**，全序首个命中，**不是合取**）：① 任一行 `indeterminate` ⇒ `indeterminate`；② 否则任一行 `violated` ⇒ `violated`；③ 否则任一行 `authorized` ⇒ `authorized`；④ 否则（全部行皆 `unbounded`/`nothing_assessed`）⇒ `nothing_assessed`。**命名边界（消歧）**：行级取值不加 `verifier_authority_` 前缀（`authorized` / `violated` / `unbounded` / `indeterminate` / `nothing_assessed`）；带前缀的是**全局字段**——`verifier_authority_state` / `verifier_authorized` / `verifier_authority_claims` / `verifier_authority_entries` / `verifier_authority_violations` / `verifier_authority_unbounded_keys` / `verifier_authority_violating_keys` / `verifier_authority_indeterminate_keys`（形状以 ADR-076 §4 为准）。

**核心红例（此前给不出的端到端判据；同时闭合 ADR-054 §2.2 悖论在 VAK 上的复本）**：
VAK 泄露 ⇒ 离线 KAK 记录 `revoked(not_after = T)` ⇒ **旧 VAK 仍留在 `--export-verifier-trust`**：
- 今天 ⇒ ① 移出 trust：**全部历史报告 `key_unknown`** ∧ 账本 `verifiable=false`（事实 7）；② 留在 trust：泄露后伪造的报告 `signature_ok`，**P42/P44/P47/P49 全绿**（事实 9）。
- 本 Phase 后 ⇒ **同一用例两面同时成立**：`t ≥ T` 的报告 ⇒ `after_revocation` ∧ 该 key 行 `violated` ∧ 全局 `violated` ∧ `verifier_authorized == false`；`t < T` 的历史报告 ⇒ `authorized` ∧ 该账户的 P37/P44 判定**逐字节不变**（`signature_ok`，因为旧 VAK 仍在 trust）。**「吊销能力」与「保史」第一次可以同时成立**——且同一用例显式断言 **P42 报告字节不变 ∧ P44 判定不变 ∧ P49 该族仍 `realized`**（证明这是新增机制，不是既有面的复用）。

**检测力必须分别声明（承重，不可省略）**：

| 对手 | 本 Phase 的检测力 |
|---|---|
| 持 **VAK 私钥**者，在授权区间**之外**伪造验证报告 | **非空泛**（**前提 = 首轮评审 M1 的身份绑定，§4 A9**）：区间可断言 ⇒ 判 `after_rotation`/`after_revocation`/`before_activation`（这正是 P44 无法表达、P49 也不设防的那一半）。**没有 A9 时该对手可一步绕过**：把记录自称的 `key_id` 填成**区间内**的另一把身份、签名块仍用 A ⇒ 面会去核对那把「区间内」的 key（这正是首轮评审 M1 指出的漏洞，T360 钉死） |
| 持 **VAK 私钥**者，在授权区间**之内**伪造 | **空泛**（与真报告不可区分——`signed_at` 是自证时间，ADR-055 R41-7 / ADR-054 §2.1 的已知代价继承）⇒ **如实声明，不夸大** |
| 持 **KAK 私钥**者 | **空泛**：可伪造授权事件本身（同 P43 逃逸 A-2 / P41 T177 家族）⇒ 拓扑代价，登记为 A8-④ |
| 主机级攻击者（可删账本尾部） | **不空泛但有条件**：删掉 revocation 事件 ⇒ 该区间向上无界 ⇒ 本面只能报 `authorized`（**如实登记 A8-⑤**）；但**锚定启用时**该事件有一条 `kind=key_lifecycle` 锚定条目（`snapshot_anchor.go:66`/`:1132`）⇒ **P49 兑现面报 `anchor_unrealized`**（ADR-074 §3.1「身份 > 账本上界 ⇒ 尾部被删」）⇒ 本 Phase **复用** P49，不重复实现（T359 钉死这条依赖） |
| 持导出私钥者（manifest 侧） | 与本 Phase 无关（P37/P41/P49 的域） |

**R210 尺子（§9 详述）**：本条产出的是**此前根本给不出**的判据——今天**没有任何面**能把「验证记录的签发者 × 签发时刻」与授权区间比对（事实 2 证明它在写入面**不可表示**、事实 3 证明**唯一**的区间消费点是 manifest、事实 4 证明报告里叫 `lifecycle` 的那一维指的是**别人**、事实 6 证明 P49 也不设防）。它不是复述（P41 的对象是 **manifest 签名密钥**、P44 的对象是**身份归属**、P49 的对象是**锚定声明**——三者的对象都不是「验证记录的签发者是否在位」），不是加固（不是把既有判据再签一遍），不是呈现（不是换一种画法）。**它也不是 D9 式的「接入」**：D9（pre-export drop）被击倒是因为判据**已存在**于读面（`runtime_dropped`/`file_dropped`/`Truncated`，`mgmt_obs.go:265-283`），缺的只是 P43 的接入；本条的判据**不在任何面**，且在写入面**不可表示**（事实 2）。

## 4. 能力定义（A1~A8）

- **A1 授权面的载体（读派生，零新账本、零新族）**：需要的三件东西——(a) 生命周期账本的**取数面**（既有 `loadKeyLifecycleState`，`snapshot_key_lifecycle.go:326`）、(b) 区间的**解析**（既有 `authorizationFor`，`:428`）、(c) 区间的**比较**（既有 `authorizeByLifecycle`，`:534`）。**全部已存在**；本 Phase 只新增：(d) **写入面主体集的显式扩张**（§4 A2）、(e) **以验证记录的签发者为对象的消费点**、(f) 一个新的只读面（组）。
- **A2 主体集的扩张（承重，本 Phase 唯一触及写入面的改动）**：`appendKeyLifecycleEvent`（`:636`）的主体判据从「`∈ signingTrust`」改为「`∈ signingTrust ∪ verifierTrust` ∧ **不得同时在两者中**」（`:648-654` 处），`keyLifecycleConfig` 增加 `verifierTrust` 字段（`:274-283`）。**账本 schema 零变更**：**不新增字段**，因此 `keyLifecycleSigned`（`:119`）/`canonicalLifecyclePayload`/`lifecycleEventDigest`（`:157-159`）**零改动**，既有 `signing-key-log.jsonl` 每一行的 `event_digest` 重算**逐字节相等**（T340 钉死）；事件**没有** role 字段，域归属由「该 key_id 属于哪个信任锚」解析——**消费者永远只问自己那把身份**，故读者无需归属、消费者无需反查（A8-⑧ 如实登记此设计）。**读面零变更**：`loadKeyLifecycleState`（`:326`）不检查主体集（本轮逐行核实：`signingTrust` 全仓 4 处使用 = 1 处配置装配 + 1 处字段定义 + 2 处写入面判据，**读面一处也不查**）。
- **A3 零触碰（承重承诺）**：`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `history_export_manifest.go` / `appendonly_log.go` / 冻结五文件 **零 diff**；`internal/protection` 零 diff；`go.mod`/`go.sum` 零改动；**P41 状态面的 `authorizations` 与 `active_key_id` 取值零变化**（`keyLifecycleSummary:828-860` 的枚举改为「本体在**签名**信任锚中且**不在**验证者锚中」，对今天的一切输入是**恒等变换**——今天验证者 key 一行也写不进去）；P35~P49 判据取值零变化；既有读面响应字节不变（新组 `verifier_authority` 全 `omitempty`，**未配置 VAK ⇒ 整组缺席** ⇒ 默认部署状态文档**逐字节不变**）。
- **A4 零副作用**：新面**严格只读**——`os.Stat` + 既有两次 load；不落盘、不写 audit、不发网络、不派发、不压缩（沿用 P46 对账 / P47 I4 / P49 A3 的零副作用纪律）。
- **A5 不折叠（承重）**：`verification_unauthorized`（P44：**身份不属于这个家族**）与 `after_revocation`/`after_rotation`/`before_activation`（P50：**身份属于这个家族但当时不在位**）是**两个独立取值**，任何一方**不得**并入另一方（沿用 P41 R41-1「独立命名，绝不折叠，响度在状态里、类别在名字里」；同 P44 I3 的两轨纪律）。同理 `unbounded` 绝不并入 `authorized`，也绝不并入 `violated`。
- **A6 域歧义必须 fail-closed**：`key_id` 同时出现在签名信任锚与验证者信任锚 ⇒ 该事件/该行的域**不可判定**（既可能是给 manifest 密钥的窗口，也可能是给验证者的）⇒ `indeterminate`，响亮报错。ADR-063 的 V1~V6 守卫（`history_export_scheduler.go:487-560`）**已**禁止两锚相交（含 `:543-555` 的逐 key 扫描），本 Phase **新增**的是**读派生面的复检**：守卫是构造期事实，而账本来自磁盘——一个被换过配置的目录可能带着守卫禁止过的组合（同 P47 D2 的教训：I1 措辞必须对齐实际执行面，而不是假设）。
- **A7 非目标**：① 不改 P41 账本结构、规范序列化与既有判定（`signature_*` 四词在 manifest 面的取值逐字节不变）；② 不改 P44 的验证者判定（`verification_unauthorized`/`key_unknown` 两轨不变，`problems` 通道在 closed 模式下仍为 nil）；③ 不改 P42 报告的**任何字节**（`lifecycle` 维度继续指 manifest 签名者——本 Phase 的新判据**只**在新组里出现，不塞进报告）；④ **不做销毁授权域的重审**（ADR-063 Q1 的另一半，§7 D14 登记）；⑤ 不新增密钥类型 / 账本 / 证据族 / 路由 / 常驻组件；⑥ 不做自动轮换/自动吊销（本 Phase 只提供**可断言**的授权区间，不提供调度）；⑦ 不引入绝对时间权威（TSA/RFC3161 仍被 R210 击倒，ADR-071 §9 ④）；⑧ 不做 HSM/KMS；⑨ **不闭合「持 KAK 私钥」与「区间内伪造」两条空泛分支**（§3 表，如实登记）；⑩ 不实现「删除保护」——删掉 revocation 事件的可检测性**复用** P49 兑现面（§3 表第 4 行 / T359），本 Phase 不重复实现；⑪ 不改 P49 兑现面的任何判据（它继续不核对签名者身份——那是本 Phase 的维度）。
- **A8 已知代价**：
  ① **`signed_at` 是自证时间**（与 P41/P42 同款，ADR-054 §2.1 / R41-7）：区间内的撒谎时间不可检测；本 Phase 的缓解只有「区间由 KAK 授权」+「事件进锚定流（P47/P49）」两条，**不引入绝对时间**；
  ② **`unbounded` 在未启用生命周期账本的部署上是常态**：那时本面**只会**报 `unbounded`/`nothing_assessed`，永不报 `authorized`——**这是正确行为，不是缺陷**（判据强度与可用证据同阶），但它意味着「没有 KAK 的部署拿不到这一维」；
  ③ **只覆盖启用之后的事实**（同 P41 已知代价 3）：启用前签发的记录只能是 `unbounded`；
  ④ **持 KAK 私钥者空泛**（§3 表）：拓扑代价，与 P43 A-2 同族；
  ⑤ **尾部删除 ⇒ 区间向上无界**：删掉 revocation 事件后本面**只能**报 `authorized`（本地不可区分），依赖 **P49 兑现面**在锚定启用时报 `anchor_unrealized`；若锚定与见证同时关闭 ⇒ 不可检测（如实登记，不掩盖）；
  ⑥ **自证**：核对者与被核对者在同一进程（与全部六族同款，ADR-063 已知代价 1）；
  ⑦ **行为变更声明（穷举）**：**只加一个新组，不改任何既有取值**——`keyLifecycleSummary` 的枚举过滤对今天的输入是恒等（验证者 key 在旧写入面不可表示）；验证者 key 首次出现在 `signing-key-log.jsonl` 后，P41 状态面的 `authorizations` **仍只列签名主体**（T351 钉死）；族数、分区表、路由表**零变化**（T358）；
  ⑧ **账本不存 role（A2 的设计代价）**：事件的域由信任锚解析，故（a）读一份**离开部署**的账本无法独立判断某行是在授权 manifest 密钥还是验证者；（b）若未来某 key_id 在两个锚之间迁移，历史行的域归属会随之变化 ⇒ 本 Phase 以 **A6 的域歧义 fail-closed** 兜底，并把「不存 role」登记为**有意的**取舍（换来的是 canonical payload 零改动与既有账本逐字节自洽）；
  ⑨ **不做 `active_key_id` 的合并**：P41 状态面的 `ActiveKeyID`（`:848-860`）继续只反映**签名主体**；验证者侧的「当前在位者」只在新组里出现（干净分离优先于便利，A3 的字节不变承诺优先）。
- **A9 身份绑定（首轮评审 M1，承重）**：本 Phase 的「签发者」**一律**取 `verificationLogEntry.Signature.KeyID`；`e.KeyID != e.Signature.KeyID` ⇒ **fail-closed**（`indeterminate`，响亮报错），**绝不**去核对 `e.KeyID` 指向的那把 key 的区间。理由（§2 事实 8，逐行核实）：`e.KeyID` 只被签名**覆盖**（`verificationSignedEntry.KeyID`，`snapshot_verification.go:318`/`:333`），**不与签名者身份绑定**——P44 的验签只用 `sb.KeyID`（`:775 pub, known := trust.keys[sb.KeyID]`），入队点 `:692` 前后无任何绑定检查，而本家族其余三位都有（`snapshot_acceptance.go:251` / `snapshot_destruction.go:315` / `snapshot_key_lifecycle.go:257` 的「authority_key_id disagrees with the signing key id」）。**不违反 A3**：校验落在**新文件**里（`snapshot_verification.go` 零 diff）。**行为变更声明**：一条手工构造的、`key_id` 与签名者错配的验证账本行此前 `sigVerdictOK` 并进入 `st.entries`，此后在**本面**判 `indeterminate`；诚实写入路径两者同源（`:729 e.Signature.KeyID = s.keyID`、`:1084 KeyID: signingKey.keyID`）⇒ **正常部署永不触发**（T341/T352 的零回归断言覆盖此点；T360 钉死判别力）。

## 5. 测试契约（T340~T361，22 例；T357 内 MU1~MU10）

| # | 断言 |
|---|---|
| T340 | 冻结面**逐文件 sha256 钉死**（含 `snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`）+ `internal/protection` 零 diff + `go.mod`/`go.sum` **逐字节**零改动 + **P41 账本字节等价**：既有 `signing-key-log.jsonl` 每一行的 `event_digest` 重算**逐字节相等**（`keyLifecycleSigned` 零字段变更）+ P35~P49 判据取值零变化 |
| T341 | **默认部署零回归**：未配置 `--export-verifier-key`/`--export-verifier-trust` ⇒ 新组**整组缺席**、状态文档**逐字节不变** |
| T342 | **核心红例（吊销与保史两全）**：KAK 记 `revoked(not_after=T)` ∧ **旧 VAK 仍在 verifier trust** ⇒ ① `t ≥ T` 的记录 ⇒ `after_revocation` ∧ 该行 `violated` ∧ 全局 `violated` ∧ `verifier_authorized == false`；② `t < T` 的记录 ⇒ `authorized` ∧ 全局 `authorized` ∧ 其 P37/P44 判定**逐字节不变**（`signature_ok`）；③ 同用例断言 **P42 报告字节不变 ∧ P49 该族仍 `realized`**（评审：证明这是新增机制，不是既有面的复用） |
| T343 | **核心红例（轮换）**：`rotated_out(not_after=T)` ⇒ 同 T342 的边界语义，但取值必须是 `after_rotation`（**不折叠**进 `after_revocation`；R41-1 独立命名） |
| T344 | **核心红例（激活前）**：`activated(not_before=T)` 且记录 `t < T` ⇒ `before_activation` ∧ 该行 `violated` |
| T345 | **不可判响亮（A5）**：生命周期账本未启用 / 该 key 无任何事件 / 事件落在保留下界之外（`Complete=false`）⇒ `unbounded`（**不是** authorized、**不是** violated），同面给出计数与原因；全局为 `nothing_assessed`（若全行如此） |
| T346 | **fail-closed（A6）**：验证账本或生命周期账本含不可分类行 / digest 不符 / prev 断链 ⇒ `indeterminate`、响亮报错、**绝不**报 `authorized`；**同一 key_id 同时出现在两个信任锚** ⇒ `indeterminate`（域歧义）；其余取值不受影响 |
| T347 | **非空泛与空集不为真**：(a) 至少一条记录的区间可断言且落区间内 ⇒ `state == "authorized"` ∧ `verifier_authorized == true` ∧ `claims > 0` ∧ 判据来源是**区间核对**（不是「没找到反例」）；(b) 验证账本为空 / 全部行 `unbounded` ⇒ `state == "nothing_assessed"` ∧ `verifier_authorized == false` ∧ `claims == 0` |
| T348 | **不折叠（A5）**：同一构造分别产出 `verification_unauthorized`（P44 异族钥）与 `after_revocation`（同族但区间外）⇒ 两个**独立**取值，任何一方不得并入另一方；断言 P44 的两轨判定不变 |
| T349 | **写入面主体集扩张与守卫（A2）**：`appendKeyLifecycleEvent` 接受 verifier trust 的 key_id（写成功、事件落盘）；拒绝既不在签名也不在验证者锚的 key_id；**拒绝同时出现在两个锚**的 key_id（fail-closed、文件不被触碰） |
| T350 | **状态机复用**：`validateLifecycleTransition`（`:750-790`）对 verifier 事件逐字同语义（首个事实必须是 `activated`；`resurrection` 被拒；`not_after` 不得早于 `not_before`；终局取值 `revoked > rotated_out`） |
| T351 | **P41 零回归**：manifest 签名的生命周期判定（`signature_before_activation`/`_after_rotation`/`_after_revocation`/`_time_unparseable`）逐字节不变；P41 状态面的 `authorizations` **不含**验证者主体、`active_key_id` **不被**验证者事件改写（A8-⑦/⑨） |
| T352 | **P44 零回归**：`verification_unauthorized`/`key_unknown` 两轨判定不变；closed 模式（无 VAK）下 `problems` 通道仍为 nil、响应字节不变；`snapshot_verification.go` 的报告字节不变（A7-③） |
| T353 | **区间边界（与 P41 逐字同语义）**：`t == not_before` ⇒ **在位**；`t == not_after` ⇒ **不在位**（严格 `<` / `≥`，逐字对齐 `authorizeByLifecycle:552-580`）；两个相邻区间不互相串味；未解析成功的边界按 P41 语义处理（`parseRFC3339Nano` 失败 ⇒ 不猜测） |
| T354 | **`time_unparseable` fail-closed**：`signed_at` 不可解析 ⇒ `signature_time_unparseable`（P41 第四词）⇒ 该行 `indeterminate`，**绝不** `authorized`，**绝不** `nothing_assessed`（同 R41-7 理由：合法签名路径永不产生不可解析值；行级走全序首个命中，ADR-075 §3） |
| T355 | **零副作用**：读面不落盘、不写 audit、不发网络、不派发、不压缩（读前后文件字节与 mtime 不变） |
| T356 | **零新增族/路由（A3）**：注册表仍 6 族、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条、路由表零新增；T277/T283/T259/T331 四条既有枚举用例取值不变 |
| T357 | 变异 MU1~MU10（MU1 摘掉区间核对（⇒ T342/T344 必红）/ MU2 把 `unbounded` 并入 `authorized`（⇒ T345/T347 必红）/ MU3 摘 fail-closed 或域歧义复检（⇒ T346 必红）/ MU4 把 `after_revocation` 折叠进 `verification_unauthorized`（⇒ T348 必红）/ MU5 让写入面接受任意 key_id（⇒ T349 必红）/ MU6 用 `<=` 比较边界（⇒ T353 必红）/ MU7 让读面产生副作用（⇒ T355 必红）/ MU8 把验证者主体并入 P41 状态面的 `authorizations` 或让 `active_key_id` 被改写（⇒ T351 必红）/ **MU9 摘掉身份绑定（改用 `e.KeyID` 作签发者、不错配即 fail-closed）（⇒ T360 必红）** / **MU10 把行级判决改成顺序赋值（允许后置赋值覆盖 `indeterminate`）（⇒ T361 必红）**）⇒ 对应用例必红，sha256 还原 |
| T358 | **畸形输入不 panic**：空验证账本 / 空生命周期账本 / 零值条目 / 超长字段 / 非法 JSON / `key_id` 为空 / `signed_at` 为空 ⇒ 不 panic，走 fail-closed 或 `unbounded` |
| T359 | **删除保护复用 P49（A8-⑤）**：删除生命周期账本尾部包含 `revoked` 事件的行（锚定条目仍在）⇒ 本面报 `unbounded`/`authorized`（**如实登记本地不可判**）∧ **P49 兑现面报 `anchor_unrealized`**——同一用例断言两面同时成立，证明该保护是**复用**而非重复实现 |
| **T360** | **身份绑定（首轮评审 M1 / §4 A9，承重）**：构造一条 `key_id = B`（B 的区间**可断言且在区间内**）、`signature.key_id = A`（**A 已吊销**、仍在 verifier trust、私钥可得）的账本行，digest / prev / stream 全部自算 ⇒ ① 该行 **必落 `indeterminate`**（身份错配 fail-closed）；② 全局 `verifier_authority_state == "indeterminate"`、`verifier_authorized == false`；③ **绝不**出现「核对 B 的区间 ⇒ `authorized`」这一绕过路径（MU9 必红）。**同用例断言诚实路径（`:729`/`:1084` 同源）不受影响**：正常写入的一条记录其 `e.KeyID == e.Signature.KeyID`，判据照常产出 |
| **T361** | **行级全序首个命中（首轮评审 M2 / §3）**：同一 key 下同时存在 ① 一条 `authorized` 记录与 ② 一条 `signed_at` 不可解析的记录 ⇒ 该行 **必为 `indeterminate`**（不得被 `authorized > 0` 覆盖、不得退化为 `nothing_assessed`）、全局 `state == "indeterminate"` ∧ `verifier_authorized == false` ∧ `verifier_authority_claims` 的语义与 ADR-076 §4 一致（MU10 必红） |

## 6. 与既有 Phase 的关系

不降级任何判据。P41 的账本、区间解析与比较函数**被复用**但**不改语义**；P44 的判定**被读**但**不改**；P42 的报告**零字节变更**；P47/P49 对 `key_lifecycle` 族的投递与兑现**自动覆盖**新的验证者授权事件（同一族、同一锚定条目类型）。P50 的工程量集中在三处：**写入面主体集的显式扩张（含域歧义守卫）**、**以验证记录签发者为对象的区间核对**、**新组的取证面（计数 + 原因 + 与 P44 两轨的分离）**。**P50 补的是它们共同的前提**：P41 说「这把**签名**密钥当时有权」、P44 说「这条报告来自观察身份」、P49 说「锚定声明兑现了」——**三条都默认「观察身份当时有权签」**，而这一点此前无人断言。

## 7. 顺路清偿登记债（本 Phase 内）

> P49 §7 的 D8~D11 **已核实全部清偿**：`docs/adr/064-phase44-architecture.md:124` 与 `docs/adr/063-phase44-scope.md:158` 均已带 A-3 对齐补记；`docs/adr/060-phase43-scope.md:223` 与 `docs/adr/061-phase43-architecture.md:293` 均已带 D9 补记；`docs/adr/063-phase44-scope.md:128` 已带 D10 补记；`snapshot_anchor_delivery.go:90-92` 的注释已是 **six-family**（本轮逐行核对）。本轮盘点**未发现新的文档级错引**，但发现**四笔登记已陈旧**（前三笔是本 Phase 认领导致的必然重写，第四笔为本轮新核实的注释陈旧）：

| # | 债务 | 处置 |
|---|---|---|
| D12 | `docs/adr/073-phase49-scope.md:163`（候选 ② 裁定「**不淘汰，但本轮不选**……排序在 ① 之后」）在 ① 已完成后已陈旧 | 改写为「**由 P50 认领**（ADR-075 §3）」——排序理由已失效，登记与实况对齐 |
| D13 | `docs/adr/063-phase44-scope.md:128` 末句（P49 D10 补记「仍未认领 + 为何本轮不认领……排序在兑现面之后」）在兑现面完成后已陈旧 | 改写为「**由 P50 认领**（ADR-075 §3：`verifier_authority` 判据）；**销毁授权域的重审**仍未认领（ADR-063 Q1 的另一半，本 Phase 非目标 A7-④）」 |
| D14 | `docs/adr/063-phase44-scope.md:158` 与 `docs/adr/064-phase44-architecture.md:124` 的 A-3 补记「**持 VAK 私钥分支原样保留**」在 P50 后过强 | 两处补记追加：「**持 VAK 私钥分支部分闭合**：P50 的授权区间使**边界之后**的伪造报告不再 `signature_ok`（`after_revocation`/`after_rotation`）；**边界之内**的伪造与**持 KAK 私钥**者仍原样保留（ADR-075 §3 表 / A8-⑤）」——**不夸大** |
| D15 | **本轮新核实**：`internal/controlplane/server/snapshot_key_lifecycle.go:278` 注释原文「P37 trust set: WHO may ever be given a window」，在主体集按 A2 扩张后**不再是全貌**（验证者锚也在此集合内）；且 `:648-654` 的拒绝文案（`"key_id %q is not in the signing trust set"`）随之不再准确 | 注释与拒绝文案更新为「签名信任锚 ∪ 验证者信任锚，且二者不得相交（ADR-075 §3/A6）」；**只改注释与错误文案，不改判据语义**（T349 断言行为不变） |

## 8. 证据体系收敛判断

本轮对证据体系做了显式盘点：十一个问题面各有可断言面；P40~P49 的登记债已核实清偿；六族账本都上了证据链（P46 对账 / P47 投递 / P49 兑现）。**收敛的只是「问题面」在**第一把身份**上的完备性——P44 引入的第二把签名身份（VAK）至今没有任何时间维度**（§2 事实 1~9 逐行核实：写入面**不可表示**、读面**无消费点**、报告里的 `lifecycle` 维度指的是**别人**、P49 也不设防、身份绑定缺失（事实 8）），且它对应的正是一条**登记在册**的已知代价（ADR-063 §8-2 / Q1）与 P49 §9 **唯一**未淘汰的候选。故本轮**不收敛**。

**如实声明（不夸大）**：本条**不是**「新维度」，而是 P44 留下的**缺口**；它**不是** D9 式的「接入」（§3 R210 段的判别）；它**只部分**闭合 A-3 的 VAK 分支（D14）。

## 9. 方向候选裁定表（自拍板，R210 尺子）

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① **验证者授权生命周期**（本 Phase） | **采纳** | 真实缺口（§2 事实 1~9 逐行核实；ADR-063 §8-2/§9 Q1 登记在册；P49 §9 **唯一**未淘汰的候选，其排序理由「在 ① 之后」已失效）；新判据对「验证记录的签发者 × 签发时刻」此前**根本给不出**（事实 2/3/4/6），且其身份前提在本家族内**缺失**（事实 8，A9 闭合）；机制**零新账本、零新族、零新路由**，复用 P41 三件既有件（`:326`/`:428`/`:534`），工程量小而确定（P41/P44 的先例：独立主体面 + 守卫审计 + 一个新判据族 + 红例 + 字节等价自证 = 恰好一个 Phase） |
| ② 销毁授权域重审（ADR-063 Q1 的另一半） | **不淘汰，但本轮不选** | 与 ① 同源（KAK 授权域的扩张），但对象是 P43 的销毁记录签发者；并入 ① 会让「验证者授权」与「销毁授权」两个威胁模型互相拖累（ADR-063 §3 点 3 的同款理由）⇒ **排序在 ① 之后**，D13 登记 |
| ③ 呈现/运维面整合 | **淘汰（仍）** | 不产生此前给不出的判据（R210；同 P47/P48/P49 §9 裁定） |
| ④ HA 多副本 | **淘汰（仍）** | 「一致性不是证据性」（ADR-054 §1.1） |
| ⑤ 外部时间权威（TSA/RFC3161） | **淘汰（仍）** | 判据类别仍是「该 digest 是否被域外记录」＝ P40 第二实例；**且本 Phase 不依赖它**（A8-①） |
| ⑥ 见证端拉取（P46 逆操作） | **淘汰（仍）** | `family_incomplete` 已表达「本地有、域外无」；拉取只是同一判据的第二种取数方式 |
| ⑦ pre-export drop 记账（D9） | **淘汰（仍）** | 判据已存在（`runtime_dropped`/`file_dropped`/`Truncated`，`mgmt_obs.go:265-283`）⇒ 只是接入（R210 击倒；D9 已登记） |
| ⑧ P47 A8-⑦ 的 `destructionObserver` 并发缺口 | **不独立成 Phase（仍）** | 既有不变量的**加固**，不产生新判据（同 P48/P49 §9） |
| ⑨ **自动轮换/自动吊销调度** | **淘汰** | 是 ① 的**执行面**，不是判据面：没有 ① 时它无从判定，有 ① 后它只是策略 ⇒ 不产生新可断言判据（R210；A7-⑥ 非目标） |

⇒ **判断**：存在且**仅存在一个**符合 R210 尺子的候选（①）。其余候选被同一把尺子击倒（③④⑤⑥⑦⑨）或属既有维度加固（⑧）；②与本条同源但对象不同，登记为后继。

## 10. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「这是 P41 的**第二实例**，只是把同一机制接到另一把钥匙上 ⇒ R210 击倒」** | blocker-if-true | **不是第二实例，是缺口**：① 判据的对象不同——P41 的对象是 manifest 签名密钥，且它在读面**已经**可断言（`authorizeByLifecycle` 唯一调用点在 `history_export_manifest.go:596-597`）；本条的对象的验证记录签发者，在**任何面**都不可断言（事实 3/4），且 P42 报告里叫 `lifecycle` 的那一维指的是**别人**（事实 4）。② 判据本身**不可表示**：写入面在 `:648-654` 结构性拒绝为验证者记账（事实 2），D9 式的「判据已存在、只差接入」在本条**不成立**。③ 若按「同一机制用在第二个对象上 = 第二实例」读，则 P44（who 用在第二把身份上）与 P45（C2）也应当被击倒——本系列的先例不支持该读法（ADR-063 §3 / ADR-065 §1）。**本条的诚实边界另在 A8-②/④/⑤**：它确实**复用**P41 的机制（这正是「最小闭环」，不是隐藏）。 |
| **Q2「主体集扩张会不会动到 P41 的账本语义 ⇒ 既有账本被读坏？」** | major | A2 给出**零 schema 变更**的设计：不加字段 ⇒ `keyLifecycleSigned`/`canonicalLifecyclePayload`/`lifecycleEventDigest` 零改动 ⇒ 既有行 `event_digest` 重算逐字节相等（T340）；`loadKeyLifecycleState` 不检查主体集（本轮逐行核实）⇒ 旧代码读到新行也只会把它当作「一个它不认识的 key 的授权」，不影响任何既有取值。**代价**（域不随行存储）已在 A8-⑧ 与 A6 显式登记。 |
| **Q3「为什么不动 P42 报告，只在新组里出判据？」** | major | A7-③：P42 报告的 `lifecycle` 维度**已经**被 P41 占用（manifest 签名者），把第二个主体塞进同一个维度名会让读者混淆两个对象（ADR-058 §3.2-D2 的教训：报告级 ruler 与逐条证据不得互换）。⇒ 新主体只在新组里出现，报告零字节变更，既有读面零回归（T352）。这是**故意的分离**，不是遗漏。 |
| **Q4「删掉 revocation 事件怎么办？判据岂不是可以被人为洗白？」** | major | 承认且**不掩盖**（A8-⑤）：本地不可区分（`authorizationFor` 在事件缺席时返回向上无界的 ledger 区间）。保护**复用** P49：`kind=key_lifecycle` 锚定条目在锚定启用时抓住它（T359 断言两面同时成立）。**锚定+见证同时关闭 ⇒ 不可检测**，如实登记。 |
| **Q5「T340~T361 的判别力够吗？T342 是不是自说自话？」** | major | T342 的判别支点是「**同一用例**下 `t ≥ T` 与 `t < T` 两条记录**取值相反**，且 P42 报告字节与 P49 兑现取值**不变**」——若实现把判据退化成「看一眼有没有 revoked 事件」（与时间无关），两条记录会取到同一个值 ⇒ T342 必红；若实现把新判据塞进 P42 报告 ⇒ T352 必红。MU1/MU4 各自钉住这两条退化路径。 |

## 11. 评审闭合表（首轮，2 项 major）

| 发现 | 级别 | 闭合 |
|---|---|---|
| **M1** 新判据的「签发者」锚在一个**未与签名绑定**的自证字段上，被本 Phase 锁定的对手可一步绕过：P44 的验签只用 `sb.KeyID`（`snapshot_verification.go:775`），入队点 `:692` 前后**无任何** `e.KeyID != sb.KeyID` 检查（亲跑 `grep -rn "disagrees" internal/controlplane/server/*.go | grep -v _test` ⇒ 家族内其余三位都有：`snapshot_acceptance.go:251` / `snapshot_destruction.go:315` / `snapshot_key_lifecycle.go:257`，**verification 侧缺席**）⇒ 持已吊销 VAK-A 私钥者可构造 `key_id = <区间内另一把 key>` + `signature.key_id = A` 的行（`e.KeyID` 只被签名**覆盖**、可任填：`verificationSignedEntry.KeyID` `:318`/`:333`），`sigVerdictOK` 并进入 `st.entries`，本面遂去核对那把「区间内」的 key ⇒ 报 `authorized`，A 的区间外事实被完全掩盖；§3 表「持 VAK 私钥者、区间外伪造 ⇒ 非空泛」这条**承重声明**被击穿，核心卖点退化为「吊销后仍可洗白」 | major | ① §3 的 `verifier_authorized` 行把签发者改为 **`Signature.KeyID`**（并声明其**不是** `e.KeyID`）；② 新增 **§4 A9 身份绑定**（承重）：`e.KeyID != e.Signature.KeyID` ⇒ fail-closed `indeterminate`——校验落在**新文件**内，**不违反** A3 对 `snapshot_verification.go` 的零 diff 承诺；③ §3 表第 1 行显式声明「**前提 = A9**」并写出没有 A9 时的绕过路径（不再让承重声明裸奔）；④ **§2 新增事实 8**（含亲跑 grep 的逐行归类）并同步 §8/§9 的事实区间（1~8 → 1~9）；⑤ 新增 **T360**（含「**绝不**出现核对 B 的区间」的负断言）+ **MU9**；⑥ §4 A8-⑦ 增加**行为变更声明**：手工构造的错配行此前 `sigVerdictOK` 并进 `st.entries`，此后在本面判 `indeterminate`——诚实写入路径两者同源（`:729` `e.Signature.KeyID = s.keyID` / `:1084` `KeyID: signingKey.keyID`）⇒ 正常部署**永不触发**（T341/T352 覆盖），**不隐瞒这是一次有意的行为变更**。**评审建议的修法原样采纳，未做缩水。** |
| **M2** §3 把行级判决写成**顺序赋值**（`row = VIOLATED if …` / `row = AUTHORIZED if …` / `row = NOTHING_ASSESSED if checked == 0`），按字面执行方向是 **fail-open**：① 某 key 全部记录 `signed_at` 不可解析 ⇒ 循环内置的 `INDETERMINATE` 被 `NOTHING_ASSESSED` 覆盖（T354 丢失）；② 一条 `authorized` + 一条 `signed_at` 不可解析 ⇒ `checked=1 ∧ authorized=1 ∧ 违纪=0` ⇒ `row = AUTHORIZED` **覆盖** `INDETERMINATE` ⇒ 全局 `verifier_authorized == true`，直接违反本 ADR §5 T354 与 ADR-076 I7 | major | ① §3 **新增「行级（全序首个命中）」权威定义**：`indeterminate > violated > authorized > unbounded > nothing_assessed`，第 ⑤ 步标注**按构造不可达**（仅防御性兜底），并明写「**不得由顺序赋值产出**」；② 全局序保持与行级同构（`indeterminate > violated > authorized > nothing_assessed`）；③ **ADR-076 §3 ⑤ 伪码改为「先计数、后按全序裁决」**（不再在循环内写 `row`）、§3.1/§5 I7 同步；④ 新增 **T361**（同 key 混合：一条合法 + 一条不可解析 ⇒ 必为 `indeterminate`）+ **MU10**（顺序赋值 ⇒ 必红）；⑤ T354 补「**绝不** `nothing_assessed`」。 |
