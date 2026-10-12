# ADR-085 — Phase 53 Scope: 能力集经主机注册表分发（SourceCache 提示通道）+ 远程目标快照归属缺陷修复

- **Status**: Accepted（Phase 53；owner 指定登记债清偿队列第 2 项）
- **Phase**: P53（部署/可用性整合——**无新判据、无新机制**；含一笔由判别测试揭发的**既有缺陷**修复）
- **Base**: HEAD = `9f8cee4`（P52 后）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12）

---

## 1. 要清偿的登记（及其前提的重新裁定）

quality-baseline 2026-10-11 §5 #3②：「**能力集经主机注册表分发（`SourceCache`）**是纯可用性
特性——重放已采集的判据，不产生新判据」。`snapshot` 包自 Phase 2 起定义 `SourceCache`
（注释即设计：*"served from a previous collection (host registry hint)"*），**生产者与
消费者均为零**——设计在册、从未接线。

**前提改写（本轮亲核，延续 R245 §E 的纪律）**：登记与质量基线 §5 #1 都建立在「目标机探测
已存在**且已接线**（`server.go:385`）」之上。判别测试（本 Phase 的 T390/T392/T393）揭发：
**server 单目标路径的探测从未真正运行过**——它被一个既有的播种/守卫交互短路（缺陷 D，
§3），而 inventory 会把**控制面自己的**能力集与人机身份**归属给远端目标**。因此本轮清偿
= 「缺陷 D 修复（前置）」+「SourceCache 提示通道（登记项本体）」。

## 2. 事实（逐行核对当前 HEAD）

| # | 事实 | 证据 |
|---|---|---|
| F1 | `SourceCache` 常量存在、注释为「host registry hint」；全仓无生产者/消费者 | `internal/core/snapshot/snapshot.go:37` |
| F2 | `ContextBuilder.Build()` 把**本机**检测出的快照与人机身份**缓存到「当前目标」键下**——无论该目标是不是远端 | `internal/core/context.go:274-322`（`states := map[TargetKey]*targetState{ b.target.Key(): {cap: capSnap, host: hostSnap} }`，:307-309） |
| F3 | `EnrichContextForTarget` 的守卫：`ctx.CapabilitySnapshot() != nil ⇒ 直接返回（不探测）`。与 F2 复合 ⇒ **server 单目标路径（plan/execute/submit/inventory）对远端目标的探测永远被短路**；ctx 里躺着的是**本机**事实 | `internal/core/ssh.go:474-477`；`server.go:382-393`（coreCtx 链） |
| F4 | 批处理扇出路径**不受影响**：`WithTarget(ctx, t)` 给子上下文**全新空缓存** ⇒ 守卫放行 ⇒ 探测运行 | `dispatcher.go:82`；`context.go:372-384`（fresh states） |
| F5 | 短路后的下游全部消费「本机事实」：resolver 的 remote 分支（`snap.Available("systemd")`）、inventory 的 Host/Capability 视图、`persistCapabilitySnapshot` 入库的本机载荷 | `platform/resolver.go:100-113`；`inventory/inventory.go:88-104`；`server.go:399-410` |
| F6 | 既有测试**未覆盖**该复合：守卫语义测试只测「显式 WithCapabilitySnapshot 不被覆盖」与「零目标 no-op」；`service_test.mustPlan` 期望在「本机快照」与「nil 快照」两条路径下**结果相同**（都被 guess 分支覆盖） | `core/ssh_discovery_test.go:120-137`；`builtin/service/service_test.go:10-19` |
| F7 | 隔离 helper 的**正确先例**：远端 target 时若宿主未投影能力，helper 显式**关闭自动检测**（保持 Capability-blind），注释明写「绝不投影=助手自己的机器，对远端目标静默错误」——正是缺陷 D 的对照物 | `plugin/isolation/helper.go:43-85` |
| F8 | 注册表 `Host` 无能力字段且不可改（冻结），但 `Labels` 是既有自由元数据槽；写通道（`POST /api/hosts`）与读通道（`GET /api/hosts/{name}` 经 `serializeHost` 输出 `labels`）**都已存在** | `hostregistry.go:29-34`；`server.go` `handleCreateHost`/`serializeHost` |
| F9 | 快照存储内容寻址、`GetByHash` 既有；缺 host→hash 索引（登记项要补的那根线） | `storage.go:87-99` |
| F10 | `Hash()` 只对 `Items` 计算（不含 `Source`/`CollectedAt`）⇒ 附上缓存副本不改哈希，审计弱引用仍指向同一内容寻址载荷 | `snapshot.go:86-104`；`storage/models.go:112` |
| F11 | 探针经 `defaultSSHClientPool` 复用连接；结果按 Context（非全局）缓存 | `core/ssh.go:348-366` |

