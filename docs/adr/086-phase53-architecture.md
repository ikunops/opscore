# ADR-086 — Phase 53 Architecture: 能力集经主机注册表分发 + 远程目标快照归属修复

- **Status**: Accepted（配套 ADR-085）
- **Base**: HEAD = `9f8cee4`（P52 后）
- **Author**: judge（ZCode 代行）

---

## 1. 文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| `internal/core/context.go` | 修改 | `Build()` 播种规则（缺陷 D 修复）——非冻结 |
| `internal/controlplane/server/server.go` | 修改 | `Config.CapabilityCacheHints`、`Server.capCacheHints`、`coreCtx` 增名参、`parseTargetBody` 增返回、三个新函数、hint 常量 |
| `cmd/opscore/main.go` | 修改 | `--capability-cache-hints`（`cfgBool`，默认 false）→ `Config` |
| `internal/core/snapshot_seeding_test.go` | 新增 | T392 |
| `internal/controlplane/server/capability_cache_hints_test.go` | 新增 | T389~T391、T393 |
| `deploy/opscore-controlplane.service` | 修改 | 注释块加一行 opt-in 示例（T387 守卫自动校验其为真实 flag） |
| `docs/deployment.md` | 修改 | §5 控制面节补 opt-in 说明（hint 可见性与清除方式） |
| `docs/operations.md` | 修改 | Drift Log 增一行：质量基线「已接线」表述与缺陷 D 实况的漂移记录 |

**冻结面零 diff**；`snapshot`/`hostregistry`/`storage`/`platform` 包**零改动**。

## 2. 实现细节

### 2.1 缺陷 D 修复（`Build()`，context.go:307-309 处）

```go
// Seeding rule (ADR-086 §2.1 / ADR-085 §3): auto-detected LOCAL facts describe
// THIS machine only and are never cached under a remote (non-zero) target's
// key; explicitly supplied snapshots describe the TARGET and always seed it.
seedCap, seedHost := capSnap, hostSnap
if !b.target.IsZero() {
    if b.capSnap == nil {  seedCap = nil }    // auto-detected caps: local-only
    if b.hostSnap == nil { seedHost = nil }   // auto-built identity: local-only
}
states := map[TargetKey]*targetState{}
if b.target.IsZero() || seedCap != nil || seedHost != nil {
    states[b.target.Key()] = &targetState{cap: seedCap, host: seedHost}
}
```

**语义对照（谁被播种）**：

| 场景 | 此前 | 此后 |
|---|---|---|
| 零目标（本地执行） | 本机快照+身份 ✓ | **不变** ✓ |
| 远端目标 + `WithCapabilitySnapshot/WithHostSnapshot`（投影/只读附挂，如 isolation helper） | 显式快照 + 本机身份（身份误归属） | 显式快照；**未被投影的部分为 nil**（与 F7 先例一致） |
| 远端目标 + 自动检测（server coreCtx、CLI plan/run） | 本机快照 + 本机身份（**缺陷 D**） | **不播种** ⇒ 富化真探测 / 诚实缺席 |

### 2.2 名参穿线

```go
func (s *Server) coreCtx(r *http.Request, username string, target core.TargetHost,
    parent context.Context, hostName string) core.Context
```

- `parseTargetBody` 返回 `(core.TargetHost, string /*name*/, error)`——string 分支带名，
  object/缺席分支 `""`（3 个调用点在 server.go 内）。
- 批处理（server.go:901 处）：`..., s.defaultTarget, parentCtx, ""`（扇出子上下文不在范围）。
- `handleInventory`：`{name}` 解析处取 `hostName`。

### 2.3 三个函数（消费紧随 Enrich 之后）

