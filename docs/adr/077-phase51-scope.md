# ADR-077 — Phase 51: Lifecycle Subject Domain（生命周期主体域 / 「这个窗口是发给谁的」）

- **Status**: Proposed (Phase 51, Scope stage)
- **Phase**: 51（Lifecycle Subject Domain）
- **Base**: Phase 50 CLOSED（HEAD = `fcf5506`）
- **Supersedes**: 无。不修改 P35~P50 的任何冻结判据；P41 的区间解析/比较（`authorizationFor` / `authorizeByLifecycle` / `validityOf`）与 `signature_*` 四词**逐字沿用**；P50 的授权面判据（`authorized` / `violated` / `unbounded` / `indeterminate` / `nothing_assessed` 与全局全序）**在既有输入上逐字沿用**；`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `history_export_manifest.go` / `appendonly_log.go` / `snapshot_verification.go` / `snapshot_anchor_realization.go` / 冻结五文件 / `internal/protection` **零 diff**；P42 报告、P44 判定、P49 兑现取值**零字节变更**。

---

## 1. 分工句（第十二问）

P37 who / P40 where / P41 when / P42 whether / P43 why-absent / P45 what-accepted / P46 whether-witnessed / P47 whether-delivered / P48 whether-recorded / P49 whether-realized / P50 whether-in-authority ⇒
**P51 = whose window is it（这条授权区间是发给**哪个域的身份**的）**：前十一个面依次问「证据是谁签的 / 放在哪 / 何时 / 是否验证过 / 为何缺席 / 接受了什么 / 域外副本是否一致 / 投递义务是否清偿 / 判定是否被留痕 / 锚定声明是否兑现 / 签发时该身份是否在位」。**其中 P41 与 P50 的判据都建立在「一条授权区间」上，而这条区间**从来没有说过它授权的是哪个域**：P50 明确把这条代价登记为「账本不存 role」（ADR-075 §4 A8-⑧），并声称迁移输入由 A6 的域歧义复检兜底。本轮亲跑证实：**A6 兜不住（§2 事实 6 的 probe 2），P41 的状态面在退役输入上失守（probe 1），P50 的承重判据接受一份从未为验证域签发的授权（probe 3）。**

## 2. 新问题（已核对代码，全部带行号；三条反例本轮亲跑复现）

**事实**：

1. **生命周期账本结构性地不存域，且这是被自认的**：`keyLifecycleConfig` 的注释原文（`snapshot_key_lifecycle.go:286-288`）「Phase 50 keeps the ledger SCHEMA unchanged: an event carries no role field, so its domain is resolved from these anchors, never stored in the row (A8-⑧ registers the resulting cost)」；`keyLifecycleSigned`（`:116-128`）的 11 个字段里**没有**任何域字段。⇒ 「窗口发给谁」在**证据里不存在**。
2. **读面只能拿「当前的信任文件」去猜域，而代码注释声称它不这么做**：`keyLifecycleSummary`（`:856`）在 `:897-901` 的过滤是 `if c.verifierTrust != nil { if _, inVerifier := c.verifierTrust.keys[k]; inVerifier { continue } }`——**这是当前 trust 文件**，不是账本；而紧邻的注释（`:886-896`）原文写着「This roll-up is therefore a pure function of the LEDGER, never of the current trust file」。⇒ **注释与它下面的代码相反**（事实 6 probe 1 是它的可复现后果）。
3. **写入面**确实**判了域，但判完就丢**：`appendKeyLifecycleEvent`（`:646`）在 `:664-677` 逐锚查表得到 `inSigning` / `inVerifier`，在 `:673` 拒绝「同时在两锚」（域不可判定）、在 `:681` 拒绝「都不在」——**这两个局部变量计算完后再未被写入任何字段**（`:678` 的 `case inVerifier:` 只做 `pub = vpub`；字段列表见事实 1）。⇒ 判定结果只用于准入，不成为证据。
4. **P50 的授权面对区间只按 key 取，完全不区域**：`snapshot_verifier_authority.go:323` `a := ls.authorizationFor(keyID)`；`:438` 的 `authorizationFor` 把该 key 的**全部**事件折成一个区间。⇒ 一个 key 的「签名域窗口」与「验证域窗口」在读取时不可分（事实 6 probe 3）。
5. **A6 的域歧义复检只查「当前」两锚的交集**：`snapshot_verifier_authority.go:140` `verifierTrustOverlap(a, b *exportTrustStore)` 的定义与 `:180` 的唯一调用点 `if overlap := verifierTrustOverlap(s.trust, s.verifierTrust); len(overlap) > 0`。⇒ 它只能抓住**同时**在两会中的 key；**顺序**迁移（第 N 次运行在 A 锚、第 N+1 次运行在 B 锚）两锚从不相交 ⇒ **A6 恒定沉默**。而 **ADR-075 §4 A8-⑧(b) 原文声称**「若未来某 key_id 在两个锚之间迁移，历史行的域归属会随之变化 ⇒ 本 Phase 以 A6 的域歧义 fail-closed 兜底」——**该兜底不存在**（probe 2 亲跑证伪）。

6. **三条可复现的反例（本轮亲跑；探针文件 `H:\zcode-workspace\.p51probe\zz_p51_probe_test.go`、`zz_p51_probe4_test.go`，全部用生产路径构造，跑完已从仓库移出）**：

   - **probe 1｜验证者退役泄漏进 P41 状态面**。命令 `go test ./internal/controlplane/server/ -run TestZZP51Probe -v`。同一目录、同一账本、同一签名者、同一 KAK：第 1 次运行配置 VAK1（`--export-verifier-key` = VAK1 + trust = [VAK1]），由 KAK 为 VAK1 开一个窗口；第 2 次运行**轮换验证者**（新 VAK2 + 新 trust 文件 = [VAK2]）。实测输出：
     ```
     run1 authorizations=[] active=""
     run2 authorizations=[{KeyID:650718620d102fad NotBefore:2026-11-01T00:00:00Z ... Source:lifecycle_ledger}] active="650718620d102fad"
     ```
     其中 `650718620d102fad` 就是**被退役的 VAK1**。⇒ ADR-075 §4 A8-⑦/A3 的承重承诺「验证者 key 首次出现在 `signing-key-log.jsonl` 后，P41 状态面的 `authorizations` **仍只列签名主体**（T351 钉死）」**在可达输入上为假**：T351 全程用同一份配置，从不改 trust 文件（`snapshot_verifier_authority_test.go:977-1040`），T362 的 `build()` 又**没有配 VAK**（`:1628`，`VerifierKeyPath` 未设 ⇒ `c.verifierTrust == nil` ⇒ 过滤分支根本不执行），⇒ 两条用例都够不到这个输入。
   - **probe 2｜顺序迁移不被 A6 捕获**。命令 `go test ./internal/controlplane/server/ -run TestZZP51Probe -v`。第 1 次运行：key M 在**验证者**锚中，为 M 开窗口；第 2 次运行：**同一个 key_id M** 移到**签名**锚中（从不同时在两锚 ⇒ 构造期守卫无话可说）。实测输出：
     ```
     run2 A6 overlap test = [] (empty means A6 stays silent)
     run2 (migrated) authorizations=[{KeyID:262a72b9cb56e52e ... Source:lifecycle_ledger}] active="262a72b9cb56e52e"
     ```
     ⇒ A6 沉默，且**验证者时代的窗口被读成签名主体的窗口**（`Source:lifecycle_ledger`）。**ADR-075 A8-⑧(b) 的兜底声明被证伪。**
   - **probe 3｜签名域窗口满足 P50 的验证域授权（承重）**。命令 `go test ./internal/controlplane/server/ -run TestZZP51ProbeSigning -v`。第 1 次运行：key M 在**签名**锚中，KAK 为 M 开窗口（未指定域）；第 2 次运行：M 迁到**验证者**锚并作为 VAK 签一条真实的验证报告（`AttestVerification`）。实测输出：
     ```
     run2 verifier_authority_state="authorized" authorized=true claims=1
       row a4bd1d059d140475 verdict=authorized checked=1 authorized=1
     ```
     ⇒ **P50 的核心判据 `verifier_authorized` 为真、`claims == 1`，而它引用的那条区间是**签名域**的授权**。**如实限定这是「未定义」而不是「实现偏离规格」**：按 ADR-075 §3 的字面定义（`not_before ≤ t < not_after`，域未提及），该输出是**符合规格**的——本条 Phase 要补的正是规格里缺的那一维（「这条区间授权的是哪个域」），不是修一个实现 bug。
   - **probe 4｜「不存 role」的代价被高估（承重，决定本条 Phase 的可行性）**。命令 `go test ./internal/controlplane/server/ -run TestZZP51ProbeTrailing -v`（**PASS**）。在 `keyLifecycleSigned` 的**末尾**追加一个 `omitempty` 字段后，旧行（该字段为空）的 `json.Marshal` 输出**逐字节不变**：
     ```
     legacy bytes identical ({"v":1,"event_seq":2,...,"prev_event_digest":"p"})
     new row={"v":1,"event_seq":2,...,"prev_event_digest":"p","role":"verifier"}
     ```
     ⇒ ADR-075 A8-⑧ 拒绝记录域的理由（「换来的是 canonical payload 零改动与既有账本逐字节自洽」）**在技术上不成立**：末尾 `omitempty` 字段**同时**满足「既有行 `event_digest` 重算逐字节相等」与「新行携带域」。**这是本条 Phase 存在的理由**——不是重开一个已被论证过的取舍，而是**该取舍的代价被实测推翻**。

7. **为什么今天全绿**：P41 的判据（窗口内/外）与 P50 的判据（在位/不在位）都只看「有没有窗口、时刻落不落进去」，都可以在一份**域错配**的证据上给出 `authorized`（probe 3）；P41 的状态面则把一份**退役验证者**的行当作签名主体列出（probe 1）。三个面（P41 状态面、P41 判定面、P50 授权面）**没有一个**能引用「这个窗口的域是什么」。

8. **同一根因在 P50 账本自描述性上的另一半**：P50 A8-⑧(a) 原文「读一份**离开部署**的账本无法独立判断某行是在授权 manifest 密钥还是验证者」。本条的读面把这一半也变成**可断言的**：域从证据里读出，而不是从部署的 live trust 文件里猜。

**悖论**：系统能断言「这条验证报告是谁签的」（P44）、「签它的身份在签它的时候有没有权」（P50）、「这个窗口几时到几时」（P41）——**唯独不能断言「这个窗口是发给谁的」**。而 P41 与 P50 这两条判据的**全部承重内容**都建立在这条区间上（P50 §3 的核心红例「吊销与保史两全」用的正是 `revocation` 边界），⇒ 一份**为另一个域签发**的区间可以冒充本域的授权，且这一点在**任何面**都不可断言。

## 3. 决策（Scope）

新增**生命周期主体域面**——**不新增密钥类型、不新增账本、不新增证据族、不新增写入路径、不新增路由、不新增常驻组件、不新增 API 参数**；把**写入面已经算出来、却被丢弃的域判定**（事实 3）落进事件，并新增一个**读派生面**把「这个窗口是发给谁的」变成可断言判据，同时把 P41 状态面与 P50 授权面**从「查当前 trust 文件」改为「读账本里的域」**。

**核心产出**：

| 新判据 | 条件 | 语义 |
|---|---|---|
| **`domain`**（主体级，值域 `signing` / `verifier` / `undeclared`） | 该主体在账本中的行**全部**携带 `role` 且一致 ⇒ 取该值；行**全部**无 `role` ⇒ `undeclared`（P51 之前的行）；行内 `role` 不一致 ⇒ 见 `conflict` | **此前给不出的判据**（事实 1/2/6-probe 3）：该 key 的授权区间属于哪个域，**从证据本身**可断言，不再依赖当前 trust 文件 |
| **`lifecycle_domain_state`**（全局标量） | 全序首个命中：① 任一主体 `conflict` ⇒ `conflict`；② 否则存在主体在**两个域**都有携带 `role` 的行 ⇒ `migrated`；③ 否则**至少一个**主体 `domain ∈ {signing, verifier}` 且无 `undeclared` 行 ⇒ `declared`；④ 否则（全部主体 `undeclared`，或账本为空）⇒ `undeclared` | 权威取值唯一在本 §3；Architecture 不得另立 |
| **`lifecycle_domain_conflict`**（主体级，值域新增） | 同一 `key_id` 的行**同时**含 `role=signing` 与 `role=verifier` 且**同一 `event_seq` 组内不一致**、或单行 `role` 不在值域内 | **fail-closed**：响亮报错、该主体不判、**绝不**报 `declared` |
| **`lifecycle_domain_migrated`**（面级 / 主体列表） | 同一 `key_id` 在**不同 `event_seq`** 上分别携带两个域的 `role`（顺序迁移，probe 2 的形状） | **如实报出**（不 fail-closed：两条事实各自可断言）；`migrated` **不**并入 `conflict`，**不**并入 `declared`（A5 不折叠） |
| **`lifecycle_domain_undeclared_events`**（int） | 账本中**没有** `role` 字段的行数（P51 之前的行） | **不可判的见证量**：这些行的域只能沿用旧语义（见 A4 的回退规则），绝不冒充 `declared` |
| **`lifecycle_domain_declared`**（bool，派生） | `= (lifecycle_domain_state == "declared")`——**派生便捷量，不得另行定义** | 「空集为真」在本定义下**不可能**（账本为空 ⇒ `undeclared`） |
| **`domain_mismatch`**（**P50 授权面**新增行级取值） | 该验证记录签发者的可断言区间来自**明确声明的签名域**（`domain == "signing"`） | **该区间不构成验证域授权**（probe 3 的形状）：**绝不算 `authorized`**、**绝不算 `violated`**（它不是越权，是**域错配**），**绝不折叠**进 `unbounded`（域是可断言的，不是不可判） |
| `lifecycle_domain_events` / `lifecycle_domain_subjects` / `lifecycle_domain_declared_events` | 见 ADR-078 §4 | 见证量 |
| P41 `signature_*` 四词 / P42/P44/P46/P47/P49 全部取值 | **逐字复用** | 不变 |
| P50 `authorized` / `violated` / `unbounded` / `indeterminate` / `nothing_assessed` | **逐字复用**；**仅**在「区间来自身明签名域」这一新输入上产出 `domain_mismatch` | 在既有输入上零回归（T380） |

**全局取值的权威定义（Scope 为准；Architecture 不得另立）**：

- **主体级 `domain`（全序首个命中，**不得**由顺序赋值产出）**：① 任一行 `role` 不在 `{signing, verifier}` 或**同一事件组内不一致** ⇒ `conflict`（fail-closed）；② 否则**存在** `role` 行 ⇒ 其值（`signing` / `verifier`；若两个域都存在 ⇒ 该主体同时是 `migrated`，`domain` 取**最后一条** `role` 行的值，并**同时**把该主体列进 `lifecycle_domain_migrated_keys`）；③ 否则（无任何 `role` 行）⇒ `undeclared`。
- **面级 `lifecycle_domain_state`（标量，全序首个命中，**不是合取**）**：`conflict > migrated > declared > undeclared`（取值名与上面的条件一一对应；`migrated` 由**任意**主体命中，`declared` 要求**至少一个**主体 `domain ∈ {signing, verifier}`）。
- **命名边界（消歧，沿用 ADR-075 §3 的纪律）**：主体级取值不加 `lifecycle_domain_` 前缀（`signing` / `verifier` / `undeclared` / `conflict`）；带前缀的是**全局字段**（`lifecycle_domain_state` / `lifecycle_domain_declared` / `lifecycle_domain_events` / `lifecycle_domain_subjects` / `lifecycle_domain_declared_events` / `lifecycle_domain_undeclared_events` / `lifecycle_domain_migrated_keys` / `lifecycle_domain_conflict_keys`；形状以 ADR-078 §4 为准）。**P50 授权面的新取值 `domain_mismatch` 沿用 P50 的命名纪律**（行级不加前缀，与 `violated` / `unbounded` 同族）。

**核心红例（此前给不出的端到端判据；三条都直接取自 §2 事实 6 的亲跑复现）**：

- **红例 A（退役，probe 1）**：验证者轮换（新 VAK + 新 trust 文件，同目录同账本）⇒ 被退役 VAK 的行在 P41 的 `authorizations` 中**仍不出现**、`active_key_id` **不被改写**；而 `lifecycle_domains` 面上该主体 `domain == "verifier"`。**今天 ⇒ 出现且改写**（probe 1 的实测输出）。**同时断言**：把该 VAK 的 `role` 抹掉（构造 P51 之前的行）⇒ 行为**回到今天**（`undeclared` 回退，T371）——证明这是**新增判据**，不是既有行为的另一种写法。
- **红例 B（迁移，probe 2）**：同一 `key_id` 先在验证域、后在签名域 ⇒ `lifecycle_domain_state == "migrated"` ∧ 该 key 出现在 `lifecycle_domain_migrated_keys` ∧ **同一用例显式断言 A6 的 `verifierTrustOverlap` 为空**（证明该保护是**新增**的，不是复用 A6）。
- **红例 C（域错配，probe 3）**：M 的窗口在**签名**域下发、M 之后作为 VAK 签一条报告 ⇒ P50 面该行 `domain_mismatch` ∧ 全局 `verifier_authority_state != "authorized"` ∧ `verifier_authorized == false`；**同一用例断言**该报告在 P42/P44 的判定**逐字节不变**（证明这是新增维度，不是既有面的复用），且**同一条窗口在 `lifecycle_domains` 面上仍如实报 `domain == "signing"`**（不因为被拒就当它不存在）。

**检测力必须分别声明（承重，不可省略）**：

| 对手 / 输入 | 本 Phase 的检测力 |
|---|---|
| **顺序迁移**（同一 keypair 从一锚移到另一锚，用**新**事件）| **非空泛**：新行携带 `role` ⇒ `migrated` 响亮、且 P41/P50 两个面按域取数（红例 A/B/C）。**前提 = 该迁移之后写入的**新**行带 `role`** |
| **迁移前写入的历史行（无 `role`）** | **空泛**：域不可从证据恢复 ⇒ 只能沿用旧语义（A4 回退），并计入 `undeclared` **响亮报出**。**如实声明，不夸大**（A8-②） |
| **退役验证者（probe 1 的形状）** | **非空泛**：只要该 VAK 的行是 P51 之后写的（带 `role=verifier`）⇒ 永不出现在 `authorizations`。**历史行**同上一行（空泛） |
| **持 KAK 私钥者** | **空泛**：可伪造任意 `role`（同 P43 逃逸 A-2 / P41 T177 家族）⇒ 拓扑代价，登记为 A8-④ |
| **持导出私钥者（manifest 侧）** | 与本 Phase 无关（域由 KAK 的锚成员关系派生，不由私钥持有者声明——A2） |
| **主机级攻击者可改 trust 文件** | **非空泛（这正是本条的目的）**：域判定改由**账本内的 `role`** 决定，故 trust 文件的**事后**改动不再改变既有行的域（红例 A）；但**写入时刻**的错配配置仍会写错 `role`（A8-①，如实登记） |

**R210 尺子（§9 详述）**：本条产出的是**此前根本给不出**的判据——今天**没有任何面**能把「一条授权区间」与「它授权的域」对上（事实 1 证明域在写入面**被判了但丢弃**、事实 2 证明读面只能拿 live trust 文件猜、事实 4/5 证明 P50 面不区域且 A6 抓不住迁移），且它是**两条既有承重判据（P41 的 when、P50 的 in-authority）的共同前提**。它不是复述（P41 的对象是时刻、P50 的对象是「当时在不在位」、A6 的对象是「**同时**在两锚」——三者都不是「这条区间属于哪个域」），不是加固（A6 只在**同时**相交时发火，本条的输入是**顺序**迁移与**退役**，A6 在其上恒定沉默——probe 2），不是呈现（不是换一种画法）。**它也不是 D9 式的「接入」**：域判定**在写入面被算出来后即被丢弃**（事实 3 的 `:664` 两个局部变量），读面**没有**任何判据可接入（事实 2：读面用的是 trust 文件而不是账本）。

## 4. 能力定义（A1~A9）

- **A1 域的载体（写入面落一条已算出的事实，零新账本、零新族）**：在 `keyLifecycleSigned`（`:116-128`）与 `keyLifecycleEntry`（`:98-114`）的**末尾**追加 `Role string \`json:"role,omitempty"\``，取值 `signing` / `verifier`，由 `appendKeyLifecycleEvent`（`:646`）在 `:664-677` **已经算出**的 `inSigning` / `inVerifier` 派生（`:673`/`:681` 的准入判据不变）。**零 API 参数**：调用方**不能**声明域（不新增 `keyLifecycleRequest` 字段），域是锚成员关系的**派生事实**，不是请求方的可任填声明（这是本 Phase 与「再加一个自证字段」的分界）。
- **A2 域的判定权在 KAK 的锚，不在私钥持有者**：`role` 由**写入时刻**的锚成员关系派生并随 KAK 签名一起进证据（`canonicalLifecyclePayload` 覆盖 `keyLifecycleSigned`，`:150-156`）。⇒ 域是**被授权的**（KAK 说「你在签名锚里」），不是**自证的**。
- **A3 零触碰（承重承诺）**：`snapshot_anchor.go` / `snapshot_witness_reconcile.go` / `snapshot_ledger.go` / `history_export_manifest.go` / `appendonly_log.go` / `snapshot_verification.go` / `snapshot_anchor_realization.go` / 冻结五文件 **零 diff**；`internal/protection` 零 diff；`go.mod`/`go.sum` 零改动；**P41 的 `authorizationFor` / `authorizeByLifecycle` / `validityOf` 三函数零改动**（域切分由**新文件**里的新函数完成，见 A5）；P42 报告、P44 判定、P46/P47/P49 取值零字节变更；P35~P50 判据取值在**既有输入**上零回归（T379/T380）。
- **A4 回退规则（承重，决定零回归）**：**没有 `role` 的行是「域中立」的**——它参与**任何**域的区间折叠，从而对 P51 之前的一切输入**逐字复现旧行为**：(a) P41 状态面把「无 `role`」的主体照旧列入 `authorizations`（T362 的退役恒等因此**保持**）；(b) P50 授权面照旧把「无 `role`」的区间当作可判区间（T342/T343/T344 因此**保持**）。⇒ **零回归是构造性的，不是靠特判**。**只有明确声明了 `role` 的行**才被域切分。
- **A5 域切分只在**新文件**里**：新增 `authorizationForDomain(st, keyID, role)`（新文件）——只折叠 `role` 匹配的行 + 全部无 `role` 的行；P41 的 `authorizationFor`（`:438`）**不动**（A3）。P41 状态面与 P50 授权面**改调**新函数：前者按「主体域」（用来决定是否列出），后者按 `verifier` 域（用来决定可达性 + `domain_mismatch`）。
- **A6 零副作用**：新面**严格只读**——`os.Stat` + 既有 load；不落盘、不写 audit、不发网络、不派发、不压缩（沿用 P46/P47/P49/P50 的纪律）。
- **A7 不折叠（承重）**：`domain_mismatch`（**域错配**：区间可断言，只是不属于这个域）与 `violated`（**越权**：属于这个域但时刻在窗口外）与 `unbounded`（**不可判**：域与时刻都无法断言）是**三个独立取值**，任何一方**不得**并入另一方（沿用 P41 R41-1「独立命名，绝不折叠，响度在状态里、类别在名字里」）。同理 `undeclared` 绝不并入 `declared`，`migrated` 绝不并入 `conflict`。
- **A8 已知代价**：
  ① **写入时刻的锚配置决定 `role`**：若 KAK 在一个**配错**的锚状态下写事件，`role` 会写错（同 P41 已知代价同源：授权事实由 KAK 与它的配置共同决定）。缓解只有「锚成员关系是构造期守卫的产物」（`history_export_scheduler.go:487-566` 的 V1~V4）；**不引入第二真相源**；
  ② **历史行（无 `role`）的域不可恢复**：P51 之前写入的行**永久**是 `undeclared`，只能沿用旧语义并响亮计入 `lifecycle_domain_undeclared_events`——**与 P41 T177 / P42 `verification_absent` / P48 A8-④「启用前的判定不可断言」同族**；**绝不**从 live trust 文件反推一个 `role` 塞回去（那正是本 Phase 要消除的猜测）；
  ③ **迁移主体的 `domain` 取最后一条 `role` 行**：语义是「它现在是哪个域」，历史行的域由**各自行**的 `role` 决定 ⇒ 同一主体可以同时在两个域的区间里成立，本 Phase **不**报错（两条事实各自可断言），只把 `migrated` 响亮列进面级；
  ④ **持 KAK 私钥者空泛**（§3 表）：拓扑代价，与 P43 A-2 / P50 A8-④ 同族；
  ⑤ **自证**：核对者与被核对者在同一进程（与全部六族同款，ADR-063 已知代价 1）；
  ⑥ **不追溯、不迁移**：**不**重写既有 `signing-key-log.jsonl`（不动一个字节，T363 钉死 `event_digest` 逐字节相等），**不**提供转换器（同 P44 Q2 的 fail-closed 纪律）；
  ⑦ **行为变更声明（穷举）**：① 新事件多一个 `role` 字段（旧行零字节变化，probe 4 / T363）；② 新增一个**只读面**（组）`key_lifecycle_domains`，其启用门与 `key_lifecycle` **完全相同**（⇒ **账本启用的部署**的状态文档**多一个顶层键**，**必须**同步更新 P50 的 `p50BaselineStatusKeys()` 基线，`snapshot_verifier_authority_test.go:417`）；③ 键生命周期账本**启用**时，P41 的状态面在**退役验证者**与**迁移**两类输入上取值改变（红例 A/B —— 这是**修**，且是 A3 承重承诺的兑现）；④ P50 授权面在**明确签名域**区间上新增 `domain_mismatch`（红例 C）——**既有输入上零变化**（T380）；
  ⑧ **不做销毁授权域的域扩展**（ADR-063 Q1 的另一半）：§9 裁定**淘汰**（其对象是 KAK 自身的授权，见 §9 ②），本 Phase 非目标 A7-⑤；
  ⑨ **不给 `lifecycle_domain_state` 加 `violated` 之类的强取值**：迁移与退役都是**合法运维动作**，本 Phase 判**是不是同一个域**，不判**该不该迁移**（「政策的执行面不是判据面」，同 P50 A7-⑥）。
