# ADR-072 — Phase 48: Runtime Decision Attestation（Architecture）

- **Status**: Proposed (Phase 48, Architecture stage)
- **Base**: ADR-071（Scope）。冲突以 Scope 为准。
- **前置**：Phase 47 CLOSED（HEAD = `df7b73e`），最大既有 T 编号 = **T296**（`snapshot_anchor_delivery_test.go`，P47 §5），故本 Phase 用 **T297~T316**。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_decision_attest.go` | **新增** | 有界非阻塞判定 sink（`protection.ProvenanceSink` + 委派式 `ProvenanceStore`）/ 持久判定日志 / per-tick 前缀摘要 / 第六族注册项 |
| `internal/controlplane/server/snapshot_decision_attest_test.go` | **新增** | T297~T316 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | ① `witnessFamilyRegistry()`（`:1656`）追加第六族；② `Status()` 追加第六族行（复用 P47 的 `anchor_delivery` 组，**不改组结构**）；③ Tick 排空队列 + 写摘要锚定条目 |
| `internal/controlplane/server/snapshot_anchor_delivery.go` | **修改** | `anchorDeliveryStreams()`（`:93`）追加第六流（`SweptBy = delivery_sweep`） |
| `internal/controlplane/server/snapshot_anchor_delivery_test.go` | **修改** | T277（`:263-269`）与 T283（`:486`）的**枚举数量** 5→6、3→4（A8-⑥ 已声明；只加数量，不改断言强度） |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | 仅注释（D5）；**响应字节零变化** |
| `cmd/opscore/main.go` | **修改** | 组装根：`Provenance:` 由裸环改为 `DecisionAttestSink(ring, …)`；sink 交给 scheduler |
| `internal/protection/**` | **零 diff** | A2 承重承诺：判定语义、七守卫顺序、`emitDecision` 调用点逐字不变 |
| `snapshot_anchor.go` / 五族实现 / 冻结面 / `go.mod` / `go.sum` | **零 diff** | 不扩展 witness 协议、不改锚定派发语义、不改 P24.2 读面 |

## 2. 第六族注册（两处静态闭表，各追加一条）

```go
// history_export_scheduler.go — witnessFamilyRegistry() 追加：
{
    Name:        witnessFamilyProtectionDecision,           // "protection_decision"
    LedgerPath:  decisionLogPath,                           // protection-decision.jsonl
    AnchorPath:  decisionAnchorPath,                        // decision-anchor.jsonl
    LocalDigest: witnessAnchorDigestFunc(decisionAnchorPath),
},

// snapshot_anchor_delivery.go — anchorDeliveryStreams() 追加：
{
    Family:     witnessFamilyProtectionDecision,
    AnchorPath: decisionAnchorPath,
    SweptBy:    anchorSweeperDeliverySweep,
    DispatchMu: &decisionDispatchMu,                        // 新增 per-stream 派发互斥（I10）
    Observer:   func(s *HistoryExportScheduler) compactionObserver { return nil },
},
```

- 两处注册表都是**静态闭表**（`witnessFamilyKnown` `history_export_scheduler.go:1694` 以注册表为准；`deliverySweepStreams` `snapshot_anchor_delivery.go:135-143` 按 `SweptBy` 过滤）⇒ 第六族进入后，**P46 的 `POST .../export/witness-reconcile` 与 P47 的 `GET .../export/scheduler` 自动覆盖它，零新路由**（ADR-071 A6）。
- `Observer = nil`：判定锚定流**不做前缀压缩观察**，理由是它没有「压缩丢弃组」的上游记账对象（与 `destruction`/`acceptance` 两族同款，ADR-070 §2 表）；若实现期发现需要，按 I7 的 fail-closed 规则另行声明，不在本 ADR 内扩张。
- `decisionDispatchMu`：新族是 `delivery_sweep` 的**第四个**被 sweep 的流，故必须与 P47 I8 同款地给其生产者调用点加锁（判定 sink 的排空即该流的生产者）——否则「同一时刻只有一个派发者」不成立（P47 I8/T289 同款理由）。

## 3. 持久化与非阻塞 sink（A2/A3/A5）

```
type DecisionAttestSink struct {
    ring  *protection.RecordingProvenanceSink   // 既有环：委派目标（I5）
    mu    sync.Mutex
    queue []protection.DecisionProvenance       // 有界队列（容量 = queueCap，常量冻结）
    dropped   int64                             // 溢出计数（跨进程持久，I3）
    truncated bool                              // 永久置真
    logPath   string                            // protection-decision.jsonl
}

// Emit 实现 protection.ProvenanceSink（provenance.go:55-57）。
// R24-4 逐字保留：入队即返回；队列满 ⇒ 逐出最旧 + dropped++ + truncated=true。
// 绝不阻塞、绝不回压、绝不改判定。
func (s *DecisionAttestSink) Emit(ctx, p) {
    s.ring.Emit(ctx, p)                         // ① 既有环行为逐字保留（I5/T298）
    s.mu.Lock(); defer s.mu.Unlock()
    if len(s.queue) >= queueCap { s.queue = s.queue[1:]; s.dropped++; s.truncated = true }
    s.queue = append(s.queue, p)                // ② 入队（微秒级，无 I/O）
}

// ProvenanceStore 角色：全部委派给 ring（I5）——gate.ProvenanceStore() 返回本对象，
// 但 Recent/ByTraceID/ByCapability/Stats 与环逐字节同义 ⇒ P24.2 读面零回归。
```

- **排空与落盘（Tick 内）**：`drainDecisionQueue()` 取走队列全部记录，按序 append 到 `protection-decision.jsonl`（`O_APPEND`，与五族日志同款单写者纪律），并**持久化 `dropped`/`truncated`**（A5：今天 `ProvenanceStats` 是进程内值，`provenance.go:129-138`，重启归零——本 Phase 必须把它落盘，否则「丢失」会被重启洗成「无丢失」，T304）。
- **摘要与锚定（Tick 内）**：对该 tick 新增前缀取 head digest（复用 `anchorDigestOf` 同族原语），经 `nextAnchorSeqPath(decisionAnchorPath)` 分配 seq，写一条锚定条目（`appendAnchorEntryPathObserved` 形态）⇒ 自动进入 P40 锚定 / P46 对账 / P47 投递。
- **不做逐条锚定**（A8-②）：判定是高频率源（每次 `Check` 一条，`gate.go:114-208`），逐条签名/锚定不可行；粒度 = per-tick 前缀，与 P34 快照的既有粒度一致。

## 4. 状态面（只读，零副作用）

- 第六族**不新增状态组**：它并入 P47 的 `anchor_delivery` 组（`history_export_scheduler.go` 末位 `omitempty` 组），故 anchoring 关闭时整组仍缺席、默认部署状态文档**逐字节不变**（P47 I9 沿用）。
- 第六族行由 `loadAnchorStatePath(decisionAnchorPath, dir, s.trust)` 构建，字段与其余五族**同构**（`present/swept_by/anchored/unanchored/pending/pending_retryable/pending_exhausted/conflicts[]/oldest_pending_recorded_at/last_unanchored_reason?/converged/error?`）。
- 新增**两个源特有字段**（仅第六族有值，其余族省略 ⇒ 零字节变化）：
  - `decision_dropped`（持久化计数）、`decision_truncated`（布尔）；
  - `decision_attested = (该窗口前缀被锚定条目覆盖) ∧ dropped == 0 ∧ 无结构性 error`（ADR-071 §3）。
- `decision_loss` 是 `dropped > 0` 的**读面命名**（沿用 R24-7 的诚实丢失词汇），不是新存储状态。
- 读面**只读**：`os.Stat` + 既有 load + 读一次持久化计数文件；不落盘、不写 audit、不发网络、不派发（P47 I4 沿用）。

## 5. 不变量（I1~I10）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff；**`internal/protection` 零 diff**；`go.mod`/`go.sum` 零改动；P35~P47 判据取值零回归（T297） |
| I2 | **非阻塞（R24-4 逐字保留）**：`Emit` 绝不等待、绝不回压、绝不改判定；慢/满 sink 下判定结果与状态码逐字不变（T305/T306） |
| I3 | 有界 + 诚实丢失：溢出逐出最旧 + `dropped++` + `truncated` 永久置真；两者**跨进程持久**（T303/T304） |
| I4 | **诚实丢失压制断言**：`dropped > 0` 的窗口**绝不**报 `decision_attested`（T303） |
| I5 | **委派式读面**：新 sink 在 `ProvenanceStore` 角色上逐字委派既有环 ⇒ `gate.ProvenanceStore()` 语义不变、P24.2 两面响应字节不变（T298） |
| I6 | 摘要粒度 = per-tick 前缀；**不逐条锚定**（A8-②） |
| I7 | fail-closed：判定日志含不可分类行 / 冲突 seq ⇒ 该族**整条不 sweep**（字节零变化）+ 响亮报错 + **族间隔离**，其余五族不受影响（P47 I3 同款；T312） |
| I8 | **不追认**：启用前的判定不可断言；**绝不**从 audit 行反推（A7-⑦/A8-④；T314） |
| I9 | 秘密边界：新族输出只含 `principal_hash` 与 advisory 字段，**无明文、无密钥**（R24-7 沿用；T313） |
| I10 | 注册闭表：两处静态表各追加一条；第六族恰有一个 sweeper 且无重复覆盖；`decisionDispatchMu` 保证该流同一时刻只有一个派发者（T307/T308/T309） |

## 6. 实现步骤（每步跑门禁）

1. 第六族注册项（两处静态表）+ `decisionDispatchMu` → 编译绿
2. `DecisionAttestSink`（I2/I3/I5：入队 + 委派环）+ T298/T305
3. 排空 + 持久日志 + 丢失持久化（I3/A5）+ T303/T304
4. per-tick 前缀摘要 + 锚定条目（I6）+ T299/T300/T301/T302/T315
5. 状态面字段（I4/I9）+ T307/T313/T311
6. fail-closed + 隔离（I7）+ T312；不追认（I8）+ T314
7. 分区扩展（I10）+ T277/T283 枚举扩展 + T308/T309/T310
8. 跨维/冻结/回归 T297/T306 + 变异 MU1~MU6（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T297→I1；T298→I5；T299/T300/T301→§3 摘要与锚定（核心红例）；T302→ADR-071 §3 `decision_attested`；T303→I4；T304→I3/A5；T305→I2；T306→I1（判定语义零变化）；T307→§2 投递面复用；T308/T309→I10；T310→§2 P46 复用；T311→§4 零副作用；T312→I7；T313→I9；T314→I8；T315→P47 I2 同款；T316→MU1~MU6（MU1 摘落盘 ⇒ T299 必红；MU2 摘 `dropped` 持久化 ⇒ T304 必红；MU3 把丢失并入 `decision_attested` ⇒ T303 必红；MU4 摘 fail-closed ⇒ T312 必红；MU5 破分区 ⇒ T308 必红；MU6 让新 sink 替换而非委派环 ⇒ T298 必红）。

## 8. 容量与成本（诚实声明）

- **热路径成本（承重）**：每次 `Check` 多一次**入队**（有界 mutex，微秒级，无 I/O）。**判定延迟与判定结果不变**（I2/T305）——这正是 R24-4 的要求，也是本 Phase 唯一触碰热路径的地方。
- **Tick 成本**：每 tick 一次队列排空（append 若干行）+ 一次前缀摘要 + 一次锚定条目。无新常驻组件（复用既有调度器）。
- **读面成本**：`Status()` 多读一条判定日志 + 一条锚定日志 + 一个持久化计数（原已读五族锚定流 + 三族状态）。管理读面，非热路径。
- **磁盘**：判定日志有界（溢出逐出最旧 + 诚实计数），上限与 `queueCap`/日志容量为冻结常量。
- **不可两全（A8-③）**：过载时被逐出的判定**在日志里不留痕**，只留计数 ⇒ 读面必须响亮报 `decision_loss` 且绝不报 `decision_attested`。本 Phase 选择「判定不被证据面拖慢」优先于「取证绝对完备」，并如实登记；**不**用阻塞/回压换取完备（那会违反 R24-4 并让保护面不可用）。
- **粒度上界（A8-②）**：单条判定的可断言性止于「落在某个已锚定窗口内」，不是逐条可验证。
