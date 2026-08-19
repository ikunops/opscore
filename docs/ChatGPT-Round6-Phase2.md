# ChatGPT 架构讨论 — Round 6：Phase 2 Built-in 模块

> 对话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093 （"系统架构设计评审"）
> 时间: 2026-07-21
> 背景: Phase 0 已完成（896 行 Go，扁平 core/，6 核心抽象，7 项测试通过，system.service.restart 跑通）。
>       Phase 1 共识在对话中经历了一轮精炼（见下文"Phase 1 共识（精炼版）"）——以下以精炼版为准。

## 前置说明（对话状态）

本会话实际发生的顺序（与 Round 5 文档略有出入，已以精炼版为准）：
1. 上传 `06-执行引擎设计.md` 文件
2. Round 4 回复（8-bit / 9→6 核心合并 / ADR-003）
3. 我发"Phase 0 已完成"消息 → ChatGPT 第一次 Phase 1 回复（Repository 模式视角）
4. 该消息因早期静默失败被重复发送一次 → ChatGPT 第二次（精炼）Phase 1 回复
   - 精炼版把 Storage 从"巨大 Repository Factory"改为"小的 Store 接口组合进 Runtime"，
     把 AuditSink→Repository 改为 AuditSink→EventBus→Subscriber，并把 Permission 退化为 Operation metadata。
   - **以下 Phase 2 讨论以精炼版共识为基础。**

## Phase 1 共识（精炼版，作为 Phase 2 前提）

- 加一层 **Runtime**：HTTP / CLI / SDK / WebSocket → Runtime Builder 组装 Context（JWT / Capability / Config / Permission / TraceID / Logger …）→ Kernel。Kernel 永远只收一个 Context。
- Storage 不用巨大 Repository Factory，而是小的 **Store 接口**（UserStore / RoleStore / AuditStore …）组合进 Runtime；默认 Memory → SQLite → Postgres。
- AuditSink 保持 `Emit(Event)`，下面接 **EventBus → AuditSubscriber → AuditStore**；Kernel 不知道写去哪。未来 Task / Plugin / Alarm 都能订阅。
- Permission 退化为 Operation 的 metadata；同步的是 **Operation Metadata**（operations 表：id / name / resource_type / action_type / risk / plugin / builtin），Role 直接绑 Operation（RoleOperation）。有 Metadata Synchronizer。
- 整体 **Operation 驱动**而非 Permission 驱动。ADR-004：Code Owns Capability, Database Owns Assignment。

## 我的提问

（见下方 ChatGPT 回复前的原始提问，提问文本保存在 `_phase2_msg.txt`）

Phase 1 我们聊下来，你后面那条回复把前面又精炼了一遍，我以精炼版为准。锁定共识：
- 加一层 Runtime：HTTP / CLI / SDK / WebSocket → Runtime Builder 组装 Context（JWT / Capability / Config / Permission / TraceID / Logger …）→ Kernel。Kernel 永远只收一个 Context，ControlPlane 因此极稳。
- Storage 不用巨大 Repository Factory，而是小的 Store 接口（UserStore / RoleStore / AuditStore …）组合进 Runtime；默认 Memory → SQLite → Postgres。
- AuditSink 保持 Emit(Event)，下面接 EventBus → AuditSubscriber → AuditStore；Kernel 不知道写去哪。未来 Task / Plugin / Alarm 都能订阅。
- Permission 退化为 Operation 的 metadata；同步的是 Operation Metadata（operations 表：id / name / resource_type / action_type / risk / plugin / builtin），Role 直接绑 Operation（RoleOperation）。有个 Metadata Synchronizer 启动同步、插件加载再同步。
- 整体 Operation 驱动而非 Permission 驱动。ADR-004：Code Owns Capability, Database Owns Assignment。

现在进 Phase 2（Built-in 模块）：把 demo 的 Service(systemctl) / Firewall(iptables) / Journal(journalctl) 迁成 Built-in，只"产生 Operation"、经 Executor 执行，且保持 8-bit（单二进制 / 可复用原语 / 不臃肿）。

三个问题，请给具体结构与子序列（如 2.1~2.N）：

1. 模块注册机制：Built-in 要往 Registry 注册 Operation，但 core/ 不能反向依赖它们（不能出现 if builtin 分支）。你倾向哪种零依赖注册？
   (a) 每模块一个包 internal/builtin/<name>/，main 里显式 Register(m) 聚合；
   (b) 模块内 init() 自注册到全局 Registry（blank import）；
   (c) 定义 Module 接口（Name / Operations / Capabilities / Register(ctx)），启动时扫描注册。
   哪种最契合"骨架硬但不臃肿"？

2. Operation 粒度与参数模型：以 Firewall 为例，是做成 `firewall.rule` 一个 Operation 带 action 参数（add/remove/list），还是拆 `firewall.rule.add` / `.remove` / `.list` 多个 Operation？参数（如一条 rule 的 spec）如何建模——强类型 struct 在 Plan 阶段校验，还是宽松 map？怎么避免重新引入 shell 命令字符串？（注意现在 Permission 已是 Operation 的 metadata，Operation 上有 Resource/Action/Risk/Capability。）