- **A9 域与身份的绑定（承重，与 P50 A9 同构）**：`role` 进 `keyLifecycleSigned` ⇒ 被 KAK 签名覆盖；**读面一律以行内的 `role` 为准**，**绝不**在 `role` 缺失时回退到「查当前 trust 文件」（那正是 probe 1 的成因）。**不违反 A3**：所有域判定落在**新文件**内。
- **A7 非目标（重新冻结，11 条）**：① 不改 P41 账本结构与既有判定（`signature_*` 四词取值逐字节不变）；② 不改 P41 的三个区间函数；③ 不改 P42 报告任何字节（`lifecycle` 维度继续指 manifest 签名者）；④ 不改 P44 判定（`verification_unauthorized` / `key_unknown` 两轨不变）；⑤ 不重审销毁授权域（§9 ② 淘汰）；⑥ 不做自动轮换/吊销调度（同 P50 A7-⑥）；⑦ 不引入绝对时间权威；⑧ 不做 HSM/KMS；⑨ 不新增密钥类型 / 账本 / 证据族 / 路由 / 常驻组件 / **API 参数**；⑩ 不追溯重写既有账本、不提供转换器；⑪ 不改 P49 兑现面的任何判据。

## 5. 测试契约（T363~T383，21 例；T382 内 MU1~MU9）

