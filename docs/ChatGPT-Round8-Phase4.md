# ChatGPT 架构讨论 — Round 8：Phase 4 Frontend

> 对话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093 （"系统架构设计评审"）
> 时间: 2026-07-21
> 背景: Phase 0 已完成（896 行 Go，扁平 core/，6 核心抽象，7 项测试通过）。
>       Phase 1：Runtime 层 + 小 Store 接口 + Audit EventBus→Subscriber + Permission=Operation metadata（ADR-004）。
>       Phase 2：Builtin=Compile-time Plugin（共享 Module 接口）/ 原子 Operation / 强类型请求 / Operation 只产 Command / platform/ Resolver。
>       Phase 3：否决 Go .so / 独立进程+UDS RPC / RemoteModule 统一接口 / Registry 双层 / 双边界隔离 / Manifest 极简 / 生命周期状态机（ADR-005）。

## Phase 0–3 冻结共识（作为 Phase 4 前提）

- **定位**：运维控制平面，不含业务逻辑；新增能力以 Module / Plugin 接入。
- **8-bit 哲学（ADR-003）**：Single Binary / Progressive Complexity / Reusable Primitives / Embedded Default / Capability Driven。
- **永久抽象链路**：Runtime → Dispatcher → Module(Builtin/Runtime) → Handler → ExecutionPlan → Platform Resolver → Command(`[]string`) → Executor → AuditSink。Context 贯穿；Capability 驱动条件执行；Operation Registry 是权限唯一事实来源。
- **Phase 1**：Runtime 层 + 小 Store 接口（Memory→SQLite→Postgres）+ Audit EventBus→Subscriber + Permission=Operation metadata（operations 表 + RoleOperation + Metadata Synchronizer）。ADR-004：Code Owns Capability, Database Owns Assignment。
- **Phase 2**：Builtin = Compile-time Plugin，共用 `Module{Name() string; Register(reg Registry)}`，main 显式注册；原子 Operation；强类型请求 struct；Operation 只产 `Command`；`platform/` Resolver 把 Requirement→具体 Command。
- **Phase 3（ADR-005）**：否决 Go .so，选 Manifest + 独立进程 + UDS RPC；RemoteModule 统一接口；Registry 双层；双边界隔离（Plugin Process + Executor）；Manifest 极简（v1 不加 menus/routes/migration）；生命周期状态机；SDK 第一版只给 Module SDK + 只读 Context（不给 Executor）；第一个插件 = plugin/demo。

## 我的提问

（提问文本保存在 `_phase4_msg.txt`，含 Phase 0–3 冻结共识摘要 + 5 个 Phase 4 问题）

Phase 0–3 已冻结，现在进 Phase 4（Frontend）。目标：在"单二进制优先 / 8-bit 轻量"约束下，把控制平面暴露成可用 UI，并支持插件 UI 扩展。五个问题：

1. **技术栈与单二进制约束**：React + Vite 编译产物用 `go:embed` 打进二进制（内部 `http.FileServer` + `embed.FS` serve `/web`），还是更轻的 htmx/原生？若选 React 怎么控 bundle 体积？
2. **动态菜单与权限 UI**：前端怎么消费 Operation Registry + RBAC 渲染动态菜单和权限门控？菜单/按钮可见性是否由 `roles + operations` 推导？前端如何拿到"当前用户可见的 operations / menus"列表（哪个 API 端点、返回什么结构）？
3. **插件 UI 扩展（Phase 3 推迟部分）**：Runtime Plugin 进程隔离下，插件怎么安全扩展前端？API 驱动的路由/菜单注入（Manifest `ui:` 段声明 routes/menus，Core 聚合下发），还是 iframe 沙箱，还是微前端？插件前端静态资源怎么分发？
4. **登录与 JWT**：SPA 怎么拿/刷新 JWT？token 存 httpOnly cookie 还是 localStorage？CSRF 怎么防？未授权跳登录、401 自动刷新怎么处理？
5. **审计与执行可视化**：前端怎么展示 ExecutionPlan 逐步结果和审计日志？是否给 Plan 执行详情页（steps + 状态 + 输出）+ Audit 时间线？数据怎么从 API 拿？

请给 Phase 4 最小但不过度的目录结构（internal/web/ + web/ 前端源码）与子序列（4.1~4.N），以及是否需要新增 ADR（ADR-006 Frontend Embedding / ADR-007 Plugin UI Isolation）。

## ChatGPT 回复（要点）

### 铁律：前端是 Runtime Client，不是权限系统
```
Frontend → API Gateway → Runtime Builder → Context → Kernel
```
绝不能出现 `Frontend → Plugin API → 绕过 Runtime → Executor`，否则 Phase 3 插件隔离失效。前端不负责：权限判断 / Command 生成 / Capability 判断 / Executor 调用。

