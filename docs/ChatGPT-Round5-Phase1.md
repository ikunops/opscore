# ChatGPT 架构讨论 — Round 5：Phase 1 控制平面

> 对话: https://chatgpt.com/c/6a5e58cd-163c-83ea-9d11-ccaa54128093
> 时间: 2026-07-21
> 背景: Phase 0 已完成（896 行 Go，扁平 core/ 包，6 核心抽象，7 项测试通过，system.service.restart 业务链跑通）

## 我的提问

我们已按 Round 4 建议把 Phase 0 写完（896 行，go build 通过，7 项测试全过）。Round 4 的建议大部分已照做：
- core/ 扁平包，10 个文件
- Handler.Plan() 临时不拆 Planner
- PlanRuntime 没暴露，Executor 内部用 executionState
- Error 体系用 typed error（ErrOperationNotFound / ErrCapabilityMissing / ErrExecutionFailed）
- Result 模型有 ExecutionResult
- Operation 结构体有 Name/Risk/Permission/Handler
- 完全无 DB，AuditSink 默认 LogSink
- ADR-003 已写，名 Lightweight Architecture Principle
- 第一条业务链 system.service.restart 完整跑通

问 Phase 1 三个问题：
1. Storage Interface 和 Auth（JWT+RBAC）的切入顺序？
2. AuditSink 从 LogSink 切到 DB 的迁移路径？
3. Permission 模型（ResourceType × ActionType）在 Phase 1 落地时，Registry 的 Operation.Permission 和数据库 RBAC 表怎么对齐？

## ChatGPT 回复（要点）

### 核心判断
Phase 0 完成 = **Kernel 已冻结**。以后再改 Kernel 只应是 Bug Fix，不是架构调整。
Phase 1 的核心目标不是"增加功能"，而是"增加治理"——解决"谁在调用 Kernel、为什么、有没有权限、有没有审批"。

### Q1：Storage Interface 先于 JWT
- 不是 `sql.DB`，而是 **Repository** 模式
- `Storage` 接口聚合 UserRepository / RoleRepository / PermissionRepository / AuditRepository / ConfigRepository
- Kernel 继续零感知，ControlPlane 依赖 Storage
- Storage 默认 **Memory** → SQLite → Postgres
- **建议 Phase 1 第一周不要接 SQLite，先用 Memory 把 Auth+RBAC 跑通，第二周再接 SQLite**

### Q2：AuditSink 迁移
- **不要让 Dispatcher 知道存哪里**
- 新增 Adapter：`Executor → AuditSink → StorageAuditSink → AuditRepository → SQLite`
- Kernel 的 `Executor → AuditSink.Emit()` 不变
- 未来可组合：`CompositeAuditSink → LogSink + StorageSink + WebhookSink`，全部 Emit
- Kernel 完全不知道实现

### Q3：Registry Permission 与 DB RBAC 统一（最关键）
- **永远让 Operation 成为权限的"唯一事实来源"（Source of Truth）**
- 数据库只保存**授权关系**，不定义权限
- Operation 注册时带 Permission（ResourceType × ActionType），启动时自动同步到数据库
- 管理员永远不能新增权限，只能授权（Role → Permission）
- 增加 **Startup Sync**：Registry → PermissionSync → Database，启动同步一次，插件加载再同步一次
- Manifest 新增 Operation → 数据库自动出现

### Phase 1 推荐顺序（按治理能力推进，非按功能）
```
Phase 1.1  Storage 抽象 (MemoryStorage + Repository 接口)
Phase 1.2  SQLiteStorage (Schema + Migration)
Phase 1.3  Operation Registry ⇄ Permission Sync ⇄ 数据库权限元数据
Phase 1.4  User / Role / JWT / RBAC
Phase 1.5  HTTP API / Middleware / Context Builder
Phase 1.6  StorageAuditSink / Task Repository / Config Repository
```

### 关键区别
传统后台：数据库 → Operation（DB 驱动）
OpsCore：**Operation（代码）→ Permission Metadata（DB）→ Role Permission（DB）→ JWT Context → Kernel**（Operation 驱动）

### 新增 ADR 建议
**ADR-004：Code Owns Capability, Database Owns Assignment**
- 代码定义能力（Capabilities / Operations / Permissions）
- 数据库只定义分配关系（Users / Roles / Grants）
- 开发者决定系统"能做什么"，管理员决定"谁可以做"
- 插件系统接入时自然继承此模型