```go
ctx = core.EnrichContextForTarget(ctx, target)
ctx = s.applyCapabilityCacheHints(ctx, hostName, target)   // 新
persistCapabilitySnapshot(ctx, s.stor, s.logger)

const capabilityHintLabel = "capability_snapshot_hash"

func (s *Server) applyCapabilityCacheHints(ctx core.Context, hostName string, target core.TargetHost) core.Context {
    if !s.capCacheHints || hostName == "" { return ctx }
    if snap := ctx.CapabilitySnapshot(); snap != nil {
        s.publishCapabilityHint(hostName, snap)      // 幂等；仅成功探针（或外层富化）到达
        return ctx                                   // 新鲜永远优先
    }
    return s.capabilityCacheFallback(ctx, hostName, target)
}

func (s *Server) publishCapabilityHint(hostName string, snap *snapshot.CapabilitySnapshot) {
    // best-effort；h.Labels[capabilityHintLabel] == snap.Hash() ⇒ 不写（幂等）
}

func (s *Server) capabilityCacheFallback(ctx core.Context, hostName string, target core.TargetHost) core.Context {
    // GetByName → Labels[hint] → Snapshots().GetByHash → Unmarshal → Source=SourceCache
    // → child := core.WithTarget(ctx, target); child.SetSnapshot(target, &snap); return child
    // 任何一步失败 ⇒ 原样返回 ctx（I3）
}
```

### 2.4 flag 与部署面

`--capability-cache-hints`（`cfgBool` 通道，默认 false）→ `Config.CapabilityCacheHints`。
参考单元注释一行 + `docs/deployment.md` §5 一条（hint 经 `GET /api/hosts/{name}` 可见、
可在 `POST /api/hosts` 时清除——删除该 label 即放弃缓存）。

## 3. 不变量（I1~I7）

| # | 不变量 |
|---|---|
| I1 | 门禁 off ⇒ 缓存**零行为变化**：不写 label、不回退（T391a） |
| I2 | **新鲜优先**：上下文已有本轮探测所得快照时，缓存绝不覆盖（T389） |
| I3 | fail-closed 于现状：hint 缺失/载荷缺失/损坏 ⇒ 原样返回，绝不部分附加、绝不 panic（T391b/c） |
| I4 | **名键隔离**：A 的 hint 绝不服务 B（T391d） |
| I5 | 发布幂等 + 变更才写；无名主机不写（T389） |
| I6 | **播种规则**：本机事实绝不归属远端目标；显式快照与零目标播种不变（T392/T393；MU4 回归形状） |
| I7 | 冻结面零 diff；无新路由；`Hash()` 不含 `Source` ⇒ 审计弱引用不变（F10） |

## 4. 测试映射与变异（MU1~MU4）

| T | 形态 | 判别点 |
|---|---|---|
| T389 | 缝（同包）+ HostStore 计数装饰器 | 发布门禁/幂等/无名/未知名/新鲜优先 |
| T390 | HTTP `/api/inventory/{name}` | hint + 探针失败 ⇒ `source=cache` |
| T391 | HTTP + 缝 | off/悬空/损坏 三态 fail-closed；名键隔离；同 server 对照组 |
| T392 | core 单元 | remote 不播种；显式/零目标播种 |
| T393 | HTTP | 不可达 ∧ 无 hint ⇒ 响应**无**本机身份与 `source=local`（缺陷 D 回归形状） |

| MU | 变异 | 预期红 |
|---|---|---|
| MU1 | 去掉 `!s.capCacheHints` 门禁 | T391a |
| MU2 | 去掉 `GetByHash` 失败分支 | T391b |
| MU3 | 缓存覆盖已有快照（反转 I2） | T389 |
| MU4 | 回退 §2.1 播种修复（恢复对远端目标的播种） | T392 + T393（缺陷 D 的回归钉） |

## 5. 实现顺序（每步跑门禁）

1. 写 T392/T393（**先红**：缺陷 D 在场）——与既有 T390/T391 红一并留档。
2. 落 §2.1（core 修复）⇒ T392/T393 转绿。
3. 落 §2.2/§2.3（server 侧）⇒ T390/T391 转绿。
4. 写 T389（发布缝）⇒ 绿。
5. 变异 MU1~MU4（现取 sha256、还原核验）。
6. docs（deployment §5 一条、operations Drift Log 一行、单元注释一行）。
7. 快速档门禁全绿 → 提交（docs 提交 + 实现提交，mktree）→ R246 归档 → 推送核验。

## 6. 成本与诚实声明

- 探测成本：远端路径每次派发一次探针（连接池复用；上限 3s）——P1-5 既定语义的**恢复**，
  不是新增（§ADR-085 5.3）。
- 缓存成本：发布仅哈希变化时一次 registry Save；回退仅失败路径一次 label 读 +
  内容寻址读 + 解码。
- **不声称**：批处理覆盖、主机身份缓存、TTL 策略、多节点（登记为边界）。
- 数据面零新增：无新表、无新路由、无新包、无新依赖。