### 4.1 前端技术方案
- **选 React + Vite + TypeScript + React Router + Zustand(可选) + 原生 CSS/Tailwind + `go:embed`**（不是 htmx——未来有 Plugin UI / 动态表单 / Plan 可视化 / WebSocket / 拓扑，React 更合适）。
- **禁止**：antd / element-plus / mui / Redux / 微前端。运维控制台组件（table/modal/button/form）自己封装。
- **Bundle 控制**：`app.js < 500KB gzip`、`vendor.js < 300KB gzip`；路由懒加载（`lazy(()=>import(...))`）。
- **Go embed**：`//go:embed dist/*` + `http.FileServer(fs.Sub(frontend,"dist"))`，最终一个二进制文件内含 kernel + controlplane + builtin + React UI。
- **新增 ADR-006：Embedded Frontend Architecture**（SPA embed / API first / frontend is client / no business logic）。

### 4.2 动态菜单与权限模型（锚定 ADR-004）
- 权限事实来源 = **Operation Registry**（不是菜单、不是前端）。Builtin 注册 Operation → Metadata Synchronizer → `operations` 表（id/name/resource_type/action_type/risk/source）；`roles` + `role_operations` 关联。
- 新增 `GET /api/v1/me` 返回 `{user, permissions:[{name,resource,action,risk}]}`。
- 前端据 permissions 生成菜单/按钮（`<Button disabled={!can("system.service.restart")}>`）——**只是隐藏，真正执行 API 再检查**；前端隐藏 ≠ 安全，安全永远在 Runtime Permission Engine。

### 4.3 Plugin UI 扩展（不选 iframe、不选微前端）
- **选 Manifest UI Extension + Static Asset Proxy**。Phase 3 的 Manifest 极简，Phase 4 扩 `ui:` 段（routes + assets path）。
- 流程：Plugin Manager Load → Register Operation + Register UI Metadata → Core 维护 `plugin_ui_registry` → `GET /api/v1/ui/extensions` 返回 routes/asset 列表 → 前端 `import()` 动态加载。
- 插件 UI 永远不能直接调 executor，只能 `Plugin UI → Core API → Permission → Dispatcher`。
- **新增 ADR-007：Plugin UI Isolation**。

### 4.4 登录 JWT
- **JWT + Refresh Token，存 httpOnly Cookie（不用 localStorage——XSS 风险）**。
- Access 15min / Refresh 7days；Cookie `HttpOnly + Secure + SameSite=Lax`。
- 登录 `POST /api/v1/auth/login` → Set-Cookie；刷新 `POST /api/v1/auth/refresh`；前端 interceptor：401 → refresh → retry → fail 跳登录。
- CSRF：`Double Submit Cookie`（Cookie 自动发送需防）。

### 4.5 ExecutionPlan 可视化（核心页面 = Operation Center，不是 Dashboard）
- `POST /api/v1/operations/{name}/execute` → task_id → WebSocket `/api/v1/tasks/{id}/stream` 流式 steps。
- 数据：`execution_tasks` + `task_steps`（step_name/command/status/output/duration）。
- 页面展示 Plan 步骤 + 状态 + Audit 时间线（如 "Restart nginx: ✓ check systemd / ✓ restart / ○ health check"）。

### Phase 4 最小代码结构
```
opscore/
├── internal/
│   ├── web/        (server.go, embed.go, api.go)
│   ├── controlplane/ (auth, permission, task, api)
│   └── core/
└── web/
    └── src/ (api, auth, router, permission, components, pages/{dashboard,operation,service,firewall,audit,plugin})
```
- **子序列 4.1~4.6**：Embedded Frontend → Auth UI → Permission Driven UI → Operation Center → Audit Timeline → Plugin UI Extension。
- **Phase 4 拆分**：4A API Contract First → 4B 最小 React Shell → 4C Operation Center → 4D Plugin UI。

### 关键结论（共识收口）
- Phase0 Kernel ✅ / Phase1 ControlPlane 🔜 / Phase2 Builtin 🔜 / Phase3 Plugin 🔜 / Phase4 Frontend 设计完成。
- **下一步不是继续设计，而是进入 Phase 1 实现**——Storage / Runtime / Auth 会反向影响 Phase 4 API。先把 Runtime Builder / Context / Store Interface / Auth / Permission / Metadata Sync 落地，前端会很自然。
- 核心价值始终是：Operation Registry + Execution Engine + Plugin Model；前端只是这个控制平面的一个视图。
