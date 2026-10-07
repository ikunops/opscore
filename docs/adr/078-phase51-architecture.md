# ADR-078 — Phase 51: Lifecycle Subject Domain（Architecture）

- **Status**: Proposed (Phase 51, Architecture stage)
- **Base**: ADR-077（Scope）。冲突以 Scope 为准。
- **前置**：Phase 50 CLOSED（HEAD = `fcf5506`），最大既有 T 编号 = **T362**（`snapshot_verifier_authority_test.go`，P50 §5），故本 Phase 用 **T363~T383**。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_lifecycle_domain.go` | **新增** | 主体域面：`role` 的**派生与读取**（`domainOf`）/ **按域折叠**（`authorizationForDomain`，复用 P41 的 `authorizationFor`）/ 冲突与迁移归类 / 面级状态派生 |
| `internal/controlplane/server/snapshot_lifecycle_domain_test.go` | **新增** | T363~T383（21 例） |
| `internal/controlplane/server/snapshot_key_lifecycle.go` | **修改** | ① `keyLifecycleEntry`（`:98-114`）与 `keyLifecycleSigned`（`:116-128`）**末尾**追加 `Role string \`json:"role,omitempty"\``；② `keyLifecycleSignedFields`（`:130`）带上它；③ `appendKeyLifecycleEvent`（`:646`）把 `:664` 已算出的 `inSigning`/`inVerifier` **落进 `Role`**（准入判据 `:673`/`:681` 一字不改）；④ `keyLifecycleSummary`（`:856`）的 `:897-901` 过滤改为 **A4-1（有 `role` 的行按行内 `role`）+ A4-2（无 `role` 的行照搬 P50 的 `∉ 当前 verifierTrust` 规则）**（D17）；⑤ `:286-288` 注释按 A8-⑧ 的新口径改写。**账本 schema 语义零变更**（旧行逐字节不变，T363） |
| `internal/controlplane/server/snapshot_verifier_authority_test.go` | **修改** | **同步四条既有钉死断言**（评审 major-4，缺一条即「未声明的行为变更」）：`:349-351`（T340 行 JSON 键集 11 ⇒ **12**）、`:386-388`（T341 无 VAK 顶层键序列）、`:404-405`（T341 有 VAK：基线 + `verifier_authority`，**含新键顺序**）、`:417`（`p50BaselineStatusKeys()` 基线本身） |
| `internal/controlplane/server/snapshot_verifier_authority.go` | **修改** | ① 新增行级取值 `domain_mismatch`（`:67-73` 的常量块）；② `:323` 的 `ls.authorizationFor(keyID)` 改为按**验证域**折叠的 `authorizationForDomain(ls, keyID, DOMAIN_VERIFIER)`；③ 行级全序插入 `domain_mismatch`（在 `violated` 之后、`authorized` 之前）；④ `verifierAuthorityKeyStatus`（`:79-95`）新增 `DomainMismatch int` 计数 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | `Status()`（`:1538`）新增 `KeyLifecycleDomains` 组（全 `omitempty`，启用门与 `KeyLifecycle` **完全相同**） |
| `snapshot_anchor.go` · `snapshot_witness_reconcile.go` · `snapshot_ledger.go` · `history_export_manifest.go` · `appendonly_log.go` · `snapshot_verification.go` · `snapshot_signature.go` · `snapshot_chain.go` · `history_export_coverage.go` · `snapshot_anchor_realization.go` · 冻结五文件 · `internal/protection/**` · `go.mod`/`go.sum` | **零 diff** | A3 承重承诺（**特别**：P41 的 `authorizationFor`（`:438`）/ `authorizeByLifecycle`（`:544`）/ `validityOf`（`:519`）三函数**零改动**；P42 报告与 P44 判定零字节；P49 兑现面零 diff） |

## 2. 载体与三条取数面（全部只读，行号本轮亲跑核对）

| 面 | 取数函数 | 本 Phase 需要的字段 |
|---|---|---|
| 生命周期账本 | `loadKeyLifecycleState(s.keyLifecycleConfig())`（`snapshot_key_lifecycle.go:336`） | `st.byKey`（逐 key 事件：`Role`、`EventSeq`、`EventType`、`NotBefore`、`NotAfter`、`Signature`）、`keyLifecycleState` 的 `window`（`:315-322`）、`st.verifiable`、`st.errs`；区间解析 `authorizationFor(keyID)`（`:438-493`） |
| P41 状态面 | `keyLifecycleSummary(c)`（`:856-909`） | 同上 + `authorizations` / `active_key_id`（`:902-905`） |
| P50 授权面 | `loadVerificationState` + `snapshot_verifier_authority.go:323` 的逐签发者区间 | 同上 + 签发者 `Signature.KeyID` |

