# ADR-065 — Phase 45: Input Integrity（输入面完整性 / 记录级接受时证据）

- **Status**: Proposed (Phase 45, Scope stage) · drafted by judge-as-executor（workbuddy 配额耗尽，用户 2026-09-19/20 授权 ZCode 代行；独立评审换人负责，方法论不变）
- **Phase**: 45（Input Integrity）
- **Base**: Phase 44 CLOSED（origin/main = `130248b`）
- **Supersedes**: 无。不修改 P35~P44 的任何冻结判据。

---

## 1. 分工句（第六问）

P37 **who**（谁签的）/ P40 **where**（域外可证）/ P41 **when**（授权期）/ P42 **whether**（是否验证过）/ P43 **why absent**（消失是否在案，publication 面）/
⇒ **P45 = what accepted（输入面）**：P30~P44 的全部证据从**导出点**起算；P45 问「**导出点之前，store 里的记录还是系统当初接受的那份吗**」。

## 2. 方向选择

C2（输入面完整性）为 R216 登记的下一候选（「真实且重大——改 durable 记录后重导出，全链合法」），P43/P44 完成后其前置（销毁语义层、验证者独立）已就位，排序在先。HA 仍冻结（一致性不是证据性）。

## 3. 新问题（已核对代码，非推断）

**事实**：

1. 唯一持久化写入口：`internal/protection/alerting.go:376` `_ = t.store.Append(...)`（P30-I6 best-effort，同步无重排 P30-I9）；唯一持久实现 = `FileBackedTransitionStore`（`file_transition_store.go`；nil store = 内存模式）。
2. durable 记录带 seq（`persistedTransition.Seq`，file_transition_store.go:64/74；恢复水位 `max(meta.last_seq, max(record.seq))` :196-205）；行字节 = `json.Marshal(newPersisted(seq, t))`（:446）——**durable 行字节即规范字节**。
3. 读投影 `ReadAll/ReadRecent/ReadBefore/Load` 报 `MinSeq/MaxSeq`（P35）但**不暴露逐记录 seq**；ring 两级有界（256 + file 10000，P30）⇒ 合法逐出存在。
4. 全仓 `acceptance / record_digest / integrity` **零命中**——输入面零完整性证据。
5. `internal/protection` 非冻结包（冻结面 = platform/governance/plugin/{runtime,isolation}/controlplane/hostregistry + server 内五个冻结文件 + appendonly_log.go），但自 P35（d0090e7）未动。

**悖论**：持 **store 写权限**（sqlite/文件直写，非进程控制）的攻击者可改记录载荷、删记录、插伪造记录；下一次导出（P33 单次 ReadAll 快照）忠实签发被篡改内容——`signature_ok`、`chain_ok`、`anchor`、`verification`、`destruction` 全绿。**「系统当初接受的记录」与「store 里现在的记录」在本地视图内同形**，且六个 Phase 的证据栈对此全部沉默：它们证明「导出之后没被改」，不证明「导出之前是什么」。

### 为什么独立成 Phase

并入 P43？销毁账本的对象是**导出产物**（artifact/manifest/日志）；输入面是**store 记录**，身份轴（record seq）、写入口（store Append）、威胁时刻（接受时 vs 销毁时）全部不同，混入即重演 R210 拒绝并入 P39 的理由。并入 P44？验证者独立是「谁裁定」，输入完整是「裁定的对象是否原样」——正交。

## 4. 能力定义（A1~A8）

### A1 — 接受账本（`record-acceptance.jsonl`）

Append **持久化成功后**（durable Sync 完成，file_transition_store.go:454-459，P32-I15 水位推进点），store 在**同一临界区内**计算 `record_digest = sha256(durable 行字节)` 并追加接受条目：

```
{v, entry_seq, record_seq, record_digest, recorded_at,
 authority_key_id, prev_entry_digest, entry_digest, signature}
```

- **KAK 签发**（授权事件家族第三次显式扩展：lifecycle ∪ destruction ∪ **acceptance**——R43-1 教训：同钥签发 = 持导出钥者可洗账本）。G1~G3 守卫同 P43。
- 哈希链 `entry_digest = sha256(canonical payload)`、`prev_entry_digest` 串联（P39/P41 同构）；**接受条目是事实不是状态机**：同 `entry_seq` 任何第二行一律 conflict ⇒ fail-closed（P41 纪律，非 P40 状态推进）。
- 强制复用共享原语 `appendonly_log.go`（真 O_APPEND / 坏行 fail-closed / 组级 compaction 逐字复制 / 失败清理句柄）；分组键 = `entry_seq`。
- bounded：`--export-acceptance-capacity`（默认 4096，0 = 全留）。

