# ADR-076 — Phase 50: Verifier Authority Lifecycle（Architecture）

- **Status**: Proposed (Phase 50, Architecture stage)
- **Base**: ADR-075（Scope）。冲突以 Scope 为准。
- **前置**：Phase 49 CLOSED（HEAD = `8188879`），最大既有 T 编号 = **T339**（`snapshot_anchor_realization_test.go`，P49 §5），故本 Phase 用 **T340~T362**（T362 由实现轮终审修复 `fcf5506` 补入；ADR-076 原文写 T340~T361，本次修正为实测值）；**T385** 由后续修复轮补入（债 D20 的清偿，见 ADR-077 §4 A8-⑪）。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_verifier_authority.go` | **新增** | 验证者授权面：验证账本 × 生命周期账本的交汇 / **签发者身份绑定（§2 脚注，M1）** / 逐签发者区间核对（复用 `authorizationFor` + `authorizeByLifecycle` 语义）/ **行级全序裁决（M2）** / 域歧义复检 / 多态判别 / 状态面派生 |
| `internal/controlplane/server/snapshot_verifier_authority_test.go` | **新增** | T340~T362（23 例） |
| `internal/controlplane/server/snapshot_key_lifecycle.go` | **修改** | ① `keyLifecycleConfig`（`:274-283`）新增 `verifierTrust *exportTrustStore`；② `appendKeyLifecycleEvent`（`:636`）主体判据由「`∈ signingTrust`」改为「`∈ signingTrust ∪ verifierTrust` ∧ **不得同时在两者中**」（`:648-654`）；③ `keyLifecycleSummary` 的枚举**只加一个**过滤——「不在验证者锚中」（`∉ verifierTrust`）；**不加**「在签名锚中」那半边（终审 M3：它对**退役**输入不是恒等——旧代码遍历 `st.byKey`、无 trust 过滤，而 trust 文件会变，见 ADR-075 §4 A3 / T362）⇒ 枚举仍是**账本的纯函数，永不依赖当前 trust 文件**；④ `:278` 注释与 `:649`/`:652` 拒绝文案更新（D15）。**账本结构 / 规范序列化 / 既有判定零变更** |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | ① `keyLifecycleConfig()`（`:637-651`）填 `verifierTrust: s.verifierTrust`；② `Status()`（`:1526`）新增 `VerifierAuthority` 组（全 `omitempty`，与 `KeyLifecycle`（`:1564`）同受「verifier 已配置」约束） |
| `snapshot_verification.go` · `snapshot_anchor.go` · `snapshot_witness_reconcile.go` · `snapshot_ledger.go` · `history_export_manifest.go` · `snapshot_signature.go` · `snapshot_chain.go` · `history_export_coverage.go` · `appendonly_log.go` · `snapshot_anchor_realization.go` · 冻结五文件 · `internal/protection/**` · `go.mod`/`go.sum` | **零 diff** | A3 承重承诺（**特别**：P42 报告零字节变更 A7-③；P44 判定零变更 A7-②；P41 `authorizeByLifecycle` 唯一调用点 `history_export_manifest.go:596-597` 不动） |

## 2. 两条取数面（全部只读，行号本轮亲跑核对）

| 面 | 取数函数 | 本 Phase 需要的字段 |
|---|---|---|
| 验证账本 | `loadVerificationState(s.verificationConfig())`（`snapshot_verification.go:619`；配置装配 `:1127`） | `st.entries`（`:587`；**只含通过** P44 签名 / 自身 digest / 链 / stream 检查的记录，入队点 `:692` 是唯一一处）→ `ReportSeq`（`:292`）、**签发者 = `Signature.KeyID`**（`:303` 的 `Signature`；**注意 `:300` 的 `KeyID` 不是签发者**，见下表脚注）、`Signature.SignedAt`；`st.verifiable`（`:589`）、`st.errs`（`:590`）、`st.total`（`:591`）；签名检查点 `:657`，信任查表 `:775`（`trust.keys[sb.KeyID]`） |
| 生命周期账本 | `loadKeyLifecycleState(s.keyLifecycleConfig())`（`snapshot_key_lifecycle.go:326`；配置装配 `history_export_scheduler.go:637-651`） | `st.byKey`（逐 key 事件）、`st.window`（`:296-301`）、`st.verifiable`、`st.errs`；区间解析 `authorizationFor(keyID)`（`:428-484`）、比较 `authorizeByLifecycle(v, signedAt, a)`（`:534-580`） |

**本 Phase 不新增任何读取原语**：`authorizationFor` / `authorizeByLifecycle` / `loadVerificationState` 三者**逐字复用**，新文件只做「取数 → 交织 → 归类 → 计数」。P41 的区间语义（`:552-580`：`t.Before(not_before)` ⇒ `before_activation`；`!t.Before(not_after)` ⇒ 终局取值 `revoked > rotated_out`；`parseRFC3339Nano` 失败 ⇒ `time_unparseable`；`Source != ledger ∨ ¬Complete` ⇒ 不判定、只带 `validityOf(a)`）**必须逐字沿用**（I2）。

**脚注（首轮评审 M1，承重）**：`verificationLogEntry.KeyID`（`:300`）**不是签发者**。P44 的验签只用 `Signature.KeyID`（`:775` `pub, known := trust.keys[sb.KeyID]`），而 `e.KeyID` 只被签名**覆盖**（`verificationSignedEntry.KeyID`，`:318`/`:333`）——即它是签名覆盖区里一个**可任填**的字段，**不与签名者身份绑定**；入队点 `:692` 前后没有任何 `e.KeyID != e.Signature.KeyID` 检查（本轮亲跑 `grep -rn "disagrees" internal/controlplane/server/*.go | grep -v _test` ⇒ 本家族其余三位都有绑定校验：`snapshot_acceptance.go:251` / `snapshot_destruction.go:315` / `snapshot_key_lifecycle.go:257`；verification 侧一行也没有）。诚实写入路径两者同源（`:729 e.Signature.KeyID = s.keyID`、`:1084 KeyID: signingKey.keyID`）⇒ 正常部署永不触发。⇒ 本 Phase 的签发者**一律**取 `Signature.KeyID`，错配走 fail-closed（I11 / §3 ①.0）。

## 3. 多态判别（本 Phase 的核心机制）

```
authorityFor(s *HistoryExportScheduler) authorityStatus:
  # ⓪ 未配置验证者 ⇒ 整组缺席（A3/I3）：没有 `--export-verifier-key`/`--export-verifier-trust`
  #    就没有任何「验证者授权」可判 —— 不是「不可判」，是「不适用」。
  if !s.verifierConfigured():                        return ABSENT      # 组不入文档

  # ① 域歧义复检（A6，承重）：守卫是构造期事实，账本来自磁盘。同一 key_id
  #    同时落在两个信任锚 ⇒ 这一行（以及它的每一条事件）的域不可判定 ⇒ 整面
  #    fail-closed。绝不猜「它更像签名密钥还是验证者」。
  if overlap := trustOverlap(s.trust, s.verifierTrust); len(overlap) > 0:
                                                     return INDETERMINATE("key(s) in two trust anchors: …")

  # ② 验证账本：加载（不可验/不可分类行/digest/链/stream 任一失败 ⇒ fail-closed）
  vs, err := loadVerificationState(s.verificationConfig())
  if err != nil:                                     return INDETERMINATE(err)
  if !vs.verifiable:                                 return INDETERMINATE(vs.errs)
  # ③ 空观测集无需读生命周期账本（I9，与 P49 I13 同款纪律）：没有任何可用记录
  #    ⇒ 没有任何签发者可核对 ⇒ nothing_assessed。否则「一个从未签发过报告的部署」
  #    会被一份它根本不需要的账本拖成 indeterminate。
  if len(vs.entries) == 0:                           return NOTHING_ASSESSED("verification log holds no usable entry")

  # ④ 生命周期账本：加载（失败或不可验 ⇒ 每一行 unbounded 还是 indeterminate？）
  #    —— 见 I4 的判别：账本「读不到」是 fail-closed（indeterminate）；
  #    账本「读了但证明不了区间」是不可判（unbounded）。两者语义不同，绝不合并。
  ls, lerr := loadKeyLifecycleState(s.keyLifecycleConfig())
  if lerr != nil || (ls != nil && !ls.verifiable):   return INDETERMINATE(...)

  # ⑤ 逐签发者判决。**签发者 = Signature.KeyID**（**不是** e.KeyID——首轮评审 M1，
  #    §2 脚注 / I11）；观测集 = 可用记录中出现过的签发者（去重、升序）。
  #    **只计数、不在循环里写 row**（首轮评审 M2：在循环里写 row 就是顺序赋值，
  #    后置赋值会覆盖 INDETERMINATE，方向是 fail-open）。
  for each keyID in observedSigners(vs.entries) (升序):
      # ①.0 身份绑定（I11/A9）：错配 ⇒ 本行 fail-closed —— **绝不**去核对 e.KeyID
      #      指向的那把 key 的区间（那正是 M1 的绕过路径：把 key_id 填成区间内的
      #      另一把身份，即可让本面替真实签发者「洗白」）。
      if ∃ e signed by keyID with e.KeyID != e.Signature.KeyID:
                                    cnt.indeterminate++; rowReason = "key_id disagrees with the signing key id"; goto verdict
      intervalAssertable := false
      a := ls.authorizationFor(keyID)                 # 复用 P41，:428
      if a.Source == lifecycleSourceLedger && a.Complete:
          intervalAssertable = true
          for each entry e signed by keyID:
              t, terr := parseRFC3339Nano(e.Signature.SignedAt)
              if terr != nil:       cnt.indeterminate++; rowReason = "signed_at is not parseable"; continue   # R41-7 第四词
              v := authorizeByLifecycle({Verdict: sigVerdictOK}, e.Signature.SignedAt, a)   # 复用 P41 比较语义
              switch v.Verdict:
              case sigVerdictOK:                     cnt.authorized++
              case sigVerdictBeforeActivation:       cnt.before_activation++
              case sigVerdictAfterRotation:          cnt.after_rotation++
              case sigVerdictAfterRevocation:        cnt.after_revocation++
              case sigVerdictTimeUnparseable:        cnt.indeterminate++
              default:                               cnt.indeterminate++    # 未知取值同样是 fail-closed
              cnt.checked++                                                 # = 真正被核对过的条数
      # 区间不可断言 ⇒ 循环一次也不进 ⇒ checked == 0（「不可判不算核对」，I6）
      verdict:                              # **全序首个命中**（ADR-075 §3；**不得**由顺序赋值产出）
          if   cnt.indeterminate > 0:                        row = INDETERMINATE
          elif (before_activation + after_rotation + after_revocation) > 0:  row = VIOLATED
          elif cnt.authorized > 0:                           row = AUTHORIZED
          elif !intervalAssertable:                          row = UNBOUNDED      # 不可判：绝不算 authorized，也绝不算 violated
          else:                                              row = NOTHING_ASSESSED   # 按构造不可达（防御性）

  # ⑥ 全局：标量、全序首个命中（ADR-075 §3，**不是合取**）
  state = NOTHING_ASSESSED
  if     ∃ row: row.verdict == INDETERMINATE:  state = INDETERMINATE
  else if ∃ row: row.verdict == VIOLATED:      state = VIOLATED
  else if ∃ row: row.verdict == AUTHORIZED:    state = AUTHORIZED
  # 否则保持 NOTHING_ASSESSED（全部行皆 NOTHING_ASSESSED 或 UNBOUNDED）
  verifier_authorized        := (state == AUTHORIZED)          # 派生便捷量，不得另行定义
  verifier_authority_claims  := Σ row.checked                  # AUTHORIZED ⇒ claims > 0
  verifier_authority_violations := Σ (before_activation + after_rotation + after_revocation)
  verifier_authority_unbounded_keys := {row.keyID | row.verdict == UNBOUNDED}
```

### 3.1 判别支点（承重）

| 观测 | 判决 | 依据 |
|---|---|---|
| 区间可断言（`Source=ledger ∧ Complete`）∧ `t < not_before` | `before_activation` ⇒ 行 `violated` | `authorizeByLifecycle:552-560`（严格 `Before`） |
| 区间可断言 ∧ `t ≥ not_after` ∧ 终局 `revoked` | `after_revocation` ⇒ 行 `violated` | `:562-571`（`!t.Before(na)`，含相等） |
| 区间可断言 ∧ `t ≥ not_after` ∧ 终局 `rotated_out` | `after_rotation` ⇒ 行 `violated` | `:572-580`；**不得**折叠进 `after_revocation`（A5/R41-1） |
| 区间可断言 ∧ 区间内 | `authorized`（计数 +1） | 该 key 的全部记录都在位 |
| 该 key **无**事件 / 激活被压缩掉 / 任一事件落在保留下界之外 | `unbounded` | `authorizationFor:433-438`（无事件）/ `:459-463`（激活缺席）/ `:477-483`（`Complete=false`）——**不可判，绝不算 authorized** |
| 生命周期账本**读不到**（load 报错）或含不可验条目 | `indeterminate`（整面） | fail-closed（I4）；与上一行的**语义不同**：**「不可用」不等于「不可判」** |
| 域歧义（同一 key_id 在两个信任锚） | `indeterminate`（整面） | A6；守则是构造期的，账本是磁盘上的 |
| **`e.KeyID != e.Signature.KeyID`（身份错配，**首轮评审 M1**）** | 该行 `indeterminate` ⇒ 面级 `indeterminate` | I11/A9；**绝不**改去核对 `e.KeyID` 所指那把 key 的区间（那正是绕过路径） |
| **同 key 混合**：一条在区间内 + 一条 `signed_at` 不可解析 | 该行 `indeterminate`（**不得**被 `authorized > 0` 覆盖，**不得**退化为 `nothing_assessed`） | 行级全序首个命中（ADR-075 §3 / I12；**首轮评审 M2**） |
| 观测集为空 | `nothing_assessed`（**不读生命周期账本**） | I9（对标 P49 I13） |

### 3.2 与 P41 / P44 / P42 / P49 的分工（不可混写）

- **P41** 的区间解析与比较**被逐字复用**，但 P41 在 **manifest** 面产生的取值**一字不改**（`history_export_manifest.go` 零 diff ⇒ 结构性保证）。
- **P44** 回答「这条报告来自哪个身份 / 是不是这个家族的」（`:757-790`：`verification_unauthorized` 是**异族**已知钥、`key_unknown` 是**不认识的**身份）；本 Phase 回答「**这个**身份**当时**有没有权」。⇒ 同一份输入上，**两个取值必须同时可见且互不掩蔽**（I5）。
- **P42** 报告**零字节变更**：其 `lifecycle` 维度继续由 `sigKeyIDOf(res)`（`:965-970`）取 **manifest 签名者**（`:852`），本 Phase 不写它（A7-③）。
- **P49** 兑现面**不改**：它核对「锚定条目 ↔ 账本行摘要」，**不核对签发者身份**；本 Phase 是它的**正交**补充（`snapshot_anchor_realization.go` 零 diff）。**同时是它的下游保护**：删掉 revocation 事件时 P49 报 `anchor_unrealized`（T359）。

## 4. 状态面（只读，零副作用）

- 新增一个**独立组** `verifier_authority`（全 `omitempty`；**未配置验证者 ⇒ 整组缺席** ⇒ 默认部署状态文档逐字节不变，T341）：

```
verifier_authority = {
  keys: {                        // 观测集：可用验证记录中出现过的**签发者**（= Signature.KeyID，升序）
    <signer_key_id>: {
      verdict,                   // authorized | violated | unbounded | indeterminate | nothing_assessed
      entries, checked,          // 该签发者名下可用记录条数 / 其中**真正被核对**（区间可断言 ∧ 时刻可解析）的条数
      authorized, before_activation, after_rotation, after_revocation, indeterminate,   // 计数
      validity: { authorized_from?, authorized_until?, terminal_event?, source },   // 复用 P41 validityOf（:509-518）
      reason?                    // unbounded / indeterminate / nothing_assessed 的响亮原因
    }, ...
  },
  verifier_authority_state,                 // 标量，全序首个命中（ADR-075 §3）：
                                            // indeterminate | violated | authorized | nothing_assessed
  verifier_authorized: <bool>,              // 派生：state == "authorized"（不得另行定义）
  verifier_authority_claims: <int>,         // Σ checked = 本次读**真正被核对过区间**的条数
  verifier_authority_entries: <int>,        // Σ entries = 观测到的可用记录条数（见证量，不是断言量）
  verifier_authority_violations: <int>,     // Σ (before_activation + after_rotation + after_revocation)
  verifier_authority_unbounded_keys: [<signer_key_id>...],      // 区间不可断言 ⇒ 不可判
  verifier_authority_violating_keys: [<signer_key_id>...],
  verifier_authority_indeterminate_keys: [<signer_key_id>...],
  reason?                                   // 面级 fail-closed 的原因（域歧义 / 身份错配 / 账本不可验）
}
```

- **`checked` 的定义是承重的**：它只统计「区间可断言 ∧ 时刻可解析」的记录。`unbounded` 行的 `checked == 0`（不可判**不算**核对），`indeterminate` 行也不贡献 `claims`——这正是 I6「非空泛」与 I7「不可判不洗白」的机器可读形式。
- **不可判必须响亮**（A5/I7）：`unbounded` / `nothing_assessed` / `indeterminate` 三态**必须**带 `reason`，且计数同面给出——「不可判」绝不呈现为「没问题」（沿用 P47 `last_unanchored_reason` / P49 A5 的纪律，不用粘滞字段）。
- 读面**只读**：两次既有 load + `os.Stat`；不落盘、不写 audit、不发网络、不派发、不压缩（T355）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（含 `snapshot_verification.go`/`snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`/`snapshot_anchor_realization.go`）；`internal/protection` 零 diff；`go.mod`/`go.sum` 零改动；P35~P49 判据取值零回归（T340/T351/T352/T362）**（P51 补记，ADR-077 §7 D18：该映射的覆盖范围如实收窄——T351 全程用同一份配置、T362 的 fixture 未配 VAK ⇒ 两条用例都够不到「验证者退役 / 顺序迁移」输入；无 `role` 输入上的零回归由 P51 的 T379/T380 钉死，这两类新输入上的判据由 T367/T368 钉死）** |
| I2 | **P41 语义逐字沿用**：`authorizationFor` / `authorizeByLifecycle` / `validityOf` 三函数**零改动**，边界比较严格沿用「`<` not_before、`≥` not_after、`revoked > rotated_out`、解析失败 ⇒ `time_unparseable`」（T353/T354） |
| I3 | **未配置验证者 ⇒ 零回归**：新组整组缺席，状态文档逐字节不变（T341） |
| I4 | **「不可用」≠「不可判」（承重）**：生命周期账本 **load 失败/不可验** ⇒ `indeterminate`（fail-closed）；账本**可读但证明不了该 key 的区间** ⇒ `unbounded`（不可判）。**两者绝不合并**——前者是「证据坏了」，后者是「证据不支持任何断言」（T345/T346） |
| I5 | **不折叠**：`verification_unauthorized`（P44 异族）与 `after_*`（P50 区间外）**必须**是两个独立取值，且**同时可见**（同一输入上 P44 的判定与 P50 的判定互不掩蔽）（T348） |
| I6 | **判据非空泛**：`authorized` 必须由「**区间可断言 ∧ 时刻落区间内**」产出，**不得**由「没找到反例」产出；`unbounded` 行**不**贡献 `claims`；全局 `verifier_authorized` **蕴含** `verifier_authority_claims > 0`；观测集为空 ⇒ `nothing_assessed`（空集不为真）（T347） |
| I7 | **不可判绝不洗白**：`unbounded` / `nothing_assessed` / `indeterminate` 绝不并入 `authorized`，也绝不并入 `violated`；必须带原因与计数（T345/T346/T347） |
| I8 | **fail-closed + 面间隔离**：验证账本或生命周期账本任一不可分类行 / digest 不符 / 断链 / stream 不符 / **域歧义** ⇒ 整面 `indeterminate`、响亮报错、**绝不**报 `authorized`；**其余面（P41/P42/P44/P49）取值不受影响**（T346） |
| I9 | **空观测集无需读生命周期账本**：`len(vs.entries) == 0` ⇒ `nothing_assessed`，在生命周期账本加载**之前**返回（与 P49 I13 同款理由：否则一个从未签发报告的部署会被它不需要的账本钉成 `indeterminate`）（T347） |
| I10 | **写入面主体集的边界（A2/A6）**：只有「`∈ signingTrust` xor `∈ verifierTrust`」的 key_id 才可被授权（一个也没有 ⇒ 拒绝；同时在两个 ⇒ 拒绝）；账本 schema / 规范序列化 / 既有 digest **零变更**（T340/T349） |
| I11 | **身份绑定（首轮评审 M1，承重）**：签发者**一律**取 `Signature.KeyID`；`e.KeyID != e.Signature.KeyID` ⇒ 该行 `indeterminate`、面级 `indeterminate`，**绝不**改去核对 `e.KeyID` 所指那把 key 的区间（§2 脚注 / ADR-075 §4 A9）。校验落在**新文件**内 ⇒ `snapshot_verification.go` 仍零 diff（T360） |
| I12 | **行级判定必须「全序首个命中」（首轮评审 M2）**：`indeterminate > violated > authorized > unbounded > nothing_assessed`；**禁止**用顺序赋值产出——后置赋值会覆盖 `indeterminate`，方向是 fail-open；`checked` 只计「区间可断言 ∧ 时刻可解析」的条数（T354/T361） |

## 6. 实现步骤（每步跑门禁）

1. `keyLifecycleConfig` 加 `verifierTrust` + `keyLifecycleConfig()` 填值 + `appendKeyLifecycleEvent` 主体集判据（xor）+ 守卫（`:648-654`）→ T349/T350
2. `keyLifecycleSummary` 枚举过滤（只列签名主体）+ 注释/文案（D15）→ **先红**：断言加了验证者事件后 `authorizations`/`active_key_id` 不变 → T351
3. 「域歧义复检」+ 两条账本的加载与 fail-closed 归类（I4/I8/I9）→ T346
4. **身份绑定（M1）**→ T360；逐签发者区间核对（复用 `authorizationFor` + `authorizeByLifecycle`）→ T342/T343/T344/T353；**行级全序裁决（M2，先计数后裁决）**→ T354/T361
5. `unbounded` / `nothing_assessed` 路径（I6/I7/I9）→ T345/T347
6. 状态面组（§4，全 `omitempty`）+ 零副作用 → T341/T355
7. 非折叠与双面同时可见（I5）→ T348
8. 删除保护复用 P49（跨面）→ T359
9. 跨维/冻结/字节等价 T340 + 畸形输入 T358 + 零新增族/路由 T356 + 变异 MU1~MU11（红→绿，sha256 还原）+ 三道门禁 + mktree 提交 → T357

## 7. 测试映射

T340→I1（冻结面 + **P41 账本字节等价** + `go.mod`/`go.sum`）；T341→I3；T342→§3.1/§3.2（新增机制 vs P42/P44/P49 沉默，含 ADR-054 §2.2 悖论的 VAK 复本）；T343→A5（轮换不折叠）；T344→§3.1（激活前）；T345→I4/I7（不可判响亮）；T346→I8（fail-closed + 域歧义）；T347→I6/I9（非空泛 + 空集不为真）；T348→I5（两轨不折叠）；T349→I10（主体集扩张与守卫）；T350→§3.1（状态机复用）；T351→I1（P41 面零回归 · **验证者侧**）；**T362→I1（P41 面零回归 · 签名锚漂移恒等：加回 `∈ signingTrust` 半边即必红）**；T352→I1（P44 面零回归 + 报告零字节）；T353→I2（边界比较语义）；T354→I2（R41-7 第四词）；T355→§4（零副作用）；T356→I1（零新增族/路由）；T357→MU1~MU11；T358→I8（畸形输入不 panic）；T359→§3.2（删除保护**复用** P49，不重复实现）；**T360→I11**（身份绑定：错配 ⇒ indeterminate，**绝不**核对 `e.KeyID` 那把 key）；**T361→I12**（行级全序首个命中：同 key 混合不得被覆盖）；**T385→I15/D20（`domain_mismatch` 需要可断言的缺席：已记账的前缀压缩 ⇒ `unbounded`；记账不可用 ⇒ 取值不动）**。

## 8. 容量与成本（诚实声明）

- **热路径成本：零**。本 Phase **不触碰**任何写入路径的判定（唯一写入面改动是 `appendKeyLifecycleEvent` 的**主体集准入**，发生在运维提交生命周期事件时，不在 Tick/Check/派发路径上），不触碰任何 `Check`/`Tick`/派发路径；P42 报告与 P44 判定零字节变更。
- **读面成本**：状态面每次多读**两条**已存在的账本（验证账本 = 至多 `--export-verify-limit`，默认见 `cmd/opscore/main.go`；生命周期账本 = 至多 `--export-key-lifecycle-capacity`）。管理读面，非热路径。
- **内存**：逐签发者一个计数结构（签发者数量 = 配置的验证者信任锚大小，通常 1~3）；不缓存（与 P46/P47/P49 的读派生纪律一致）。
- **不可两全（A8-② 的精确口径）**：生命周期账本关闭或该 key 无事件时，本面**只能**报 `unbounded`/`nothing_assessed`——**这是正确行为**，不是缺陷：判据强度与可用证据同阶。**绝不**把「没有账本」读成「越权」（那会把每一次未启用 KAK 的部署指控成伪造）。
- **依赖 P49（A8-⑤）**：删除 revocation 事件后本面**只能**报 `authorized`（向上无界），保护来自 `kind=key_lifecycle` 锚定条目 ⇒ P49 `anchor_unrealized`。**锚定与见证同时关闭 ⇒ 不可检测**，如实登记。
- **上界（A8-①/③/④）**：区间核对用的是**自证时间** `signed_at`（区间内撒谎不可检测）；只覆盖启用之后的事实；持 KAK 私钥者可伪造授权事件本身 ⇒ 拓扑代价。
- **行为变更声明（首轮评审 M1 的必然代价，穷举）**：① 一条**手工构造**的、`e.KeyID != e.Signature.KeyID` 的验证账本行，此前 `sigVerdictOK` 并进入 `st.entries`（P44 行为），此后在**本面**判 `indeterminate`；诚实写入路径两者同源（`snapshot_verification.go:729`/`:1084`）⇒ 正常部署**永不触发**（T341/T352 覆盖）；② 本面**不**回头改 `snapshot_verification.go` 的验签结论（零 diff），故该错配行在 P42/P44 面**仍然**是 `sigVerdictOK`——**两个面会对同一行给出不同结论**，这是**有意的**（本面明确拒绝在不成立的签发者身份上做任何断言；把结论塞进 P42 报告则违反 A7-③）。⇒ 声明的边界：**错配行的 P44 判定不变、只有本面新增 `indeterminate`**，不夸大为「P44 缺口的闭合」。
- **零新增面**：注册表仍 6 族（**不新增族**：验证者授权事件是 `key_lifecycle` 族的事件，`snapshot_anchor.go:66`/`:1132`），分区表仍 6 流，路由表零新增 ⇒ P46/P47/P49 三个面**自动**覆盖新事件，无需任何改动。

## 9. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「I4 为什么要把『账本读不到』与『证明不了区间』分成两个取值？会不会把同一件事说成两种？」** | blocker-if-true | 两者**可同时可达且含义相反**：把一个目录的 `signing-key-log.jsonl` 截断 ⇒ load **成功**但 `verifiable=false`（fail-closed 到 `indeterminate`）；把 KAK 配置去掉 ⇒ load **失败**（同样 `indeterminate`）；而「该 key 从未被记账」⇒ load 成功且**完全可验**，只是**没有**这个 key 的事件 ⇒ `authorizationFor` 返回 `unbounded`（`:439-444` 的原文「Asserting a window for it would invent history」）。⇒ 前两者是「证据坏了」（响亮），后者是「证据不支持断言」（不可判），把后者报成 `indeterminate` 会让**每一个只授权过 manifest 密钥的部署**误报损坏；把前者报成 `unbounded` 会掩盖损坏。T345/T346 判别，MU2/MU3 钉死。 |
| **Q2「`keyLifecycleSummary` 的过滤会不会改变 P41 既有输出？」** | major | 过滤条件**只有**「不在验证者锚中」（`∉ verifierTrust`）——**没有**「在签名锚中」半边（终审 M3：那半边对**退役**输入不是恒等，会把一把仍持有账本行、但从 trust 文件退役的签名 key 从取值里删掉；ADR-075 §4 A3 是承重承诺，故不采用）。**（P51 补记，ADR-077 §7 D17）**本行原以「验证者 key 在 `appendKeyLifecycleEvent`（`:648-654`）**根本写不进去**」论证恒等，而同一 ADR §1③ 恰恰**扩张**了写入面 ⇒ **该论证在本 ADR 落地后即为假**（验证者行确实写得进去），原论据自相矛盾。正确的口径是：这唯一的过滤对**P50 时代可产生的**一切输入是**恒等变换** ⇒ 既有输出逐字节不变（T351 先红后绿：先造一条验证者事件，断言 `authorizations`/`active_key_id` **仍是**改造前的值；T362 钉死签名锚漂移下的恒等）。**退役与顺序迁移这两类输入由 P51 认领**（ADR-077 §7 D16/D18）：无 `role` 的历史行沿用本行这条规则（A4-2，登记为 A8-② 的残差），携带 `role` 的行改从**行内**读出域（A4-1，T367/T368 钉死）。 |
| **Q3「新加 `verifierTrust` 字段会不会改变 6 族的启用门 / 分区表？」** | major | 不会：`verifierTrust` 只进 `keyLifecycleConfig`（一个纯结构体），族注册表（`witnessFamilyRegistry`）、启用门（`witnessFamilyEnabled`）、派发分区表（`anchorDeliveryStreams`）**零改动**（T356 断言四条既有枚举用例取值不变）。 |
| **Q4「T342 的判别力在哪里？会不会是自说自话的用例？」** | major | 判别支点是**同一用例内两条记录取值相反**（`t ≥ T` ⇒ `after_revocation`；`t < T` ⇒ `authorized`）+ **三个不变**（P42 报告字节、P44 判定、P49 该族取值）。若实现退化成「看一眼有没有 revoked 事件」（与时间无关），两条记录会同值 ⇒ 必红；若把判据塞进 P42 报告 ⇒ T352 必红。MU1/MU4 各自钉住这两条退化路径。 |
| **Q5「本 Phase 会不会把 `unbounded` 变成『大多数部署永远拿不到这一维』，从而没有价值？」** | note | **会**，且已在 A8-② 显式声明：没有 KAK 的部署拿不到这一维。价值集中在**启用了生命周期账本、且发生过轮换/吊销**的部署 —— 那正是 ADR-054 §2.2 悖论真实发生的场景。**不夸大**：本 Phase 不声称覆盖面，只声称「此前给不出的那条判据现在给得出」。 |

## 10. 评审闭合表（首轮 2 项 major + 终审 1 项 major —— 与 ADR-075 §11 同源）

| 发现 | 级别 | 闭合（Architecture 侧） |
|---|---|---|
| **M1** 新判据的「签发者」锚在 `verificationLogEntry.KeyID`——一个**未与签名绑定**的自证字段上：P44 验签只用 `sb.KeyID`（`snapshot_verification.go:775`），入队点 `:692` 前后无绑定检查（本家族其余三位都有：`snapshot_acceptance.go:251`/`snapshot_destruction.go:315`/`snapshot_key_lifecycle.go:257`）⇒ 持已吊销 VAK-A 私钥者可构造 `key_id = <区间内另一把 key>` + `signature.key_id = A` 的行（`e.KeyID` 只被签名**覆盖**、可任填 `:318`/`:333`），`sigVerdictOK` 并进入 `st.entries` ⇒ 本面去核对那把「区间内」的 key ⇒ 报 `authorized`，真实签发者的区间外事实被掩盖 | major | ① §2 表把「签发者」改为 **`Signature.KeyID`** 并加**脚注**给出完整证据链（含亲跑 `grep -rn "disagrees"` 的逐行归类与写入路径同源事实 `:729`/`:1084`）；② §3 ⑤ 新增 **①.0 身份绑定**步骤：错配 ⇒ 该行 `indeterminate`，**绝不**去核对 `e.KeyID` 那把 key；③ §3.1 新增判别支点行；④ §4 增加面级 `reason`（含「身份错配」）；⑤ §5 新增 **I11**；⑥ §6 步骤 4 前置身份绑定；⑦ §7 映射 T360；⑧ §8 新增**行为变更声明**（并限定范围：错配行的 P44 判定**不变**，不夸大为 P44 缺口闭合）；⑨ ADR-075 同步 §2 事实 8 / §3 表 / §4 A9 / T360 / MU9。**评审建议的修法原样采纳。** |
| **M2** §3 ⑤ 把行级判决写成**顺序赋值**（`row = VIOLATED if …` / `row = AUTHORIZED if …` / `row = NOTHING_ASSESSED if checked == 0`），按字面执行是 **fail-open**：循环内先置的 `INDETERMINATE` 会被后置赋值覆盖（全部不可解析 ⇒ 退化为 `nothing_assessed`，丢失 T354；一条合法 + 一条不可解析 ⇒ 退化为 `authorized`，直接违反 I7） | major | ① §3 ⑤ 重写为**先计数、后裁决**：循环内只累加计数，循环结束后按**全序首个命中**裁决（`indeterminate > violated > authorized > unbounded > nothing_assessed`），第 ⑤ 步标注**按构造不可达**（防御性兜底）；② 在伪码注释中明写「在循环里写 `row` 就是顺序赋值，方向是 fail-open」；③ §3.1 新增「同 key 混合」判别支点行；④ §5 新增 **I12**（并明写「**禁止**用顺序赋值产出」）；⑤ §4 的 `checked` 定义收紧为「区间可断言 ∧ 时刻可解析」的条数（`unbounded`/`indeterminate` 行不贡献 `claims`）；⑥ §6 步骤 4 标注「先计数后裁决」、§7 映射 T361、MU10；⑦ §9 补 MU10 的判别力说明；⑧ ADR-075 同步 §3 行级全序定义 / T354 / T361 / MU10。 |
| **终审 M3** `keyLifecycleSummary` 的枚举被改成「在签名锚中 ∧ 不在验证者锚中」，但**前半（`∈ signingTrust`）不是恒等**：旧代码遍历 `st.byKey`、无 trust 过滤，而 trust 文件会变 ⇒ 普通退役（同目录同账本同签名者、被退役 key 仍持有账本行）下 `authorizations`/`active_key_id` 取值改变，违反 I1/A3/§8-⑦；原实现对**空锚**开了例外，非空情形仍回归，T351（只写验证者事件）抓不到 | major | ① §1 ③ 与 §8 Q2 同步为「**只加** `∉ verifierTrust` 一个过滤」；② §5 I1 的映射补 **T362**；③ §7 映射 T362→I1、§9 MU1~MU11；④ §5 I10 的「主体集边界」**不变**（写入面仍按 xor 判据，与本读面过滤无关）。**不走**「声明为行为变更」那条路（A3 承重）。 |