## 3. 缺陷 D（判别测试揭发，先红在本 ADR 之前）

**缺陷**：`Build()` 把本机快照种到远端目标键下（F2）→ 富化守卫短路（F3）→ ①server 路径对
远端目标**从不探测**；②失败/未探测时，`inventory`（与 resolver 输入）把**控制面机器**的
能力与身份**归属给目标主机**——「正确的代码，错误的归属」。

**红证据（实测，两个独立判别器）**：
1. T390（HTTP，命名主机 `web-01` → `127.0.0.1:1`）：响应中 `"host_id":"DESKTOP-MCDHV8N"`、
   `"source":"local"`——被请求主机名下返回的是**控制面本机**的观测。
2. T392（core 单元）：`NewContext().WithTarget(remote).Build()` 后
   `CapabilitySnapshot() != nil`（本机载荷）且 `HostSnapshot() != nil`（本机身份）。

**修复（最小面，且与 F7 的既有先例同构）**：`Build()` 仅在**显式提供**快照
（`WithCapabilitySnapshot`/`WithHostSnapshot`——它们描述**目标**）或**目标为本机**
（零目标）时播种当前目标键；自动检测的**本机**事实，对非零（远端）目标**不播种**。
⇒ 远端目标缓存从空开始：富化真探测（F3 守卫恢复原意）；探测失败则目标**诚实无快照**
（resolver 走其文档化的猜测路径），而不是被塞进别人的事实。
端到端无行为回归的证明见 §5.2。

## 4. 设计：SourceCache 提示通道（登记项本体，opt-in）

1. **发布**：命名主机探针**成功** ⇒ `labels["capability_snapshot_hash"] = snap.Hash()`
   （变更才写，幂等）。
2. **消费**：命名主机探针**失败**（上下文无快照）⇒ 读 hint → `GetByHash` →
   `Source=SourceCache` 附加。**新鲜探针永远优先**。
3. **门禁**：`--capability-cache-hints`（默认 off）⇒ 未启用逐字节不变（零回归）。
4. **fail-closed 于现状**：hint 缺失/载荷缺失/损坏 ⇒ 一律落回「无快照」路径，
   绝不部分附加、绝不 panic。
5. **名键隔离**：hint 按主机名存取，A 的缓存绝不服务 B。
6. **不做**：批处理扇出（F4 本已正常，登记为后续）、主机身份缓存（只重放能力集）、
   TTL/新鲜度策略、新路由（可见性走 F8 既有读通道）。

## 5. 声明与代价（诚实）

### 5.1 声明的行为变更（穷举——漏一条即未声明变更）

1. **server 远端目标路径现在会探测**（plan / execute / `/api/executions` submit /
   `/api/inventory/{name}` / default-target）：成功 ⇒ 真观测进 ctx（并入库、并（若启用）
   发布 hint）；失败 ⇒ 无快照（此前：本机事实冒充）。
2. **`/api/inventory/{name}` 对不可达主机**：此前显示控制面自身 Host/Capability；
   此后 Host/Capability 为空（诚实缺席），或（启用缓存且有 hint 时）显示 `source=cache`。
