# 运维与演进（Operations）

> **SSOT**：本文档固化 OpsCore 的演进纪律、继电器铁律、机械守卫纪律与继电器流程。
> 所有内容经 R68–R71 多轮 GPT 签字确认。

## 1. 演进章程（ADR-021 §6）

任何 **Major 演进** 必须走三级签字，且 **不直接编码** —— 先定范围：

1. **Scope ADR**（如 ADR-033）：定义范围与非目标、冻结边界。
2. **Architecture ADR**（如 ADR-034）：定义信息/代码结构，不写实现。
3. **Implementation**：落地，经实现签字（如 R69）后提交。

每级都发继电器给固定 ChatGPT 线程签字（verdict: A 接受 / B 需修改 / C 拒绝）。

## 2. 继电器铁律（Relay Iron Rules）

实现 → `_align_gpt.py` 发往 **固定 ChatGPT 线程**（`6a610ef1-…「Execution Snapshot 设计」`）
→ 读回文 → 落地 → 编译验证 → `git commit`（单引号、不 push）→ 写 `round_N_prompt.txt`
→ 启动继电器。

- **只抓不重发**：回文补救用 `fetch_reply.py --round N`（capture-only），**绝不重跑 `_align_gpt.py`**
  （重发会污染线程）。
- **绝不重跑 `_align_gpt.py`**：它没有「只抓不重发」模式，`send_one` 会再发一次 prompt。
- 编译环境：`GOTOOLCHAIN=local GOSUMDB=off`（零触网、用本地工具链）。
- **Runtime Contract 零变更**：所有 Phase 不改动冻结的执行契约。
- 提交纪律：`git commit -m '...'`（单引号 message），**不 push**，等完整 Phase 收口或按铁律。

## 3. 机械守卫纪律（Mechanical Guard Discipline）

- **AST import guard**：外围包禁止 import 冻结执行路径
  （`core/execution`、`plugin/runtime`、`plugin/isolation`、`controlplane/*`、`builtin/*`）。
- **`TestNoExecMethod`**：外围包禁止 `Run`/`Exec`/`Invoke`/`Apply`/`Execute`/`Command`/`Emit`/
  `Dispatch`/`Rollback`/`Kill`/`Schedule` 等执行方法。
- **wiring / lifecycle tests**：组合根与生命周期有专门测试覆盖。
- **零新依赖**：配置层用 `encoding/json`（无 yaml）；`go.mod` 仅极少依赖。
- **`governancepolicy` 守卫**：`NewFileRepository` 内部 `MkdirAll`、空 dir → `ErrInvalidID`、
  `Repository` 无 `Close`（基于单次文件读写）；`TestNoEngineEval` 禁止调用引擎。

## 4. 质量门禁（Quality Gate）

- `go build ./...` / `go vet ./...` / `go test ./...` 全绿（沙箱偶发 `0xc0000142` 用预热+重试绕过）。
- `pre-commit` 钩子跑 quality gate，PASS 才允许提交。
- 冻结包（`external` / `platformview` / `correlation` / `governancepolicy`）`git diff` 须为空。

## 5. 继电器流程（Relay Flow）

1. 共享 Chrome 在 `9225` 在线；目标线程 `Execution Snapshot 设计`（`6a610ef1-…`）在一个标签里打开。
2. `_align_gpt.py --round N --prompt-file round_N_prompt.txt --dry-run` 验证命中固定会话（不发送）。
3. 确认后后台启动真实继电器：`_align_gpt.py --round N --prompt-file round_N_prompt.txt --timeout 600`。
4. 完成后回文落 `_chat_rounds_align/round_N.txt`。
5. 卡死补救：`fetch_reply.py --round N --prompt-file round_N_prompt.txt`（capture-only，不重发）。

## 6. Drift Log（文档与代码漂移记录）

| Fact | Drift | Impact | Future Major Candidate |
|------|-------|--------|------------------------|
| 顶层 `README.md` 曾描述 Phase 0 CLI 与「Phase 0（当前）」 | 架构概览停留在早期，未反映 7 层只读栈 + opscore-server 部署 | 新读者误以为仅有 CLI、无 HTTP 部署面 | 已通过 Phase 16.2 重写 README 索引纠正 |

> Drift 处理原则（R71）：代码事实 ≠ 文档时，**绝不改冻结代码迁就文档**；记录为上述
> Fact→Drift→Impact，必要时列为 Future Major Candidate。文档不得发明不存在的 endpoint。