3. Capability → Executor 映射：Service 要 systemctl（System/Process）、Firewall 要 iptables（Network）、Journal 要 journalctl（File/Process）。Phase 0 的 Capability 是"查询"语义，到这里怎么落成具体 Executor 选择？是每个 Operation 声明 RequiredCapability，Executor 层按 Capability 路由到 System/Network/File/Process 子执行器？还是 Built-in 模块直接拿 Executor 句柄、自己决定调哪个子执行器？

请给出 Phase 2 最小但不过度的代码结构（每个模块落地时的文件与注册方式）。

## ChatGPT 回复（要点）

> 关键转折：**Builtin = 编译期插件（Compile-time Plugin）**，为 Phase 3 插件系统铺路。

### 冻结的新原则
- **Builtin 不是特殊模块，只是"编译进来的插件"**。Builtin=Compile-time Module，Plugin=Runtime Module，接口完全一致，Dispatcher 永远不知道区别。

### Q1 模块注册：选 (c) Module 接口 + (a) main 显式注册，不用 (b) init()
- `init()` 问题：启动顺序不可见、Capability/Logger/Config 初始化先后混乱、以后插件更易乱。
- 纯 `Register()` 的问题：Builtin 和 Plugin 会变成两套 API（`Register` vs `RegisterPlugin`）。
- 统一 `Module`：`type Module interface { Name() string; Register(reg Registry) }`。
- main 聚合：`modules := []Module{ system.New(), firewall.New(), journal.New() }; for _, m := range modules { m.Register(reg) }`。
- Plugin 实现同一接口，只是来源不同（编译期 vs 运行期）。

### Q2 Operation 粒度与参数
- **走 Operation 风格，不走 REST 风格**：`firewall.rule.add` / `.remove` / `.list`，而非 `firewall.rule` + action 参数。
- **冻结：一个 Operation = 一个可授权动作**。否则 RBAC 要开始解析参数，ControlPlane 瞬间复杂。
- **参数模型：Handler 自己拥有强类型请求 struct**（如 `RestartServiceRequest{ Service string }`），`Plan(ctx, req RestartServiceRequest)`。HTTP 收 JSON → Runtime Decode 成 struct → Kernel 永远拿 struct。不用 `map[string]any` / `interface{}`（那是退回 JavaScript）。
- **冻结：Operation 永远不能生成 Shell，只能生成 Command**。`iptables -A INPUT ...` 永远不行；`[]string{"iptables","-A","INPUT","-p","tcp"}` 才行。**Command 是 Executor 唯一语言**。

### Q3 Capability → Executor：不绑死，加 Resolver 层
- Operation 声明 **Requirement（能力需求）**，不是 **Executor（具体执行器）**。
  - 例：`Requires: systemd`，不是 `Use: SystemExecutor`。
  - Capability 回答"能不能"，Executor 回答"怎么做"，不要混。
- 新增 **Resolver**：`Operation → Requirement → Execution Resolver → Command`（而非 `Operation → Executor`）。
  - 例：RestartService 需要 ServiceManager；Resolver 看 Capability：`systemd → systemctl`、`OpenRC → rc-service`、`BusyBox → service`，然后生成 ExecutionStep。
  - Executor 永远不知道 systemctl，只知道 Command。
- 新增 **`platform/` 包**：负责 systemctl vs service、iptables vs nft、journalctl vs 其他日志系统。Builtin 只表达业务意图（"重启服务"），不表达实现（"systemctl restart"）。

### Phase 2 推荐目录结构（轻量，不继续拆 package）
```
internal/
├── core/
├── runtime/
├── builtin/
│   ├── module.go          // Module 接口
│   ├── registry.go        // ModuleRegistry（可选，main 聚合也可）
│   ├── system/{module.go, service.go, process.go}
│   ├── firewall/{module.go, rule.go, policy.go}
│   └── journal/{module.go, log.go}
├── platform/
│   ├── resolver.go        // Capability → Command Resolver
│   ├── systemd.go / iptables.go / journalctl.go / ...
└── controlplane/
```

### 冻结的 6 层链路（每层只回答一个问题）
```
Runtime        → 是谁在请求？
Dispatcher     → 应该交给谁处理？
Builtin Module → 用户想完成什么业务？（表达业务意图）
ExecutionPlan  → 需要哪些步骤？
Platform Resolver → 当前平台怎样实现这些步骤？
Executor       → 安全执行 Command 并回报结果
```
- Kernel 不知道 HTTP/JWT/数据库；Builtin 不知道 Linux 发行版；Executor 不知道业务语义；Platform 不知道用户身份。
- 每层职责单一，同时保持 8-bit：单二进制、模块少、复用高、演进空间足。

### 待办（Phase 3 衔接）
- `Module` 接口定义、`platform/` Resolver 是 Phase 3 插件系统的直接前置。
- "Builtin=Compile-time Plugin" 意味着 Phase 3 的 Runtime Plugin 复用同一 `Module` 接口。

