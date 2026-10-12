# ADR-087 — Phase 54 Scope: 吊销粒度精化（管理端代吊销 + jti 按令牌拒绝名单）

- **Status**: Accepted（Phase 54；owner 指定登记债清偿队列第 3 项）
- **Phase**: P54（凭证加固——**无新判据、无新机制**；清偿 ADR-082 §2/§6 明文登记的后续精化）
- **Base**: HEAD = `14f2220`（P53 后）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12）

---

## 1. 要清偿的登记（ADR-082 明文）

1. §6：「登记：管理端**代用户吊销**（admin 面）与「登出其余会话」的会话语义，列为后续精化。」
2. §2：「jti 保持装饰字段——per-**token** 吊销是它未来的用途（登记为后续精化）」。

## 2. 事实（逐行核对当前 HEAD）

| # | 事实 | 证据 |
|---|---|---|
| F1 | `POST /api/auth/revoke` **无 body 语义**：忽略请求体，只吊销**调用者自己**的 floor；端点要求有效 **access** 令牌（泄漏的 refresh 不能吊销——防反向 DoS，ADR-082 §3.3） | `server.go` `handleRevoke` |
| F2 | `jti` 已生成且每令牌唯一（16 字节随机；登录与轮换各得新 jti）——正是被保留的 per-token 吊销用途 | `auth/token.go` `newJTI`/`NewClaims` |
| F3 | floor 机制（per-user、`<=`、fail-closed）已落地；`RevokeRefresh` 的注释仍写着「per-token 需要 jti denylist（被推迟）」 | `auth/service.go:99-140` |
| F4 | Config KV 接口：`Get/Set/List` —— **没有 Delete**（设计时就要考虑登记项的生命周期） | `storage/storage.go:66-70` |
| F5 | admin 判定约定：`isAdmin(username)`（角色名 = `sync.DefaultAdminRole`），403「admin role required」 | `server.go` `isAdmin`/`hostAdmin` |
| F6 | 轮换语义：Refresh 每次发**新 jti** 的 refresh ⇒ 「只杀这一枚、其余会话照常」的表达需要 per-jti 名单；floor（时间下限）**表达不了**「登出其余会话」 | `service.go` `Refresh` |

## 3. 设计

### 3.1 管理端代吊销（floor 的 admin 面）

`POST /api/auth/revoke` 接受**可选** JSON body：

| body | 语义 | 授权 |
|---|---|---|
| 无 / 空 | 调用者自己的 floor（**现状，逐字节不变**） | 仅需有效 access（既有） |
| `{"username":"alice"}` | alice 的 floor（全端登出） | `alice == 调用者` 或调用者 **admin**；否则 403 |
| `{"refresh_token":"..."}` | 该令牌的 jti 进名单（**只杀这一枚**） | 令牌 `sub == 调用者` 或调用者 **admin**；否则 403 |
| 两者同给 | 400（意图必须唯一，不猜测） | — |

### 3.2 jti 按令牌拒绝名单（per-jti denylist）

- **写入**：`DenyRefreshToken(jti, exp)` → `Set("auth:token-denylist:<jti>", exp)`。
- **检查**：`Refresh` 在 floor 检查**之后**：名单命中（值非空）⇒ `ErrTokenRevoked`；**存储读错误 ⇒ fail-closed 拒绝**（读不到的名单是遵守不了的名单，同 floor 纪律）。
- **持有自证**：per-token 吊销需要**呈上该令牌**（用 refresh secret 验签后取 jti）——名单里没有「盲杀 jti」的入口，因为系统没有「已发 jti 登记表」可展示（§4.1）。
- **生命周期**：令牌过期后其 jti 不可能再出现在任何有效令牌里 ⇒ 名单项**天然惰性**、永不再命中；Config KV 无 Delete（F4），条目数 = 历史被吊销令牌数（声明增长界，§6）。
- **并发**：每枚令牌一个独立键、只写不读改写 ⇒ 无丢失更新的竞态，无需进程内互斥。

### 3.3 语义边界

1. floor 与名单**独立互补**：floor 杀「某个时刻之前的全部」，名单杀「这一枚」。
2. 轮换**后继**令牌（新 jti）不受影响 ⇒ `{"refresh_token"}` 即「登出其余会话」的会话语义（F6）。
3. **access 令牌不查名单**（同 ADR-082 §3.2 的热路径取舍：15 分钟 TTL 自灭；每请求一次存储读被拒）。
4. 管理端「按 jti 盲杀」（不呈令牌）**不作**：无会话登记表即无可信 jti 来源；虚构一个 jti 参数只会制造「以为杀死了」的假象（§4.1）。

## 4. 非目标

1. **会话列表/已发 jti 登记表**：新状态面（谁登记、何时清理、如何与轮换对账）——登记为后续候选，非本轮。
2. **多节点共享名单**：单节点规范（与既有 floor 同一存储假设）。
3. **令牌指纹黑名单/泄露检测**：超出登记范围。

## 5. 测试契约（T394~T398）与变异（MU1~MU3）

| T | 覆盖 |
|---|---|
| T394 | auth 单元：deny → 该令牌 `ErrTokenRevoked`；**同用户第二会话照常轮换**；**他人不受影响**；floor 仍工作；空 jti 拒绝 |
| T395 | auth 单元：轮换后杀**旧 jti** ⇒ 旧失、新照常（「登出其余会话」粒度） |
| T396 | HTTP：无 body 吊销 = 现状（自身 floor；吊销后 refresh 401） |
| T397 | HTTP：`{refresh_token}` 自吊销 ⇒ 该枚 401、第二会话 200 |
| T398 | HTTP：admin 边界——admin 杀他人令牌 200；**非 admin 403 且目标令牌仍可用**（授权先于状态变更）；非 admin `{username:他人}` 403 且他人令牌仍可用；两者同给 400；坏令牌 400 |

| MU | 变异 | 预期红 |
|---|---|---|
| MU1 | username 分支跳过 admin 检查 | T398 |
| MU2 | Refresh 不查名单 | T394/T395/T397 |
| MU3 | 先 deny 再授权（授权与状态变更倒序） | T398（403 后令牌仍须可用） |

## 6. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「为什么 per-token 要呈上令牌，而不是 `{username, jti}`？」** | 系统没有「已发 jti 登记表」（§4.1 非目标）——没有可信 jti 来源，盲杀参数只是自证式假象。呈上令牌 = **持有者自证**（验签），语义诚实；admin 没有令牌时用 floor（全端），两者覆盖互补。 |
| **Q2「名单没有 Delete 会不会无限增长？」** | 条目 = 历史被吊销令牌数（典型为 0~个位数），键值极小；过期项天然惰性（§3.2）。增长界**如实声明**（§6 成本），compaction 登记为后续（需要存储层 Delete，属另一层改动）。 |
| **Q3「并发吊销会丢写吗？」** | 不会：per-jti 独立键、单次 Set，无读改写（§3.2）。 |
| **Q4「为什么不用 floor 实现 per-token？」** | floor 是**时间下限**：它要么杀太多（该用户全部）要么杀太少；「登出其余会话」（杀旧留新）恰是 floor 不可表达的形状（F6）。两个机制回答两个不同问题。 |
| **Q5「这是新判据吗？」** | 不是：凭证管理语义（谁能让哪枚令牌失效），与证据链十二问面无关；不新增任何证据面/路由（扩展既有端点）。 |