| # | 断言 |
|---|---|
| T363 | 冻结面**逐文件 sha256 钉死**（含 `snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`/`snapshot_verification.go`/`snapshot_anchor_realization.go`）+ `internal/protection` 零 diff + `go.mod`/`go.sum` 零改动 + **P41 账本字节等价**：既有 `signing-key-log.jsonl` 每一行的 `event_digest` 重算**逐字节相等**（末尾 `omitempty` 字段的恒等，probe 4） |
| T364 | **默认部署零回归**：生命周期账本**未启用** ⇒ 两个组**整组缺席**、状态文档**逐字节不变**；账本启用 ⇒ 顶层键序列**等于**「P50 基线 + `key_lifecycle_domains`」（A8-⑦② 的穷举声明被机器化） |
| T365 | **写入面记录域（A1/A2）**：签名锚中的 key 写事件 ⇒ 行使 `role == "signing"`；验证者锚中的 key ⇒ `role == "verifier"`；**调用方无法声明域**（`keyLifecycleRequest` 零新增字段，类型层面钉死）；两锚都不在 / 同时在两锚的拒绝行为**逐字不变**（P50 T349 取值不变） |
| T366 | **域断言（核心新判据）**：三个主体（签名行、验证者行、无 `role` 的旧行）同账本 ⇒ `lifecycle_domains` 面逐主体报 `signing` / `verifier` / `undeclared` ∧ `lifecycle_domain_state == "declared"` ∧ `lifecycle_domain_declared == true` ∧ `lifecycle_domain_undeclared_events == 1` |
| **T367** | **红例 A（退役，probe 1 形状）**：VAK1 的窗口写入后轮换验证者（新 VAK + 新 trust 文件，同目录、同账本、同签名者）⇒ ① 被退役 VAK 的 key **不**出现在 `authorizations`；② `active_key_id` **不被改写**；③ `lifecycle_domains` 面上该主体 `domain == "verifier"`。**同用例断言**：抹掉 `role` 后行为回到今天（A4 回退，T371）⇒ 证明这是**新增**判据 |
| **T368** | **红例 B（迁移，probe 2 形状）**：同一 `key_id` 先验证域、后签名域（新事件）⇒ `lifecycle_domain_state == "migrated"` ∧ 出现在 `lifecycle_domain_migrated_keys` ∧ **同用例断言 `verifierTrustOverlap` 为空**（A6 沉默 ⇒ 保护是新增的，不是复用） |
| **T369** | **红例 C（域错配，probe 3 形状，承重）**：M 的窗口在**签名**域下发、M 迁入验证者锚并签一条真报告 ⇒ ① P50 面该行 `verdict == "domain_mismatch"`；② 全局 `verifier_authority_state != "authorized"` ∧ `verifier_authorized == false`；③ **绝不**出现「按签名域区间判 `authorized`」这一路径；④ **断言 P42 报告字节与 P44 判定不变**；⑤ `lifecycle_domains` 面上该主体仍如实报 `domain == "signing"` |
| T370 | **域冲突 fail-closed（A7）**：同一 `key_id` 的同一事件组内 `role` 不一致 / 单行 `role` 不在值域内 ⇒ 该主体 `conflict`、面级 `lifecycle_domain_state == "conflict"`、响亮报错、**绝不**报 `declared` |
| T371 | **A4 回退恒等**：**全部**行无 `role` 的账本 ⇒ ① `lifecycle_domain_state == "undeclared"` ∧ `lifecycle_domain_declared == false`；② P41 的 `authorizations`/`active_key_id` **等于** P50 的取值（含 T362 的退役输入）；③ P50 授权面**无** `domain_mismatch`、取值等于 P50 |
| T372 | **不折叠（A7/A5）**：同一构造分别产出 `domain_mismatch`（域错配）、`violated`（同域但越权）、`unbounded`（不可判）⇒ 三个**独立**取值且**同时可见**；`undeclared` / `migrated` / `conflict` 两两不折叠 |
| T373 | **非空泛与空集不为真**：(a) 至少一个主体有 `role` 且无 `conflict` ⇒ `state == "declared"` 且判据来源是**行内 `role`**（`declared_events > 0`），**不得**由「没找到反例」产出；(b) 账本为空 ⇒ `state == "undeclared"` ∧ `lifecycle_domain_declared == false` |
| T374 | **不可判必须响亮**：`undeclared` / `migrated` / `conflict` 三态**必须**带 `reason` 与计数；「不可判」绝不呈现为「没问题」 |
| T375 | **零副作用**：读面不落盘、不写 audit、不发网络、不派发、不压缩（读前后文件字节与 mtime 不变） |
| T376 | **零新增族/路由/参数（A3/A7-⑨）**：注册表仍 6 族、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条、路由表零新增、`keyLifecycleRequest` 字段集不变；T356 等既有枚举用例取值不变 |
| T377 | **P41 零回归 · 判定面**：`signature_before_activation` / `_after_rotation` / `_after_revocation` / `_time_unparseable` 四词在 manifest 面取值逐字节不变（`history_export_manifest.go` 零 diff ⇒ 结构性保证） |
| T378 | **畸形输入不 panic**：`role` 为空 / 非枚举值 / 超长 / 非法 JSON / 零值条目 / 空账本 ⇒ 不 panic，走 `conflict` 或 `undeclared` |
| T379 | **P41 零回归 · 状态面（A4）**：在**无 `role`** 的输入上，`authorizations`/`active_key_id` 逐字节等于 P50（**先红后绿**：实现前把域判定接到 live trust 文件上 ⇒ T367 必红） |
| T380 | **P50 零回归（A4）**：在**无 `role`** 的输入上 `verifier_authority_*` 全部取值不变（T342/T343/T344/T347/T353/T354/T361 原样通过）；**只有**明确签名域区间触发 `domain_mismatch` |
| T381 | **删除保护复用 P49（A8-⑤ 同款）**：删除账本中含 `role` 的行、锚定条目仍在 ⇒ 本地域面如实降级（`undeclared`）∧ P49 兑现面报 `anchor_unrealized`——同用例断言两面同时成立，证明是**复用**而非重复实现 |
| T382 | **变异 MU1~MU9**（MU1 摘掉 `role` 的写入（⇒ T365/T366/T367 必红）；MU2 把域判定改回「查 live trust 文件」（⇒ **T367 必红**——这正是 HEAD 的形状）；MU3 把 `undeclared` 并入 `declared`（⇒ T371/T373 必红）；MU4 把 `migrated` 并入 `conflict`（⇒ T368 必红）；MU5 把 `domain_mismatch` 并入 `unbounded` 或 `violated`（⇒ T369/T372 必红）；MU6 让 `domain_mismatch` 仍算 `authorized`（⇒ T369 必红）；MU7 让调用方可声明 `role`（⇒ T376 必红）；MU8 把域判定做成顺序赋值（⇒ T370 必红）；MU9 让读面产生副作用（⇒ T375 必红））⇒ 对应用例必红，sha256 还原 |
| T383 | **跨面同时可见（I5 同款）**：同一构造上 `lifecycle_domains` 面的域断言、P41 状态面的取值、P50 授权面的取值三者**互不掩蔽**；特别断言红例 C 中「P50 面拒绝」与「域面如实报 `signing`」同时成立 |

