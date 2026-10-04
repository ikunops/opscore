# ADR-070 — Phase 47: Anchor Delivery Reliability（Architecture）

- **Status**: Proposed (Phase 47, Architecture stage)
- **Base**: ADR-069（Scope）。冲突以 Scope 为准。
- **前置**：Phase 46 CLOSED（HEAD = `9aaab99`），最大既有 T 编号 = **T275**（`snapshot_witness_reconcile_test.go:1069/1093`），故本 Phase 用 T276~T292。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_anchor_delivery.go` | **新增** | 投递面探针（只读）/ sweep / per-stream 派发互斥 / `dispatchAnchorPathObserved`（同义派发 + 家族 observer） |
| `internal/controlplane/server/snapshot_anchor_delivery_test.go` | **新增** | T276~T292 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | ① Tick 尾追加 `s.sweepAnchorDelivery(ctx)`（`:799` 之后）；② `Status()` 追加末位 `omitempty` 组 `anchor_delivery`（`:1486-1530` 内）；③ lifecycle 生产者调用点（`:672`）包裹 `keyLifecycleDispatchMu` |
| `internal/controlplane/server/snapshot_verification.go` | **修改** | verification 生产者调用点（`:1264`）包裹 `verificationDispatchMu` |
| `snapshot_anchor.go` / 五家族实现 / 冻结面 / `go.mod` / `go.sum` | **零 diff** | 不扩展 witness 协议、**不改 delivery 状态机**（pending/anchored/unanchored、attempts 上限、conflict 不落行逐字沿用）、不改 P46 的 GET/POST 两面与响应字节 |

**为什么 lifecycle/verification 需要新的派发互斥**：`destruction-anchor.jsonl` 有 `destructionDispatchMu`（`snapshot_destruction.go:844`，N9 血统），而 lifecycle/verification 的锚定派发**没有任何互斥**（全包非测试代码的包级互斥只有 `acceptancePendingMu:678`、`destructionWriteMu:752`、`destructionDispatchMu:844`、`verificationWriteMu:1004`——后者护的是验证**账本**不是锚定流）。P47 引入一个 sweep 就等于给这两条流引入**第二个派发者**，因此必须同时给生产者调用点加锁，使「每条流同一时刻只有一个派发者」成为不变量（I8/T289）。这是**行为变更**，已在 ADR-069 A8-5 声明。

## 2. 投递流注册表与分区（静态、冻结）

```go
type anchorDeliveryStream struct {
    Family     string                                  // 与 P46 witnessFamilyRegistry 同名（五家族）
    AnchorPath func(dir string) string                 // 该流自己的锚定日志
    SweptBy    string                                  // 唯一 sweeper 的所有者（分区，I1）
    DispatchMu *sync.Mutex                             // 该流的派发互斥（I8）
    Observer   func(*HistoryExportScheduler) compactionObserver // 该流压缩时的记账 observer
}
```

| Family | AnchorPath | SweptBy | DispatchMu | Observer |
|---|---|---|---|---|
| `ledger` | `anchorLogPath` (`:1550`) | `anchor_housekeeping` | （既有 `anchorHousekeeping`，本 Phase 不改） | `nil`（P40 既有形态） |
| `key_lifecycle` | `keyLifecycleAnchorPath` (`:1023`) | **`delivery_sweep`** | `keyLifecycleDispatchMu`（新增） | `s.destructionObserver(kindAnchorCompaction)`（与生产者 `:1143-1145` 同款） |
| `verification` | `verificationAnchorPath` (`:1030`) | **`delivery_sweep`** | `verificationDispatchMu`（新增） | `s.destructionObserver(kindAnchorCompaction)`（与生产者 `snapshot_verification.go:1338-1340` 同款） |
| `destruction` | `destructionAnchorPath` (`:1034`) | **`delivery_sweep`** | `destructionDispatchMu`（既有） | `nil`（该族自己的 **ADR-061 I5** 自压缩终止规则，`snapshot_anchor.go:1064-1071`） |
| `acceptance` | `acceptanceAnchorPath` (`:1038`) | `acceptance_pending` | （既有 `dispatchAcceptancePending`，本 Phase 不改） | `nil`（该族自己的 **ADR-065 I5**，`:1108`） |

- 分区是**静态表**且冻结：五条流各恰有一个 sweeper，**没有一条流被两个 sweeper 覆盖**（I1/T277/T283）。P47 只把 2/5 补成 5/5，**不合并**既有两处 sweep（合并会改 P40/P45 的既有 tick 顺序与调用面，且违反最小闭环）。
- `Observer` 列的取值不是新设计，而是**逐字复制生产者今天用的那一个**——sweep 与生产者在压缩记账上必须不可区分（I5/T291 的差分判据）。

## 3. sweep 算法

```
sweepAnchorDelivery(ctx):                       // Tick 尾最后一步（I10）
  if !s.anchorEnabled(): return

  for st in deliverySweepStreams():             // lifecycle → verification → destruction（顺序冻结）
      st.DispatchMu.Lock()                      // I8：与生产者互斥，绝不嵌套第二个 per-stream 锁
      path := st.AnchorPath(s.cfg.Dir)
      state, err := loadAnchorStatePath(path, s.cfg.Dir, s.trust)
      if err != nil:
          s.setDeliveryError(...)               // A4 fail-closed：该流整条不 sweep，字节零变化
          st.DispatchMu.Unlock(); continue      // 流间隔离：不阻断其余流
      for seq in state.seqs():
          e := state.latest[seq]
          if e.State != anchorStatePending: continue          // anchored/unanchored 一律跳过（I2）
          if e.Attempts >= s.anchorMaxAttempts():             // 与 anchorHousekeeping:1166-1172 同款
              final := e; final.State = anchorStateUnanchored
              final.LastError = fmt.Sprintf("attempts exhausted (%d)", e.Attempts)
              appendAnchorEntryPathObserved(path, s.cfg.AnchorCapacity, final, st.Observer)
              continue
          if derr := s.dispatchAnchorPathObserved(ctx, path, e, st.Observer); derr != nil:
              s.setDeliveryError(derr.Error())
      st.DispatchMu.Unlock()
