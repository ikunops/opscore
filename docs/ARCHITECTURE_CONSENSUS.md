# OpsCore 架构主共识（Phase 0–4 冻结版）

> 与 ChatGPT 八轮架构讨论的**最终冻结结论**（2026-07-21）
> 对话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093
> 逐轮记录: `ARCHITECTURE_DISCUSSIONS.md`；细节见 `ChatGPT-Round1~8-*.md`
> 本文件是**后续实现的单一事实来源（Single Source of Truth）**。

---

## 0. 系统定位

**OpsCore = Operations Control Plane（运维控制平面）**，不含业务逻辑；新增能力一律以 Module / Plugin 形式接入。

**8-bit 轻量哲学（ADR-003）**：
- Single Binary First（单二进制优先）
- Progressive Complexity（渐进复杂度）
- Reusable Primitives（可复用原语）
- Embedded Default（内嵌默认）
- Capability Driven（能力驱动）

---

## 1. 永久抽象链路（宪法级）

```
Runtime → Dispatcher → Module(Builtin / Runtime Plugin)
        → Handler → ExecutionPlan
        → Platform Resolver → Command([]string)
        → Executor → AuditSink
```
- `Context` 贯穿全程（User/Host/Permission/Capability/TraceID/Logger/Config/Executor）。
- `Capability` 驱动条件执行（插件只查 `ctx.Capability()`，不自己探测）。
- **Operation Registry 是权限唯一事实来源**（Code Owns Capability, Database Owns Assignment — ADR-004）。

### 核心不变约束（宪法）
1. 禁止裸命令执行：只有预注册的 Operation 可执行。
2. 命令不经 shell：`exec.Command(args...)` 参数数组；Operation 永远只产 `Command`（`[]string`）。
3. 插件**绝不能**直接执行系统命令，必须经 Core Executor。
4. 审计不可绕过：Executor 后置中间件（AuditSink）。

---

## 2. Phase 0 — Kernel（✅ 已完成）

- 896 行 Go，扁平 `core/`，零外部依赖，3.6MB 二进制。
- 6 核心：`Context / Dispatcher / Handler / ExecutionPlan / Executor / AuditSink` + `Capability` + `Operation Registry`。
- 第一条业务链 `system.service.restart` 闭环跑通（CLI → Dispatcher → Handler.Plan → ExecutionPlan → Executor → CommandStep → systemctl → AuditSink）。
- 7 项测试全过。

---

## 3. Phase 1 — Control Plane（✅ 已落地）

- 加 **Runtime 层**（不在 Kernel）；Kernel 永远不知 HTTP/JWT/DB。
- **小 Store 接口**（非巨大 Repository Factory）：默认 Memory → SQLite → Postgres。
- **AuditSink → EventBus → Subscriber**（StorageAuditSink 适配器；Kernel `Emit()` 不变）。
- **Permission 退化为 Operation metadata**：`operations` 表（name/resource_type/action_type/risk/source）+ `roles` + `role_operations`；**Metadata Synchronizer** 启动同步。DB 只存授权关系。
- **ADR-004**：Code Owns Capability, Database Owns Assignment。

### 子序列
```
1.1 Storage 抽象 (MemoryStorage + Repository)        ✅ 5097734
1.2 SQLiteStorage (Schema + Migration)               ✅ fd1be5c
1.3 Operation Registry ⇄ Permission Sync            ✅ 3537700
1.4 User / Role / JWT / RBAC                         ✅ 0475646
1.5 HTTP API / Middleware / Context Builder          ✅ e1a2dd8
1.6 StorageAuditSink / Task / Config Repository      ✅ fd1a449 (StorageAuditSink + admin /api/audit; Task/Config Store 已实现待 Phase 2 Task Engine 消费)
```

> 运行：`go run ./cmd/opscore serve --storage sqlite --db opscore.db --admin-user admin --admin-pass <pw> --jwt-secret <secret>`。
> 端点：`POST /api/auth/login|refresh|register`、`GET /api/health|me|operations|audit`、`POST /api/operations/{name}/plan|run`。

---

## 4. Phase 2 — Built-in 模块（🔜 实现）

- **Builtin = Compile-time Plugin**，与 Phase 3 Runtime Plugin 共用：
  ```go
  type Module interface { Name() string; Register(reg Registry) }
  modules := []Module{ system.New(), firewall.New(), journal.New() }
  for _, m := range modules { m.Register(reg) }   // main 显式注册，不用 init()
  ```
- **原子 Operation**：一个 Operation = 一个可授权动作；Handler 自拥强类型请求 struct（不用 `map`/`interface{}`）。
- **Operation 只产 `Command`（`[]string`），绝不 Shell 字符串**。
- **`platform/` Resolver**：Operation 声明 `Requirement`（如 systemd），Resolver 映射成具体 `Command`（systemctl / rc-service）。Executor 永远只知道 Command。
- **6 层链路**：Runtime → Dispatcher → Builtin Module(业务意图) → ExecutionPlan(执行目标) → Platform Resolver(平台实现) → Command(唯一执行语言) → Executor(安全执行)。

---

## 5. Phase 3 — Plugin 平台（🔜 实现，ADR-005）

- **否决 Go plugin (.so)**：只支持 Linux、编译版本强绑定、Plugin panic 拖垮 Core、可绕过 Executor。
- **选定：Manifest + 独立进程 + UDS RPC**：
  `Manifest → Plugin Manager → 启动 Plugin Process → UDS → Module Proxy → Registry`
