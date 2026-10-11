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

## 7. 附带收口：D20 残余在本设计下**不可构造**（证据 = T384 + 折叠规则）

ADR-077 A8-⑪ 登记的残余——「销毁面**关闭**期间发生的压缩未被记账 ⇒ 该期间的
`domain_mismatch` 仍是既有取值」——经本轮**逐一核对可达性**后收敛：

**残差形状**（要害）：某主体的 verifier 域行**曾被记账压缩裁掉**、且它现在只剩 signing 域行
⇒ 验证域折叠计数为 0 ⇒ `domain_mismatch` 把「曾有而不可考」误断为「在案的都不属于本域」。

**不可构造论证**：
1. **post-A11（本 ADR 前身 e0967dd 的单域不变量）**：写面拒绝跨域行政事件 ⇒ 一个主体的行
   只会落在**单一已声明域**。verifier 行存在过 ⇒ 后续 signing 行被拒绝 ⇒ 「verifier 行被裁掉
   且剩 signing 行」的形状**写不出来**（T384 钉死该拒绝）。
2. **pre-A11（无 `role` 行）**：按 A4-2，P50 授权面把无 `role` 行**一律折叠进验证域**
   ⇒ 只要有任一行幸存，折叠计数 > 0 ⇒ 不触发 `domain_mismatch`；若全部被裁 ⇒
   `len(byKey)==0` ⇒ 走常规路径读 `unbounded`（无区间可判）——同样**不触发**。
3. **手工/异构账本**（不经本写面）仍可能含该形状：此时 `domain_mismatch` 的字面断言
   （「在案的每一条授权都属于另一域」）对**幸存行**是准确的；不可考的部分由 P51 域面的
   `undeclared_events` 计数响亮呈现（A8-②），不是静默。

⇒ **残差闭合**：本设计（A11 单域 + A4-2 折叠 + 窗口串行化）下，该误断形状无法经生产写面产生；
手工形状下断言字面为真且不可考部分响亮。ADR-077 A8-⑪ 的残余段以此为准更新。

## 8. 附带裁决：rewrite 失败留下的 stuck-intended 尾是**诚实的现状**，不自动愈合

`compactLogPrefixGroupsObserved` 在 **rewrite 失败路径不调用 complete**（§2）⇒ 会留下一个
只有 `intended` 行的尾组（账面记录了一次从未发生的销毁——字节仍在）。曾考虑「下一个窗口的
begin 自动愈合：把卡住的尾组补记为 `aborted`」——**裁决：不做**：

1. **`intended` 恰好陈述了已知的一切**：「有一次销毁意图，其结果未知」。自动补 `aborted`
   等于**编造确定的结果**——rewrite 失败可能是原子替换前的任何一点（文件可能完好、也可能撕裂），
   记录者无法事后区分。
2. **撕裂情形已 fail-closed**：`rewriteLogLines` 若中途失败留下不可分类行，加载器会整体拒绝
   该账本（I2）⇒ 不可读即不可谎报。
3. **可见性已足够**：`destruction_unconfirmed` 让操作者看到「有待确认的销毁意图」，
   配合族账本的实际状态即可人工判定；P49 兑现面对 intended-only 组**不记账**（保守，
   绝不假指控——与 ADR-080 §2.2 同方向）。
4. **窗口互斥（本 ADR）已消灭最坏的形状**：ADR-080 的窗口交错会让**中间组**永久卡在
   `intended`（其后有他族行，连 abort 都不可达）；互斥使卡住的组只可能是**尾组**
   （前一轮 rewrite 失败的遗留）——至少状态可见、不阻塞后续窗口。

⇒ 登记为**预期行为**：rewrite 失败 ⇒ 尾组停留 `intended` + `destruction_unconfirmed`
（响亮、保守、不虚构）。若未来要闭合，需先给压缩机制立「complete 恰好一次（含 rewrite
失败路径）」的契约（ADR-080 §3 (a)），并区分「未销毁（aborted）」与「已销毁但未确认
（需对照族账本窗口）」两种情形——各自都是设计轮。

## 9. 附带登记：`TestServer_Inventory_Detail` 的环境敏感抖动

全量门禁一次重包档失败（`system.host.info Ok:false, Steps:[]`），3/3 复跑通过。根因：
inventory 的 detail 白名单经**真实执行栈**跑每个 op，而测试的 echoHandler 是
**真实的 `cmd /c echo` 进程 spawn**（5s 超时）——重负载下进程创建偶发失败 ⇒ Ok:false。
**与本轮任何改动无关**（窗口串行化不触及执行栈）。登记为**测试环境敏感抖动**；
不为掩盖而加重试（那会削弱「detail 是真实执行输出」的断言）。
