# ChatGPT 架构讨论 — 第二轮 + 第三轮完整回复

> 对话链接: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093
> 第二轮：Phase 0 Kernel 7个具体问题 + 创意想法
> 第三轮：Planner/PlanRuntime/Handler 分层 + 9个永久抽象

---

## 第二轮回复：7 个问题逐一回答

### 整体评价
方向 95% 赞同，5% 调整。这 5% 影响整个项目以后是否优雅。

### 问题1：Context — Builder 分层，不是类型分层

**不要** AnonymousContext → AuthenticatedContext 类型继承。

**改为**：一个 Context 接口，一个实现，不同 Builder：

```go
type Context interface {
    User() UserContext
    Host() HostContext
    Capability() CapabilityContext
    Resource() ResourceContext
    Trace() TraceContext
    Logger() Logger
}

// Phase 0
ctx := NewContext().WithHost(...).WithCapability(...)

// Phase 1
ctx := NewContext().WithUser(...).WithPermission(...)
```

**原因**：以后 CLI/SDK/Plugin/Scheduler/HTTP 全部都是 Context，只是 Builder 不同。Kernel 真正零感知。

### 问题2：AuditSink 而非 AuditWriter

```go
type AuditSink interface {
    Emit(AuditEvent)
}
```

叫 Sink 因为以后不只是 Write：DB/Kafka/File/Webhook/OpenTelemetry 都是 Sink。Kernel 只 Emit，具体存储是 Adapter。

### 问题3：Permission 结构体而非字符串

```go
// 不要
PermissionCode string

// 改为
RequiredPermission Permission

type Permission struct {
    ResourceType string
    Action       string
}
```

与宪法 Permission = Resource × Action 一致，不回退到字符串。

### 问题4：Dispatcher 只知道 Handler

```go
type Handler interface {
    Execute()
}
```

Registry 保存 Operation → Handler。Builtin 注册 BuiltinHandler，Plugin 注册 PluginHandler。Dispatcher 永远 `handler.Execute()`，不知道是不是 Plugin。**消除 if plugin 永久分支。**

### 问题5：Kernel 永远同步

Executor 永远只有同步。异步属于 Task Engine，不是 Executor。

```
Task Engine → Executor  （不是 Executor 管理 Task）
```

### 问题6：Operation as Code — 冻结

**这是我最好的想法之一，建议直接冻结。** Operation 本质是代码，不是配置。IDE 跳转、重构、补全全部支持。YAML 做不到。

Manifest 保留，但职责不同：Manifest 负责菜单/权限/配置/Migration/版本/依赖；Operation 负责执行。两者完全不冲突。

### 问题7：CommandBuilder 升级为 Planner → ExecutionPlan

不做直接 Builder 函数，不做 Template。做 Builder Interface：

```go
type CommandBuilder interface {
    Build(Context, Params) (Command, error)
}

type Command struct {
    Executable string
    Args       []string
    Env        map[string]string
    WorkingDir string
    Timeout    time.Duration
}
```

**重大发现**：Operation 不应该直接包含 Command。应该是：

```
Operation → Planner → ExecutionPlan → Executor
```

一个 "restart" 不是一条命令，而是一个计划：check service → reload daemon → restart → health check。

---

## 第三轮回复：PlanRuntime + 9 个永久抽象

### 核心新概念：PlanRuntime（运行态） vs ExecutionPlan（静态）

**Plan 必须是不可变（Immutable）的。** 运行时状态不能污染 Plan。

```go
type PlanRuntime struct {
    Plan        *ExecutionPlan
    CurrentStep int
    Results     []StepResult
    StartedAt   time.Time
    FinishedAt  time.Time
}
```

以后 Task Engine / WebSocket / Rollback / Retry / Resume **全部都是 Runtime，不是 Plan**。

### Handler 不直接返回 Plan

```
Handler → Planner → ExecutionPlan
```

RestartHandler 不应该自己拼 Plan，它返回一个 Planner（如 `RestartServicePlanner{}`），Planner 负责 Build。

这样以后可以：
```
RestartPlanner → Linux Planner → Systemd Planner → OpenRC Planner
```
不同平台不同 Plan，Handler 不用知道。

### DryRun 是 API 行为，不是 Executor 功能

`Dispatcher.Plan()` 天然就是 DryRun。`Dispatcher.Execute()` 是真正执行。Dispatcher 暴露两个接口。

### Plan 永远不缓存

Plan 依赖 Context（Capability/OS/Version/Plugin）。Ubuntu 用 systemctl，CentOS 可能用 service，Container 可能不用 systemctl。Plan 一定动态生成。

但 Planner 可以缓存静态 Metadata（Operation/Permission/Risk/Timeout）。

### 模块结构：core/ 而非 kernel/

```
internal/
├── core/           # 不是 kernel，是 Core
│   ├── context/
│   ├── dispatcher/
│   ├── executor/
│   ├── planner/
│   ├── registry/
│   ├── audit/
│   ├── capability/
│   ├── resource/
│   └── permission/
├── controlplane/   # Phase 1
├── builtin/        # Phase 2
└── plugin/         # Phase 3
```

### 新 Git 仓库

不是 Branch，不是 v2，是**全新仓库**。Demo 保持为 `opscore-demo`，新项目 `opscore`。因为现在不是重构，是**重写**。

### Executor 执行 PlanRuntime

```go
Execute(ctx, runtime)  // 不是 Plan，不是 Command
```

Runtime 里有 Plan + CurrentStep。Executor 每次推进 Runtime。这样 Pause/Resume/Retry/Rollback 全部天然支持。

---

## 最终冻结：9 个永久抽象

从 6 个升级为 9 个：

```
Context          // 运行上下文
    │
Dispatcher       // 路由
    │
Handler          // 业务入口
    │
Planner          // 执行规划
    │
ExecutionPlan    // 静态执行计划（Immutable）
    │
PlanRuntime      // 运行时状态（Mutable）
    │
Executor         // 执行器
    │
ExecutionStep    // 原子执行单元
    │
AuditSink        // 事件输出
```

**Command 降级** — 以后 Docker/Kubernetes/HTTP/SSH/SDK 最后都是 ExecutionStep，Command 只是其中一种 Step。

### 克制原则
不要过早把 Planner 拆成很多层级。先保留 Handler → Planner → ExecutionPlan 这一层，等真正出现多平台需求时再引入更细的 Planner 组合。

---

## 三轮讨论的架构演进总结

| 轮次 | 核心输出 |
|------|----------|
| 第一轮 | Architecture First 四阶段：Phase 0 Kernel → Phase 1 Control Plane → Phase 2 Builtin → Phase 3 Plugin → Phase 4 Frontend |
| 第二轮 | 7 个具体修正：Context 单类型+Builder / AuditSink / Permission 结构体 / Dispatcher 只知 Handler / Kernel 永远同步 / Operation as Code / Planner→ExecutionPlan |
| 第三轮 | PlanRuntime（Immutable vs Mutable）/ Handler→Planner 分离 / DryRun 是 API 行为 / 9 个永久抽象 / 新代码库重写 |
