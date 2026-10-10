# ADR-081 — Destruction-Window Serialization（销毁窗口串行化 / ADR-080 登记债的闭合）

- **Status**: Accepted（维护轮，2026-10-11）
- **Phase**: 维护（非证据链 Phase；无新判据、无判据取值变化）
- **Base**: ADR-080（HEAD = `2ae924f`）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-10）
- **Supersedes**: ADR-080 §3 的「不在维护轮修」裁决——其前提（窗口互斥会泄漏）被本 ADR §2 的设计**绕开**（互斥不在 observe/complete 回调上，而在**整个压缩调用**外层，`defer` 释放 ⇒ 无泄漏路径）。

---

## 1. 要闭合的债（ADR-080 §2.2）

两个族的压缩窗口交错时，销毁日志的**链尾规则**（completed 行必须直接紧跟 intended 行）
使第二族的 begin 令第一族的 complete 被拒 ⇒ 败者销毁**永久 `intended`**：
读面报 `destruction_unconfirmed`（可见 ✓），但败者族的锚定派发不发生、P49 兑现面对该族
**保守降级**（`out_of_window` 而非 `compacted`）。可达性：HTTP 驱动的族压缩（P45 验收的
并发追加）与 Tick 驱动的族压缩交错。

## 2. 设计：窗口互斥包住**整个压缩调用**（不是 observe/complete 回调）

ADR-080 否决的「窗口互斥」是把锁放在 observe（begin）与 complete 回调上——而
`compactLogPrefixGroupsObserved`（**冻结**）在 **rewrite 失败路径不调用 complete** ⇒ 锁泄漏
⇒ 之后所有销毁永久阻塞。本设计**绕开**它：

```go
// snapshot_destruction.go（非冻结）
var destructionWindowMu sync.Mutex

func compactAccountedLog(path string, keepGroups int, classify groupClassifier,
                         observe compactionObserver) error {
	destructionWindowMu.Lock()
	defer destructionWindowMu.Unlock()                 // ← 无论 rewrite 成败都释放：无泄漏路径
	return compactLogPrefixGroupsObserved(path, keepGroups, classify, observe)
}
```

互斥覆盖 **observe → rewrite → complete 的整段窗口**，`defer` 保证了「无论压缩内部发生什么
（含 rewrite 失败）锁必然释放」。窗口原子化后，两个族的压缩**串行**通过销毁日志 ⇒ 链尾规则
永不被触发 ⇒ ADR-080 的拒斥不可达。**语义零变化**：无竞争时互斥无争用，压缩行为逐字节一致。

## 3. 接线（六个开窗点；两处在冻结文件，走 ADR-079 先例）

| 开窗点 | 文件（冻结?） | 接线 |
|---|---|---|
| ledger 追加 | `history_export_scheduler.go:1293`（非冻结，**调用**冻结的 `appendLedgerEntryObserved`） | 窗口互斥**包住该调用**（append+compaction 一体）⇒ 冻结文件零触碰 |
| acceptance 轮转 | `snapshot_acceptance.go:560`（非冻结） | 改调 `compactAccountedLog` |
| key_lifecycle 轮转 | `snapshot_key_lifecycle.go:882`（非冻结） | 同上 |
| verification 轮转 | `snapshot_verification.go:1116`（非冻结） | 同上 |
| **锚定流前缀压缩** | `snapshot_anchor.go:640`（**冻结**） | `compactAnchorPrefixPathObserved` 改调 `compactAccountedLog`，**但仅在 `observe != nil` 时取锁**——nil-observer 调用（销毁锚定流的自身簿记，`:1113`）运行在 **L2 持有者内**，若取窗口互斥则成环（§4）。此为 ADR-079 先例的第二次适用：**纯同步性、单行、附 ADR 与回归形状**。**冻结钉随移**：四个测试（P46 I9 / P49 T317 / P50 / P51）把 `snapshot_anchor.go` 的 sha256 钉为 `eca58235…`，本改动使其变为 `2d2f8488…` —— 四个钉**有据同步移动**（本表 + ADR-079 先例），这正是冻结测试的职责：任何冻结面变化都必须**响亮**并强制一次显式决策，而非被允许悄悄通过 |
| decision 日志轮转 | `snapshot_decision_attest.go:677`（非冻结） | **不接线**：P48 §3.4 声明 nil observer ⇒ 无窗口 |

## 4. 锁序分析（无环证明，I8 同款纪律）

新增边（窗口持有者只取销毁族锁）：`[familyWriteMu | L1] → destructionWindowMu →
destructionWriteMu (L3) → destructionDispatchMu (L2) → s.mu (L4)`。

**无环的关键**：不存在任何「持 L2/L3/L4 者再取 destructionWindowMu」的路径——
(a) 销毁日志**没有**经观察原语的自身压缩（grep 全部六个调用点核实）；
(b) 销毁锚定流的自身簿记压缩传 **nil observer** ⇒ 接线 (e) 的条件守卫使其**不取**窗口互斥；
(c) decision 日志不接线。
⇒ `destructionWindowMu` 在锁序图中**只作为出边**出现，偏序无环。.familyWriteMu/L1 持有者
（验收 HTTP 记录、KL 追加、lifecycle sweep）到达窗口点时，其已持锁均属「→ destructionWindowMu」
的合法前驱。

## 5. 验收（证据）

| 验证 | 结果 |
|---|---|
| **生产形状并发用例**（`TestADR081ConcurrentFamilyCompactionsCompleteEveryWindow`：attest ∥ acceptance append 双协程，VerifyCapacity=1 / AcceptanceCapacity=2 ⇒ 每次调用各开一窗，12+12 窗口）+ `-race` × 2 | **全部窗口 completed（零 intended-only）**；读面 `Unconfirmed` 为空；销毁锚定流**每完成窗口恰一条锚**、零冲突零不可用；**ok** |
| 修复前同形状 | ADR-080 的败者拒斥（`intended` 永久 + `unconfirmed ≥ 1`） |
| ADR-080 直接 begin/complete 形状回归 | 保持原判（该形状不经压缩机制 ⇒ 不受窗口互斥影响；生产路径不可达，保留为链尾规则的 API 级证据） |
| server 全套 + `-race` | 见 §7 |
| 全量门禁（非 race） | 见 §7 |

## 6. 后果

- 正面：ADR-080 的记账降级（败者族 `out_of_window`）**不可达**；销毁账本在多写者下保持
  「每窗口必完成、必锚定」；`-race` 下的并发族压缩成为常驻回归形状。
- 代价：冻结面 +1 次同步性触碰（`snapshot_anchor.go:640`，ADR-079 先例第二次适用）；
  族压缩被全局串行（容量驱动的压缩本就低频，串行代价可忽略——窗口持有者只做一次
  ≤capacity 行的 rewrite 与两次账本追加）。
- 未竟：`drainDecisionAttest` 的压缩不接线（无窗口）；若未来给 decision 日志加观察者，
  **必须**同时接入窗口互斥（在 `compactAccountedLog` 的注释里已写明该义务）。
