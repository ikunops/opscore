# OpsCore 架构讨论完整记录

> 与 ChatGPT 的架构讨论，2026-07-21
> 对话链接: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093

## 讨论概览

| 轮次 | 主题 | 核心输出 |
|------|------|----------|
| 第一轮 | Architecture First 四阶段 | Phase 0 Kernel 必须先做，Executor 独立于 HTTP/JWT/DB |
| 第二轮 | Phase 0 七个具体问题 | Context Builder 分层 / AuditSink / Permission 结构体 / Dispatcher 只知 Handler / Kernel 永远同步 / Operation as Code / Planner→ExecutionPlan |
| 第三轮 | PlanRuntime + 9 个永久抽象 | Plan 不可变 / Handler→Planner 分离 / DryRun 是 API 行为 / 9 抽象冻结 / 新代码库重写 |
| 第四轮 | 轻量化设计哲学 | Phase 0 合并为 6 个核心 / 1000 行以内 / 无 DB / 扁平 core/ 包 / 8-bit 原则写入 ADR |
| 第五轮 | Phase 1 控制平面 | 加 Runtime 层 / 小 Store 接口（非 Repository Factory）/ Audit EventBus→Subscriber / Permission 退化为 Operation metadata / ADR-004 |
| 第六轮 | Phase 2 Built-in 模块 | Builtin=Compile-time Plugin（共享 Module 接口）/ 原子 Operation / 强类型请求 struct / Operation 只产 Command / 新增 platform/ Resolver |
| 第七轮 | Phase 3 Plugin 平台 | 否决 Go .so / 独立进程+UDS RPC / RemoteModule 统一接口 / Registry 双层 / 双边界隔离 / Manifest 极简 / 生命周期状态机 / ADR-005 |
| 第八轮 | Phase 4 Frontend | React+Vite+go:embed / 前端=Runtime Client（铁律）/ Operation Registry 驱动动态菜单 / Manifest UI 扩展 / JWT httpOnly Cookie / Operation Center 可视化 / ADR-006+007 |

## 第一轮：Architecture First

ChatGPT 否定了 8 步渐进式开发计划，提出四阶段方案：

```
Phase 0 (Kernel)     → Context/Operation/Executor/Capability/Dispatcher/Audit
                        无 DB、无 JWT、无用户
Phase 1 (Control Plane) → PostgreSQL + JWT + RBAC + API + Audit Storage
Phase 2 (Built-in)    → 迁移 demo 模块，只产生 Operation
Phase 3 (Plugin)      → Manager + Registry + Gateway + Manifest + SDK
Phase 4 (Frontend)    → Login + 动态菜单 + 权限 UI + 插件 UI
```

核心论点：Executor 必须先独立，否则 CLI/SDK/REST/Plugin 全部耦合。

## 第二轮：7 个 Phase 0 修正

1. **Context 单类型 + Builder** — 不做 AnonymousContext 类型继承
2. **AuditSink 而非 AuditWriter** — Emit 接口，支持 DB/Kafka/File
3. **Permission 结构体** — {ResourceType, Action}，不回退字符串
4. **Dispatcher 只知 Handler** — 消除 if plugin 永久分支
5. **Kernel 永远同步** — 异步属于 Task Engine
6. **Operation as Code** — Go 代码注册，非 YAML
7. **CommandBuilder → Planner → ExecutionPlan** — 一个 restart 是一个计划，不是一条命令

## 第三轮：PlanRuntime + 9 个永久抽象

### PlanRuntime（Mutable）vs ExecutionPlan（Immutable）

Plan 必须不可变。运行时状态独立：
```go
type PlanRuntime struct {
    Plan        *ExecutionPlan
    CurrentStep int
    Results     []StepResult
}
```

### 9 个永久抽象

```
Context → Dispatcher → Handler → Planner → ExecutionPlan
→ PlanRuntime → Executor → ExecutionStep → AuditSink
```

Command 降级为 ExecutionStep 的一种——以后 Docker/K8s/HTTP/SSH 都是 Step。

### 关键决策
- 新建 Git 仓库，不是 Branch，不是 v2，是全新重写
- 模块用 `core/` 而非 `kernel/`
- Plan 永远不缓存（依赖 Context 动态生成）
- DryRun = Dispatcher.Plan()，不是单独引擎

## 第四轮：轻量化设计哲学

### 用户的 8-bit 约束

> "骨架要有，但不能太臃肿，最后部署了太吃资源。像 8-bit 游戏一样，用很少的资源完成项目。"

### GPT 的回应

1. **Phase 0 合并为 6 个核心**（不是 9 个独立 package）
2. **目标 1000 行以内**（实际 896 行）
3. **Phase 0 完全无 DB**（Phase 1 才引入 Storage Interface）
4. **扁平 core/ 包**（不拆子目录，等第二个开发者来了再拆）
5. **前端 Phase 2 后做 React + embed.FS**
6. **8-bit 原则正式写入 ADR-003**

