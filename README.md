# OpsCore

**Operations Control Plane** — 统一身份/权限/执行/审计/插件治理的运维控制平面。

> 本文档为顶层索引。详细设计见 [`docs/`](docs/)，架构决策见 [`docs/adr/`](docs/adr/)。
> 所有文档遵循 SSOT 原则：与代码不符处以代码为准（见 [`docs/operations.md`](docs/operations.md) 的 Drift Log）。

## 设计哲学

OpsCore 遵循 **Lightweight Control Plane** 原则（详见 [ADR-003](docs/adr/003-lightweight-architecture.md)）：

- **Single Binary First** — 单二进制部署，`scp opscore && ./opscore` 即可运行
- **Progressive Complexity** — 复杂度随需求增长，不提前设计
- **Reusable Primitives** — Context / Operation / Step / Resource 复用而非新建
- **Embedded Default** — 默认无需外部服务（无 DB 也能跑）
- **Capability Driven** — 能力决定行为，不是配置堆叠

## 架构概览（7 层只读栈 + 部署面）

```
Runtime Core (core/plugin/controlplane/builtin)        ← 冻结执行引擎
   │
Platform Operations (observability/cluster/            ← 四大外围能力（冻结）
                    enterprise/governance)
   │
Governance Policy Persistence (governancepolicy)       ← Policy 存储唯一所有者
   │
Platform Integration (platformview)                    ← 只读集成门面
   │
Event Correlation (correlation)                        ← 只读关联门面
   │
External Interface (external /v1)                      ← 稳定版本化只读契约
   │
Deployment (harness + cmd/opscore-server)              ← 唯一组合根
```

- 每个上层只依赖下层的 **查询接口**，绝不 import 冻结执行路径。
- 完整分层、SSOT 映射与冻结边界见 [docs/architecture.md](docs/architecture.md)。
- 冻结边界以代码为准；任何 Major 演进须走 Scope → Architecture → Implementation 三级签字。

## 二进制

| 二进制 | 入口 | 用途 |
|--------|------|------|
| `opscore` | `cmd/opscore` | Phase 0 执行 CLI（`list` / `plan` / `run`） |
| `opscore-cli` | `cmd/opscore-cli` | Phase 11.1 只读 CLI，只绑 `external/v1` |
| **`opscore-server`** | `cmd/opscore-server` | **推荐部署入口**：Phase 12.2 Deployment Harness 唯一组合根 |
| `opscore-pkg` | `cmd/opscore-pkg` | 打包辅助 |

## 快速开始

```bash
# 构建
make build

# —— 执行 CLI（Phase 0）——
./opscore list
./opscore plan system.service.restart --name nginx      # dry-run
./opscore run  system.service.restart --name nginx      # 真正执行

# —— 部署面（推荐）——
./opscore-server -config deploy/opscore.json.example    # 启动只读服务 + 运营探针
#   external/v1  → :8080   (/external/v1/{execution,host,policy,correlation})
#   运营探针      → :8081   (/healthz /readyz /versionz)
```

## 文档导航

| 文档 | 内容 |
|------|------|
| [docs/architecture.md](docs/architecture.md) | 7 层架构、SSOT 映射、Boundary Matrix |
| [docs/deployment.md](docs/deployment.md) | 单节点部署、配置、双 bind、优雅关闭 |
| [docs/policy-lifecycle.md](docs/policy-lifecycle.md) | Policy 持久化与只读生命周期 |
| [docs/external-contract.md](docs/external-contract.md) | 版本化只读公共契约（external/v1） |
| [docs/operations.md](docs/operations.md) | 演进章程、继电器铁律、机械守卫、Drift Log |
| [deploy/README.md](deploy/README.md) | 参考部署物（systemd / distroless / 升级说明） |
| [docs/adr/](docs/adr/) | 架构决策记录（ADR-031/032/033/034 等） |

## 演进状态

| 阶段 | 内容 | 状态 |
|------|------|------|
| Phase 0~14 | 内核优先、九抽象、轻量架构、插件运行时/隔离、平台操作、治理策略、平台集成、事件关联、外部契约 `external/v1`、部署面 | ✅ CLOSED（ADR-001~032） |
| Phase 15~23 | 部署生产化、文档与参考部署、配额保护 | ✅ CLOSED（ADR-033~051） |
| Phase 24~39 | **证据链地基**：告警历史导出的快照内部一致性、跨快照 seq 覆盖、Ed25519 签名 + 独立信任锚、逐跳发布链、与保留期无关的 chain ledger | ✅ CLOSED |
| Phase 40~51 | **证据链的域外/时间/身份维度**：发布锚定（域外见证）、密钥生命周期、验证报告、销毁留痕、验证者独立性（VAK）、输入完整性、见证对账、投递清偿、判定留痕、锚定兑现、验证者授权生命周期、生命周期主体域 | ✅ CLOSED（ADR-052~081） |
| 证据体系 | **两次独立对抗挑战判定收敛**：十二个问题面各有机制、状态面、判别用例与变异；登记债全部清偿或具名 | ✅（ADR-079~081 为竞态检测落地与并发收口） |

> 演进纪律见 [docs/operations.md](docs/operations.md)：Major 变更须 Scope → Architecture →
> Implementation 三级签字，且先定范围、不直接编码。冻结面（执行引擎与证据原语）的
> 纯同步性缺陷修复走 ADR-079/081 先例：ADR + 检测器证据 + 回归形状，缺一不可。
> 竞态检测自 2026-10-10 起可用（`RACE=1` 门禁档位，ADR-079）。

## 与 Demo 的关系

本仓库是全新重写，不是 [opscore-demo](https://github.com/YuDong999/opscore) 的分支。
Demo 保留了原始的 metrics 采集、服务识别、防火墙操作等逻辑，作为参考实现。

## 技术栈

- **语言**: Go 1.22+
- **依赖**: 极少（配置层用 `encoding/json`，无 yaml）
- **入口**: `opscore-server`（HTTP 只读服务）+ `opscore`（执行 CLI）
- **部署**: 单节点 systemd / distroless 容器（多节点为预留 inert seam）

## License

MIT