**载体决策（A1，承重）**：域写进 `keyLifecycleSigned` 的**末尾**、`omitempty`、**由写入面派生**（不是 API 参数）。

- **为什么末尾 + `omitempty` 是恒等**：`canonicalLifecyclePayload`（`:150-156`）是 `json.Marshal(keyLifecycleSignedFields(e))`，字段顺序 = 声明顺序，`omitempty` 的空串**不产生字节** ⇒ 旧行（`Role == ""`）的 payload 与 digest **逐字节不变**（T363 钉死）。**本轮亲跑验证**：探针 `H:\zcode-workspace\.p51probe\zz_p51_probe4_test.go`（`go test ./internal/controlplane/server/ -run TestZZP51ProbeTrailing -v` ⇒ **PASS**）实测「旧行字节相同 ∧ 新行多出 `"role":"verifier"`」。⇒ ADR-075 A8-⑧ 为拒绝记录域而开出的价格（canonical payload 零改动）**不成立**（ADR-077 §2 事实 6 probe 4）。
- **为什么域不能由调用方声明**：`keyLifecycleRequest` **零新增字段**（T376 类型层面钉死）。域是 `appendKeyLifecycleEvent` 在 `:664` **已算出**的锚成员关系的派生值（`:675-682` 的 switch 已经把它绑定到 `pub`），随 KAK 签名进证据 ⇒ 与 `authority_key_id` 同级：**是被授权的域，不是自证的声明**（A2/A9）。⇒ 不重演 P50 首轮 M1（自证字段被一步绕过）的教训。
- **写入面只会落两个取值**：`signing`（`:676` 的 `case inSigning:` 分支）/ `verifier`（`:678` 的 `case inVerifier:`）；**空串只可能是 P51 之前的行**（`appendKeyLifecycleEvent` 的两个 `return zero, err` 分支 `:673`/`:681` 保证 P51 之后**不存在**无域的新行）。读面的**第四个**取值 `conflict` **不是**写入面产出，而是**读面对畸形行的 fail-closed 归类**（`role` 不在值域内 / 同一事件组内不一致）。

## 3. 多态判别（本 Phase 的核心机制）