### A2 — 字节级接受承诺（零第二序列化器）

`record_digest` 直接哈希 **durable 行字节**（as persisted）。理由：①不引入第二套 canonical（纪律）；②字节级承诺最强——载荷语义等价重排也算 modified（R205「幸存行逐字复制」同一哲学）；③实现即一行 `sha256(line)`。

### A3 — 提交顺序：record-first

```
durable 行 Sync 成功（既有代码）→ 接受条目追加（同 s.mu 临界区内）
```

- 崩溃窗 = 记录已持久但未接受 ⇒ 对账报 **`record_unaccepted`（响亮，非静默、非故障断言）**。
- **禁止 acceptance-first**：其崩溃窗产生「账本有而盘无」⇒ missing 误报（phantom，同 P39 禁 ledger-first 同构）。
- 接受账本写路径的并发纪律按 P43 教训钉死：临界区覆盖 read→append→compaction 全程；**锁内无 witness 网络 I/O**（P43 N1/N2：派发收集后置）。

### A4 — 对账（verify/export 时逐记录）

以 ReadAll 快照（含逐记录 seq，见 A6）对照接受账本：

| 情形 | 结论 |
|---|---|
| 快照记录 ∧ 账本同 record_seq 条目 ∧ digest 相等 | `record_intact` |
| 快照记录 ∧ 账本有条目 ∧ **digest 不等** | **`record_modified`（可断言，核心产出）** |
| 账本有条目（record_seq ∈ [MinSeq, MaxSeq]）∧ 快照缺失 | **`record_missing_in_span`（可断言删除）** |
| 账本条目 record_seq < MinSeq | `outside_span` ⇒ **不断言**（ring 合法逐出与删除不可区分，R40-1 边界纪律） |
| 快照记录 ∧ 账本无条目（启用前 / 崩溃窗） | `record_unaccepted`（响亮） |
| 账本含不可解析行 / 链断 / 签名不过 | 整本不可验 ⇒ `input_unverifiable` + `input_error`（绝不静默、绝不谎报 intact） |

- **per-record seq 暴露**（A6 机制）：`TransitionReadResult` 增 `Seqs []int64`（与 `Transitions` 平行对齐，omitempty）——**internal/protection/alerting.go 仅此一处 additive 修改**，durable 文件格式零变更。
- aggregate：`input_integrity{verdict ∈ input_absent|input_ok|input_modified|input_incomplete|input_unverifiable, trust_reason?, modified_ids[], missing_in_span_ids[], unaccepted_ids[], outside_span_count, coverage_floor, input_error?}`。

### A5 — 锚定（第五独立流）与销毁记账

- 接受条目在锚定启用时进 P40 通道：`kind=acceptance`，**独立文件** `acceptance-anchor.jsonl`（第四…第五流，I5 自指教训：**该流自身 compaction 不被观察**）。
- 接受账本自身的 compaction **记入销毁账本**（`destructionObserver(kind=acceptance_compaction)`）——它与 ledger/anchor/verification 同类，都是证据销毁；递归终止：销毁记录派发只进 destruction-anchor（不回 acceptance 流），destruction-anchor 自身 compaction 不被观察（P43 既有结构，无环）。

### A6 — 零回归（硬约束）

- 未配置 `--export-acceptance-log` ⇒ 不建账本、Append 路径零新增开销（一个 nil 判断）、verify/export 输出逐字节不变、状态面新字段 omitempty。
- 不改 ReadRecent/ReadBefore/cursor 语义；不改 P35~P44 任何判据、取值、输出字段；`history_export_coverage.go` 零 diff。
- 新增修改面：`file_transition_store.go`（钩子）、`internal/protection/alerting.go`（**仅** Seqs 字段 additive）、新 `snapshot_acceptance.go`(+test)、scheduler/`server.go`/`main.go` 接线、`snapshot_anchor.go`（第五流，仅加法）、`snapshot_destruction.go`（新 kind 常量）。
- `go.mod`/`go.sum` 零改动；冻结包零 diff。

### A7 — 非目标（重新冻结）

1. 不做逐记录签名（账本条目签名 ≠ 每条记录一个签名；记录完整性由「账本条目 + 字节摘要」合成证明）。
2. 不改 durable 文件格式 / 不做 store 版本升级（migrate v2 已定，零变更）。
3. 不解决进程内攻击者（能控制进程者可同改 store 与账本——同 P40/P41/P42 家族声明）。
4. 不回填启用前历史（coverage floor = 账本首条目 record_seq − 1；之前的记录恒 `outside_coverage`，绝不报 unaccepted）。
5. 不新增常驻组件（记账在 Append 内联；对账在 verify tick / 读时）。
6. 不做「modified ⇒ 停止导出」（反向 fail-open：攻击者改记录即可掐断导出——P42 同款原则）。
7. 不做内存模式（nil store）的输入面——功能自然缺席，诚实声明。