## 6. 与既有 Phase 的关系

不降级任何判据。P41 的区间解析与比较**被复用**但**不改语义**（A3/A5：新函数按域切分，旧函数零改动）；P50 的五个行级取值与全局全序**被复用**，只在**明确签名域**这一新输入上新增 `domain_mismatch`；A6 的域歧义复检**保持不变**（它继续覆盖「同时」在两会的情形，本 Phase 覆盖「顺序」迁移，两者非重叠、非替代）。P51 的工程量集中在三处：**写入面把已算出的域落进事件**（末尾 `omitempty` 字段，旧行零字节变化）、**读面的域断言（新面）**、**两个既有面从「查 live trust 文件」改为「读账本内的域」**。**P51 补的是它们共同的前提**：P41 说「这把密钥当时有权」、P50 说「这个观察者在签报告时在位」——**两条都默认「这条区间授权的是**这个**域」**，而这一点此前无人断言（事实 1~8）。

## 7. 顺路清偿登记债（本 Phase 内）

> P50 §7 的 D12~D15 **已核实全部清偿**：`docs/adr/073-phase49-scope.md:163` 已带「由 P50 认领」；`docs/adr/063-phase44-scope.md:128` 已带 D13 补记；`docs/adr/063-phase44-scope.md:158` 与 `docs/adr/064-phase44-architecture.md:124` 已带 D14 补记；`internal/controlplane/server/snapshot_key_lifecycle.go:286-288` 注释与 `:661`/`:673`/`:681` 拒绝文案已按 D15 更新；`snapshot_anchor_delivery.go:90-92` 已是 **six-family**。本轮盘点**未发现新的文档级错引**，但发现**四笔登记与实况不符**——其中两笔是本条 Phase 的**存在理由**：