```
# ---- 主体级域判定（新文件；**有 role 的行**绝不查 live trust 文件 —— A9/A4-1）----
# domain 的值域**恰四个**：SIGNING | VERIFIER | UNDECLARED | CONFLICT。
# migrated 是**独立的主体级 bool**，**不是**第五个取值（ADR-077 §3；评审 M2）。
domainOf(st *keyLifecycleState, keyID) domainStatus:   # {domain, migrated}
    rows := st.byKey[keyID]                          # 升序（既有顺序）
    if any row.Role ∉ {"", "signing", "verifier"}  ⇒ {CONFLICT, false}
    if 同一 event_seq 组内 Role 不一致              ⇒ {CONFLICT, false}
    roles := {row.Role | row.Role != ""}
    if |roles| == 0:                                ⇒ {UNDECLARED, false}   # 只可能是 P51 之前的行
    if |roles| == 1:                                ⇒ {该唯一值,   false}
    # 两个域都有 ⇒ 顺序迁移（probe 2 的形状）。domain 取**最后一条** role 行的值，
    # 同时置 migrated=true（A8-③）。**这不是 conflict**（两条事实各自可断言）。
    return {last role row's value, migrated=true}

# ---- 按域折叠（新文件；P41 的 authorizationFor 逐字复用，A3/A5）----
# **两个返回值**：区间 + **实际折叠的行数**（调用点判定「本域在册有没有行」只许用它；
# keyAuthorization (:417-428) **没有** rows 字段 —— 评审 blocker 的附带项）。
rowsForDomain(c keyLifecycleConfig, st, keyID, want) int:
    n := |{r ∈ st.byKey[keyID] : r.Role == want}|
    # A4-2：无 role 的行（P51 之前）按**该消费者在 P50 时代的那条规则**归属：
    #   · want == SIGNING  ⇒ 该行参与 ⟺ keyID ∉ 当前 verifierTrust  （= P50 的 :897-901 过滤）
    #   · want == VERIFIER ⇒ 该行一律参与（P50 授权面的观测集本身即「验证锚内、签过报告者」，
    #                        它那时根本没有域这一维）
    if want == SIGNING  ∧ keyID ∉ c.verifierTrust:  n += |{r : r.Role == ""}|
    if want == VERIFIER:                             n += |{r : r.Role == ""}|
    return n

authorizationForDomain(c keyLifecycleConfig, st, keyID, want) (keyAuthorization, int):
    n := rowsForDomain(c, st, keyID, want)
    if n == 0:        return keyAuthorization{Source: lifecycleSourceUnbounded, …}, 0   # 本域在册无行
    copy := *st; copy.byKey = {keyID: 按上面的归属规则选出的行}   # 浅拷贝，其余字段原样带入（window 等）
    return copy.authorizationFor(keyID), n           # ← P41 的函数，零改动（I2）

# ---- 面级（新组 key_lifecycle_domains；只读，零副作用）----
domainSummary(c keyLifecycleConfig) domainStatusSummary:
    # ⓪ 未启用账本 ⇒ 整组缺席（A8-⑦②）：与 key_lifecycle 同一个启用门 ⇒ 两者同时出现/同时缺席
    if !c.enabled():                                 return ABSENT
    st, err := loadKeyLifecycleState(c)
    if err != nil:                                   return 面级 reason = err（组内如实报出）
    for each keyID in st.byKey (升序):
        d := domainOf(st, keyID)
        row := { domain: d.value, events, declared_events, undeclared_events,
                 validity: validityOf(authorizationForDomain(st, keyID, d.value)), reason? }
        if d == CONFLICT:  conflict_keys += keyID; row.reason = "…declares two domains…"
        if d.wasMigrated:  migrated_keys += keyID
    state = lifecycle_domain_state（标量、全序首个命中 —— **不是合取**）：
        if   ∃ row: row.domain == CONFLICT      ⇒ "conflict"
        elif ∃ key: WASMIGRATED                 ⇒ "migrated"
        elif ∃ row: row.domain ∈ {SIGNING, VERIFIER} ⇒ "declared"   # 存在无 role 的行**不**降级（评审 M3）
        else                                    ⇒ "undeclared"     # 全 UNDECLARED 或空账本
    lifecycle_domain_declared := (state == "declared")   # 派生便捷量，不得另行定义

# ---- P41 状态面（改取数源，D17；A4-2 ⇒ 对无 role 的输入逐字等于 P50）----
keyLifecycleSummary:  for k in st.byKey (升序):
    if _, n := authorizationForDomain(c, st, k, DOMAIN_SIGNING); n == 0:  continue
    # 有 role 的行按账本（A4-1）；无 role 的行按 P50 的 :897-901 规则（A4-2）
    …
# ---- P50 授权面（插入 ⑤.0；其余一字不改）----
for each keyID in observedSigners(vs.entries):
    d := domainOf(ls, keyID)
    if d.domain == CONFLICT:               cnt.indeterminate++; rowReason="…"; goto verdict
    a, n := authorizationForDomain(c, ls, keyID, DOMAIN_VERIFIER)   # ← 按**验证域**折叠（原来是不区域的 :323）
    # 域错配（评审 M2 的闭合口径）：**以折叠结果定义**，不引用主体级 domain ⇒
    # 迁移主体（有 role=verifier 行）**不走**本分支，两个面不可能相反。
    if n == 0 ∧ keyID 在册有行:            cnt.domain_mismatch++; goto verdict   # 在册授权**全部**属另一个域
    …（P50 原有 ①.0 身份绑定 / 区间可断言 / `authorizeByLifecycle` / 全序裁决全部逐字保留）
```

### 3.1 判别支点（承重）

| 观测 | 判决 | 依据 |
|---|---|---|
| 行的 `role == "signing"`（P51 之后写入） | 主体 `domain == "signing"`；**不出现在 P50 的验证域授权里**（`domain_mismatch`） | A1/A2/A9；ADR-077 §3 红例 C |
| 行的 `role == "verifier"`（P51 之后写入） | 主体 `domain == "verifier"`；**永不进入 P41 的 `authorizations`**（与 live trust 文件无关） | A9；ADR-077 §3 红例 A（probe 1 的形状） |
| 该 key 的行**全部** `role == "signing"`，而它作为验证者签发了一条报告 | P50 面 `domain_mismatch`；**注意触发是「按验证域折叠后 `n == 0`」而不是 `domain == "signing"`** ⇒ 一个**迁移**主体（最后一行是 signing 但**有** `role=verifier` 行）**仍走**验证域的常规判定 | A7；红例 C（probe 3）；评审 M2 |
| 行**没有** `role`（P51 之前的行） | `undeclared`（主体级），**且**按 **A4-2** 归属：P41 状态面 = `keyID ∈ 当前 verifierTrust` ⇒ 不列、否则列；P50 授权面 = 一律参与验证域折叠 ⇒ **两个消费者的取值逐字等于 P50**（含 T362 的退役恒等与 T351 的验证者排除）。**这不是「域中立」**（评审 blocker） | A4-2；T371 |
| 同一 key 在**不同 `event_seq`** 上两个域 | `lifecycle_domain_state == "migrated"`；`domain` 取最后一条 `role` 行；**不** fail-closed | A8-③；红例 B（probe 2） |
| 同一 key 在**同一事件组**内两个域 / `role` 不在值域内 | `conflict` ⇒ 面级 `conflict`、响亮报错、**绝不** `declared` | A7；T370 |
| 账本为空 / 全部行 `undeclared` | `lifecycle_domain_state == "undeclared"` ∧ `declared == false`（**空集不为真**） | T373 |