### 冻结三个东西后开始写代码

1. **Core API**：Context/Handler/Dispatcher/Executor/AuditSink 接口
2. **目录结构**：cmd/ internal/core/ internal/builtin/ docs/ migrations/
3. **第一条业务链**：system.service.restart 完整闭环

## 最终架构

```
CLI → Dispatcher → Handler.Plan → ExecutionPlan → Executor → CommandStep → systemctl → AuditSink
```

Phase 0 代码：896 行，3.6MB 二进制，零外部依赖。

## 第五轮：Phase 1 控制平面（Round 5）

### 核心判断
Phase 0 完成 = **Kernel 已冻结**。Phase 1 目标不是"增加功能"而是"增加治理"——解决谁在调用 Kernel、为什么、有没有权限、有没有审批。

### 三个问题
1. **Storage Interface 先于 JWT**：Repository 模式（非 sql.DB），Storage 默认 Memory → SQLite → Postgres。Phase 1 第一周用 Memory 跑通 Auth+RBAC，第二周接 SQLite
2. **AuditSink 迁移**：不让 Dispatcher 知道存哪里，新增 Adapter（StorageAuditSink → AuditRepository → SQLite）。Kernel 的 Emit() 不变，未来可 CompositeAuditSink
3. **Registry Permission 与 DB RBAC 统一**：Operation 是权限唯一事实来源，DB 只存授权关系。Startup Sync 自动同步 Operation → 数据库。管理员只能授权不能新增权限

### Phase 1 顺序
```
1.1 Storage 抽象 (MemoryStorage + Repository)
1.2 SQLiteStorage (Schema + Migration)
1.3 Operation Registry ⇄ Permission Sync
1.4 User / Role / JWT / RBAC
1.5 HTTP API / Middleware / Context Builder
1.6 StorageAuditSink / Task / Config Repository
```

### 新增 ADR 建议
**ADR-004：Code Owns Capability, Database Owns Assignment** — 代码定义能力，数据库只定义分配关系。

详见 `docs/ChatGPT-Round5-Phase1.md`

> 注：第五轮在对话中经历了一轮精炼——最终锁定为「加 Runtime 层 + 小 Store 接口（而非巨大 Repository Factory）+ AuditSink→EventBus→Subscriber + Permission 退化为 Operation metadata（operations 表 / RoleOperation 关系 / Metadata Synchronizer）」。以下 Phase 2/3 讨论以精炼版为准。

## 第六轮：Phase 2 Built-in 模块（Round 6）

### 冻结的新原则
- **Builtin = 编译期插件（Compile-time Plugin）**，与 Phase 3 的 Runtime Plugin 共用同一 `Module` 接口。

### 三个问题结论
1. **注册机制**：选 `Module` 接口 + main 显式注册（不用 `init()`，因其启动顺序不可见；也不用纯 `Register()` 以免 Builtin/Plugin 两套 API）。
   ```go
   type Module interface { Name() string; Register(reg Registry) }
   modules := []Module{ system.New(), firewall.New(), journal.New() }
   for _, m := range modules { m.Register(reg) }
   ```
2. **Operation 粒度与参数**：走 Operation 风格（`firewall.rule.add/.remove/.list`）而非 REST action 参数；**一个 Operation = 一个可授权动作**；Handler 自拥强类型请求 struct（不用 `map`/`interface{}`）；**Operation 永远只产 `Command`（`[]string`），绝不生成 Shell 字符串**。
3. **Capability → Executor**：不绑死；Operation 声明 **Requirement**（如 `Requires: systemd`），新增 **`platform/` Resolver** 把 Requirement→具体 `Command`（systemd→systemctl、OpenRC→rc-service…）。Executor 永远只知道 Command。

### 冻结的 6 层链路
```
Runtime → Dispatcher → Builtin Module(业务意图) → ExecutionPlan(执行目标)
       → Platform Resolver(平台实现) → Command(唯一执行语言) → Executor(安全执行)
```
每层只回答一个问题；Kernel 不知 HTTP/JWT/DB，Builtin 不知发行版，Executor 不知业务语义，Platform 不知用户身份。

详见 `docs/ChatGPT-Round6-Phase2.md`

## 第七轮：Phase 3 Plugin 平台（Round 7）

### 核心判断
Phase 3 是最易"走偏"的一步——引入插件极易膨胀成 Operator 平台，与 8-bit 冲突。目标：**Core 稳定、Plugin 可替换、Executor 仍是唯一危险执行入口、不引入巨大 Runtime**。