| # | 债务 | 处置 |
|---|---|---|
| D16 | `docs/adr/075-phase50-scope.md:92`（A8-⑧(b)）声称「key_id 在两个锚之间迁移 ⇒ 本 Phase 以 **A6 的域歧义 fail-closed** 兜底」 | **证伪并改写**：A6 只查**当前**两锚的交集（`snapshot_verifier_authority.go:140`/`:180`），**顺序**迁移两锚从不相交 ⇒ A6 恒定沉默（probe 2 亲跑）。改写为「**由 P51 认领**（ADR-077 §3：`lifecycle_domain_migrated` + 行内 `role`）」 |
| D17 | `internal/controlplane/server/snapshot_key_lifecycle.go:886-896` 的注释声称该 roll-up「is therefore a pure function of the LEDGER, never of the current trust file」，而 `:897-901` 的过滤读的正是**当前** trust 文件 | 实现轮**改代码**（按行内 `role` 过滤 ⇒ 注释为真）；同时改写 `docs/adr/076-phase50-architecture.md:208`（§9 Q2 以「验证者 key 根本写不进去」论证恒等，而同一 ADR §1③ 恰恰**扩张**了写入面 ⇒ 该论证在 HEAD 上自相矛盾） |
| D18 | `docs/adr/075-phase50-scope.md:91`（A8-⑦）与 `docs/adr/076-phase50-architecture.md:163`（I1）把「`authorizations` 仍只列签名主体」记为「T351 钉死」 | **改写为不夸大**：T351 全程用同一份配置（`snapshot_verifier_authority_test.go:977-1040`），T362 的 fixture **未配 VAK**（`:1629-1648`）⇒ 两条用例都够不到「验证者退役 / 迁移」输入；该承诺的可达性由 P51 的 T367/T368 补上 |
| D19 | `docs/adr/063-phase44-scope.md:128` 末句仍把「**销毁授权域的重审**」登记为「仍未认领」 | **裁定并改写**：ADR-077 §9 ② 判定**淘汰**（该域的对象是 KAK **自身**的授权——销毁记录的签发者是 KAK，`snapshot_destruction.go:312`/`:647`，而 KAK 是授权的根，**任何**为它开窗口的动作都是自证 ⇒ 空泛，被 R210 击倒；另设 DAK 已由 ADR-061 §1.3 三点论证否决）⇒ 不再悬挂为候选 |

