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
| `internal/controlplane/server/snapshot_key_lifecycle.go` | **修改** | ① `keyLifecycleEntry`（`:98-114`）与 `keyLifecycleSigned`（`:116-128`）**末尾**追加 `Role string \`json:"role,omitempty"\``；② `keyLifecycleSignedFields`（`:130`）带上它；③ `appendKeyLifecycleEvent`（`:646`）把 `:664` 已算出的 `inSigning`/`inVerifier` **落进 `Role`**（准入判据 `:673`/`:681` 一字不改）；④ `keyLifecycleSummary`（`:856`）的 `:897-901` 过滤由「查 `c.verifierTrust`」改为「查**行内 `role`**」（D17）；⑤ `:286-288` 注释按 A8-⑧ 的新口径改写。**账本 schema 语义零变更**（旧行逐字节不变，T363） |
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
- **三个域取值**：`signing`（`:676` 的 `case inSigning:` 分支）/ `verifier`（`:678` 的 `case inVerifier:`）/ 空串（**只可能是 P51 之前的行**——`appendKeyLifecycleEvent` 的两个 `return zero, err` 分支 `:673`/`:681` 保证 P51 之后**不存在**无域的新行）。

## 3. 多态判别（本 Phase 的核心机制）

```
# ---- 主体级域判定（新文件；绝不查 live trust 文件 —— A9）----
domainOf(st *keyLifecycleState, keyID) domainStatus:
    rows := st.byKey[keyID]                          # 升序（既有顺序）
    if any row.Role ∉ {"", "signing", "verifier"}  ⇒ CONFLICT("role is not one of the two domains")
    if 同一 event_seq 组内 Role 不一致              ⇒ CONFLICT("one event group declares two domains")
    roles := {row.Role | row.Role != ""}
    if |roles| == 0:                                return UNDECLARED      # 只可能是 P51 之前的行
    if |roles| == 1:                                return 该唯一值        # SIGNING | VERIFIER
    # 两个域都有 ⇒ 顺序迁移（probe 2 的形状）：域取**最后一条** role 行的值，
    # 并同时被列为 migrated（A8-③）。**这不是 conflict**（两条事实各自可断言）。
    return MIGRATED(last role row's value)

# ---- 按域折叠（新文件；P41 的 authorizationFor 逐字复用，A3/A5）----
authorizationForDomain(st, keyID, want) keyAuthorization:
    # 域中立 = 行没有 role（P51 之前）⇒ 参与**任何**域的折叠 ⇒ 零回归是构造性的（A4）
    keep := [r for r in st.byKey[keyID] if r.Role == "" or r.Role == want]
    if len(keep) == 0:      return keyAuthorization{Source: lifecycleSourceUnbounded, ...}   # 无事实
    copy := *st; copy.byKey = {keyID: keep}          # 浅拷贝，其余字段原样带入（window 等）
    return copy.authorizationFor(keyID)              # ← P41 的函数，零改动（I2）

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
        elif ∃ row: row.domain ∈ {SIGNING, VERIFIER} ∧ 无 UNDECLARED 行 ⇒ "declared"
        else                                    ⇒ "undeclared"     # 全 UNDECLARED 或空账本
    lifecycle_domain_declared := (state == "declared")   # 派生便捷量，不得另行定义

# ---- P41 状态面（改取数源，D17；形如 A4 的构造性恒等）----
keyLifecycleSummary:  for k in st.byKey (升序):
    if len(authorizationForDomain(st, k, DOMAIN_SIGNING).rows) == 0:  continue   # A4/A9：行内 role，不查 trust 文件
    …
# ---- P50 授权面（插入 ⑤.0；其余一字不改）----
for each keyID in observedSigners(vs.entries):
    d := domainOf(ls, keyID)
    if d == CONFLICT:                      cnt.indeterminate++; rowReason="…"; goto verdict
    a := authorizationForDomain(ls, keyID, DOMAIN_VERIFIER)      # ← 按**验证域**折叠（原来是不区域的 :323）
    if d == SIGNING:                       cnt.domain_mismatch++; goto verdict   # 域错配：区间属于另一个域
    …（P50 原有 ①.0 身份绑定 / 区间可断言 / `authorizeByLifecycle` / 全序裁决全部逐字保留）
```

### 3.1 判别支点（承重）