### 3.2 与 P41 / P50 / P44 / A6 的分工（不可混写）

- **P41** 的**时刻**语义（`:544-594` 的 `before_activation` / `after_rotation` / `after_revocation` / `time_unparseable` 与严格 `t < not_before`、`t ≥ not_after`）**逐字复用**且**零改动**——本 Phase 只换**折叠哪些行**。
- **P50** 的**五词与全序**逐字保留；`domain_mismatch` 是**新增第六词**（域错配）。**与 `violated` 的分界**：`violated` = 「这个域的身份，时刻落在窗口外」；`domain_mismatch` = 「这条区间根本不属于这个域」。**与 `unbounded` 的分界**：`unbounded` = 域与时刻都无法断言；`domain_mismatch` = 域**可以**断言，且断言结果是「不是这个域」。
- **P44** 回答「这条报告来自这个家族的哪个身份」（`:757-790`）；本 Phase 回答「这条区间发给哪个域」。**两者必须同时可见、互不掩蔽**（T383）。
- **A6 的域歧义复检（`snapshot_verifier_authority.go:140`/`:180`）保持不变**：它覆盖「**同时**在两个锚」（构造期守卫禁止、读面复检），本 Phase 覆盖「**顺序**迁移」与「**退役**」——两者**非重叠、非替代**（红例 B 显式断言 A6 在迁移输入上为空）。

## 4. 状态面（只读，零副作用）

新增一个**独立组** `key_lifecycle_domains`（全 `omitempty`；**账本未启用 ⇒ 整组缺席**）：

```
key_lifecycle_domains = {
  subjects: {                    // 账本中出现过的每一个主体（升序）
    <key_id>: {
      domain,                    // **恰四个取值**：signing | verifier | undeclared | conflict
      migrated,                  // bool —— **不是**第五个取值（评审 M2）；= 两个域都有带 role 的行
      events, declared_events, undeclared_events,
      validity: { authorized_from?, authorized_until?, terminal_event?, source },  // 复用 P41 validityOf
      reason?                    // UNDECLARED / MIGRATED / CONFLICT 的响亮原因
    }, ...
  },
  lifecycle_domain_state,                  // 标量，全序首个命中：conflict | migrated | declared | undeclared
  lifecycle_domain_declared: <bool>,       // 派生：state == "declared"（不得另行定义）
  lifecycle_domain_subjects: <int>,        // |subjects|
  lifecycle_domain_events: <int>,          // 账本事件总数（见证量）
  lifecycle_domain_declared_events: <int>, // 带 role 的行数（断言量的来源）
  lifecycle_domain_undeclared_events: <int>,  // 无 role 的行数（P51 之前的行；**不可判的见证量**）
  lifecycle_domain_migrated_keys: [<key_id>...],
  lifecycle_domain_conflict_keys: [<key_id>...],
  reason?                                  // 面级原因（账本不可验等）
}
```

- **`undeclared_events` 是承重的**：它是「本面的域断言**没有**覆盖到哪些行」的机器可读形式。**它不蕴含任何状态**：`declared` **不**要求 `undeclared_events == 0`（评审 M3——否则**任何**存有 P51 之前行的账本永远拿不到 `declared`）；非空泛的锚点是 **`declared` ⟹ `declared_events > 0`**，`declared` 与 `undeclared_events > 0` **可以并存**且两者都响亮（T373(a) 钉死这一对）。
- **不可判必须响亮**（A7/A8-②）：`undeclared` / `migrated` / `conflict` 三态**必须**带 `reason` 与计数（沿用 P47 `last_unanchored_reason` / P49 A5 / P50 §4 的纪律，不用粘滞字段）。
- **P50 面的新增计数**：`verifier_authority` 组内每行新增 `domain_mismatch`（int，omitempty）；全局新增 `verifier_authority_domain_mismatches`（int）；**全局标量 `verifier_authority_state` 在新输入上可取 `domain_mismatch`**（全序 `indeterminate > violated > domain_mismatch > authorized > nothing_assessed`）。
- 读面**只读**：既有 load + `os.Stat`；不落盘、不写 audit、不发网络、不派发、不压缩（T375）。

