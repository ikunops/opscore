# ADR-079 — Race-Detector Baseline（竞态检测基线 / 冻结面缺陷的处置先例）

- **Status**: Accepted（维护轮，2026-10-10）
- **Phase**: 维护（非证据链 Phase；不新增判据、不改任何判据取值）
- **Base**: Phase 51 CLOSED（HEAD = `b9e0c44`）
- **Author**: judge（ZCode 代行），用户授权「下载就下载，持续推进优化，我只要结果」（2026-10-10）

---

## 1. 背景：`-race` 从「不可用」到「可用」

历届文档（含本会话的 P49~P51 记录）一致记载「`-race` 跑不了（本机无 gcc）」。本轮**核实**了该说法
（`CGO_ENABLED=0`、无 `gcc`、`go run -race` 明确报 `-race requires cgo`），随后**经用户授权**下载并解压了
便携版工具链（WinLibs GCC 16.2.0，x86_64 UCRT，261 MB zip → `H:/tools/mingw64/`，不装系统、不改任何机器配置）。

由此，本仓库**历史上第一次**可以在竞态检测器下运行测试：

- `internal/controlplane/server` 全套（~2000 用例）+ `-race` + `-count=1`：**527.98s，零数据竞争**；
- 8 个并发型用例 × `-count=2` × `-cpu 1,4`（32 次运行）：**全部通过，零数据竞争**；
- 快速档 53 包 + `-race`：**发现 1 处真实数据竞争**（见 §2）。

## 2. 缺陷：`FileLoader.lastErrors` 的无锁并发写

**检测器报告**（`TestWatcher_SurvivesReloadError` 触发，`-race`）：

```
Write at 0x00c00014c430 by goroutine 9:
  runtime.(*FileLoader).Discover()   file_loader.go:69
  runtime.(*Watcher).poll()          watcher.go:155      ← 轮询协程（Start 启动）
Previous write at 0x00c00014c430 by goroutine 11:
  runtime.(*FileLoader).Discover()   file_loader.go:69
  runtime.(*Manager).Reload()        manager.go:415
  runtime.(*Watcher).runReload()     watcher.go:221      ← 去抖重载协程
```

**机制**：`FileLoader.lastErrors`（`file_loader.go:46`）在 `Discover()` 里被**无锁**地清空再追加
（`:69` / `:74` / `:87` / `:103`），`LoadErrors()`（`:57`）**无锁**读。`Watcher` 激活后存在**两条并发路径**
驱动同一个 Loader：**轮询协程**（`Start` → `poll`，按 interval 周期性 Discover）与**去抖重载协程**
（`enqueueReload` → `runReload` → `Manager.Reload` → `Discover`）。`runReload` 的 `inflight.LoadOrStore`
只串行化**同一 id 的重载**，**不串行化轮询**；不同 id 的两次重载也会共享同一 Loader ⇒ 竞态可达。
**后果**：`lastErrors` 是切片——并发写可造成撕裂/丢失，`Manager` 折叠进 Bootstrap 错误集的错误报告因此不可靠；
且这是检测器在**常规测试路径**上直接命中的缺陷（不是构造出来的极端时序）。

**为什么此前从未被发现**：本项目从未跑过 `-race`（工具链缺失）；普通测试按顺序调用这些路径，竞态只在
Watcher 的两条路径**真的并发**时显现。

## 3. 决策

1. **修复**：给 `FileLoader` 加**叶级互斥**（`mu`），`Discover` 把错误累积在**局部**切片、结束时一次性发布，
   `LoadErrors` 在锁内读并**返回副本**（调用方持有的旧切片不可能与下一次 Discover 的写入竞争）。
   **不改任何接口、不改任何行为语义、不加任何调用约束** —— `mu` 是叶子锁（持有时不取其它锁，无死锁可能）。
2. **冻结面例外（本 ADR 的核心裁决）**：`internal/plugin/runtime` 在冻结面上（ADR-010/011/012/013；
   守卫测试 `management/guards_test.go` 只锁 **import 方向**，非哈希）。本裁决认为：
   **冻结保护的是运行时契约与行为语义（ADR-013：「依赖它，但绝不修改被冻结的 Runtime Contract」），
   不是保护缺陷永生**。一个被检测器实证的数据竞争是**正确性缺陷**：修复**只增加同步、不改接口、不改行为、
   不改调用方式**，属于维护而非演进；且修复由 ADR 记录（本文件）而非静默混入。**先例**：
   冻结面上的**纯同步性缺陷修复**，须有 ADR + 检测器证据 + 回归用例，方可修改。
3. **门禁接入**：`_zcode_gate.sh` 新增 **`RACE=1`** 选项（设置 PATH/`CGO_ENABLED=1` 并追加 `-race`）；
   产品构建**保持** `CGO_ENABLED=0`。
4. **诚实边界**：竞态检测器**只报运行时真的发生的竞争**；P47 A7-⑦ 登记的 `destructionObserver` 并发缺口
   （`snapshot_destruction.go:1365-1400` 内联派发不受 `destructionDispatchMu` 保护）**未被本轮检测到**——
   因为**没有测试让那条路径真的并发**。检测器通过 ≠ 该缺口不存在；它仍是登记在册的已知代价。

## 4. 后果

- 正面：`FileLoader` 的错误报告在双路径并发下可靠；`-race` 成为可用的门禁档位；冻结面获得了
  「缺陷可修、修须留痕」的明确先例。
- 代价：`plugin/runtime` 的字节基线变化（冻结面 +1 次 diff，随本 ADR 记录）；`LoadErrors` 返回副本
  （调用方若依赖「同一底层数组」的别名语义——无此调用方，已核实）。
- 未竟：`-race` 只覆盖**运行时发生**的竞争；轮询/重载路径在更多 id、更密节奏下的组合仍未被系统性
  并发用例覆盖（T248 形状的用例只覆盖 acceptance 追加）。

## 5. 证据

- `internal/controlplane/server` + `-race`：**527.98s，零竞争**（HEAD `b9e0c44`）。
- `internal/plugin/runtime` + `-race`：修复前**必现**（`TestWatcher_SurvivesReloadError` /
  `TestWatcher_DebounceCollapsesRapidChanges` 两处 `race detected during execution of test`）；修复后复跑见 §7。
- 并发用例 × `-race` × `-cpu 1,4` × `-count=2`：32 次运行全绿。