| 观测 | 判决 | 依据 |
|---|---|---|
| 行的 `role == "signing"`（P51 之后写入） | 主体 `domain == "signing"`；**不出现在 P50 的验证域授权里**（`domain_mismatch`） | A1/A2/A9；ADR-077 §3 红例 C |
| 行的 `role == "verifier"`（P51 之后写入） | 主体 `domain == "verifier"`；**永不进入 P41 的 `authorizations`**（与 live trust 文件无关） | A9；ADR-077 §3 红例 A（probe 1 的形状） |
| 行**没有** `role`（P51 之前的行） | `undeclared`：**域中立**——参与任何域的折叠 ⇒ P41/P50 两个面的取值**逐字等于 P50**（含 T362 的退役恒等） | A4 回退；T371 |
| 同一 key 在**不同 `event_seq`** 上两个域 | `lifecycle_domain_state == "migrated"`；`domain` 取最后一条 `role` 行；**不** fail-closed | A8-③；红例 B（probe 2） |
| 同一 key 在**同一事件组**内两个域 / `role` 不在值域内 | `conflict` ⇒ 面级 `conflict`、响亮报错、**绝不** `declared` | A7；T370 |
| 该 key 的行**全部**是 `role == "signing"` 而它作为验证者签发了一条报告 | P50 面该行 `domain_mismatch`（**不是** `authorized`，**不是** `violated`，**不是** `unbounded`） | A7 不折叠；红例 C（probe 3） |
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
      domain,                    // signing | verifier | undeclared | conflict
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

- **`undeclared_events` 是承重的**：它是「本面的域断言**没有**覆盖到哪些行」的机器可读形式——`declared` 蕴含 `undeclared_events == 0`（T373）。
- **不可判必须响亮**（A7/A8-②）：`undeclared` / `migrated` / `conflict` 三态**必须**带 `reason` 与计数（沿用 P47 `last_unanchored_reason` / P49 A5 / P50 §4 的纪律，不用粘滞字段）。
- **P50 面的新增计数**：`verifier_authority` 组内每行新增 `domain_mismatch`（int，omitempty）；全局新增 `verifier_authority_domain_mismatches`（int）；**全局标量 `verifier_authority_state` 在新输入上可取 `domain_mismatch`**（全序 `indeterminate > violated > domain_mismatch > authorized > nothing_assessed`）。
- 读面**只读**：既有 load + `os.Stat`；不落盘、不写 audit、不发网络、不派发、不压缩（T375）。

## 5. 不变量（I1~I12）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（含 `snapshot_verification.go`/`snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`history_export_manifest.go`/`appendonly_log.go`/`snapshot_anchor_realization.go`）+ `internal/protection` 零 diff + `go.mod`/`go.sum` 零改动 + **既有账本 `event_digest` 逐字节等价**（末尾 `omitempty` 字段）；P35~P50 判据取值在**既有输入**上零回归（T363/T379/T380） |
| I2 | **P41 语义逐字沿用**：`authorizationFor` / `authorizeByLifecycle` / `validityOf` 三函数**零改动**；按域折叠**只改折叠集合**，不改比较语义（T377） |
| I3 | **未启用账本 ⇒ 整组缺席**：`key_lifecycle_domains` 与 `key_lifecycle` **同一个启用门**（两组的出现/缺席严格同步）（T364） |
| I4 | **域从证据读出，绝不从 live trust 文件反推（承重，A9）**：读面**唯一**的域来源是行内 `role`；任何「查 `c.verifierTrust` / `c.signingTrust` 决定域」的写法都是 MU2（**HEAD 的形状**）（T367） |
| I5 | **不折叠**：`domain_mismatch` ≠ `violated` ≠ `unbounded`；`undeclared` ≠ `declared`；`migrated` ≠ `conflict`；四面（域面 / P41 状态面 / P41 判定面 / P50 授权面）取值**同时可见、互不掩蔽**（T372/T383） |
| I6 | **判据非空泛**：`declared` 必须由「**行内 `role`** 且无 `conflict`」产出，**不得**由「没找到反例」产出；账本为空或全无 `role` ⇒ `undeclared`（空集不为真）（T373） |
| I7 | **不可判绝不洗白**：`undeclared` / `migrated` / `conflict` 绝不并入 `declared`；`domain_mismatch` 绝不算 `authorized`（T369/T372） |
| I8 | **fail-closed + 面间隔离**：账本不可验 / 不可分类行 / digest 不符 / 断链 / `role` 非法 / **同组域冲突** ⇒ 相应主体 `conflict`、面级 `conflict`、响亮报错；**其余面（P41/P42/P44/P49/P50）取值不受影响**（T370） |
| I9 | **零新增**：账本结构语义零变更、族仍 6、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条、路由零新增、`keyLifecycleRequest` **零新增字段**、密钥类型零新增（T376） |
| I10 | **写入面准入判据不变**：「既不在签名也不在验证者锚」拒绝、「同时在两锚」拒绝的**行为与文案**逐字不变（P50 T349 取值不变）；本 Phase **只**追加「把已判定的域落进事件」（T365） |
| I11 | **A4 回退恒等（承重）**：`role` 缺席的行是**域中立**的 —— 全部行无 `role` 的账本上，P41 状态面与 P50 授权面的取值**逐字等于 P50**（含 T362 的退役恒等与 T351）**先红后绿**（T371/T379/T380） |
| I12 | **主体级域判定必须「全序首个命中」**：`conflict > {signing, verifier} > undeclared`；**禁止**用顺序赋值产出（P50 首轮 M2 的教训：后置赋值会覆盖 fail-closed，方向是 fail-open）（T370） |

