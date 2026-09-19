# ADR-066 — Phase 45: Input Integrity（Architecture）

- **Status**: Proposed (Phase 45, Architecture stage) · drafted by judge-as-executor（同 ADR-065 程序说明）
- **Base**: ADR-065（Scope）
- **凡与 ADR-065 冲突，以 ADR-065 为准并回改本 ADR。**

---

## 1. 文件清单

| 文件 | 变更 | 内容 |
|---|---|---|
| `internal/controlplane/server/snapshot_acceptance.go` | **新增** | 条目模型 / KAK 签发与验证 / 持久化（共享原语）/ 对账算法 / 视图 / HTTP 面 |
| `internal/controlplane/server/snapshot_acceptance_test.go` | **新增** | T237~T257 |
| `internal/controlplane/server/file_transition_store.go` | **修改** | Append 钩子（s.mu 内、Sync 后）：digest 捕获 + 接受条目追加；构造期注入 recorder |
| `internal/protection/alerting.go` | **修改（仅 additive）** | `TransitionLoadResult`（= ReadResult alias）增 `Seqs []int64`（与 Transitions 平行，omitempty）；只此一处 |
| `internal/controlplane/server/history_export_scheduler.go` | **修改** | acceptance 配置字段 / 构造守卫 G1~G3 / 对账接线 / 状态面字段 |
| `internal/controlplane/server/snapshot_anchor.go` | **修改（仅加法）** | 第五流 `acceptance-anchor.jsonl`：`anchorKindAcceptance` + 3 个 omitempty 字段 + `anchorAcceptanceEntry`（模式照抄 `anchorDestructionEntry`，**nil observer**） |
| `internal/controlplane/server/snapshot_destruction.go` | **修改（仅常量）** | `destructionKindAcceptanceCompaction` |
| `internal/controlplane/server/server.go` / `cmd/opscore/main.go` | **修改** | 路由 + 2 个 flag |
| 五个 server 冻结文件 / `appendonly_log.go` / `history_export_coverage.go` / `go.mod` / `go.sum` | **零 diff** | — |

## 2. 定稿字节

### 2.1 条目结构

```go
type acceptanceEntry struct {
    // ---- payload: immutable, KAK-signed ----
    V               int    `json:"v"` // 1
    EntrySeq        int64  `json:"entry_seq"`
    RecordSeq       int64  `json:"record_seq"`
    RecordDigest    string `json:"record_digest"` // sha256(canonical durable 行字节：json.Marshal(newPersisted(seq,t)))
    RecordedAt      string `json:"recorded_at"`   // RFC3339Nano, store 时钟（P34-CLOCK-1 同源纪律）
    AuthorityKeyID  string `json:"authority_key_id"`
    PrevEntryDigest string `json:"prev_entry_digest,omitempty"` // 首行豁免（I3）
    // ---- derived ----
    EntryDigest string          `json:"entry_digest"` // sha256(canonical payload)
    Signature   *signatureBlock `json:"signature,omitempty"`
}
```

- canonical payload = P37 `json.Marshal(固定结构体)` 家族，**禁第二套序列化器**。
- `authority_key_id == 导出签名 key_id` ⇒ 构造失败（G3，P41 T173 同款）。
- **事实非状态**：无 state 区；同 `entry_seq` 任何第二行 ⇒ conflict（load 与 append 双处守卫）。

### 2.2 链

`prev_entry_digest` 串联；链首豁免指向被合法裁掉的条目（I3，P41 同构）。**line_digest 不需要**——接受条目无状态推进，entry_digest 即链值（P41 模式，非 P43 双摘要模式）。

## 3. 写路径接入（file_transition_store.go）

Append 现有临界区 `s.mu` 内、`f.Sync()` 成功与水位推进之后（P32-I15 点，file_transition_store.go:462-470）：

```go
if s.acceptance != nil {
    if aerr := s.acceptance.record(seq, line, s.clock); aerr != nil {
        // 记录已持久（唯一成功点已过）；记账失败响亮暴露，绝不回滚、绝不阻塞导出
        s.acceptanceErr = aerr.Error()
    }
}
```

- **record-first**（ADR-065 A3）：崩溃窗 ⇒ record_unaccepted 响亮。
- `record(seq, line, clock)` 内部：`entry_seq` 由账本自身 max+1 导出（**无水位文件**，R41-6 纪律）、KAK 签名、`appendLogLine`。自有互斥 `acceptanceMu`（并发域 = store.Append 持 s.mu 单写者 + compaction；锁序 **s.mu → acceptanceMu → destructionWriteMu → destructionDispatchMu**，与 P43/P44 既有锁序图合并后必须无环）。
- **锁内无 witness I/O**（P43 N1/N2）：锚定派发收集后置——`record()` 只收集 pending，由 scheduler tick 尾部 `dispatchAcceptancePending()` 统一派发（dispatchMu 内）。
- `acceptanceErr` 暴露于状态面（`input_error`）；**失败不补记**（补记弱化接受时语义），粘滞至下一次成功追加。
- 账本路径 = export dir 同域（与 chain-ledger/chain-anchor/verification-log 一致）。
- **compaction**：账本按 `entry_seq` 组级 prefix compaction，观察者 = `destructionObserver(kind=acceptance_compaction)`（销毁账本记账——它是证据销毁）；**acceptance-anchor 流自身 compaction 不被观察**（I5，nil observer，P43 既有无环结构）。
- 容量满 ⇒ 拒绝新条目入账（Append 仍成功——记录已持久；该记录将 `record_unaccepted` + `input_error` 响亮）——**绝不**裁最旧组腾位（A5/家族纪律：丢最旧 = 洗掉接受证据）。