3. **CLI `plan/run --target-host <remote>`**：此前消费种下的本机事实；此后无快照，
   走 resolver 的文档化猜测路径（两条路径都非观测；猜测不再冒充观测）。
4. **batch 扇出**：不变（F4）。
5. **零目标（本地）路径**：不变。
6. **隔离 helper**：显式投影仍播种；blind 情形不变（F7）。
7. **`service_test.mustPlan` 类断言**：结果不变（“systemctl” guess 与旧路径同值；F6）。

### 5.2 无回归证明方式

- 全量快速档门禁 + server 全包 + core 全包；
- T393 直接钉「无本机归属」：命名主机不可达且无 hint ⇒ 响应**不得**出现本机身份/本机
  能力（`source=local` 断言缺席）；
- T392 钉紧播种规则；变异 MU4（回退播种修复）必须令 T392/T393 红。

### 5.3 代价

- 远端路径新增探测延迟（F11：连接池复用；上限 3s 超时），但这是 P1-5「观测而非猜测」的
  既定代价——此前是**静默未执行**，不是「省了」。
- hint 可见可清（F8），`Source=cache`/`CollectedAt` 自述陈旧（F10 审计引用不变）。

## 6. 测试契约（T389~T393）

| T | 覆盖 |
|---|---|
| T389 | 发布缝：off 不写；on 写且幂等；无名/空不写；未知名不 panic；**新鲜优先**（已有 ssh 快照不被覆盖） |
| T390 | 消费（HTTP）：命名主机 + hint + 探针失败 ⇒ `source=cache` 且能力来自缓存载荷 |
| T391 | fail-closed：off ⇒ 无缓存；hint 悬空 ⇒ 无缓存；载荷损坏 ⇒ 无缓存且不 panic；**A 的 hint 不服务 B**；同一 server 上有效 hint 仍工作（对照组） |
| T392 | core 播种规则：remote ⇒ 不播种（`CapabilitySnapshot()==nil` ∧ `HostSnapshot()==nil`）；显式快照 ⇒ 播种；零目标 ⇒ 播种（本机事实） |
| T393 | 无本机归属（HTTP）：命名主机不可达 ∧ 无 hint ∧ 门禁 off ⇒ **不得**出现本机身份/`source=local` 归属（缺陷 D 的回归形状） |

## 7. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「把缺陷 D 塞进 P53 是不是夹带？」** | 不是：D 是**登记的判别器（T390）自己揭发的**，且是登记项本体的**前置**——「探测失败 ⇒ 回退重放」这条设计只有在「探测失败时上下文真的无快照」时才成立；不修 D，缓存回退永远不会触发（本机快照永远非空）。ADR-086 §4 的变异 MU4 把两者钉在一起。 |
| **Q2「修 Build() 播种会不会有隐藏消费方？」** | 已穷举：生产调用点只有 server.coreCtx、CLI plan/run、isolation helper 三处 + 批处理（不经 Build）（F3/F4/F7）。CLI 的变更已在 §5.1-3 声明；helper 的 blind 情形不变；测试面已逐库核对（F6 及 §5.2）。 |
| **Q3「为什么缓存只在失败时回退，不全用缓存？」** | 成功路径零行为变化是可证明的最小侵入；全用缓存会把采集时点变成决策时点=改变成功路径判据输入（R210）。 |
| **Q4「hint 被第三方篡改？」** | hint 是**内容寻址哈希的引用**、不是信任输入：载荷被哈希锁定，篡改只能指向另一个**真实采集过**的集合或不存在；后者 fail-closed（§4.4）。不声称它是签名。 |
| **Q5「这算新判据吗？」** | 不算：十二问面与既有判别字段不动；`Source=cache` 是 Phase 2 就定义的观测标签取值，本 Phase 是其首个生产者；缺陷 D 的修复是**回归**（把误归属改回诚实缺席）。 |
