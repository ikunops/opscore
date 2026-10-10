# ADR-080 — Concurrent family destructions serialize through the chain-tail rule（并发族销毁经链尾规则串行化 / 败者永久 intended 的 fail-closed 行为）

- **Status**: Accepted（维护轮，2026-10-11；登记 + 不修的裁决）
- **Phase**: 维护（非证据链 Phase；无新判据、无判据取值变化）
- **Base**: Phase 51 CLOSED + ADR-079（HEAD = `b5911e1`）
- **Author**: judge（ZCode 代行），用户授权持续推进（2026-10-10）
- **Supersedes**: ADR-069 A7-⑦ / A8-⑦ 的**字面描述**（该描述已过时，见 §2.1）；不修改 P43/P47/P49 的任何判据。

---

## 1. 背景

ADR-079 让 `-race` 首次可用后，维护轮写了一个并发用例
（`snapshot_destruction_concurrency_test.go`）去驱动 ADR-069 A7-⑦ 登记的缺口：
「`destructionObserver` 路径的派发不受 `destructionDispatchMu` 保护」。用例形状：
两个族（ledger / key_lifecycle）的压缩观察者**强制窗口重叠**——两个 begin 都落地后，
两个 complete 并发运行；`-race` 下 `-count=3`。

## 2. 发现（证据）

### 2.1 A7-⑦ 的字面主张**已过时**

现行代码里，内联派发**确实**经过 `dispatchDestructionPending`
（`snapshot_destruction.go:849-860`，取 `destructionDispatchMu`）：
观察者的 complete 闭包 → `completeDestruction`（`:798-812`，`destructionWriteMu` 释放后）
→ `dispatchDestructionPending` → 锁内 → `c.anchorDispatch`。
P47 的 I8/B1 改造（collect→drain、L1→L2 锁序）已经把 A7-⑦ 描述的**裸派发**包了起来。
`-race` 下该并发形状 **3/3 次运行零内存竞争**。⇒ A7-⑦ 的**登记理由消失**，其字面描述
与实况不符（它写于 I8/B1 之前）。

### 2.2 新发现（可达、fail-closed）：链尾规则使败者的完成**永久不可能**

`completeDestructionLocked` 拒绝了败者的完成：

```
destruction: refusing to advance destruction_seq 1 — the group is no longer the
chain tail (another destruction opened after it); it stays intended and will
surface as destruction_unconfirmed / <nil>
```

**机制**：链式设计要求「completed 行必须**直接紧跟**它的 intended 行」（行链 `prev_digest`
把二者锁在一起）。两个族的压缩窗口重叠时——后 begin 的族的 intended 行使先 begin 的组
**不再是链尾** ⇒ 先者的 `completeDestruction` 被**拒绝** ⇒ 该销毁**永久停在 `intended`**：

- 状态面如实报 `destruction_unconfirmed`（**可见**，绝不静默）；
- 销毁账本保持可验证（链完整、每组可用，`-race` 零报告）；
- **该族的锚定派发不发生**（completion 被拒 ⇒ 无 anchor entry）；
- **P49 兑现面保守降级**：`intended`-only 组不计入记账 ⇒ 该族窗口下的行读
  `anchor_out_of_window`（不可判）而非 `compacted` —— **保守，绝不产生假指控**。

**可达性**：begin↔complete 窗口在一次 `compactLogPrefixGroupsObserved` 调用内**同步**
（observe → rewrite → complete），但两个族的压缩发生在**两个不同的文件**上，在 OS 调度层面
可以交错；只要第二个族的 begin 落在第一个族的 begin 与 complete 之间即触发。单写者 Tick
设计下不触发；**多写者**（如 P45 的并发验收追加，T248 形状）下可达。

## 3. 裁决：登记，**不在维护轮修**

**为什么不修**：修复方向是给 begin↔complete 窗口加一把「销毁窗口互斥」。但
`compactLogPrefixGroupsObserved` 的契约**不保证 rewrite 失败路径会调用 complete**：

```go
complete, err = observe(path, groups, dropped)
if err != nil { return err }              // begin 失败 ⇒ 文件字节不变 ✓（锁须在此释放）
if rerr := rewriteLogLines(path, kept); rerr != nil { return rerr }   // ← complete 永不调用
if complete != nil { return complete() }
```

窗口互斥由 complete 释放 ⇒ **rewrite 失败 = 互斥泄漏 = 之后所有销毁永久阻塞**。
另一路径（observe 失败）文件字节不变、无 intended 残留 ✓。此外 rewrite 失败还会留下一个
「账面 intended 但字节未删」的组（本就存在的既有行为，与 ADR-071 A5 的 fail-closed 纪律一致）。

⇒ 关闭此债需要**二选一的设计变更**（都超出维护轮）：
(a) 给压缩机制加「complete 恰好一次（含 rewrite 失败路径）」的契约——改**冻结的** `appendonly_log.go` 契约；
(b) 显式的窗口所有者标志 + 恢复语义——引入新的失败模式。

**登记的已知代价（A8）**：
① 并发族销毁**至多一个窗口能完成**；败者永久 `intended`，状态面可见（`destruction_unconfirmed`）；
② 败者族的 P49 兑现面**保守降级**（`out_of_window`，非 `compacted`）——不产生假指控，但记账强度下降；
③ 触发前提是**两个族的压缩窗口重叠**——Tick 单写者下同族不可达，但 HTTP 驱动的族压缩（P45 验收，T248 形状的并发追加）与 Tick 驱动的族压缩可交错；④ 修复需要 §3 的设计变更，登记为后续设计轮的候选。

## 4. 证据形状

`snapshot_destruction_concurrency_test.go`（`TestADR080ConcurrentFamilyDestructionsFailClosedAndStayVisible`）
在 `-race` × `-count=3` 下钉住四个**不变量**（不钉谁输谁赢）：

0. 恰好一个窗口完成、一个被拒，且拒绝理由**恰为**「no longer the chain tail」（其它错误 = 未文档化的新缺陷）；
1. 销毁账本**可验证**、每组可用，恰 1 个 completed + 1 个 intended-only（**永不**第三态、**永不**撕裂）;
2. 读面把拒斥组报为 `destruction_unconfirmed`（**响亮**，绝不折进 confirmed/accounted）;
3. 销毁锚定流**零冲突 anchor_seq**、全部可验、恰好 +1 条（被拒窗口不派发——保守降级的实证）。

## 5. 对登记账的处置汇总

| 登记项 | 本轮处置 |
|---|---|
| ADR-069 A7-⑦ / A8-⑦（「内联派发不受 `destructionDispatchMu` 保护」） | **字面主张过时**（I8/B1 已包裹；`-race` 3/3 零内存竞争）⇒ 以本 ADR §2.1 更正；其**登记理由消失** |
| 新登记：并发族销毁的链尾串行化（§2.2） | **登记为已知代价**（§3），修复方向具名（§3 的 (a)/(b)），留给设计轮 |