## 5. 不变量（I1~I13）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（含 `snapshot_verification.go`/`snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`/`snapshot_anchor_realization.go`）+ `internal/protection` 零 diff + `go.mod`/`go.sum` 零改动 + **既有账本 `event_digest` 逐字节等价**（末尾 `omitempty` 字段）；P35~P50 判据取值在**既有输入**上零回归（T363/T379/T380） |
| I2 | **P41 语义逐字沿用**：`authorizationFor` / `authorizeByLifecycle` / `validityOf` 三函数**零改动**；按域折叠**只改折叠集合**，不改比较语义（T377） |
| I3 | **未启用账本 ⇒ 整组缺席**：`key_lifecycle_domains` 与 `key_lifecycle` **同一个启用门**（两组的出现/缺席严格同步）（T364） |
| I4 | **已声明域的行只从证据读出（承重，A9/A4-1）**：携带 `role` 的行的域**唯一**来源是行内 `role`；任何「查 `c.verifierTrust` / `c.signingTrust` 决定**这些**行的域」的写法都是 MU2（**HEAD 的形状**）（T367）。**作用域边界**：本不变量**不**覆盖 A4-2 的无 `role` 行——那是 I13，且它是**唯一**允许读 live trust 的地方（评审 blocker 的闭合口径） |
| I5 | **不折叠**：`domain_mismatch` ≠ `violated` ≠ `unbounded`；`undeclared` ≠ `declared`；`migrated` ≠ `conflict`；四面（域面 / P41 状态面 / P41 判定面 / P50 授权面）取值**同时可见、互不掩蔽**（T372/T383） |
| I6 | **判据非空泛**：`declared` 必须由「**行内 `role`** 且无 `conflict`」产出，**不得**由「没找到反例」产出；账本为空或全无 `role` ⇒ `undeclared`（空集不为真）（T373） |
| I7 | **不可判绝不洗白**：`undeclared` / `migrated` / `conflict` 绝不并入 `declared`；`domain_mismatch` 绝不算 `authorized`（T369/T372） |
| I8 | **fail-closed + 面间隔离**：账本不可验 / 不可分类行 / digest 不符 / 断链 / `role` 非法 / **同组域冲突** ⇒ 相应主体 `conflict`、面级 `conflict`、响亮报错；**其余面（P41/P42/P44/P49/P50）取值不受影响**（T370） |
| I9 | **零新增**：账本结构语义零变更、族仍 6、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条、路由零新增、`keyLifecycleRequest` **零新增字段**、密钥类型零新增（T376） |
| I10 | **写入面准入判据不变**：「既不在签名也不在验证者锚」拒绝、「同时在两锚」拒绝的**行为与文案**逐字不变（P50 T349 取值不变）；本 Phase **只**追加「把已判定的域落进事件」（T365） |
| I11 | **A4 回退恒等（承重，与 I13 同源）**：全部行无 `role` 的账本上，P41 状态面与 P50 授权面的取值**逐字等于 P50**（含 T362 的退役恒等与 T351 的验证者排除）——**理由是 I13 的逐消费者旧规则**，**不是**「域中立」（评审 blocker 已删除该措辞）。**先红后绿**（T371/T379/T380） |
| I12 | **主体级域判定必须「全序首个命中」**：`conflict > {signing, verifier} > undeclared`；**禁止**用顺序赋值产出（P50 首轮 M2 的教训：后置赋值会覆盖 fail-closed，方向是 fail-open）（T370） |
| I13 | **A4-2 回退必须逐消费者照搬其 P50 时代的规则（承重，评审 blocker）**：无 `role` 的行**不得**被统一成「一律保留」或「一律排除」——**P41 状态面**按 `keyID ∈ 当前 verifierTrust ⇒ 不列、否则列`（`:897-901` 的原规则），**P50 授权面**按「一律参与验证域折叠」（P50 时代没有域这一维）。⇒ 对**全部**无 `role` 的输入，两个消费者的取值**逐字等于 P50**（T351/T362 双双保持，T371 钉死三种输入）。**违反形态**：任何统一规则 ⇒ MU10（**HEAD 上实测为红**，ADR-077 §2 probe 5） |

## 6. 实现步骤（每步跑门禁）