## 4. 逐记录 seq 暴露（protection additive）

`TransitionLoadResult` / `TransitionReadResult` 增：

```go
// Seqs (Phase 45) aligns 1:1 with Transitions, carrying each record's durable
// seq. Nil when the store does not track per-record seq (nil store / legacy).
Seqs []int64
```

`FileBackedTransitionStore` 的 **ReadAll** 投影从 `persistedTransition.Seq` 填充（Load/ReadRecent 留 nil——对账仅消费 ReadAll）。**零格式变更、零语义变更**（既有消费方忽略新字段）。

## 5. 对账算法（`InputIntegrityView`）

```
input_integrity(c):
  if 未启用:              return {verdict: input_absent}          // 503 面
  load 接受账本（fail-closed：坏行/链断/签名不过 ⇒ input_unverifiable + input_error）

  if snapshot.LoadErr != nil || snapshot.Corrupt:
                            return {verdict: input_unverifiable, input_error}   // 绝不 input_ok 假绿
  if 账本文件缺失/空:        return {verdict: input_absent}                      // 与「从未接受」同形，诚实
  snapshot := store.ReadAll()                              // 含 Seqs
  ledger  := map[record_seq → record_digest]（窗口 [min_entry, max_entry]，continuous）
  coverage_floor := 首条目 record_seq − 1

  for each record in snapshot（配对 seq）:
      d := sha256(其 durable 行字节)          // 见 §6 字节重建
      ledger 有同 record_seq?
        digest 相等  → intact
        digest 不等  → record_modified（可断言）       ★核心产出
      ledger 无      → seq < coverage_floor → outside_coverage（启用前，不断言）
                       else                  → record_unaccepted（响亮）
  for each ledger 条目 seq:
      seq ∈ [MinSeq, MaxSeq] 且快照缺失 → record_missing_in_span（可断言删除）
      seq > MaxSeq                     → record_missing_in_span（可断言——上界无合法逐出）
      seq < MinSeq                     → outside_span（不断言，R40-1 纪律）

  verdict = modified|missing 存在 ⇒ input_modified
            unaccepted 存在     ⇒ input_incomplete
            否则                 ⇒ input_ok
```

- **字节重建（§6 关键）**：对账需要「记录当次的 durable 行字节」，而 ReadAll 返回的是解析后的 struct。机制：`persistedTransition` 重序列化 = `json.Marshal(newPersisted(seq, t))` 与写入路径**同一构造函数同一字段序** ⇒ 字节确定。**风险**：Go struct 字段序变化/新字段会改变重序列化字节 ⇒ 假 modified。缓解钉死：`newPersisted` 为唯一构造点（已成立，:74）+ **T238 反向守卫**（未篡改的记录重导出必须 intact——若重序列化漂移，此测试先红）。`recorded_at` 用 store 时钟（P34-CLOCK-1：store 的时间，不取 scheduler clock）。
- 读面：`GET /management/v1/protection/alerts/history/export/input-integrity`（:8082 admin-only，未启用 503，禁用面不出现新字段）。
- flags：`--export-acceptance-log`（false）、`--export-acceptance-capacity`（4096）。KAK 零新增 flag。

## 6. 不变量（I1~I9）

| # | 不变量 |
|---|---|
| I1 | record-first：durable Sync 成功后才记账；崩溃窗 = unaccepted（响亮），绝不 phantom |
| I2 | record_digest = sha256(durable 行字节)；重序列化唯一构造点 newPersisted；T238 反向守卫防漂移假阳性 |
| I3 | 接受条目事实非状态：同 entry_seq 任何第二行 ⇒ conflict fail-closed |
| I4 | 链首豁免（合法 compaction 后首行 prev 指向被裁条目不算篡改） |
| I5 | 接受账本自身 compaction 记入销毁账本；acceptance-anchor 流自身 compaction 不被观察（无环终止） |
| I6 | 锁序 s.mu → acceptanceMu → destructionWriteMu → destructionDispatchMu，无环；锁内零网络 I/O |
| I7 | capacity 满 ⇒ 拒绝新条目（记录照常持久 + unaccepted 响亮），绝不裁最旧组 |
| I8 | span 边界纪律：seq < MinSeq ⇒ outside_span 不断言；coverage_floor 之前 ⇒ outside_coverage；窗口强制输出 |
| I9 | 未启用零回归：不建文件、Append 零开销、输出逐字节一致；P35~P44 全部取值零变化 |

## 7. 实现步骤（每步跑门禁）

1. `TransitionLoadResult/ReadResult.Seqs` additive + 四投影填充 → 包测试绿
2. `snapshot_acceptance.go`：条目/canonical/KAK/持久化/窗口
3. store 钩子接入（I1/I6/I7）+ T237/T241/T247
4. 对账算法 + T238/T239/T240/T249/T250/T251
5. 锚定第五流 + 销毁记账 + T244
6. 读面 + flags + T236
7. 变异 M1~M3（红→绿）+ 三道门禁 + 冻结面自证 → mktree 提交

## 8. 测试映射

ADR-065 §5 T237~T257 ↔ 本 ADR：T238→§3 钩子；T239→§5 字节重建+I2 反向守卫；T240/T241/T250/T254→§5 span 纪律（含上界）+I8；T242→I1；T243/T244→I2/I3；T245→I5；T248→I6；T249→I6；T255→§5 corrupt 分支；T256→I7；T257→I4+I2；T251→I8。