## 8. 证据体系收敛判断

本轮对证据体系做了显式盘点：十二个问题面各有可断言面；P50 的登记债已核实清偿（§7 首段逐条核对）；六族账本都上了证据链。**收敛的只是「问题面」的**列举**——P41 的「何时」与 P50 的「当时在不在位」这两条承重判据，其共同前提「**这条区间授权的是哪个域**」至今不可断言**（§2 事实 1~8 逐行核实 + 三条亲跑复现：写入面判了域却丢弃、读面拿 live trust 文件猜、A6 对顺序迁移恒定沉默、P50 的承重判据接受签名域授权），且 P50 **自己**把这条代价登记为 A8-⑧ 并**声称**已由 A6 兜底（§7 D16 证伪），同时其为「不记录域」给出的价格（canonical payload 零改动）**被 probe 4 实测推翻**。⇒ 这不是「重开一个已论证过的取舍」，是**取舍的代价不成立**。故本轮**不收敛**。

**如实声明（不夸大）**：本条**不是**「新维度」（域是 P41/P50 已有区间的属性，不是新证据面），也**不是** P50 缺陷的修补——按 ADR-075 §3 的**字面**定义，probe 3 的输出是**符合规格**的（规格里没有域这一维）；本条补的是**规格缺的那一维**。它**只**覆盖「P51 之后写入的行」的域，**历史行永久 `undeclared`**（A8-②，与「启用前不可断言」同族）。