## 6. 实现步骤（每步跑门禁）

1. `keyLifecycleEntry` / `keyLifecycleSigned` / `keyLifecycleSignedFields` 末尾加 `Role`（`omitempty`）+ 写入面落值（`:664` 的两个局部变量）→ T363/T365
2. 新文件：`domainOf` + `authorizationForDomain`（浅拷贝后调 P41 的 `authorizationFor`）→ T366/T370/T371
3. **P41 状态面的过滤改取数源**（`:897-901` ⇒ 行内 `role`）→ **先红后绿**：T367（先造 P51 后的验证者行 + 轮换 trust 文件，断言退役主体**不**出现）+ T379
4. **P50 授权面改按验证域折叠 + `domain_mismatch`** → T369/T380（T342/T343/T344/T347/T353/T354/T361 必须原样通过）
5. 迁移与冲突归类（`migrated` / `conflict` + 面级全序）→ T368/T370/T372/T373
6. 新组 `key_lifecycle_domains`（§4，全 `omitempty`）+ 零副作用 → T364/T374/T375
7. **更新 P50 的基线 `p50BaselineStatusKeys()`（`snapshot_verifier_authority_test.go:417`）**，加入 `key_lifecycle_domains`（A8-⑦②的穷举声明）→ T364
8. 跨维/冻结/字节等价 T363 + 畸形输入 T378 + 零新增 T376 + 删除保护复用 P49 T381 + 变异 MU1~MU9（红→绿，sha256 还原）+ 三道门禁 + mktree 提交 → T382

## 7. 测试映射

T363→I1（冻结面 + 账本字节等价 + `go.mod`/`go.sum`）；T364→I3（默认零回归 + 基线穷举）；T365→I10（写入面落域、调用方不可声明）；T366→A1/A9（域断言）；T367→I4（退役：**先红后绿**，D17）；T368→A8-③/I5（迁移，且 A6 沉默）；T369→I7（域错配：**绝不** `authorized`）；T370→I8/I12（冲突 fail-closed + 全序）；T371→I11（A4 回退恒等）；T372→I5（三态不折叠）；T373→I6（非空泛 + 空集不为真）；T374→A7（不可判响亮）；T375→§4（零副作用）；T376→I9（零新增）；T377→I2（P41 判定面零回归）；T378→I8（畸形不 panic）；T379→I11（P41 状态面零回归）；T380→I11（P50 零回归）；T381→§3.2（删除保护**复用** P49）；T382→MU1~MU9；T383→I5（跨面同时可见）。

## 8. 容量与成本（诚实声明）