### A8 — 已知代价（诚实声明）

1. **上界 = 攻击者无进程控制**。进程内攻击者同改 store 与账本 ⇒ 退化为自证（家族同款）。账本整体删除 ⇒ `input_absent` 与「从未启用」本地不可区分（P41 T177 同族），锚定启用时域外可检测。
2. **MinSeq 抬高掩饰删除**：攻击者篡改 store 元数据抬高 MinSeq ⇒ 缺失记录落 outside_span ⇒ 不可断言（与合法逐出不可区分，R40-1 家族代价）。反向（压低 MaxSeq）无收益：更高 seq 的账本条目反而 missing_in_span 暴露。
3. **崩溃窗 `record_unaccepted` 是正常态**（秒级），响亮但不得据此推断异常。
4. 启用前历史零保护（coverage floor 之前恒 outside_coverage）——家族同款（P40/P41 起点代价）。
5. 记账有界 ⇒ 早期接受条目被合法 compaction 裁掉 ⇒ 对应记录的 intact 断言不可用（账本窗口纪律：结果强制携带 `acceptance_window{min_entry, max_entry, entries, continuous}` + `coverage_floor`；段不连续 ⇒ `window_discontinuous` 可断言）。

## 5. 测试契约（T236~T252）

| # | 断言 |
|---|---|
| T236 | 未启用 ⇒ 不建账本、Append 零开销、输出逐字节不变 |
| T237 | Append ⇒ 接受条目落账本（record_seq 对齐、digest = durable 行字节哈希、KAK 签名验真） |
| T238 | **核心红例（non-vacuousness）**：篡改 durable 记录字节 ⇒ P35~P44 全绿 ⇒ 仅 P45 观测 `record_modified` |
| T239 | 删记录（span 内）⇒ `record_missing_in_span` |
| T240 | seq < MinSeq ⇒ `outside_span` 不断言（与 T239 同 fixture 判别） |
| T241 | record-first 崩溃窗 ⇒ `record_unaccepted`（响亮） |
| T242 | 账本坏行 ⇒ fail-closed 拒追加 + `input_error`，文件字节不动 |
| T243 | 同 entry_seq 第二行（含同 payload）⇒ conflict（事实非状态） |
| T244 | 锚定第五流 + acceptance 流自身 compaction 不被观察 + acceptance_compaction 记入销毁账本（无环） |
| T245 | 跨维零干扰：P35~P44 全部取值零变化 |
| T246 | 冻结面零 diff（protection 核心仅 alerting.go Seqs additive） |
| T247 | 并发 Append 全部被记账，entry_seq 无跳空（P43 临界区教训） |
| T248 | 锁内无 witness I/O（派发收集后置） |
| T249 | MinSeq 抬高 ⇒ 缺失落 outside_span（诚实红例：不可断言，绝不误报 modified/missing） |
| T250 | 账本窗口 + coverage_floor 强制输出；段不连续 ⇒ `window_discontinuous` |
| T251 | coverage floor 之前的记录 ⇒ outside_coverage（启用前历史，绝不 unaccepted） |
| T252 | 变异 M1~M3（摘 modified 判定 / 摘 span 边界 / 放行同 entry_seq 第二行 ⇒ T238/T240/T243 必红，sha256 还原） |

## 6. 与既有 Phase 的关系

```
P30~P34 导出管线      ┐
P35~P39 证据五层      ├— 全部从导出点起算 ⇒ 输入面同形缺口
P40~P44 见证/时间/验证/销毁/验证者 ┘
P45 接受时证据：把「系统当初接受了什么」固化为 KAK 签发的字节级承诺 ⇒ 输入面可对账
```

不降级任何既有判据；第四…第六维正交新增（input_integrity 与 anchor/lifecycle/verification 并列）。

## 7. 提请评审确认的取舍（已裁决，可反驳）

- **Q1** digest 基于 durable 行字节而非独立 canonical —— 采纳（A2 理由）。
- **Q2** record-first 顺序 —— 采纳（A3 理由）。
- **Q3** KAK 签发（授权事件家族第三扩展）—— 采纳（R43-1 同构）。
- **Q4** `internal/protection/alerting.go` 单点 additive 是否可接受 —— 采纳（非冻结；备选 = 结果类型留在 server 包新建，但那会分裂结果类型，更差）。
