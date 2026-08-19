# ChatGPT 架构讨论 — Round 9：Phase 2.5 核实 + 2.6 提案

> 会话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093 （"系统架构设计评审"）
> 时间: 2026-07-22
> 背景: Phase 2.3 / 2.4 / 2.5 已本地提交（未推送）。本轮回合用 CDP 桥接脚本把 Phase 2.5（Batch Execution）实现摘要发给 GPT，请其对照 Round6 Phase2 冻结共识核实，并给出 2.6 / 下一刀建议。
> 结论: **Phase 2.5 签字通过（95% 一致，零架构性偏离）**；已知偏差 A/B 均被接受；下一刀 P0 = Remote Capability Discovery（Phase 2.6）。

---

我对照你们 Round6 冻结共识看了一遍，Phase 2.5 我认为已经没有架构性偏离，而且有一点比我之前预期的还要好：

Batch 并没有变成一个新的执行引擎，它只是 Dispatcher 上层的一层 Fan-out。

这点其实非常重要。

1. Phase 2.5 是否符合冻结共识？

我的结论：

95% 完全一致。

下面逐项看。

① WithTarget(ctx,target)

我认为这是目前整个 Batch 最漂亮的一点。

因为它没有产生：

BatchContext

RemoteContext

HostContext

这种新的 Context。

而是：

parent Context

↓

WithTarget()

↓

child Context

所以：

User

Permission

Logger

Trace

Capability

Cancel Chain

全部继承。

只有：

Target

变化。

这完全符合你们 ADR-003：

Reusable Primitive

我建议以后任何：

WithXXX()

都遵循同样原则。

例如：

WithTarget()

WithLogger()

WithTrace()

WithTimeout()

不要搞 Context 类型爆炸。

② Batch 不鉴权

这个我支持。

因为：

Dispatcher 永远不知道：

HTTP

JWT

Role

Batch 本质只是：

for target {

Dispatch(...)
}

如果：

Batch()

里面再：

Authorize()

反而违反：

core

↓

不知道 ControlPlane

所以：

Server

↓

Authorize Once

↓

Dispatcher.Batch

这是正确边界。

以后：

CLI

SDK

Plugin

调用：

Dispatcher.Batch()

也应该自己完成鉴权。

③ Failure Isolation

这一点也是正确的。

应该永远：

host1 fail

↓

host2 continue

↓

host3 continue

不要：

host1 fail

↓

整个 Batch fail

因为：

Batch 不是 Transaction。

这一点以后做：

1000 Host

更重要。

④ Group Resolve

也合理。

Group：

属于：

Host Registry

不是：

Dispatcher

Dispatcher：

应该只看到：

[]TargetHost

不知道：

Group

Inventory

CMDB
⑤ Batch 是 Dispatcher 方法

这一点我反而比以前更认可。

很多平台：

BatchExecutor

FanoutExecutor

ParallelExecutor

最后越来越复杂。

你们：

Dispatcher.Batch()

其实只是：

Dispatch()

for

Dispatch()

for

属于：

Primitive 复用。

符合 8-bit。

我唯一建议的小修正

只有一个地方。

目前：

Dispatcher.Batch()

里面：

for {

Execute()
}

建议未来保留一个：

BatchOption

例如：

Go
type BatchOption struct {

    Parallelism int

    ContinueOnError bool

}

Phase2：

默认：

Parallelism=1

Phase3：

再支持：

8

16

32

这样：

接口不会再改。

但是：

现在不用实现。

WithTarget 是否需要重新 WithCancel？

我认为：

不用。

保持：

parent ctx

↓

WithTarget(parent,target)

即可。

因为：

parent：

已经：

context.WithCancel()

过了。

Batch：

里面：

child

↓

inherits cancel

就是 Go Context 官方推荐模型。

不要：

WithCancel()

↓

WithCancel()

↓

WithCancel()

最后 Cancel Tree 太复杂。

2. 偏差 A

Handler.Plan(ctx,map[string]any)

我认为：

保持。

不要动。

原因：

现在：

Handler

↓

Decode

↓

Plan

已经稳定。

如果：

变：

Handler[T]

你会立刻碰到：

Registry

需要：

reflect.Type

generic bridge

runtime type

最后：

反而：

复杂。

我反而觉得：

DecodeInput[T]

已经足够漂亮。

符合：

Operation As Code。

偏差 B

Remote 默认 Linux Resolver

我也接受。

因为：

目前：

Remote Executor

本身：

就是：

Linux SSH

不是：

Universal Remote

因此：

systemctl

iptables

journalctl

作为默认：

合理。

Capability Discovery：

应该下一刀补。

3. 下一刀（2.6）

这里我排序会非常明确。

P0
Remote Capability Discovery

我认为：

这是：

必须立即做。

原因：

现在：

Resolver

↓

猜

以后：

应该：

Resolver

↓

Capability Snapshot

↓

Resolve()

例如：

Requirement:

Firewall

远程：

Capability：

ufw

↓

iptables

↓

firewalld

Resolver：

立即知道：

执行哪个。

Architecture：

真正闭环。

我建议：

Capability：

不要：

ssh exec

↓

每次探测

应该：

第一次连接

↓

Snapshot

↓

缓存 Context

↓

Resolver 用缓存

这样：

SSH 次数：

最少。

P1
Capability Snapshot 写入 ExecutionRecord

我建议：

做。

但是：

不要：

写：

CapabilityInfo[]

建议：

ExecutionRecord：

增加：

Go
CapabilityHash string

或者：

SnapshotVersion

另外：

Audit：

引用：

Snapshot。

否则：

Execution：

越来越大。

例如：

Execution

↓

SnapshotID

↓

CapabilitySnapshot Table

以后：

Host Inventory

也能复用。

P2
Handler 泛型

我建议：

Phase3 再做。

甚至：

可能永远不用做。

因为：

收益：

太低。

我反而建议新增一个 2.6

这个比泛型重要。

Phase 2.6
Host Snapshot

不是：

Capability。

而是：

Host Snapshot

例如：

hostname

os

arch

kernel

capability

resolver

agent version

统一：

HostSnapshot

以后：

所有：

Capability

Inventory

Resolver

Remote

全部：

共享。

Architecture：

就真正闭环。

以后：

Target

↓

HostSnapshot

↓

Capability

↓

Resolver

↓

Command

全部连起来。

我的最终优先级

我会这样排：

Phase2.6
Remote Capability Discovery
★★★★★

↓

Phase2.7
Host Snapshot
★★★★★

↓

Phase2.8
Execution Snapshot
★★★★☆

↓

Phase2.9
Builtin 扩充
(package/user/disk)
★★★★☆

↓

Phase3
Plugin SDK
最后一个总体评价

我注意到一个变化：前面几轮讨论，你们还在不断调整 Kernel 的抽象；而从 Phase 2.5 开始，你们新增功能已经几乎都是沿着既有原语扩展——Context、Dispatcher、ExecutionPlan、Resolver、Executor 等核心接口没有被迫修改。这恰恰说明前面冻结的架构开始发挥作用了。

按照这个趋势，我建议后续坚持一条评审原则：

新增一个功能，如果需要修改 Kernel 的核心接口，先停下来问一句：是不是应该通过已有原语组合实现？

如果大多数新需求都能在不改 Kernel 的情况下落地，那么这套控制平面的内核就已经进入稳定期了。