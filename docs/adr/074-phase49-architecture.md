# ADR-074 — Phase 49: Anchor Realization（Architecture）

- **Status**: Proposed (Phase 49, Architecture stage)
- **Base**: ADR-073（Scope）。冲突以 Scope 为准。
- **前置**：Phase 48 CLOSED（HEAD = `45a580e`），最大既有 T 编号 = **T316**（`snapshot_decision_attest_test.go`，P48 §5），故本 Phase 用 **T317~T336**。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_anchor_realization.go` | **新增** | 兑现注册表（六行闭表）/ per-family 兑现索引 / 多态判别 / 与 P43 记账的区间匹配 / 状态面派生 |
| `internal/controlplane/server/snapshot_anchor_realization_test.go` | **新增** | T317~T336 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | `witnessFamily`（`:1648-1665`）新增两个字段 `ArtifactDigest`（从账本记录取/重算摘要）与 `CompactionKind`；六族注册项（`:1670-1716`）各填 |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | 状态面新增 `anchor_realization` 组（**全 `omitempty`**，兑现面未启用时整组缺席 ⇒ 默认部署字节不变） |
| `internal/controlplane/server/snapshot_anchor_delivery.go` | **修改** | **仅注释**（D11：`:90-92` 的「five-family」→「six-family」）；**代码零变更** |
| `snapshot_anchor.go` · `snapshot_witness_reconcile.go` · `snapshot_ledger.go` · `appendonly_log.go` · 冻结五文件 · `internal/protection/**` · 六族主账本实现 · `go.mod`/`go.sum` | **零 diff** | A2 承重承诺 |

## 2. 兑现注册表（六行闭表；第 6 行**委派**，见 §2.2）

`history_export_scheduler.go` 的 `witnessFamily`（`:1648-1665`）追加两个字段（该文件**不在冻结面**）：

```go
type witnessFamily struct {
    Name        string
    LedgerPath  func(dir string) string
    AnchorPath  func(dir string) string
    LocalDigest func(dir string, seq int64) (string, bool)
    // Phase 49 (ADR-073 A1) — the realization face's two per-family facts:
    // ArtifactDigest returns the family's ARTIFACT digest for one ledger
    // record, i.e. the value the anchor entry's claim must equal. nil ⇒ the
    // family's realization is DELEGATED (see RealizationDelegated).
    ArtifactDigest func(rec any) (string, error)
    // CompactionKind is the P43 destruction kind that accounts for a LEGAL
    // prefix drop of THIS family's main ledger. "" ⇒ no accounting exists, so
    // a below-window identity can never read `compacted`.
    CompactionKind string
}
```

### 2.1 六族的取数面与摘要来源（全部只读；行号本轮亲跑核对）

| 族 | 锚定条目身份 / 摘要字段 | 账本取数面 | 从账本记录得到摘要的方式 | `CompactionKind` |
|---|---|---|---|---|
| `ledger` | `PublicationID` / `ManifestDigest`（`snapshot_anchor.go:1561-1572`） | `loadLedgerState`（`snapshot_ledger.go:153`）→ `usable map[int64]ledgerEntry`（`:139`） | 账本行**存储值** `e.ManifestDigest`（`snapshot_ledger.go:48`）。**注意：该值不是内容绑定摘要**（`canonicalLedgerPayload` 只用于签名，账本行无自摘要字段）⇒ §3 的**空泛**声明 | `ledger_compaction`（`snapshot_destruction.go:64`；由 `history_export_scheduler.go:1269` 记账） |
| `key_lifecycle` | `EventSeq` / `EventDigest`（`snapshot_anchor.go:1131-1132`） | `loadKeyLifecycleState`（`snapshot_key_lifecycle.go:326`）→ `entries`（`:306`） | 账本行**存储值** `e.EventDigest`；该值已由 load 用 `lifecycleEventDigest` **自校验**（`:357-358`）⇒ 内容绑定 | `key_lifecycle_compaction`（`:66`；`snapshot_key_lifecycle.go:806` 记账） |
| `destruction` | `DestructionSeq` / `DestructionDigest`（`:1055-1056`） | `loadDestructionState`（`snapshot_destruction.go:412`）→ `bySeq`（`:385`） | 账本行**存储值** `e.EntryDigest`；已由 load 自校验（`:478`） | `self_compaction`（`:68`；`:941` 记账——销毁账本自身的压缩） |
| `verification` | `ReportSeq` / `ReportDigest`（`snapshot_verification.go:1337`） | `loadVerificationState`（`:619`）→ `entries`（`:587`） | **必须重算**：从账本行构造**报告的 7 字段** payload（`reportDigest`，`:277-283`）——**不是** `verificationEntryDigest`（`:345-350`，11 字段：多 `v`/`stream_id`/`key_id`/`prev_digest`） | `verification_compaction`（`:67`；`snapshot_verification.go:1116` 记账） |
| `acceptance` | `AcceptanceSeq` / `AcceptanceDigest`（`:1098-1100`） | `loadAcceptanceState`（`snapshot_acceptance.go:307`）→ `entries`（`:287`） | 账本行**存储值** `e.EntryDigest`；已由 load 自校验（`:354-355`） | `acceptance_compaction`（`:69`；`snapshot_acceptance.go:560` 记账） |
| `protection_decision` | `PublicationID`（链头 seq）/ `ManifestDigest`（链头摘要）（`snapshot_decision_attest.go:723-752`） | `protection-decision.jsonl`（P48 哈希链） | **委派**：见 §2.2 | `""`（P48 §3.4 已声明 nil observer，`snapshot_decision_attest.go:668`） |

### 2.2 第六行的委派声明（承重，不可省略）

`protection_decision` 族的兑现**不由本面判定**：P48 的本地重算（ADR-072 §3.3，`snapshot_decision_attest.go:756-824` 的 `decisionLogStateAt`）产出的是**严格更强**的判据（重走链 + 链头比对），本面的朴素形式（"链头 seq 的记录摘要 == 锚定摘要"）是它的真子集。故：

- 该族的取数函数为 `nil`，本面对它报 **`anchor_realization_delegated`**（第 6 个取值），**绝不**报 `realized`/`unrealized`；
- **闭表纪律**：注册表仍**恰好六行**（与 `witnessFamilyRegistry` 同构）——将来新增族时**必须**同时决定其兑现归属，不能静默漏判；T328 钉死「本面与 P48 面结论不冲突」。

## 3. 多态判别（A4/A5；本 Phase 的核心机制）

```
realizeFamily(f witnessFamily, s *HistoryExportScheduler) familyRealization:

  # ① 锚定侧：加载（带 trust ⇒ unusable 可见，与 P46 的签名盲载不同）
  ast, err := loadAnchorStatePath(f.AnchorPath(dir), dir, s.trust)   // snapshot_anchor.go:480
  if err != nil:                                    return INDETERMINATE(err)
  if len(ast.conflicts) > 0:                        return INDETERMINATE("conflicting anchor seq")
  if len(ast.unusable) > 0:                         return INDETERMINATE("anchor entry(ies) do not verify")
  if f.ArtifactDigest == nil:                       return DELEGATED          # §2.2

  # ② 账本侧：加载 + 建身份→摘要索引 + 取保留窗口 [Lmin, Lmax]
  lst, err := loadFamilyLedger(f)                   # 五族各自的既有 load
  if err != nil || lst.hasUnclassifiable || lst.hasConflict:
                                                    return INDETERMINATE(...)
  idx, Lmin, Lmax := indexAndWindow(lst)

  # ③ 记账侧：P43 的已完成压缩记录（区间表）
  acc := loadCompactionAccounting(s, f.CompactionKind)   # 未启用 ⇒ 空表

  # ④ 逐条判决
  for each usable anchor entry e in ast.latest (按 anchor_seq 升序):
      id, claim := f.identityAndClaim(e)
      if id <= 0:                                   INDETERMINATE("zero identity"); break
      if id < Lmin:
          if acc.covers(id):    row = COMPACTED       # 合法前缀压缩（P43 记账）
          else:                 row = OUT_OF_WINDOW   # 不可判（沿用 P46 :245-248 纪律）
      else if id > Lmax:
          row = UNREALIZED                            # 尾部被删（锚定条目仍在）
      else if rec, ok := idx[id]; !ok:
          row = UNREALIZED                            # 窗口内无此身份
      else:
          dg, derr := f.ArtifactDigest(rec)
          if derr != nil:       row = INDETERMINATE(derr)
          else if dg != claim || !f.claimFieldsMatch(e, rec):
                                                    row = UNREALIZED   # 摘要不符 / 内容字段不符
          else:                 row = REALIZED
      rows = append(rows, row)

  if any row == INDETERMINATE:   family = INDETERMINATE
  else if any row == UNREALIZED: family = UNREALIZED
  else if len(rows) == 0:        family = NO_ANCHORS        # 空窗口：不判 realized
  else:                          family = REALIZED
```

### 3.1 三态判别支点（A4，承重）

| 观测 | 判决 | 依据 |
|---|---|---|
| 身份 < 账本保留下界 ∧ **有**已完成的该族压缩记录覆盖 | `COMPACTED` | 合法前缀压缩（P43 记账）——**不是**伪造 |
| 身份 < 账本保留下界 ∧ **无**记账 | `OUT_OF_WINDOW` | **不可判**（销毁面未启用 ⇒ 压缩与删除不可区分）；沿用 P46 `:245-248` 的「no assertion, no waiver, silence」 |
| 身份 > 账本上界 | `UNREALIZED` | 账本尾部被删而锚定条目仍在（P45 F2「无合法出口」同族：账本行先于锚定条目落盘，上界以上无合法来源） |
| 身份在窗口内 ∧ 无该身份记录 | `UNREALIZED` | 窗口内缺席不可能由合法前缀压缩解释 |
| 身份在窗口内 ∧ 摘要/内容字段不符 | `UNREALIZED` | 锚定条目的声明被它自己所声称的证据**否证** |

- **闭区间语义**：`from_seq ≤ id ≤ to_seq`（`destructionTarget`，`snapshot_destruction.go:109-111`；构造点 `:1391`）。
- **只认 `completed`**：`intended` / `aborted` 的记录**不**记账（`snapshot_destruction.go:75-78`；状态机函数 `:797`/`:811` 的 `completeDestruction`，观察者完成回调 `:1400-1410`）。
- **`kind` 必须相符**：`acc` 只收 `kind == f.CompactionKind` 的记录（`:731-738` 的 `destructionKinds` 闭集）。
- **`NoAnchors`**：锚定窗口为空时**绝不**报 `realized`（空集不构成"全部兑现"）。

### 3.2 与 P46 / P47 / P43 的分工（不可混写）

- **P46** 的窗口在**锚定 seq 轴**、比对对象是**域外投影**（`snapshot_witness_reconcile.go:244-282`）；本面的窗口在**证据身份轴**、比对对象是**主账本**。二者**没有一个共同的比对对象**。
- **P47** 只回答"投递是否清偿"（`snapshot_anchor_delivery.go:386`），与内容真伪无关。
- **P43** 只**被读**：本面用它的压缩记账做区间匹配，**不改**它的任何判据、不改 `knownPublications`（`snapshot_destruction.go:1251-1266` 把锚定条目当真——A7-⑩ 非目标）。

## 4. 状态面（只读，零副作用）

- 新增一个**独立组** `anchor_realization`（全 `omitempty`；兑现面未启用 ⇒ 整组缺席 ⇒ **默认部署状态文档逐字节不变**，T318）。
- 每族一行（**六行**，与注册表同构）：

```
anchor_realization = {
  <family>: {
    verdict,                    // realized | unrealized | indeterminate | no_anchors | delegated
    checked, realized, compacted, out_of_window, unrealized,   // 计数
    reason?,                    // indeterminate / out_of_window 的响亮原因
    window: { min_seq, max_seq, entries }   // 该族锚定窗口（沿用 P46 词汇）
  }, ...
  anchor_realized: <bool>       // 全部「非委派且非空」的族皆 realized
}
```

- **`anchor_realized` 是全局合取**：任一**非委派**族为 `unrealized`/`indeterminate`/`no_anchors` ⇒ 假；`delegated` 族**不参与**该合取（§2.2）。
- **不可判必须响亮**（A5）：`indeterminate` 与 `out_of_window` 的族**必须**带 `reason`，且计数同面给出——「不可判」绝不呈现为「没问题」。
- 读面**只读**：`os.Stat` + 既有 load + 一次销毁账本读；不落盘、不写 audit、不发网络、不派发、不压缩（T330）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（**含 `snapshot_anchor.go`/`snapshot_witness_reconcile.go`/`snapshot_ledger.go`/`appendonly_log.go`**）；`internal/protection` 零 diff；`go.mod`/`go.sum` 零改动；P35~P48 判据取值零回归（T317） |
| I2 | **零新族 / 零新写入路径 / 零新路由 / 零新常驻组件**：注册表仍 6 族、分区表仍 6 流、`deliverySweepStreams()` 仍 4 条；既有枚举用例取值不变（T331） |
| I3 | **默认部署零回归**：兑现面未启用 ⇒ 状态文档逐字节不变（T318） |
| I4 | **零副作用**：读面不落盘、不写 audit、不发网络、不派发、不压缩（T330） |
| I5 | **判据非空泛**：`realized` 必须由「**重算/取摘要后逐字节相等**」产出，**不得**由「没找到反例」产出；空窗口 ⇒ `no_anchors`，**不是** `realized`（T326） |
| I6 | **两种「记录变少」严格分离**：合法前缀压缩（P43 记账 ⇒ `compacted`）vs 尾部删除/伪造（⇒ `unrealized`）；判别支点 = **身份是否落在账本窗口内 ∧ 是否有已完成的该族压缩记录覆盖**（T322/T332） |
| I7 | **不可判绝不洗白**：`out_of_window` / `indeterminate` 绝不并入 `realized`；必须带原因与计数（T323/T324） |
| I8 | **fail-closed + 族间隔离**：锚定流或账本侧任一不可分类行 / 冲突 seq / load 失败 ⇒ 该族 `indeterminate`、整族不判、响亮报错，**其余族取值不受影响**（T324） |
| I9 | **verification 族的摘要重算必须走 `reportDigest`（7 字段）**，不得用 `verificationEntryDigest`（11 字段）——两套规范序列化不同（T325） |
| I10 | **闭表 + 委派显式**：兑现注册表恰六行；`protection_decision` 行委派（`nil` 取数 ⇒ `delegated`），本面对它**不产出** `realized`/`unrealized`；与 P48 面结论不冲突（T328） |

## 6. 实现步骤（每步跑门禁）

1. `witnessFamily` 加两字段 + 六族填写（§2.1）+ 编译绿；**确认既有枚举用例不变**（T331 先红：新增族数即红）
2. 五族账本索引 + 窗口（§3 ①②）+ 各族的 `ArtifactDigest`（含 verification 的 7 字段重算，I9）→ T325/T333
3. 逐条判决（§3 ④）+ 三态判别（§3.1）→ T319/T320/T321/T332
4. P43 记账区间匹配（§3 ③，闭区间 + `completed` + `kind` 相符）→ T322/T334
5. 不可判路径（A5/I7）→ T323
6. fail-closed + 族间隔离（I8）→ T324
7. 状态面组（§4，全 `omitempty`）+ 零副作用 → T318/T326/T330
8. 委派与闭表（§2.2/I10）→ T328
9. 空泛声明与 unusable（§3 表 / T327 / T329）
10. 跨维/冻结/回归 T317 + 畸形输入 T335 + 变异 MU1~MU6（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T317→I1；T318→I3；T319→§3.2（新增机制 vs P46/P47 沉默，含 A-3 持导出私钥分支）；T320→§3.1（摘要/内容字段不符）；T321→§3.1（尾部删除）；T322→I6（合法压缩不误报）；T323→I7（不可判响亮）；T324→I8；T325→I9；T326→I5；T327→§2.1（`ledger` 族空泛，如实登记）；T328→I10（委派，与 P48 不冲突）；T329→I8（unusable 不得变绿）；T330→I4；T331→I2；T332→I6（三态判别）；T333→§3.1（身份边界四态）；T334→§3.1（闭区间 + `completed` + `kind`）；T335→I8（畸形输入不 panic）；T336→MU1~MU6（MU1 摘「重算摘要比对」⇒ T319/T320 必红；MU2 把 `out_of_window` 并入 `unrealized` ⇒ T323/T332 必红；MU3 摘 fail-closed ⇒ T324 必红；MU4 用 `verificationEntryDigest` 替代重算 `reportDigest` ⇒ T325 必红；MU5 摘 `ledger` 族空泛声明 ⇒ T327 必红；MU6 让读面产生副作用 ⇒ T330 必红）。

## 8. 容量与成本（诚实声明）

- **热路径成本：零**。本 Phase **不触碰**任何写入路径、任何 `Check`/`Tick`/派发路径；锚定条目结构零变更（A7-①）。
- **读面成本**：状态面每次多读**五条主账本**（锚定流已由 P46/P47 面读过）+ 一次销毁账本读 + 每族一次索引构建（线性于保留条数，默认上界 4096/族，`cmd/opscore/main.go:497`/`:503`）。管理读面，非热路径。
- **内存**：每族一个 `身份 → 摘要` 索引（默认 ≤4096 项）——读时构建，不缓存（与 P46/P47 的读派生纪律一致）。
- **不可两全（A8-③ 的精确口径）**：`--export-anchor-capacity` 与 `--export-ledger-capacity` 是独立旋钮，且锚定流「未确认组永不逐出」（`cmd/opscore/main.go:503`）⇒ **锚定流可能比主账本保留更多**。该残差**只能**由 `out_of_window` 表达（不可判），**绝不**由 `unrealized` 表达——否则合法配置变更会被读成伪造。
- **依赖销毁面（A8-②）**：销毁面关闭时，下界以下一律 `out_of_window`；本 Phase **不**因此降级判据强度，只如实报"不可判"。
- **空泛边界（A8-①）**：`ledger` 族对本 Phase 的主对手（持导出私钥者）**空泛**——如实声明并 T327 钉住；该族由 P38/P39 链 + P40 对账承担。
- **上界（A8-④）**：兑现只证明"载体所声称的证据产物存在且摘要一致"，**不**证明证据产物的内容为真；**A-3 的持 VAK 私钥分支原样保留**（A7-⑧）。
