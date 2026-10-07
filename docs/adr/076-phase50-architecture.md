# ADR-076 — Phase 50: Verifier Authority Lifecycle（Architecture）

- **Status**: Proposed (Phase 50, Architecture stage)
- **Base**: ADR-075（Scope）。冲突以 Scope 为准。
- **前置**：Phase 49 CLOSED（HEAD = `8188879`），最大既有 T 编号 = **T339**（`snapshot_anchor_realization_test.go`，P49 §5），故本 Phase 用 **T340~T359**。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_verifier_authority.go` | **新增** | 验证者授权面：验证账本 × 生命周期账本的交汇 / 逐签发者区间核对（复用 `authorizationFor` + `authorizeByLifecycle` 语义）/ 域歧义复检 / 多态判别 / 状态面派生 |
| `internal/controlplane/server/snapshot_verifier_authority_test.go` | **新增** | T340~T359 |
| `internal/controlplane/server/snapshot_key_lifecycle.go` | **修改** | ① `keyLifecycleConfig`（`:274-283`）新增 `verifierTrust *exportTrustStore`；② `appendKeyLifecycleEvent`（`:636`）主体判据由「`∈ signingTrust`」改为「`∈ signingTrust ∪ verifierTrust` ∧ **不得同时在两者中**」（`:648-654`）；③ `keyLifecycleSummary`（`:828-860`）的枚举改为「在签名锚中 ∧ 不在验证者锚中」；④ `:278` 注释与 `:649`/`:652` 拒绝文案更新（D15）。**账本结构 / 规范序列化 / 既有判定零变更** |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | ① `keyLifecycleConfig()`（`:637-651`）填 `verifierTrust: s.verifierTrust`；② `Status()`（`:1526`）新增 `VerifierAuthority` 组（全 `omitempty`，与 `KeyLifecycle`（`:1564`）同受「verifier 已配置」约束） |
| `snapshot_verification.go` · `snapshot_anchor.go` · `snapshot_witness_reconcile.go` · `snapshot_ledger.go` · `history_export_manifest.go` · `snapshot_signature.go` · `snapshot_chain.go` · `history_export_coverage.go` · `appendonly_log.go` · `snapshot_anchor_realization.go` · 冻结五文件 · `internal/protection/**` · `go.mod`/`go.sum` | **零 diff** | A3 承重承诺（**特别**：P42 报告零字节变更 A7-③；P44 判定零变更 A7-②；P41 `authorizeByLifecycle` 唯一调用点 `history_export_manifest.go:596-597` 不动） |

## 2. 两条取数面（全部只读，行号本轮亲跑核对）

| 面 | 取数函数 | 本 Phase 需要的字段 |
|---|---|---|
| 验证账本 | `loadVerificationState(s.verificationConfig())`（`snapshot_verification.go:619`；配置装配 `:1127`） | `st.entries`（`:587`；**只含通过** P44 签名 / 自身 digest / 链 / stream 检查的记录）→ `ReportSeq`（`:292`）、`KeyID`（`:300`，**签发者**）、`Signature.SignedAt`（经 `signatureBlock`）；`st.verifiable`（`:589`）、`st.errs`（`:590`）、`st.total`（`:591`）；签名检查点 `:657` |
| 生命周期账本 | `loadKeyLifecycleState(s.keyLifecycleConfig())`（`snapshot_key_lifecycle.go:326`；配置装配 `history_export_scheduler.go:637-651`） | `st.byKey`（逐 key 事件）、`st.window`（`:296-301`）、`st.verifiable`、`st.errs`；区间解析 `authorizationFor(keyID)`（`:428-484`）、比较 `authorizeByLifecycle(v, signedAt, a)`（`:534-580`） |

**本 Phase 不新增任何读取原语**：`authorizationFor` / `authorizeByLifecycle` / `loadVerificationState` 三者**逐字复用**，新文件只做「取数 → 交织 → 归类 → 计数」。P41 的区间语义（`:552-580`：`t.Before(not_before)` ⇒ `before_activation`；`!t.Before(not_after)` ⇒ 终局取值 `revoked > rotated_out`；`parseRFC3339Nano` 失败 ⇒ `time_unparseable`；`Source != ledger ∨ ¬Complete` ⇒ 不判定、只带 `validityOf(a)`）**必须逐字沿用**（I2）。

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

  # ⑤ 逐签发者（key_id）判决：观测集 = 可用记录中出现过的 KeyID（去重、排序）
  for each keyID in observedSigners(vs.entries) (升序):
      a := ls.authorizationFor(keyID)                 # 复用 P41，:428
      if a.Source != lifecycleSourceLedger || !a.Complete:
          row = UNBOUNDED(reason = reasonOf(a))       # 不可判：绝不算 authorized，也绝不算 violated
      else:
          for each entry e of that keyID:
              t, terr := parseRFC3339Nano(e.Signature.SignedAt)
              if terr != nil:                         row = INDETERMINATE("signed_at is not parseable")   # R41-7 第四词
              else:
                  v := authorizeByLifecycle({Verdict: sigVerdictOK}, e.Signature.SignedAt, a)   # 复用 P41 比较语义
                  switch v.Verdict:
                  case sigVerdictOK:                  cnt.authorized++
                  case sigVerdictBeforeActivation:    cnt.before_activation++
                  case sigVerdictAfterRotation:       cnt.after_rotation++
                  case sigVerdictAfterRevocation:     cnt.after_revocation++
                  case sigVerdictTimeUnparseable:     row = INDETERMINATE(...)
                  default:                            row = INDETERMINATE("unexpected lifecycle verdict " + v.Verdict)
          row = VIOLATED   if (before_activation + after_rotation + after_revocation) > 0
          row = AUTHORIZED if authorized > 0 ∧ 违纪为 0
          row = NOTHING_ASSESSED if checked == 0        # 该 key 的记录一条也没被真正核对

  # ⑥ 全局：标量、全序首个命中（ADR-075 §3，**不是合取**）
  state = NOTHING_ASSESSED
  if     ∃ row: row.verdict == INDETERMINATE:  state = INDETERMINATE
  else if ∃ row: row.verdict == VIOLATED:      state = VIOLATED
  else if ∃ row: row.verdict == AUTHORIZED:    state = AUTHORIZED
  # 否则保持 NOTHING_ASSESSED（全部行皆 NOTHING_ASSESSED 或 UNBOUNDED）
  verifier_authorized        := (state == AUTHORIZED)          # 派生便捷量，不得另行定义
  verifier_authority_claims  := Σ row.checked                  # AUTHORIZED ⇒ claims > 0
  verifier_authority_violations := Σ (before_activation + after_rotation + after_revocation)
  verifier_authority_unbounded  := Σ row.unbounded
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
  keys: {                        // 观测集：可用验证记录中出现过的签发者（升序）
    <key_id>: {
      verdict,                   // authorized | violated | unbounded | indeterminate | nothing_assessed
      checked, authorized, before_activation, after_rotation, after_revocation, unbounded,   // 计数
      validity: { authorized_from?, authorized_until?, terminal_event?, source },   // 复用 P41 validityOf（:509-518）
      reason?                    // unbounded / indeterminate / nothing_assessed 的响亮原因
    }, ...
  },
  verifier_authority_state,                 // 标量，全序首个命中（ADR-075 §3）：
                                            // indeterminate | violated | authorized | nothing_assessed
  verifier_authorized: <bool>,              // 派生：state == "authorized"（不得另行定义）
  verifier_authority_claims: <int>,         // Σ checked；authorized ⇒ > 0
  verifier_authority_violations: <int>,     // Σ (before_activation + after_rotation + after_revocation)
  verifier_authority_unbounded: <int>,      // Σ unbounded
  verifier_authority_violating_keys: [<key_id>...],
  verifier_authority_indeterminate_keys: [<key_id>...],
  reason?                                   // 面级 fail-closed 的原因（域歧义 / 账本不可验）
}
```

- **`verifier_authority_claims` 报告级与全局级已正确区分**：`claims` 只统计**区间可断言且时刻可解析**的记录（「本次读实际核对了多少条」），故 `unbounded` 行不贡献 `claims`——这正是「不可判不算核对」的机器可读形式（I6）。
- **不可判必须响亮**（A5/I7）：`unbounded` / `nothing_assessed` / `indeterminate` 三态**必须**带 `reason`，且计数同面给出——「不可判」绝不呈现为「没问题」（沿用 P47 `last_unanchored_reason` / P49 A5 的纪律，不用粘滞字段）。
- 读面**只读**：两次既有 load + `os.Stat`；不落盘、不写 audit、不发网络、不派发、不压缩（T355）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（含 `snapshot_verification.go`/`snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`/`snapshot_anchor_realization.go`）；`internal/protection` 零 diff；`go.mod`/`go.sum` 零改动；P35~P49 判据取值零回归（T340/T351/T352） |
| I2 | **P41 语义逐字沿用**：`authorizationFor` / `authorizeByLifecycle` / `validityOf` 三函数**零改动**，边界比较严格沿用「`<` not_before、`≥` not_after、`revoked > rotated_out`、解析失败 ⇒ `time_unparseable`」（T353/T354） |
| I3 | **未配置验证者 ⇒ 零回归**：新组整组缺席，状态文档逐字节不变（T341） |
| I4 | **「不可用」≠「不可判」（承重）**：生命周期账本 **load 失败/不可验** ⇒ `indeterminate`（fail-closed）；账本**可读但证明不了该 key 的区间** ⇒ `unbounded`（不可判）。**两者绝不合并**——前者是「证据坏了」，后者是「证据不支持任何断言」（T345/T346） |
| I5 | **不折叠**：`verification_unauthorized`（P44 异族）与 `after_*`（P50 区间外）**必须**是两个独立取值，且**同时可见**（同一输入上 P44 的判定与 P50 的判定互不掩蔽）（T348） |
| I6 | **判据非空泛**：`authorized` 必须由「**区间可断言 ∧ 时刻落区间内**」产出，**不得**由「没找到反例」产出；`unbounded` 行**不**贡献 `claims`；全局 `verifier_authorized` **蕴含** `verifier_authority_claims > 0`；观测集为空 ⇒ `nothing_assessed`（空集不为真）（T347） |
| I7 | **不可判绝不洗白**：`unbounded` / `nothing_assessed` / `indeterminate` 绝不并入 `authorized`，也绝不并入 `violated`；必须带原因与计数（T345/T346/T347） |
| I8 | **fail-closed + 面间隔离**：验证账本或生命周期账本任一不可分类行 / digest 不符 / 断链 / stream 不符 / **域歧义** ⇒ 整面 `indeterminate`、响亮报错、**绝不**报 `authorized`；**其余面（P41/P42/P44/P49）取值不受影响**（T346） |
| I9 | **空观测集无需读生命周期账本**：`len(vs.entries) == 0` ⇒ `nothing_assessed`，在生命周期账本加载**之前**返回（与 P49 I13 同款理由：否则一个从未签发报告的部署会被它不需要的账本钉成 `indeterminate`）（T347） |
| I10 | **写入面主体集的边界（A2/A6）**：只有「`∈ signingTrust` xor `∈ verifierTrust`」的 key_id 才可被授权（一个也没有 ⇒ 拒绝；同时在两个 ⇒ 拒绝）；账本 schema / 规范序列化 / 既有 digest **零变更**（T340/T349） |

## 6. 实现步骤（每步跑门禁）

1. `keyLifecycleConfig` 加 `verifierTrust` + `keyLifecycleConfig()` 填值 + `appendKeyLifecycleEvent` 主体集判据（xor）+ 守卫（`:648-654`）→ T349/T350
2. `keyLifecycleSummary` 枚举过滤（只列签名主体）+ 注释/文案（D15）→ **先红**：断言加了验证者事件后 `authorizations`/`active_key_id` 不变 → T351
3. 「域歧义复检」+ 两条账本的加载与 fail-closed 归类（I4/I8/I9）→ T346
4. 逐签发者区间核对（复用 `authorizationFor` + `authorizeByLifecycle`）→ T342/T343/T344/T353/T354
5. `unbounded` / `nothing_assessed` 路径（I6/I7/I9）→ T345/T347
6. 状态面组（§4，全 `omitempty`）+ 零副作用 → T341/T355
7. 非折叠与双面同时可见（I5）→ T348
8. 删除保护复用 P49（跨面）→ T359
9. 跨维/冻结/字节等价 T340 + 畸形输入 T358 + 零新增族/路由 T356 + 变异 MU1~MU8（红→绿，sha256 还原）+ 三道门禁 + mktree 提交 → T357

## 7. 测试映射

T340→I1（冻结面 + **P41 账本字节等价** + `go.mod`/`go.sum`）；T341→I3；T342→§3.1/§3.2（新增机制 vs P42/P44/P49 沉默，含 ADR-054 §2.2 悖论的 VAK 复本）；T343→A5（轮换不折叠）；T344→§3.1（激活前）；T345→I4/I7（不可判响亮）；T346→I8（fail-closed + 域歧义）；T347→I6/I9（非空泛 + 空集不为真）；T348→I5（两轨不折叠）；T349→I10（主体集扩张与守卫）；T350→§3.1（状态机复用）；T351→I1（P41 面零回归）；T352→I1（P44 面零回归 + 报告零字节）；T353→I2（边界比较语义）；T354→I2（R41-7 第四词）；T355→§4（零副作用）；T356→I1（零新增族/路由）；T357→MU1~MU8；T358→I8（畸形输入不 panic）；T359→§3.2（删除保护**复用** P49，不重复实现）。

## 8. 容量与成本（诚实声明）

- **热路径成本：零**。本 Phase **不触碰**任何写入路径的判定（唯一写入面改动是 `appendKeyLifecycleEvent` 的**主体集准入**，发生在运维提交生命周期事件时，不在 Tick/Check/派发路径上），不触碰任何 `Check`/`Tick`/派发路径；P42 报告与 P44 判定零字节变更。
- **读面成本**：状态面每次多读**两条**已存在的账本（验证账本 = 至多 `--export-verify-limit`，默认见 `cmd/opscore/main.go`；生命周期账本 = 至多 `--export-key-lifecycle-capacity`）。管理读面，非热路径。
- **内存**：逐签发者一个计数结构（签发者数量 = 配置的验证者信任锚大小，通常 1~3）；不缓存（与 P46/P47/P49 的读派生纪律一致）。
- **不可两全（A8-② 的精确口径）**：生命周期账本关闭或该 key 无事件时，本面**只能**报 `unbounded`/`nothing_assessed`——**这是正确行为**，不是缺陷：判据强度与可用证据同阶。**绝不**把「没有账本」读成「越权」（那会把每一次未启用 KAK 的部署指控成伪造）。
- **依赖 P49（A8-⑤）**：删除 revocation 事件后本面**只能**报 `authorized`（向上无界），保护来自 `kind=key_lifecycle` 锚定条目 ⇒ P49 `anchor_unrealized`。**锚定与见证同时关闭 ⇒ 不可检测**，如实登记。
- **上界（A8-①/③/④）**：区间核对用的是**自证时间** `signed_at`（区间内撒谎不可检测）；只覆盖启用之后的事实；持 KAK 私钥者可伪造授权事件本身 ⇒ 拓扑代价。
- **零新增面**：注册表仍 6 族（**不新增族**：验证者授权事件是 `key_lifecycle` 族的事件，`snapshot_anchor.go:66`/`:1132`），分区表仍 6 流，路由表零新增 ⇒ P46/P47/P49 三个面**自动**覆盖新事件，无需任何改动。

## 9. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「I4 为什么要把『账本读不到』与『证明不了区间』分成两个取值？会不会把同一件事说成两种？」** | blocker-if-true | 两者**可同时可达且含义相反**：把一个目录的 `signing-key-log.jsonl` 截断 ⇒ load **成功**但 `verifiable=false`（fail-closed 到 `indeterminate`）；把 KAK 配置去掉 ⇒ load **失败**（同样 `indeterminate`）；而「该 key 从未被记账」⇒ load 成功且**完全可验**，只是**没有**这个 key 的事件 ⇒ `authorizationFor` 返回 `unbounded`（`:439-444` 的原文「Asserting a window for it would invent history」）。⇒ 前两者是「证据坏了」（响亮），后者是「证据不支持断言」（不可判），把后者报成 `indeterminate` 会让**每一个只授权过 manifest 密钥的部署**误报损坏；把前者报成 `unbounded` 会掩盖损坏。T345/T346 判别，MU2/MU3 钉死。 |
| **Q2「`keyLifecycleSummary` 的过滤会不会改变 P41 既有输出？」** | major | 过滤条件是「在签名锚中 ∧ 不在验证者锚中」。**今天**，验证者 key 在 `appendKeyLifecycleEvent`（`:648-654`）**根本写不进去** ⇒ 该条件对今天的一切输入是**恒等变换** ⇒ 既有输出逐字节不变（T351 先红后绿：先造一条验证者事件，断言 `authorizations`/`active_key_id` **仍是**改造前的值）。 |
| **Q3「新加 `verifierTrust` 字段会不会改变 6 族的启用门 / 分区表？」** | major | 不会：`verifierTrust` 只进 `keyLifecycleConfig`（一个纯结构体），族注册表（`witnessFamilyRegistry`）、启用门（`witnessFamilyEnabled`）、派发分区表（`anchorDeliveryStreams`）**零改动**（T356 断言四条既有枚举用例取值不变）。 |
| **Q4「T342 的判别力在哪里？会不会是自说自话的用例？」** | major | 判别支点是**同一用例内两条记录取值相反**（`t ≥ T` ⇒ `after_revocation`；`t < T` ⇒ `authorized`）+ **三个不变**（P42 报告字节、P44 判定、P49 该族取值）。若实现退化成「看一眼有没有 revoked 事件」（与时间无关），两条记录会同值 ⇒ 必红；若把判据塞进 P42 报告 ⇒ T352 必红。MU1/MU4 各自钉住这两条退化路径。 |
| **Q5「本 Phase 会不会把 `unbounded` 变成『大多数部署永远拿不到这一维』，从而没有价值？」** | note | **会**，且已在 A8-② 显式声明：没有 KAK 的部署拿不到这一维。价值集中在**启用了生命周期账本、且发生过轮换/吊销**的部署 —— 那正是 ADR-054 §2.2 悖论真实发生的场景。**不夸大**：本 Phase 不声称覆盖面，只声称「此前给不出的那条判据现在给得出」。 |

## 10. 评审闭合表（首轮，待评审后填写）

（本轮为 Architecture 首轮交付；评审轮发现的 blocker/major 在本表逐条闭合，并与 ADR-075 §11 同步。）