- **Module 接口统一**：Builtin 与 Plugin 共用 `Module`；Plugin 不在一个进程，故引入 `RemoteModule{client RPCClient; manifest Manifest}`，Core 眼中二者都是 Module。
- **Registry 双层**：`builtin map + plugin map`，插件卸载只删 `plugin/*` 不影响 builtin。
- **双边界隔离**：Plugin Process = 运行隔离（防死循环/panic/leak）；Executor = 权限隔离（防危险命令/非法参数/root misuse）。
- **Plugin Process 最小限制**：Phase 3 只需 Process Supervisor（`systemd-run` / `exec.CommandContext`），cgroup 未来接；**不引入容器**。
- **Manifest 极简（plugin.yaml）v1**：name/version/apiVersion/operations/capabilities/config/permissions/runtime；**不加 menus/routes/migration**（Phase 4 UI 再扩 `ui:`）。
- **生命周期状态机**：Installed→Loaded→Enabled→Running→Disabled→Uninstalled；unload 用 **soft delete**（`enabled=false`，保留审计）。
- **SDK 第一版**：只给 Module SDK + 只读 `PluginContext`（Logger/Config/Capability）+ `RegisterOperation` 辅助；**不给 Executor**。
- **第一个真实插件 = `plugin/demo`**（验证全链路），Docker/K8s 再接入。
- **ADR-005：Plugin Isolation Principle**（Builtin 与 Runtime Plugin 同 Module 模型 / 不进 Core 进程 / 只产 Operation+ExecutionPlan / Executor 唯一 OS 入口 / 进程隔离不引容器 / Manifest 描述能力不描述执行细节）。

### 子序列
```
3.1 Plugin Protocol (handshake + manifest + RemoteModule)
3.2 Plugin Manager (install/load/enable/disable/uninstall)
3.3 Plugin Registry (Operation proxy + Metadata Sync)
3.4 SDK (Module SDK + Handler SDK)
3.5 第一个真实插件 plugin/demo
```

---

## 6. Phase 4 — Frontend（设计完成，🔜 实现）

**铁律：前端是 Runtime Client，不是权限系统**——
`Frontend → API Gateway → Runtime Builder → Context → Kernel`。
**绝不能** `Frontend → Plugin API → 绕过 Runtime → Executor`（否则 Phase 3 隔离失效）。
前端不负责：权限判断 / Command 生成 / Capability 判断 / Executor 调用。

- **技术栈**：React + Vite + TypeScript + React Router + Zustand(可选) + 原生 CSS/Tailwind + `go:embed`。**禁止** antd/mui/redux/微前端。Bundle 控制 `app.js<500KB` / `vendor.js<300KB` gzip；路由懒加载。
- **动态菜单与权限**：唯一事实来源 = Operation Registry。`GET /api/v1/me` 返回 `{user, permissions[]}`；前端只隐藏，真正执行 API 再检查（前端隐藏 ≠ 安全）。
- **Plugin UI 扩展**：不选 iframe、不选微前端；选 **Manifest UI Extension + Static Asset Proxy**（plugin.yaml 加 `ui:` 段）→ `GET /api/v1/ui/extensions` → 前端 `import()` 动态加载。插件 UI 永远不能直调 executor。
- **登录 JWT**：JWT + Refresh Token 存 **httpOnly Cookie**（不用 localStorage，防 XSS）；Access 15min / Refresh 7days；`HttpOnly+Secure+SameSite=Lax`；CSRF 用 Double Submit Cookie。
- **ExecutionPlan 可视化**：核心页面 = **Operation Center**（非 Dashboard）；`POST /api/v1/operations/{name}/execute` → task_id → WebSocket stream steps；`execution_tasks` + `task_steps` 表。
- **ADR-006 Embedded Frontend / ADR-007 Plugin UI Isolation**。

### 子序列
```
4.1 Embedded Frontend (React+Vite+embed.FS+API Gateway)
4.2 Auth UI (login/refresh/logout)
4.3 Permission Driven UI (/me + button guard)
4.4 Operation Center (execute + plan preview + task view)
4.5 Audit Timeline
4.6 Plugin UI Extension (manifest ui + asset proxy + dynamic import)
```
> Phase 4 拆分：4A API Contract First → 4B 最小 React Shell → 4C Operation Center → 4D Plugin UI。

---

## 7. ADR 清单

| ADR | 主题 | 核心 |
|-----|------|------|
| ADR-001 | 架构宪法 | 分层 / Context / Command 抽象 / 权限模型 / Capability 统一 / Executor 分层 |
| ADR-002 | 执行引擎 | 禁裸命令 / 不经 shell / Operation 不可动态注册 / 审计不可绕过 |
| ADR-003 | 8-bit 轻量哲学 | Single Binary / Progressive Complexity / Reusable Primitives / Embedded Default / Capability Driven |
| ADR-004 | 能力 vs 分配 | Code Owns Capability, Database Owns Assignment |
| ADR-005 | 插件隔离 | Builtin=Runtime 同 Module 模型 / 不进 Core 进程 / 进程隔离不引容器 / Manifest 描述能力 |
| ADR-006 | 前端嵌入 | SPA embed / API first / frontend is client / no business logic |
| ADR-007 | 插件 UI 隔离 | 插件 UI 经 Core API，不能直调 executor |

---

## 8. 当前状态与下一步

```
Phase0 Kernel        ✅ 已完成（896 行 Go，7 测试通过）
Phase1 ControlPlane  🔜 下一步实现
Phase2 Builtin       🔜
Phase3 Plugin        🔜
Phase4 Frontend      设计完成
```

**共识收口结论**：Phase 0–3 设计已冻结，Phase 4 设计完成。**下一步不是继续设计，而是进入 Phase 1 实现**——Storage / Runtime / Auth 会反向影响 Phase 4 API。先把 Runtime Builder / Context / Store Interface / Auth / Permission / Metadata Sync 落地，前端会很自然。

核心价值始终是：**Operation Registry + Execution Engine + Plugin Model**；前端只是这个控制平面的一个视图。
