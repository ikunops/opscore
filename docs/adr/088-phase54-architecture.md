# ADR-088 — Phase 54 Architecture: 吊销粒度精化（管理端代吊销 + jti 拒绝名单）

- **Status**: Accepted（配套 ADR-087）
- **Base**: HEAD = `14f2220`（P53 后）
- **Author**: judge（ZCode 代行）

---

## 1. 文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| `internal/controlplane/auth/service.go` | 修改 | `tokenDenyKey`/`DenyRefreshToken`/`refreshDenied`/`ParseRefreshClaims`；`Refresh` 增名单检查；`RevokeRefresh` 注释更新（F3 的过时表述） |
| `internal/controlplane/auth/auth_test.go` | 修改 | T394/T395 |
| `internal/controlplane/server/server.go` | 修改 | `handleRevoke` body 语义（revokeRequest + 分支 + 授权先于状态变更） |
| `internal/controlplane/server/server_revoke_test.go` | 修改 | T396~T398（既有文件，扩展） |
| `docs/adr/082-refresh-revocation-floor.md` | 修改 | 追加「更新（P54）」节：§2/§6 登记由 ADR-087/088 清偿 |

**冻结面零 diff**；`storage`/`token.go`/`rbac.go` 包零改动；无新路由、无新依赖。

## 2. 实现细节

### 2.1 auth 服务（service.go）

```go
// tokenDenyKey: "auth:token-denylist:" + jti
func (s *AuthService) DenyRefreshToken(jti string, exp int64) error {
	if jti == "" { return errors.New("auth: jti required") }
	return s.stor.Config().Set(tokenDenyKey(jti), strconv.FormatInt(exp, 10))
}

// refreshDenied: 读名单；读错误 ⇒ 返回错误（fail-closed）。
// 值只作记录（诊断/未来的 compaction 依据）；命中即拒绝，与 exp 无关。
func (s *AuthService) refreshDenied(jti string) (bool, error) { ... }

// ParseRefreshClaims: 用 refresh secret 验签取 Claims（持有者自证）。
func (s *AuthService) ParseRefreshClaims(token string) (Claims, error) {
	return ParseToken(token, s.refreshSecret)
}
```

`Refresh` 检查顺序（floor 之后，轮换之前）：

```go
	if claims.Jti != "" {
		if denied, derr := s.refreshDenied(claims.Jti); derr != nil {
			return "", "", derr           // fail-closed：读不到的名单是遵守不了的名单
		} else if denied {
			return "", "", ErrTokenRevoked
		}
	}
```

### 2.2 端点语义（server.go `handleRevoke`）

```go
type revokeRequest struct {
	Username     string `json:"username"`
	RefreshToken string `json:"refresh_token"`
}
// body 可缺席（既有调用方 POST 无体）：json.NewDecoder 容忍 io.EOF；
// 其它解码错误 ⇒ 400。
```

分支（**授权一律先于任何状态变更**）：

| body | 步骤 |
|---|---|
| 无/空 | `s.auth.RevokeRefresh(caller)` → 200（**既有响应逐字节不变**，I1） |
| `username` 非空 | `username != caller && !s.isAdmin(caller) ⇒ 403`；否则 `RevokeRefresh(username)` → 200 |
| `refresh_token` 非空 | `claims, err := s.auth.ParseRefreshClaims(tok)`；err ⇒ 400；`claims.Sub != caller && !isAdmin ⇒ 403`；`DenyRefreshToken(claims.Jti, claims.Exp)` err ⇒ 500；200 |
| 两者同给 | 400（意图必须唯一） |

### 2.3 文档更新

`RevokeRefresh` 的注释（F3）改为如实两条机制并存；ADR-082 追加更新节（清偿登记）。

## 3. 不变量（I1~I6）

| # | 不变量 |
|---|---|
| I1 | 无 body 的 `POST /api/auth/revoke`：响应与效果**逐字节/逐语义不变**（T396） |
| I2 | **授权先于状态变更**：403 路径绝不产生任何 floor/名单写入（T398 用「403 后目标令牌仍可用」钉住） |
| I3 | fail-closed：名单读取错误 ⇒ Refresh 拒绝（不是放行）；空 jti 拒绝写入 |
| I4 | **粒度**：名单只杀命中的 jti；同用户其余会话照常轮换（T394）；他人不受影响（T394） |
| I5 | **轮换语义**：杀旧 jti 不影响其后继（新 jti）——「登出其余会话」（T395） |
| I6 | 冻结面零 diff；无新路由；access 热路径零改动（ADR-082 §3.2 取舍沿用） |

## 4. 测试映射与变异

| T | 形态 | 判别点 |
|---|---|---|
| T394 | auth 单元（内存存储） | deny → 命中令牌 `ErrTokenRevoked`；第二会话照常；他人照常；floor 仍工作；空 jti 拒绝 |
| T395 | auth 单元 | 轮换后杀旧 jti：旧失、新照常（== 登出其余会话） |
| T396 | HTTP | 无 body：现状语义（先红对照见 §5；吊销后 refresh 401） |
| T397 | HTTP | 自吊销单枚：该枚 401、第二会话 200 |
| T398 | HTTP | admin 杀他人令牌 200；非 admin 403 且**令牌仍可用**；非 admin 代 username 403 且**他人令牌仍可用**；两字段 400；坏令牌 400 |

| MU | 变异 | 预期红 |
|---|---|---|
| MU1 | username 分支去掉 admin 检查 | T398 |
| MU2 | Refresh 移除名单检查 | T394/T395/T397 |
| MU3 | 把 deny 放到授权检查之前 | T398（403 后仍可用被破坏） |

## 5. 实现顺序（每步跑门禁）

1. 写 T396~T398（HTTP，**只用既有 API**）⇒ 跑一次：**实测红**——当前 `handleRevoke` 忽略
   body：`{username: 他人}` 实际吊销的是**调用者自己**的 floor（他人的 refresh 仍有效）、
   `{refresh_token}` 同样无效 ⇒ 断言全红（红证据留行）。
2. 实现 §2.1/§2.2 ⇒ T396~T398 绿。
3. 写 T394/T395（单元）⇒ 绿。
4. 变异 MU1~MU3（现取 sha256、还原核验）。
5. ADR-082 更新节 + `RevokeRefresh` 注释更新。
6. 快速档门禁（build/vet/test）全绿 → 提交（docs 提交 + 实现提交，mktree）→ R247 归档 → 推送核验。

## 6. 成本与诚实声明

- 名单存储：每枚被吊销令牌一行（`auth:token-denylist:<jti>`）；过期项天然惰性、永不再命中
  （ADR-087 §3.2）；**无 Delete 通道**（Config KV F4）⇒ 增长界 = 历史被吊销令牌数，
  compaction 需存储层改动，登记为后续。
- 运行成本：Refresh 峰值多一次 Config 读（仅当 jti 非空）；access 路径零改动。
- **不声称**：会话列表、盲杀、多节点共享、access 侧即时吊销（各自登记/沿用取舍）。