1. `keyLifecycleEntry` / `keyLifecycleSigned` / `keyLifecycleSignedFields` 末尾加 `Role`（`omitempty`）+ 写入面落值（`:664` 的两个局部变量）→ T363/T365
2. 新文件：`domainOf` + `authorizationForDomain`（浅拷贝后调 P41 的 `authorizationFor`）→ T366/T370/T371
3. **P41 状态面的过滤改取数源**（`:897-901` ⇒ 行内 `role`）→ **先红后绿**：T367（先造 P51 后的验证者行 + 轮换 trust 文件，断言退役主体**不**出现）+ T379
4. **P50 授权面改按验证域折叠 + `domain_mismatch`** → T369/T380（T342/T343/T344/T347/T353/T354/T361 必须原样通过）
5. 迁移与冲突归类（`migrated` / `conflict` + 面级全序）→ T368/T370/T372/T373
6. 新组 `key_lifecycle_domains`（§4，全 `omitempty`）+ 零副作用 → T364/T374/T375
7. **同步既有钉死断言（评审 major-4 补全，四条全列）**：**(a)** `snapshot_verifier_authority_test.go:349-351`（T340）的行 JSON 键集 **11 ⇒ 12**（加 `role`）；**(b)** `:386-388`（T341 无 VAK：顶层键序列 `== p50BaselineStatusKeys()`）；**(c)** `:404-405`（T341 有 VAK：`== 基线 + ["verifier_authority"]`，**含新键相对 `verifier_authority` 的顺序**）；**(d)** 基线本身 `:417`（`p50BaselineStatusKeys()` 加 `key_lifecycle_domains`）→ T364
8. 跨维/冻结/字节等价 T363 + 畸形输入 T378 + 零新增 T376 + 删除保护复用 P49 T381 + 变异 **MU1~MU10**（含 MU10 = 统一 A4-2 ⇒ T351/T371(b) 必红；红→绿，sha256 还原）+ 三道门禁 + mktree 提交 → T382

## 7. 测试映射

T363→I1（冻结面 + 账本字节等价 + `go.mod`/`go.sum`）；T364→I3（默认零回归 + 基线穷举）；T365→I10（写入面落域、调用方不可声明）；T366→A1/A9（域断言）；T367→I4（退役：**先红后绿**，D17）；T368→A8-③/I5（迁移，且 A6 沉默）；T369→I7（域错配：**绝不** `authorized`）；T370→I8/I12（冲突 fail-closed + 全序）；T371→I11/I13（A4-2 逐消费者回退恒等）；T372→I5（三态不折叠）；T373→I6（非空泛 + 空集不为真）；T374→A7（不可判响亮）；T375→§4（零副作用）；T376→I9（零新增）；T377→I2（P41 判定面零回归）；T378→I8（畸形不 panic）；T379→I11/I13（P41 状态面零回归）；T380→I11/I13（P50 零回归）；T381→§3.2（删除保护**复用** P49）；T382→MU1~MU10（**MU10 含 HEAD 上实测为红的判别点**）；T383→I5（跨面同时可见）。

## 8. 容量与成本（诚实声明）

- **热路径成本：零**。本 Phase **不触碰**任何写入路径的**判定**（唯一写入面改动是 `appendKeyLifecycleEvent` 落一个**已经算好**的派生字段，发生在运维提交生命周期事件时，不在 Tick/Check/派发路径上）；P42 报告与 P44 判定零字节变更。
- **读面成本**：状态面每次多读**同一条**账本（不新增第二次 load——新面与 `keyLifecycleSummary` 共用一次 `loadKeyLifecycleState`），多一次 O(行数) 的域归并。管理读面，非热路径。
- **内存**：逐主体一个小结构（主体数 = 账本中的 key 数，通常 1~3）；不缓存（与 P46/P47/P49/P50 的读派生纪律一致）。
- **不可两全（A8-② 的精确口径）**：**P51 之前写入的行**的域**永久不可断言** ⇒ 本面对它们只报 `undeclared`（响亮、带计数），P41/P50 两个面**沿用旧语义**。这是**正确行为**，不是缺陷（判据强度与可用证据同阶），但意味着**存量部署在升级瞬间拿不到这一维**。
- **`migrated` 是合法的**：迁移是运维动作，本 Phase 判「是不是同一个域」，不判「该不该迁移」（A8-⑨）。
- **行为变更声明（穷举，A8-⑦；评审 major-4 补入三条既有钉死断言）**：① **新事件**多一个 `role` 字段（**旧行零字节变化**，T363）；② **新增一个只读组** `key_lifecycle_domains`，启用门与 `key_lifecycle` 相同 ⇒ **凡生命周期账本启用的部署**，状态文档**多一个顶层键**；③ **【补入】三条既有钉死断言必然变红，必须同步更新**：**(a)** `snapshot_verifier_authority_test.go:349-351`（T340）钉死持久化生命周期行的 JSON 键集为**恰 11 键**，而该行经**写入面**产生（`:320` `f.activateVAK`）⇒ **必须扩为 12 键**；**(b)** `:386-388`（T341 无 VAK 的顶层键序列）与 **(c)** `:404-405`（T341 有 VAK 时 `== 基线 + ["verifier_authority"]`，**含顺序**）⇒ 连同基线 `:417` 一并更新；**漏掉任何一条都是「未声明的行为变更」**；④ P41 状态面在「**退役**」「**迁移**」两类输入上取值改变（红例 A/B —— 这是**修**，兑现 ADR-075 §4 A3/A8-⑦ 的承重承诺，**仅**对携带 `role` 的行；对无 `role` 的行由 I13 保证逐字不变）；⑤ P50 授权面在「**按验证域折叠后无行**」这一**新输入**上新增 `domain_mismatch`；**既有输入上零变化**（T380 断言 T342/T343/T344/T347/T353/T354/T361 原样通过）；⑥ 族数、分区表、路由表、`keyLifecycleRequest` 字段集**零变化**（T376）。
- **依赖 P49（同 P50 A8-⑤）**：删除含 `role` 的行 ⇒ 本地域面如实降级为 `undeclared`（**不**冒充 `declared`），锚定启用时由 `kind=key_lifecycle` 锚定条目触发 P49 的 `anchor_unrealized`。**锚定与见证同时关闭 ⇒ 不可检测**，如实登记。