- **热路径成本：零**。本 Phase **不触碰**任何写入路径的**判定**（唯一写入面改动是 `appendKeyLifecycleEvent` 落一个**已经算好**的派生字段，发生在运维提交生命周期事件时，不在 Tick/Check/派发路径上）；P42 报告与 P44 判定零字节变更。
- **读面成本**：状态面每次多读**同一条**账本（不新增第二次 load——新面与 `keyLifecycleSummary` 共用一次 `loadKeyLifecycleState`），多一次 O(行数) 的域归并。管理读面，非热路径。
- **内存**：逐主体一个小结构（主体数 = 账本中的 key 数，通常 1~3）；不缓存（与 P46/P47/P49/P50 的读派生纪律一致）。
- **不可两全（A8-② 的精确口径）**：**P51 之前写入的行**的域**永久不可断言** ⇒ 本面对它们只报 `undeclared`（响亮、带计数），P41/P50 两个面**沿用旧语义**。这是**正确行为**，不是缺陷（判据强度与可用证据同阶），但意味着**存量部署在升级瞬间拿不到这一维**。
- **`migrated` 是合法的**：迁移是运维动作，本 Phase 判「是不是同一个域」，不判「该不该迁移」（A8-⑨）。
- **行为变更声明（穷举，A8-⑦）**：① **新事件**多一个 `role` 字段（**旧行零字节变化**，T363）；② **新增一个只读组** `key_lifecycle_domains`，启用门与 `key_lifecycle` 相同 ⇒ **凡生命周期账本启用的部署**，状态文档**多一个顶层键** ⇒ **必须**同步更新既有基线 `p50BaselineStatusKeys()`（`snapshot_verifier_authority_test.go:417`）与 T341 的断言；③ P41 状态面在「**退役**」「**迁移**」两类输入上取值改变（红例 A/B —— 这是**修**，兑现 ADR-075 §4 A3/A8-⑦ 的承重承诺）；④ P50 授权面在「**明确签名域**区间」这一**新输入**上新增 `domain_mismatch`；**既有输入上零变化**（T380 断言 T342/T343/T344/T347/T353/T354/T361 原样通过）；⑤ 族数、分区表、路由表、`keyLifecycleRequest` 字段集**零变化**（T376）。
- **依赖 P49（同 P50 A8-⑤）**：删除含 `role` 的行 ⇒ 本地域面如实降级为 `undeclared`（**不**冒充 `declared`），锚定启用时由 `kind=key_lifecycle` 锚定条目触发 P49 的 `anchor_unrealized`。**锚定与见证同时关闭 ⇒ 不可检测**，如实登记。

## 9. 自我对抗（预判首轮评审会打的点，先自答）

| 预判质疑 | 级别 | 自答 |
|---|---|---|
| **Q1「`domain` 会不会是第三个自证字段（P50 首轮 M1 的教训：字段只要被签名覆盖就可能被攻击者任填）？」** | blocker-if-true | **不会**，且这正是 M1 教训的应用：① `role` **不是**请求参数（`keyLifecycleRequest` 零新增字段，I9/T376 类型层面钉死），调用方**无法**声明；② 它是 `appendKeyLifecycleEvent` 在 `:664` 由**锚成员关系**（构造期守卫 V1~V4 的产物）算出的**派生值**，与 `authority_key_id` 同级；③ P50 首轮 M1 的病根是「入队点前后**没有**任何 `key_id` 与签发者的绑定检查」，而本条的域**只有一条写入路径**且由写入面自己填 ⇒ 不存在「另一个来源可任填」的第二个真相源。**代价**如实登记（A8-①：写入时刻配错锚 ⇒ 写错 `role`），**不夸大**。 |
| **Q2「改 `snapshot_key_lifecycle.go` 的 roll-up 过滤会不会又造成一次 P50 终审 M3 那样的回归？」** | major | M3 的教训正是本条的设计约束：**过滤条件必须对旧输入是恒等**。本条的过滤是「该主体是否有 `role == "signing"` 或**无 `role`** 的行」——对一个全部行无 `role` 的账本（= 一切 P51 之前的输入，P50 的 T362 输入）**恒等**（I11/T371/T379）。**不再**依赖任何 live trust 文件（I4）⇒ 顺带消除 D17 的自相矛盾。 |
| **Q3「改为「按域折叠区间」会不会改变 P50 的 `authorized`？」** | major | 只在**新输入**上：`role` 缺席的行仍有贡献（域中立），故 P50 的全部既有输入取值不变（T380 直接断言 T342/T343/T344/T347/T353/T354/T361 原样通过）。唯一改变的是「该主体的行**全部**属签名域」这一新输入 ⇒ `domain_mismatch`（红例 C）。 |
| **Q4「把 `undeclared` 一律当成 signing 不是更简单？」** | major | 会**打破 T362**：T362 要求「退役的**manifest** key 继续列出」（历史行无 `role`）与红例 A 要求「退役的**verifier** key 不再列出」（行有 `role=verifier`）在**同样的可观测量**上给出相反要求 ⇒ 判据只能是**行内的域**，不能是「有没有域」。A4 的「域中立」措辞正是为此：`undeclared` **不是** 「当它是 signing」，而是「它对任何域都可用」——这样两个要求同时成立。 |
| **Q5「新组会不会让每个启用账本的部署的状态文档都变，从而违反 P41/P50 的字节承诺？」** | note | **会**，且已按 A8-⑦② **穷举声明**并纳入实现步骤 7（更新 `p50BaselineStatusKeys()`）。这不是隐瞒的回归，是**新面必然附带**的行为变更（P48 第六族曾迫使三条既有枚举用例同步扩展，ADR-071 A8-⑥ 同款先例）。**不夸大**：本 Phase 只声明「新增一个只读组」，不声明「既有取值变了」。 |
