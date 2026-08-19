# ChatGPT 架构讨论 — Round 7：Phase 3 Plugin 平台

> 对话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093 （"系统架构设计评审"）
> 时间: 2026-07-21
> 背景: Phase 0 已完成（896 行 Go，扁平 core/，6 核心抽象，7 项测试通过，system.service.restart 跑通）。
>       Phase 1 精炼共识：Runtime 层 + 小 Store 接口 + Audit EventBus→Subscriber + Permission 退化为 Operation metadata（ADR-004）。
>       Phase 2 共识：Builtin=Compile-time Plugin（共享 Module 接口）；原子 Operation；强类型请求 struct；Operation 只产 Command；新增 platform/ Resolver。

## Phase 2 冻结结论（作为 Phase 3 前提）

- 统一 `Module` 接口：`type Module interface { Name() string; Register(reg Registry) }`。Builtin 与 Plugin 共用，Dispatcher 不知区别。
- 原子 Operation（一个 Operation = 一个可授权动作）；Handler 自拥强类型请求 struct（不用 map/interface{}）。
- Operation 永远只产 `Command`（`[]string`），绝不 Shell 字符串。
- 新增 `platform/` 包：Operation 声明 Requirement，Resolver 把 Requirement→具体 Command（systemd→systemctl 等）。Executor 永远只知道 Command。
- 6 层链路：Runtime → Dispatcher → Builtin Module → ExecutionPlan → Platform Resolver → Command → Executor。

## 我的提问

（提问文本保存在 `_phase3_msg.txt`）

Phase 2 我们冻结了几件事，作为 Phase 3 的直接前提：
- Builtin = Compile-time Plugin；统一 Module 接口 `Module { Name() string; Register(reg Registry) }`；Plugin = Runtime Module，接口一致，Dispatcher 永远不知道区别。
- Operation 只产 Command（`[]string`），绝不 Shell；平台差异由 platform/ Resolver 承担（Operation 声明 Requirement，Resolver 映射成具体 Command）。
- 6 层链路：Runtime → Dispatcher → Builtin Module（业务意图）→ ExecutionPlan（执行目标）→ Platform Resolver（平台实现）→ Command（唯一执行语言）→ Executor（安全执行）。

现在进 Phase 3（Plugin 平台）。目标：让外部开发者用同一套 Module 接口写 Runtime Module，在"单二进制优先 / 8-bit 轻量"约束下，安全地扩展系统能力。

三个问题，请给具体机制与子序列（3.1~3.N）：

1. 插件发现与加载（最关键的硬骨头）：单二进制优先前提下，Runtime Plugin 怎么被加载进进程？候选：
   (a) Go plugin 包（.so 动态链接）——跨平台 / 签名 / 版本耦合麻烦；
   (b) 独立进程 + 本地通信（gRPC / stdio），主进程 Kernel 通过同一 Module 接口远程驱动插件；
   (c) 启动时扫描 manifest 目录，编译进来的走 Module，外部插件由 Plugin Manager 加载后同样 Register 进 Registry；
   (d) WASM 运行时。
   哪种最契合 8-bit（单二进制 / 不引入重运行时 / 演进空间足）？若选 (c)，Plugin Manager 怎么把外部插件的 Operation 安全地 Register 进同一个 Registry，又不影响 Kernel 稳定性（崩溃 / 提权 / 版本错配怎么防）？

2. 隔离与沙箱：插件 Handler 崩溃 / 死循环 / 提权 怎么隔离？是进程级隔离（对应上面的 (b)），还是进程内 panic recovery + 资源上限（对应 (a)(c)(d)）？结合 Phase 2 的"插件只产 Command、真正危险动作在 Executor 统一执行"，Executor 是不是天然的隔离边界？

3. 生命周期与 Manifest / SDK：插件的 install / enable / disable / upgrade / unload 怎么设计？Metadata Synchronizer 在插件 load 时同步 Operation metadata、unload 时 cleanup（这点你之前提过）。Manifest 应声明什么（routes / permissions / capabilities / config / migrations / menus）？Plugin SDK 应给插件作者提供哪些最小原语（Module 接口 + Context + 注册辅助 + 类型化 Request）？

请给 Phase 3 最小但不过度的结构与子序列。

## ChatGPT 回复（要点）

Phase 3 被定位为"最容易走偏"的一步——一旦引入插件，系统极易膨胀成 Kubernetes Operator 平台，与 8-bit 哲学冲突。核心目标：**Core 稳定、Plugin 可替换、Executor 仍是唯一危险执行入口、不引入巨大 Runtime**。

### 关键决策：否决 Go plugin (.so)
- 生态问题：只支持 Linux、编译版本必须严格一致、Go runtime 强绑定（Core go1.24 / Plugin go1.23 可能直接无法加载）、升级困难。
- 安全边界缺失：Plugin 进同一进程 → Plugin panic 拖垮 Core；Plugin 若拿到 `os.Exec` 可直接绕过 Executor。
- 结论：**Plugin cannot directly access OS** 这一宪法约束，.so 模式无法满足。

### 选定方案：Manifest + 独立进程 + UDS RPC（非二选一，而是组合）
```
Manifest → Plugin Manager → 启动 Plugin Process → UDS → Module Proxy → Registry
```

