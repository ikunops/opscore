# OpsCore 工程规范手册（团队约定）

> 目的：让代码质量**不依赖个人**，靠门禁 + 清单 + 约定兜底。配套脚本见 `scripts/`。
> 适用范围：所有提交、PR、Phase 2/3/4 开发。

---

## 1. 质量门禁（强制执行）

每次提交前自动跑，挡住已知回归：

```bash
# 一次性启用（提交钩子）
git config core.hooksPath .githooks

# 手动 / CI 运行
bash scripts/quality-gate.sh
```

门禁做四件事：
1. `go build ./...` —— 全模块编译
2. `go vet ./...` —— 静态检查
3. `go test ./...` —— 单元/集成测试
4. **回归守卫**：
   - A. `cmd/opscore/main.go` 不能被 `.gitignore` 误屏蔽（曾因 `opscore` 规则裸写，整目录源码丢失）
   - B. UI 配置注入必须是合法 JS + 合法 JSON（`node scripts/check-ui.js`）。**这直接防止了 2026-07-22 那次"页面全死、按钮无反应"的 `{}` 注入 bug 复发**。

> 门禁依赖 `node`（做 UI 语法/JSON 校验）与 `curl`（起 demo 服务）。缺 node 时该守卫仅 WARN 跳过，其余照常。
> 注意：本沙箱 `/tmp` 不可用，门禁临时文件用**项目相对路径**（`.gate_*`），已加入 `.gitignore`。

**铁律**：门禁不过，不提交。CI 也跑同一脚本，失败不合并。

---

## 2. 提交规范（Conventional Commits）

```
<type>(<scope>): <subject>   # 英文小写，祈使句，不超过 72 字符

<body> 可选，说明 why（不是 what）；关联 Phase/ADR。
```

| type | 含义 |
|---|---|
| `feat` | 新能力（新 operation / 新模块） |
| `fix` | bug 修复 |
| `refactor` | 重构，行为不变 |
| `test` | 增删测试 |
| `docs` | 文档（含 ADR） |
| `chore` | 工具/构建/门禁 |

示例：`fix(server): inject UI config without trailing brace (dead script)`

**提交前自检**：
- [ ] 改动真的进了暂存区（`git status` 确认文件不是 ignored）
- [ ] 没把密钥/密码写进代码或日志（仅测试用 `--target-insecure` 等需注释标明）
- [ ] 没留 `fmt.Print` / 死代码 / 注释掉的旧实现

---

## 3. Code Review 清单（reviewer 必查）

### 安全（P0/P1，来自质量基线报告）
- [ ] **无开放注册**：`/api/auth/register` 默认需管理员或显式开启（P0-1）
- [ ] **JWT 密钥**：非 demo/非 memory 模式下，`--jwt-secret` 不得为默认值 `change-me-in-prod`，否则启动即退（P0-2，待实现）
- [ ] 默认管理员 `admin/admin`：非 demo 必须警告/强改密（P1-1）
- [ ] 刷新令牌泄露可吊销？目前 jti 未消费，标注为已知限制（P1-2）
- [ ] 登录有频率限制？（P1-3，待实现）
- [ ] 内部错误不回显给客户端，服务端 `logger.Error`（P1-4）
- [ ] 错误比较用 `errors.Is(err, io.EOF)` 而非字符串（P1-6）

### 正确性
- [ ] 命令执行走 `exec.Command(args...)` 或 SSH 逐参单引号，**绝不 `sh -c`**（注入防护）
- [ ] RBAC 先鉴权后执行；未知用户失败即拒绝
- [ ] 能力探测（Capability）必须在**目标机**上做，不在控制平面本机（P1-5，架构债）
- [ ] 资源释放：`defer` / `context` 取消 / 超时双控

### 可维护性
- [ ] 类型/函数有单行职责说明，公开 API 有 doc 注释
- [ ] 不引入未使用的依赖（`go mod tidy` 干净）
- [ ] 命名与现有包一致（Operation as Code、Context、Handler、ExecutionPlan…）

### 测试
- [ ] 新 operation / 新 Handler 有单测
- [ ] 远程路径有"内嵌 SSH 假主机"整链测试（参考 `services_test.go`）
- [ ] `go test ./...` 全绿

---

## 4. ADR 流程（架构决策记录）

- 任何**接口/模块边界/权限模型/执行模型**变更，**先写 ADR**，再写代码。
- ADR 编号递增（`ADR-00N`），存放 `docs/`，含：背景、决策、理由、后果、状态。
- 冻结项（架构宪法 `00-设计原则.md`）变更需全员确认，不得私下改。
- 文档语言：中文；代码注释：英文；用户文案：中文。

---

## 5. 测试标准

- **单元**：纯函数 / Handler 规划逻辑，快速、无网络。
- **集成**：用 `internal/demo` 内嵌 SSH 假主机跑"登录→RBAC→Context(target)→SSH→systemctl→审计"整链（无真实机器也能测）。
- **禁止**：测试依赖真实 LAN 主机（如 `192.168.94.20`）；真机联调走独立交付二进制，不进 CI。
- 覆盖率目标：核心包（core / auth / server）增量改动 ≥ 70%。

---

## 6. 分支与推送

- 本地开发在 `main`（当前仓库为全新重写，非重构旧 demo）。
- **推送需显式授权**：当前用户要求"先本地测试、本地提交，暂不推送"。任何 `git push` 前必须确认。
- 远程仓库归属 `YuDong999/opscore`（旧 demo 已改名 `opscore-lite`，两者绝不合并）。

---

*本手册与 `docs/quality-baseline-2026-07-22.md` 配套。门禁脚本改动即视为规范更新，无需额外审批。*