## 9. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「`domain` 会不会是第三个自证字段（P50 首轮 M1 的教训：字段只要被签名覆盖就可能被攻击者任填）？」** | blocker-if-true | **不会**，且这正是 M1 教训的应用：① `role` **不是**请求参数（`keyLifecycleRequest` 零新增字段，I9/T376 类型层面钉死），调用方**无法**声明；② 它是 `appendKeyLifecycleEvent` 在 `:664` 由**锚成员关系**（构造期守卫 V1~V4 的产物）算出的**派生值**，与 `authority_key_id` 同级；③ P50 首轮 M1 的病根是「入队点前后**没有**任何 `key_id` 与签发者的绑定检查」，而本条的域**只有一条写入路径**且由写入面自己填 ⇒ 不存在「另一个来源可任填」的第二个真相源。**代价**如实登记（A8-①：写入时刻配错锚 ⇒ 写错 `role`），**不夸大**。 |
| **Q2「改 `snapshot_key_lifecycle.go` 的 roll-up 过滤会不会又造成一次 P50 终审 M3 那样的回归？」** | major | M3 的教训正是本条的设计约束：**过滤条件必须对旧输入是恒等**。前稿答「域中立 ⇒ 构造性恒等」是**错的**（评审 blocker，ADR-077 §2 probe 5 实测：对 P50 时代**仍被信任**的验证者行，统一规则会把它重新列出 ⇒ `TestP50T351` 必红）。**正确的答法是 I13**：无 `role` 的行**逐消费者照搬其 P50 时代的规则**（P41 状态面 = `∉ 当前 verifierTrust` ⇒ 列），⇒ 恒等**由「就是同一条规则」得到**（T351/T362 双双保持，T371 三种输入钉死），而**有 `role` 的行**才走账本（I4）——两者**不混写**。 |
| **Q3「改为「按域折叠区间」会不会改变 P50 的 `authorized`？」** | major | 只在**新输入**上：无 `role` 的行**一律参与验证域折叠**（I13 —— 这恰是 P50 授权面在 P50 时代的规则），故 P50 的全部既有输入取值不变（T380 直接断言 T342/T343/T344/T347/T353/T354/T361 原样通过）。唯一改变的是「**按验证域折叠后该主体无行**」这一新输入 ⇒ `domain_mismatch`（红例 C）。 |
| **Q4「把无 `role` 的行一律当成 signing（或一律「域中立」）不是更简单？」** | major | **会同时打破两条既有用例**（评审 blocker，本轮实测）：① **统一「当 signing」** ⇒ T362 要求的「退役 **manifest** key 继续列出」成立，但 P50 时代**仍被信任**的验证者行会**被列出** ⇒ `TestP50T351` 必红；② 前稿的**「域中立」**是同一个错误的另一种说法（在 HEAD 上等价于「删掉过滤」）⇒ 实测同样 T351 必红、T362 仍绿。⇒ 唯一可行的是 **I13：逐消费者照搬其 P50 时代的那条规则**（两条规则不同，且各自**就是**旧规则）。**「统一」在这里不可实现**，这是本条 Phase 的存在证明（ADR-077 §2 probe 5）。 |
| **Q5「新组会不会让每个启用账本的部署的状态文档都变，从而违反 P41/P50 的字节承诺？」** | note | **会**，且已按 A8-⑦②③ **穷举声明**并纳入实现步骤 7（四条既有钉死断言 + 基线：`:349-351`/`:386-388`/`:404-405`/`:417`）。这不是隐瞒的回归，是**新面必然附带**的行为变更（P48 第六族曾迫使三条既有枚举用例同步扩展，ADR-071 A8-⑥ 同款先例）。**不夸大**：本 Phase 只声明「新增一个只读组」，不声明「既有取值变了」。 |

