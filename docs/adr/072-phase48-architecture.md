# ADR-072 — Phase 48: Runtime Decision Attestation（Architecture）

- **Status**: Proposed (Phase 48, Architecture stage) · Revision 2（对齐 ADR-071 修订版：机制边界、哈希链本地重算、载体决策、两种「记录变少」、穷举测试改动）
- **Base**: ADR-071（Scope 修订版）。冲突以 Scope 为准。
- **前置**：Phase 47 CLOSED（HEAD = `df7b73e`），最大既有 T 编号 = **T296**（`snapshot_anchor_delivery_test.go`，P47 §5），故本 Phase 用 **T297~T316**。

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_decision_attest.go` | **新增** | 哈希链判定日志 / 有界非阻塞 sink（`protection.ProvenanceSink` + **委派式** `ProvenanceStore`）/ `decisionGroupOf` + 观察者 / 链头重算与比对 / 第六族锚定条目构造器 / 第六族注册项 |
| `internal/controlplane/server/snapshot_decision_attest_test.go` | **新增** | T297~T316 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | ① `witnessFamilyRegistry()`（`:1656`）追加第六族；② **`witnessFamilyEnabled`（`:1742-1760`）新增第六族 case**（5-case switch + `default false` ⇒ 漏则恒报 `not_enabled`，ADR-071 D7/A8-⑥）；③ Tick 排空队列 + 写链头锚定条目 |
| `internal/controlplane/server/snapshot_anchor_delivery.go` | **修改** | `anchorDeliveryStreams()`（`:93`）追加第六流（`SweptBy = delivery_sweep` + `decisionDispatchMu`） |
| `internal/controlplane/server/snapshot_anchor_delivery_test.go` | **修改** | T277（`:268` `len(streams) != 5`）与 T283（`:486` 三条 sweep 流）的**枚举数量** 5→6、3→4 |
| `internal/controlplane/server/snapshot_witness_reconcile_test.go` | **修改** | T259（`:418` `len(res.Families) != 5`）的**枚举数量** 5→6（ADR-071 A8-⑥/D6；评审 M5 补齐） |
| `internal/controlplane/server/mgmt_obs.go` | **修改** | 仅注释（D5）；**响应字节零变化** |
| `cmd/opscore/main.go` | **修改** | 组装根：`Provenance:` 由裸环改为 `DecisionAttestSink(ring, …)`；sink 交给 scheduler |
| `internal/protection/**` · `snapshot_anchor.go` · 五族实现 · 冻结面 · `go.mod`/`go.sum` | **零 diff** | A2 承重承诺（判定语义/七守卫/`emitDecision` 调用点逐字不变）+ A1 载体承诺（**复用既有槽位，不加字段**） |

**注（评审 M5）**：P47 的投递面行**不需要手改**——`AnchorDeliveryStatus()`（`snapshot_anchor_delivery.go:351-353`）遍历 `anchorDeliveryStreams()` 自动产生每一行；本 Phase 只需改注册表本身。

## 2. 第六族注册（三处，全部为静态闭表/switch）

```go
// (a) history_export_scheduler.go — witnessFamilyRegistry() 追加：
{
    Name:        witnessFamilyProtectionDecision,        // "protection_decision"（新常量，定义在新文件）
    LedgerPath:  decisionLogPath,                        // protection-decision.jsonl
    AnchorPath:  decisionAnchorPath,                     // decision-anchor.jsonl
    LocalDigest: witnessAnchorDigestFunc(decisionAnchorPath),
},
// (b) history_export_scheduler.go — witnessFamilyEnabled() 追加 case：
case witnessFamilyProtectionDecision:
    return s.decisionAttestConfig().enabled()            // 第六族自己的启用门（与其余族同构）
// (c) snapshot_anchor_delivery.go — anchorDeliveryStreams() 追加：
{
    Family:     witnessFamilyProtectionDecision,
    AnchorPath: decisionAnchorPath,
    SweptBy:    anchorSweeperDeliverySweep,
    DispatchMu: &decisionDispatchMu,                     // 新增 per-stream 派发互斥（I10）
    Observer:   func(*HistoryExportScheduler) compactionObserver { return nil },
},
```

- (a) 决定 P46 的 `POST .../export/witness-reconcile` 与 GET 探针（`snapshot_witness_reconcile.go:91/:384` 遍历注册表；`history_export_scheduler.go:1694` `witnessFamilyKnown` 以注册表为准）；(c) 决定 P47 的分区与状态面。**零新路由**（ADR-071 A6）。
- `Observer = nil`：第六族的**锚定流**不做前缀压缩观察，理由与 destruction/acceptance 两族同款（它是问责系统自身的簿记；ADR-070 §2 表）。**注意**：这与 §3 的**主判定日志**压缩观察者是两回事（后者见 I6）。
- `decisionDispatchMu`：第六族是 `delivery_sweep` 的**第四个**被 sweep 的流 ⇒ 必须与 P47 I8 同款给其生产者调用点加锁，否则「同一时刻只有一个派发者」不成立（P47 I8/T289）。

## 3. 判定记录：哈希链、载体与本地重算（A1/A4）

### 3.1 记录与链

```
decisionRecord := {
    seq, prev_digest, digest,                 // 链：digest = sha256(canonical(其余字段))；prev_digest = 前一条的 digest
    at, trace_id, capability_id, principal_hash, guard, decision, action,
    threshold, observed, detail, latency_micros,   // 逐字取自 protection.DecisionProvenance（provenance.go:20-50）
}
```

- 落盘：`O_APPEND` 单写者（`appendonly_log.go:99` 同款），**每条记录一行 JSON**。
- 链头 = 最后一条记录的 `digest`；`seq` 单调（`max+1`，与五条主账本同款）。

### 3.2 锚定条目的载体（评审 M3 闭合：复用两个既有非-omitempty 槽位）

`snapshot_anchor.go` 属铁律冻结面，`anchorEntry`/`anchorSigned`（`:100-177`）**没有通用 payload 摘要字段**，只有按族分配的槽位。故第六族**复用**：

| 槽位 | 第六族的取值 | 依据 |
|---|---|---|
| `Kind`（omitempty） | `"protection_decision"` | 新常量定义在**新文件**（`anchorKind*` 块在冻结文件 `:65-78`，不得改） |
| `PublicationID`（非 omitempty，非 publication 族历史恒为 0） | **锚定时刻的判定链头 `seq`** | 既有槽位；使 `identityID()`（`:208-217`）返回有意义的身份而非回退到 0 |
| `ManifestDigest`（非 omitempty，非 publication 族历史恒为 `""`） | **锚定时刻的判定链头 `digest`** | 既有槽位；`anchorSignedFields`（`:177`）拷贝它 ⇒ **在签名覆盖区内** |
| `AnchorSeq` / `RecordedAt` / `State` | 与其余族同款 | |

- **语义重载显式声明**（ADR-071 A8-⑨）：`ManifestDigest` 对 publication 族是 manifest 摘要，对非 publication 族历史上恒为 `""`（`anchorDestructionEntry` `:1054`、`anchorAcceptanceEntry` `:1098`、`anchorKeyLifecycleEvent` `:1132` 均不设它）。第六族把它用作**判定链头摘要**——字段名不改（改即破坏冻结面与既有五族的签名 payload）。T297 断言五族既有条目**字节不变**（对它们 `manifest_digest` 仍为 `""`）。
- `identityID()` 对第六族返回 `PublicationID`（= 链头 seq > 0），因此**不**走 0 回退；`st.byID[identityID()] = e`（`:532`）键为链头 seq，有意义。T302 钉死该行为。

### 3.3 本地重算（评审 M1/M2 闭合：本 Phase 的**新增机制**）

```
attestDecision():
  st  := loadAnchorStatePath(decisionAnchorPath(dir), dir, s.trust)   // 复用 P47 同款加载
  if len(st.conflicts) > 0:                      return DIVERGENT     // 锚定流自相矛盾：不可评估
  a   := 最新【anchored】条目（State==anchored 中 max PublicationID）   // 存在但未确认的条目锚定不了任何东西
  if 无 anchored 条目:                            return NO_ANCHOR
  recs, headSeq, headDigest, chainOK := walkDecisionChain(decisionLogPath(dir))
  if !chainOK:                                   return DIVERGENT     // 链内断裂：篡改
  if headSeq <  a.PublicationID:                 return TRUNCATED     // 尾部被删（前缀）
  if headSeq == a.PublicationID && headDigest != a.ManifestDigest:  return DIVERGENT   // 同长度不同内容：篡改
  if headSeq >  a.PublicationID:                 return PENDING_ANCHOR // durable 但未被见证（F1：绝不 attested）
  return ATTESTED                                                      // 链完整 ∧ 链头与最新已确认锚定条目严格一致
```

- `walkDecisionChain`：按 `seq` 升序读，逐条校验 `prev_digest == 前一条.digest`。**允许**首条的 `prev_digest` 指向已被**合法前缀压缩**掉的记录（故不能把「首条 prev_digest 无对应」判为断裂）——这是与「尾部删除」的判别支点（I6/T315）。
- **与 P46 的分工（不可混写）**：P46 的对账只比对 `digestOfLocalAnchor(&e)`（本地**锚定条目**）与 witness 投影的 `AnchorDigest`（`snapshot_witness_reconcile.go:244-282`），`LedgerPath` 只参与存在性探针（`:175`/`:384`）⇒ **P46 对主判定日志的内容一无所知**。本节的本地重算**不是** P46 的复用，是新增面。T299/T300 必须同时断言「本地重算报 divergent/truncated」与「P46 报 `family_intact`」，以证明二者不重叠。

### 3.4 非阻塞 sink（A2/A3/A5）

```
type DecisionAttestSink struct {
    ring  *protection.RecordingProvenanceSink   // 委派目标（I5）
    mu    sync.Mutex
    queue []protection.DecisionProvenance       // 有界（queueCap 冻结常量）
    dropped int64                               // 队列溢出计数（跨进程持久，I3）——仅此计入 decision_loss
    logPath string
}

// Emit 实现 protection.ProvenanceSink（provenance.go:55-57）。
// R24-4 逐字保留：入队即返回；队列满 ⇒ 逐出最旧 + dropped++。绝不阻塞/回压/改判定。
func (s *DecisionAttestSink) Emit(ctx, p) {
    s.ring.Emit(ctx, p)                         // ① 既有环行为逐字保留（I5/T298）
    s.mu.Lock(); defer s.mu.Unlock()
    if len(s.queue) >= queueCap { s.queue = s.queue[1:]; s.dropped++ }
    s.queue = append(s.queue, p)                // ② 入队（微秒级，无 I/O）
}
// ProvenanceStore 角色：Recent/ByTraceID/ByCapability/Stats 全部委派给 ring（I5）。
```

- **Tick 排空**：取走队列全部记录，按序算 `digest`/`prev_digest`/`seq` 后 append 到 `protection-decision.jsonl`；并**持久化 `dropped`**（A5：今天 `ProvenanceStats` 是进程内值，`provenance.go:129-138`，重启归零 ⇒ 必须落盘，否则「丢失」会被重启洗成「无丢失」，T304）。
- **Tick 摘要**：写一条第六族锚定条目（§3.2 载体），`PublicationID`/`ManifestDigest` = 当前链头 ⇒ 自动进入 P37 签名 / P40 锚定 / P46 对账 / P47 投递。
- **不做逐条锚定**（A8-②）：判定是高频率源（每次 `Check` 一条，`gate.go:114-208`）。

## 4. 状态面（只读，零副作用）

- 第六族**不新增状态组**：并入 P47 的 `anchor_delivery` 组（末位 `omitempty`），anchoring 关闭时整组仍缺席 ⇒ 默认部署状态文档**逐字节不变**（P47 I9 沿用）。
- 第六族行与其余五族**同构**（`present/swept_by/anchored/unanchored/pending/pending_retryable/pending_exhausted/conflicts[]/oldest_pending_recorded_at/last_unanchored_reason?/converged/error?`），另加**三个源特有字段**（仅第六族有值，其余族省略 ⇒ 零字节变化）：

```
decision_queue_dropped   = 持久化计数（仅「从未落盘」的丢失；合法前缀压缩不计入——I6）
decision_log_state       = attested | divergent | truncated | pending_anchor | no_anchor | not_enabled
decision_attested        = (decision_log_state == attested)
                           ∧ (该锚定条目已 anchored)          // ② P47 投递面
                           ∧ (decision_queue_dropped == 0)    // ③
                           ∧ (无结构性 error)                  // ④
```

- **`decision_attested` 的四个合取项缺一不可**（评审 M2）：缺 ① 就退化为「锚定条目存在」，而锚定条目每 tick 必写 ⇒ 判据近乎恒真。T302 断言其非空泛。
- `decision_log_state` 的取值由 §3.3 的本地重算产出；`no_anchor` 表示该族尚无【已确认】锚定条目（首 tick 之前或全部条目仍在途）；`pending_anchor` 表示存在已确认前缀但本地日志已延伸至其外（durable 但未被见证——锚定派发失败或积压；**绝不 attested**，终审 MAJOR F1 修订：原「或已更长，由更新的锚定条目覆盖」的放宽被推翻——该假设恰在锚定追加失败且尾部静默时失效，Scope §3 ① 的严格等式胜出）。
- 读面**只读**：`os.Stat` + 既有 load + 走一遍判定日志 + 读一次持久化计数；不落盘、不写 audit、不发网络、不派发（P47 I4 沿用）。

## 5. 不变量（I1~I11）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（**含 `snapshot_anchor.go`**）；**`internal/protection` 零 diff**；`go.mod`/`go.sum` 零改动；P35~P47 判据取值零回归；五族既有锚定条目字节不变（T297） |
| I2 | **非阻塞（R24-4 逐字保留）**：`Emit` 绝不等待、绝不回压、绝不改判定；慢/满 sink 下判定结果与状态码逐字不变（T305/T306） |
| I3 | 有界 + 诚实丢失：队列溢出逐出最旧 + `dropped++`；`dropped` **跨进程持久**（T303/T304） |
| I4 | **诚实丢失压制断言**：`decision_queue_dropped > 0` 的窗口**绝不**报 `decision_attested`（T303） |
| I5 | **委派式读面**：新 sink 在 `ProvenanceStore` 角色上逐字委派既有环 ⇒ `gate.ProvenanceStore()` 语义不变、P24.2 两面响应字节不变（T298） |
| I6 | **两种「记录变少」严格分离**：合法前缀压缩（`compactLogPrefixGroupsObserved` `appendonly_log.go:140` + `decisionGroupOf` + 观察者；**链头不变**；**不计入** `dropped`）vs 尾部删除/篡改（链头不符）；判别支点 = 链头是否仍在锚定头上（T315） |
| I7 | **本地重算非空泛**：`decision_attested` 必须包含「重走链 + 链头比对」；`decision_log_divergent`/`decision_log_truncated` 由**本地重算**产出，**不是** P46 复用（T299/T300/T302） |
| I8 | **载体不改结构**：`anchorEntry`/`anchorSigned` 零字段变更；第六族复用 `Kind` + `PublicationID` + `ManifestDigest`；语义重载已在 ADR-071 A8-⑨ 声明（T297/T302） |
| I9 | fail-closed：判定日志含不可分类行 / 锚定流冲突 seq ⇒ 对应面**整条不 sweep**（字节零变化）+ 响亮报错 + **族间隔离**（P47 I3 同款；T312） |
| I10 | 注册闭表：三处注册各追加一条；第六族恰有一个 sweeper 且无重复覆盖；`witnessFamilyEnabled` 有第六族 case（否则恒 `not_enabled`）；`decisionDispatchMu` 保证该流同一时刻只有一个派发者（T307/T308/T309） |
| I11 | **不追认**：启用前的判定不可断言；**绝不**从 audit 行反推（A7-⑧；T314） |

## 6. 实现步骤（每步跑门禁）

1. 第六族注册（三处：注册表 / enable switch / 分区表）+ `decisionDispatchMu` → 编译绿；同步扩 T277/T283/T259 的枚举（D6）
2. 哈希链判定日志 + `walkDecisionChain`（§3.1/§3.3）+ T299/T300（先红：未实现时无 divergent/truncated）
3. `DecisionAttestSink`（I2/I3/I5：入队 + 委派环）+ T298/T305
4. Tick 排空 + 丢失持久化（I3/A5）+ T303/T304
5. 第六族锚定条目构造器（§3.2 载体）+ 链头摘要（I6/I8）+ T302
6. 本地重算面 + 状态字段（I4/I7）+ T301/T307/T310/T311/T313
7. fail-closed + 隔离（I9）+ T312；不追认（I11）+ T314
8. 前缀压缩 + 观察者（I6）+ T315；投递唯一性（I10）+ T308/T309
9. 跨维/冻结/回归 T297/T306 + 变异 MU1~MU6（红→绿，sha256 还原）+ 三道门禁 + mktree 提交

## 7. 测试映射

T297→I1/I8；T298→I5；T299→I7（本地重算 vs P46 沉默）；T300→I7；T301→§3.3 与 P46 的分工；T302→§4 `decision_attested` 四合一 + I8 载体；T303→I4；T304→I3/A5；T305→I2；T306→I1；T307→§2 投递面复用；T308→I10（含 enable case）；T309→I10；T310→§2 P46 复用；T311→§4 零副作用；T312→I9；T313→ADR-071 A8-⑧；T314→I11；T315→I6（压缩 vs 尾部删除）；T316→MU1~MU6（MU1 摘本地重算 ⇒ T299 必红；MU2 摘 `dropped` 持久化 ⇒ T304 必红；MU3 把丢失并入 `decision_attested` ⇒ T303 必红；MU4 摘 fail-closed ⇒ T312 必红；MU5 破分区或漏 `witnessFamilyEnabled` case ⇒ T308 必红；MU6 让新 sink 替换而非委派环 ⇒ T298 必红）。

## 8. 容量与成本（诚实声明）

- **热路径成本（承重）**：每次 `Check` 多一次**入队**（有界 mutex，微秒级，无 I/O）。**判定延迟与判定结果不变**（I2/T305）——本 Phase 唯一触碰热路径的地方。
- **Tick 成本**：一次队列排空（append 若干行 + 逐条算链摘要）+ 一条锚定条目。无新常驻组件（复用既有调度器）。
- **读面成本**：`Status()` 多读一条判定日志（走链）+ 一条锚定日志 + 一个持久化计数（原已读五族锚定流 + 三族状态）。管理读面，非热路径。
- **磁盘与保留（评审 M4 后的精确口径）**：判定日志 **append-only + 合法前缀压缩**（`compactLogPrefixGroupsObserved` + `decisionGroupOf` + 观察者；容量为冻结常量）。压缩是**有界保留**，不是丢失——被压缩的记录在压缩前已持久并被锚定条目覆盖，且**链头不变**；因此**不计入** `decision_loss`（I6/T315）。
- **不可两全（A8-③）**：**队列**过载时被逐出的判定在日志里不留痕，只留计数 ⇒ 读面必须响亮报 `decision_loss` 且绝不报 `decision_attested`。本 Phase 选择「判定不被证据面拖慢」优先于「取证绝对完备」，**不**用阻塞/回压换取完备（那会违反 R24-4 并让保护面不可用）。
- **粒度上界（A8-②）**：单条判定的可断言性止于「它落在某个已锚定窗口内且其后的链未被改动」，不是逐条可验证。