## 9. 方向候选裁定表（自拍板，R210 尺子）

| 候选 | 裁定 | 依据 |
|---|---|---|
| ① **生命周期主体域**（本 Phase） | **采纳** | 真实缺口（§2 事实 1~8 逐行核实 + probe 1/2/3 亲跑复现；ADR-075 A8-⑧ 登记在册且**其自称的兜底被 probe 2 证伪**，其拒绝理由被 probe 4 推翻）；新判据 `domain` / `lifecycle_domain_state` / `domain_mismatch` 对「这条区间发给谁」此前**根本给不出**（事实 1 写入面判了域却丢弃、事实 2 读面只能猜、事实 4/5 P50 面不区域且 A6 抓不住顺序迁移）；它是 P41（when）与 P50（in-authority）两条承重判据的**共同前提**；机制**零新账本、零新族、零新路由、零新 API 参数、零新密钥类型**，复用 P41 的全部读取原语与 P50 的全序纪律，工程量小而确定（P41/P50 的先例：写入面落一个派生字段 + 一个新面 + 两个既有面改取数源 + 红例 + 字节等价自证 = 恰好一个 Phase） |
| ② **销毁授权域重审**（ADR-063 Q1 的另一半，P50 §9 ② 遗留） | **淘汰** | 该域的对象是 **KAK 自身**的授权：销毁记录的签发者与验证者都是 KAK（`snapshot_destruction.go:312` 原文「not in the trusted KAK set」、`:647` `e.AuthorityKeyID = c.ka.signer.keyID`），而 KAK 是**授权的根**（P41 的 KAK 面即「谁可以给窗口」）。**为根开窗口 = 自证**（KAK 签自己的窗口）⇒ 判据空泛，被 R210 击倒；唯一让它非自证的路是**另设 DAK**，而 ADR-061 §1.3 已以三点论证否决（「DAK 防的对手已具备等价破坏力」/「不产生新维度判据，只是把门槛再抬一级」/「不配占一个密钥位」）⇒ 重开需新证据，本轮**无**新证据（D19 登记） |
| ③ 呈现/运维面整合 | **淘汰（仍）** | 不产生此前给不出的判据（R210；同 P47/P48/P49/P50 §9 裁定） |
| ④ HA 多副本 | **淘汰（仍）** | 「一致性不是证据性」（ADR-054 §1.1） |
| ⑤ 外部时间权威（TSA/RFC3161） | **淘汰（仍）** | 判据类别仍是「该 digest 是否被域外记录」＝ P40 第二实例；**且本 Phase 不依赖它**（自证时间 A8-⑤ 同款） |
| ⑥ 见证端拉取（P46 逆操作） | **淘汰（仍）** | `family_incomplete` 已表达「本地有、域外无」；拉取只是同一判据的第二种取数方式 |
| ⑦ pre-export drop 记账（D9） | **淘汰（仍）** | 判据已存在（`runtime_dropped`/`file_dropped`/`Truncated`，`mgmt_obs.go:265-283`）⇒ 只是接入（R210 击倒） |
| ⑧ P47 A8-⑦ 的 `destructionObserver` 并发缺口 | **不独立成 Phase（仍）** | 既有不变量的**加固**，不产生新判据（同 P48/P49/P50 §9） |
| ⑨ **追溯重写既有账本、把历史行补上 `role`** | **淘汰** | 会让 `event_digest` 变化 ⇒ 破坏「既有账本逐字节自洽」与 KAK 签名（等于替历史重新签名 = 洗白）；且与 P44 Q2 / P48 A8-④「不追认启用前的判定、不从旁路反推」同族。⇒ **A8-② 如实登记为永久边界** |
| ⑩ **在 `keyLifecycleRequest` 上开放 `role` 由调用方声明** | **淘汰** | 把「被授权的域」降级为「自证的声明」，正面违反 A2 与 P50 A9 建立的「签发者不得自证」纪律（同 P50 首轮 M1 的教训） |