### 关键决策
- **否决 Go plugin (.so)**：只支持 Linux、编译版本强绑定、Go runtime 耦合、Plugin panic 拖垮 Core、可绕过 Executor。
- **选定：Manifest + 独立进程 + UDS RPC**（非二选一，是组合）：`Manifest → Plugin Manager → 启动 Plugin Process → UDS → Module Proxy → Registry`。
- **Module 接口统一**：Builtin 与 Plugin 共用 `Module { Name() string; Register(reg Registry) }`；Plugin 不在一个进程，故引入 `RemoteModule{client RPCClient; manifest Manifest}`，Core 眼中二者都是 Module。
- **Registry 双层**：`builtin map + plugin map`，插件卸载只删 plugin/* 不影响 builtin。
- **双边界隔离**：Plugin Process = 运行隔离（防死循环/panic/leak）；Executor = 权限隔离（防危险命令/非法参数/root misuse）。
- **Plugin Process 最小限制**：Phase 3 只需 Process Supervisor（systemd-run / exec.CommandContext），cgroup 未来接；不引入容器。
- **Manifest 极简（plugin.yaml）**：name/version/apiVersion/operations/capabilities/config/permissions/runtime；**v1 不加 menus/routes/migration**（属 Control Plane，Phase 4 UI 再扩）。
- **生命周期状态机**：Installed→Loaded→Enabled→Running→Disabled→Uninstalled；Metadata Synchronizer 在 load 时同步、unload 时 soft delete（保留审计）。
- **SDK 第一版**：只给 Module SDK + 只读 PluginContext（Logger/Config/Capability）+ RegisterOperation 辅助；**不给 Executor**。
- **第一个真实插件 = plugin/demo**（验证全链路），Docker/K8s 再接入。

### 冻结原则 → ADR-005（Plugin Isolation Principle）
1. Builtin Module 与 Runtime Plugin 遵循同一 Module 模型。
2. Runtime Plugin 不进入 Core 进程。
3. Plugin 只能产生 Operation 和 ExecutionPlan，不能直接执行系统命令。
4. Executor 是唯一 OS 访问入口。
5. Plugin Runtime 使用进程隔离，不引入容器作为默认依赖。
6. Manifest 描述能力，不描述执行细节。

详见 `docs/ChatGPT-Round7-Phase3.md`

## 第八轮：Phase 4 Frontend（Round 8）

### 核心判断
Phase 4 最大风险是前端反过来侵蚀 Control Plane 设计。**铁律：前端是 Runtime Client，不是权限系统**——`Frontend → API Gateway → Runtime Builder → Context → Kernel`，绝不能 `Frontend → Plugin API → 绕过 Runtime → Executor`（否则 Phase 3 隔离失效）。前端不负责权限判断 / Command 生成 / Capability 判断 / Executor 调用。

### 关键决策
- **技术栈**：React + Vite + TS + React Router + Zustand(可选) + 原生 CSS/Tailwind + `go:embed`（不是 htmx）。禁止 antd/mui/redux/微前端。Bundle 控制 app.js<500KB / vendor.js<300KB gzip；路由懒加载。
- **动态菜单与权限**：唯一事实来源 = Operation Registry（非菜单/前端）。`GET /api/v1/me` 返回 user+permissions[]；前端只隐藏，真正执行 API 再检查（前端隐藏 ≠ 安全）。
- **Plugin UI 扩展**：不选 iframe、不选微前端；选 **Manifest UI Extension + Static Asset Proxy**（plugin.yaml 加 `ui:` 段）→ `GET /api/v1/ui/extensions` → 前端 `import()` 动态加载。插件 UI 永远不能直调 executor。
- **登录 JWT**：JWT + Refresh Token 存 **httpOnly Cookie**（不用 localStorage，防 XSS）；Access 15min / Refresh 7days；SameSite=Lax；CSRF 用 Double Submit Cookie。
- **ExecutionPlan 可视化**：核心页面 = Operation Center（非 Dashboard）；`POST /operations/{name}/execute` → task_id → WebSocket stream steps；`execution_tasks` + `task_steps` 表。
- **子序列 4.1~4.6**：Embedded Frontend → Auth UI → Permission Driven UI → Operation Center → Audit Timeline → Plugin UI Extension。
- **新增 ADR-006 Embedded Frontend / ADR-007 Plugin UI Isolation**。

### 共识收口结论
Phase0 Kernel ✅ / Phase1~3 🔜 / Phase4 Frontend 设计完成。**下一步不是继续设计，而是进入 Phase 1 实现**（Storage/Runtime/Auth 会反向影响 Phase 4 API）。先把 Runtime Builder / Context / Store Interface / Auth / Permission / Metadata Sync 落地，前端会很自然。

详见 `docs/ChatGPT-Round8-Phase4.md`