### 3.1 Plugin Runtime 基础协议（先做协议，不做 SDK）
- Handshake：Plugin 启动连 `/var/run/opscore/plugin/docker.sock`，发 `{type:"handshake", plugin:{name,version,apiVersion}}`。
- Core 回 `{name,version,operations:[{name,resource,action,risk}],capabilities:[...]}`。
- **Core 不执行，只注册 Metadata**。

### 3.2 Module 接口统一（Builtin 与 Plugin 共用）
- Builtin: `type Module interface { Name() string; Register(reg Registry) }`
- Plugin 不在一个进程，无法真 `Register()`，故引入 **ProxyModule / RemoteModule**：
  ```go
  type RemoteModule struct {
      client RPCClient
      manifest Manifest
  }
  ```
- Core 眼中二者都是 `Module`，最终：**Builtin Module → Registry** 与 **Remote Module → Registry** 完全一致。

### 3.3 Registry 安全注册（双层隔离，防污染 Kernel）
```go
type Registry struct {
    builtin map[string]Operation
    plugin  map[string]Operation
}
```
- 拆成 builtin registry + plugin registry：插件卸载只需删 `plugin/docker/*`，不影响 builtin。

### 3.4 双边界隔离（Executor ≠ 运行隔离）
- **Plugin Process** = 运行隔离边界：防死循环 / panic / memory leak / goroutine leak。
- **Executor** = 权限隔离边界：防危险命令 / 非法参数 / root misuse。
- 链路：**Plugin(不可信) → RPC → Core → Executor(可信执行) → OS**。

### 3.5 Plugin Process 最小限制（8-bit：先不引入容器）
- Phase 3 只需 **Process Supervisor**：
  ```go
  type PluginRuntime struct {
      PID int
      RestartPolicy
      MemoryLimit
      Timeout
  }
  ```
- 启动：`systemd-run` 或 `exec.CommandContext`；cgroup 未来再接。

### 3.6 Manifest 设计（plugin.yaml，v1 保持极简）
```yaml
name: docker
version: 1.0.0
apiVersion: v1
operations:
  - name: docker.container.restart
    resource: container
    action: restart
    risk: medium
capabilities:
  - docker
config:
  - key: docker.socket
    type: string
permissions:
  autoSync: true
runtime:
  type: process
  socket: docker.sock
```
- **v1 不加 menus / routes / migration**——这些属 Control Plane，Phase 4 UI 插件阶段再扩 `ui:`。
- 第一版 Manifest 只描述**能力**，不描述执行细节。

### 3.7 插件生命周期状态机
`Installed → Loaded → Enabled → Running → Disabled → Uninstalled`
- install：复制 `/opt/opscore/plugins/docker/` + 注册 manifest。
- load：读 manifest，验证 signature + apiVersion。
- enable：启 process。disable：停 process，保留 metadata。uninstall：删 plugin registry + metadata。
- **Metadata Synchronizer**：load 时 Handshake→Manifest→Operation Metadata→Sync→DB（生成 operations/capabilities/plugin_records）；unload 用 **soft delete**（`enabled=false`），保留审计历史。

### 3.8 SDK 第一版（只给最小原语，不给 Executor）
```go
type PluginModule interface {
    Manifest() Manifest
    Register() error
}
type PluginContext interface {  // 只读
    Logger()
    Config()
    Capability()
}
sdk.RegisterOperation(sdk.Operation{Name:"docker.container.restart", Handler:&RestartHandler{}})
```
- **不给插件 Executor**——插件不能执行，只能产生 Operation / ExecutionPlan。

### Phase 3 最小代码结构
```
internal/plugin/
├── manager/   (manager.go, lifecycle.go)
├── protocol/  (handshake.go, rpc.go)
├── registry/  (proxy.go)
├── manifest/  (parser.go)
├── runtime/   (process.go)
└── sdk/       (module.go, context.go, register.go)
```

### 子阶段顺序
- **3.1 Plugin Protocol**：handshake + manifest + RemoteModule
- **3.2 Plugin Manager**：install / load / enable / disable / uninstall
- **3.3 Plugin Registry**：Operation proxy + Metadata Sync
- **3.4 SDK**：Module SDK + Handler SDK
- **3.5 第一个真实插件**：先做 `plugin/demo`（验证 Runtime Module→Registry→Operation→Plan→Executor 全链路），Docker/K8s 再接入。

### 冻结原则 → ADR-005（Plugin Isolation Principle）
1. Builtin Module 与 Runtime Plugin 遵循同一 Module 模型。
2. Runtime Plugin 不进入 Core 进程。
3. Plugin 只能产生 Operation 和 ExecutionPlan，不能直接执行系统命令。
4. Executor 是唯一 OS 访问入口。
5. Plugin Runtime 使用进程隔离，不引入容器作为默认依赖。
6. Manifest 描述能力，不描述执行细节。

最终形态：
```
            Control Plane
   Runtime → Dispatcher → Module System
                /          \
          Builtin      Runtime Plugin
                        |
                     UDS RPC
                        |
                  ExecutionPlan
                        |
            Platform Resolver
                        |
                    Executor
                        |
                     Linux
```
（接近轻量版 HashiCorp Plugin Model + Kubernetes Control Plane 思想，但保持单二进制、小内存、无重依赖、插件隔离、能力可扩展。）