⇒ **判断**：存在且**仅存在一个**符合 R210 尺子的候选（①）。②③④⑤⑥⑦⑩ 被同一把尺子击倒，⑧ 属既有维度加固，⑨ 属破坏证据链的洗白路径。

## 10. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「A8-⑧ 已把「账本不存 role」论证为**有意的**取舍（换来 canonical payload 零改动），本条是重开一个已裁决的取舍 ⇒ 不构成缺口」** | blocker-if-true | ① **代价被实测推翻**：probe 4 证明末尾 `omitempty` 字段让旧行**逐字节不变**（`event_digest` 重算相等）⇒ 「零改动」这个价格是**假的**，取舍的前提不成立。② **自称的兜底被证伪**：A8-⑧(b) 说迁移由 A6 兜底，probe 2 实测 A6 沉默（它只查**同时**相交）。③ **承重承诺已在可达输入上失守**：probe 1 实测退役验证者进入 P41 的 `authorizations`，违反 ADR-075 A3/A8-⑦。⇒ 这**不是**「重开一个已论证的取舍」，是**该取舍的两条支撑（代价 / 兜底）都不成立**。本条**不**主张推翻「不引入新账本」的取舍（本 Phase 确实零新账本）。 |
| **Q2「这是不是 P50 的实现 bug，改几行就行、不配一个 Phase？」** | major | **不是 bug，是规格缺一维**：按 ADR-075 §3 的**字面**定义（`not_before ≤ t < not_after`，未提域），probe 3 的 `authorized` 是**符合规格**的输出；probe 1 的过滤也正是 ADR-076 §1③ 与 §8 Q2 **明文规定**的形状（`∉ verifierTrust`）。要让它「错」，必须先有「域」这一维 ⇒ 先有本 Phase。且改法**不是**几行：域必须进**证据**（A1/A2/A9），否则 P41 的退役恒等（T362 要求仍列出）与验证者退役（T367 要求不列出）在**同样的可观测量**上给出**相反要求**（无 `role` 时二者不可分）⇒ **任何**纯读面改法都必然违反其中一条。**这是本条 Phase 的存在证明**。 |
| **Q3「`domain` 会不会变成第三个自证字段（同 P50 首轮 M1 的教训）？」** | major | **不会**：`role` **不是**调用方可填的参数（A1：`keyLifecycleRequest` 零新增字段，T376 类型层面钉死），而是 `appendKeyLifecycleEvent` 在 `:664-677` **已算出的锚成员关系**的派生值，且随 KAK 签名进证据（A2/A9）。⇒ 它断言的是「**KAK 当时认为你在哪个锚**」，与 `AuthorityKeyID` 同级。**代价如实登记**（A8-①：写入时刻配错锚 ⇒ 写错 `role`），**不夸大**。 |
| **Q4「历史行永久 `undeclared`，那这条判据对存量部署是不是没有价值？」** | note | 对**存量行**没有（A8-②，同「启用前不可断言」）。价值集中在**发生过退役/迁移**且**在那之后写入**了事件的部署——而 probe 1/2/3 证明的正是那正是 A3 承重承诺真实失守的场景。**不声称覆盖面，只声称「此前给不出的那条判据现在给得出」**（P50 §9 Q5 同款纪律）。 |
| **Q5「T367~T369 的判别力够吗？会不会是自说自话的用例？」** | major | 三条都是**先红后绿**：它们在 HEAD 上**实测为红**（probe 1/2/3 的输出就是判据），修复后绿 ⇒ 不可能自说自话。MU2（把域判定改回查 live trust 文件）**就是 HEAD 的形状** ⇒ T367 必红；若实现把 `domain_mismatch` 仍算 `authorized` ⇒ T369 必红（MU6）。 |