```

**同义派发（`dispatchAnchorPathObserved`）的理由与代价**：`dispatchAnchorPath`（`snapshot_anchor.go:948-1001`）把 observer 硬编码为 `nil`（`:1000` 走 `appendAnchorEntryPath`），而 lifecycle/verification 的压缩**必须**带 `destructionObserver`（否则 sweep 触发的一次前缀压缩会把被丢弃的组**不记账**地删掉——直接违反 I5）。冻结面（`snapshot_anchor.go` 零 diff）不允许给该函数加 observer 参数，故新增函数**逐字复制**其语义（`anchorDigestOf` → `beforeAnchorDispatch` 钩子 → `anchorTransport.deliver` → `classifyAnchorResponse` → `next := e; next.Attempts++` → anchored/pending/unanchored 三分支与上限终态化 → `appendAnchorEntryPathObserved(..., observer)`），**唯一差异是 observer**。

- 复制是**被冻结面逼出来的**，不是偏好：备选（解冻 `snapshot_anchor.go` 加参数）被否决——冻结面零 diff 是 P47 的核心承诺之一，且解冻会触碰 P40 的判据承重面。
- 复制由 **T291 差分用例**钉住：同输入 + 同 scripted witness 响应下，sweep 落的状态推进行与生产者路径落行**逐字节相同**（含 `attempts`/`state`/`ack_id`/`anchored_at`/`last_error`/`sig`），一旦语义漂移该用例必红。
- 参数化传入的 `observer` 与 `path` 是唯一自由度，其余全部来自冻结函数读到的同一批原语（`anchorDigestOf`/`classifyAnchorResponse`/`appendAnchorEntryPathObserved` 均为包内可调用，无需改冻结文件）。

## 4. 状态面（只读，A5）

- `AnchorDeliveryStatus() anchorDeliveryStatusSummary` 在 `Status()` 内**持 `s.mu` 计算**（与既有 `keyLifecycleSummary`/`verificationSummary`/`destructionSummary` 同一位置、同一并发立场：`history_export_scheduler.go:1523-1540`）。
- `s.anchorEnabled()` 为假 ⇒ 该组为 `nil`（JSON 里整组缺席）⇒ 默认部署状态文档**逐字节不变**（I9）。
- 每流一行由 `loadAnchorStatePath(path, s.cfg.Dir, s.trust)` 构建（**与 sweep 同款加载**，含 trust，与 `anchorHousekeeping`/`dispatchAcceptancePending` 一致）：

```
row.present            = witnessFilePresent(path)          // 复用 P46 的共享探针（:1700）
row.anchored/unanchored/pending = 按 st.latest 的 state 计数
row.pending_retryable  = pending ∧ attempts <  max
row.pending_exhausted  = pending ∧ attempts >= max         // 单列，绝不并入 pending（ADR-069 A8-①/②）
row.oldest_pending_recorded_at = min{ e.RecordedAt | e.State==pending }（time.Parse 比较；解析失败则省略该字段，绝不猜）
row.converged          = pending == 0
row.error              = 该流 load 失败时的响亮原因（绝不静默为 0/0/0）
全局 converged         = 五流皆 converged ∧ 无 error
```

- `oldest_pending_recorded_at` 的语义**精确声明**：`RecordedAt` 是锚定条目**首次记录**的时间，状态推进保留它（`next := e`，`:980`），故它是 backlog 年龄的**下界**，不是「进入 pending 的时刻」。
- 读面**不派发、不落盘、不写 audit、不发网络**（T290）；sweep 只发生在 Tick 内（I4）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | **分区**：五条流各恰有一个 sweeper（`anchor_housekeeping` / `acceptance_pending` / `delivery_sweep`×3）；分区静态冻结；同一 tick 内同一条 entry 绝不重复派发（T277/T283） |
| I2 | `unanchored` 是终态、**永不复活**（ADR-052 §6-12 冻结）；sweep 只推进 `pending`（T282） |
| I3 | fail-closed：不可读的流**整条不 sweep**（不落盘、字节零变化）+ 错误响亮 + **流间隔离**（T286） |
| I4 | 读面零副作用：只读、不派发、不写 audit、不发网络（T290） |
| I5 | **投递语义等价**：sweep 的状态推进与 `dispatchAnchorPath` 逐字节相同，唯一差异是家族 observer（T291） |
| I6 | `converged` ≠ 投递成功：`unanchored` 也收敛 ⇒ 必须与 `anchored`/`unanchored` 计数同面呈现（T285） |
| I7 | 冻结面零 diff；`go.mod`/`go.sum` 零改动；P40~P46 判据取值零回归（T276） |
| I8 | **每流一个派发互斥**：`keyLifecycleDispatchMu` / `verificationDispatchMu`（新增）、`destructionDispatchMu`（既有）；锁序 `{per-stream dispatchMu} → destructionWriteMu → s.mu`，**绝不反向**，且**绝不嵌套两个 per-stream 锁**（T289） |
| I9 | 状态面 append-only：`anchor_delivery` 为**末位** `omitempty` 组，anchoring 关闭时整组缺席 ⇒ 默认部署逐字节不变（T284） |
| I10 | 顺序冻结：sweep 在 `anchorHousekeeping`（`:781`）→ `dispatchAcceptancePending`（`:798`）→ `drainDestructionAnchorQueue`（`:799`）**之后**，是 Tick 尾的最后一步；既有三步的顺序与语义零改动 |

## 6. 实现步骤（每步跑门禁）

1. 投递流注册表 + 分区表 + 三个派发互斥 → 编译绿
2. `dispatchAnchorPathObserved` + **T291 差分**（先写红：未实现时编译不过/行为不等 ⇒ 记录红证据一行）
3. `sweepAnchorDelivery`（I2/I3）+ T278/T279/T280/T281/T282/T286/T287
4. 生产者调用点包裹互斥（I8）+ T283/T289
5. `AnchorDeliveryStatus` + `Status()` 末位组（I6/I9）+ T284/T285/T290
6. Tick 尾接入（I10）+ T277/T288
7. 跨维/冻结/回归 T276 + 变异 M1~M4（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T276→I7；T277→I1；T278/T279/T280→§3 恢复路径；T281→§3 上限终态化；T282→I2；T283→I1；T284→I9；T285→I6；T286→I3；T287→§3 矛盾 pending；T288→ADR-069 §3 压缩活性；T289→I8；T290→I4；T291→I5；T292→M1~M4（M1 摘 sweep 调用 ⇒ §3 恢复路径必红；M2 摘 fail-closed ⇒ I3；M3 摘 unanchored 不复活 ⇒ I2；M4 破分区 ⇒ I1）。

## 8. 容量与成本（诚实声明）

- **Tick 成本**：每 tick 多 3 次锚定日志 load（pending 通常为 0 ⇒ 无派发、无网络）。这是 P47 换取恢复面的固定开销；不引入新常驻组件（ADR-069 A7-6）。
- **读面成本**：每次 `Status()` 多 5 次锚定日志 load（原已 load chain-anchor + 3 个家族状态）。管理读面，非热路径；不缓存（缓存会引入「读到的不是当前状态」的第二真相源）。
- **日志成本**：sweep 的每次推进追加一条状态行（与生产者同款，`state` 不进签名区 ⇒ 状态推进不需重签，ADR-053 §1.3）。
- **重投的 witness 成本**：`409 reason=duplicate` 是幂等确认（`:823-825`，T129）；HTTP witness 无额外副作用。**离线 `file://` dev witness 会把重投逐字追加一行**（`fileAnchorTransport.deliver` `:715-737` 不去重，ack_id 按同 seq 行数递增）——这是 P45 acceptance sweep 已在承受的既有代价，P47 不新增也不掩盖（ADR-069 A8 未列此项，因为它不改变判据取值；此处显式登记以免被读成新保证）。