## 10. 评审闭合表（首轮：1 项 blocker + 3 项 major —— 与 ADR-077 §11 同源）

| 发现 | 级别 | 闭合（Architecture 侧） |
|---|---|---|
| **B1** A4 的「无 `role` 的行域中立 ⇒ 零回归是构造性的」在 **P50 时代写的无 `role` 的验证者行**上为假（P50 排除、域中立保留）；I4 与 A4/T362 对无 `role` 行给出相反要求；§3 的 `authorizationForDomain(...).rows` 是**未定义**表达式（`keyAuthorization`（`:417-428`）无 `rows`） | blocker | ① `domainOf` 与 `rowsForDomain`/`authorizationForDomain` 拆开：后者**显式返回折叠行数** `(keyAuthorization, int)`，两个调用点（P41 状态面、P50 授权面）**只**用它判「本域在册有没有行」，删除 `.rows`。② **I13 新增（承重）**：无 `role` 的行逐消费者照搬 P50 时代的规则，并写出两条具体规则（P41 = `∉ 当前 verifierTrust` ⇒ 列；P50 面 = 一律参与验证域折叠）。③ **I4 收窄作用域**至 A4-1 的行，并明写 A4-2 是唯一允许读 live trust 的地方。④ §3.1 该行改为「`undeclared`（主体级）**且**按 A4-2 归属」并删除「域中立」措辞。⑤ §9 Q2/Q3/Q4 重写（统一规则**不可实现**，实测 T351 必红），Q5 补三条钉死断言。⑥ §8 行为变更声明③补全三条既有钉死断言。 |
| **M2** `migrated` 在 Scope（面级列表）与 Architecture（`domainOf` 返回 `MIGRATED` 状态）之间定义分歧；P50 面触发写作 `d == SIGNING` ⇒ 迁移主体两个面给出相反判决；§4 的 `subjects.domain` 值域与 `MIGRATED` 自相矛盾 | major | ① `domainOf` 返回 **`{domain, migrated}`**，`domain` 值域**恰四个**（`SIGNING | VERIFIER | UNDECLARED | CONFLICT`），`migrated` 是**独立 bool**，§4 schema 写明 `migrated` **不是**第五个取值。② §3 的 `domain_mismatch` 触发**改为「按验证域折叠后 `n == 0`」**，**不**引用主体级 `domain` ⇒ 迁移主体（有 `role=verifier` 行）走常规判定，两面的判决不可能相反。③ §3.2 与 §3.1 同步该口径。 |
| **M3** §4 原文「`declared` 蕴含 `undeclared_events == 0`」与 T366 要求的 `declared ∧ undeclared_events == 1` 不可能同时成立；面级条件 ③ 的「无 `undeclared` 行」未界定作用域 | major | ① **删除该蕴含式**；非空泛锚点改为 **`declared` ⟹ `declared_events > 0`**，并明写 `declared` 与 `undeclared_events > 0` **可以并存**（否则任何存有历史行的账本永远拿不到 `declared`）。② §3 面级伪码的条件 ③ 去掉「无 UNDECLARED 行」。③ §9 Q5 的「穷举声明」同步。④ ADR-077 §3 权威定义 / T373(a) / §10 Q6 同源闭合。 |
| **M4** 行为变更声明漏掉一条必然打红的既有冻结面断言：`snapshot_verifier_authority_test.go:349-351`（T340）把**持久化生命周期行的 JSON 键集**钉死为**恰 11 键**，而行经写入面产生（`:320`）⇒ 新行落 `role` 后 T340 必红；§1 与 §6 只安排了 `p50BaselineStatusKeys()` | major | ① §1 **新增一行**：`snapshot_verifier_authority_test.go` **修改**（四条钉死断言逐条列出）。② §6 步骤 7 由「更新基线」改为**四条具体断言**（`:349-351` 11⇒12 / `:386-388` / `:404-405` 含顺序 / `:417`）。③ §8 行为变更声明③补全并写「漏掉任何一条都是未声明的行为变更」。 |
