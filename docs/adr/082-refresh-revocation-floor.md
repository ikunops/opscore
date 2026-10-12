# ADR-082 — Refresh-Token Revocation Floor（刷新令牌吊销下限 / 质量基线 P1-2 闭合）

- **Status**: Accepted（维护轮，2026-10-11）
- **Phase**: 维护（非证据链 Phase；无新判据、无判据取值变化）
- **Base**: ADR-081（HEAD = `542e363`）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-10）
- **Supersedes**: 质量基线 2026-07-22 P1-2 的「已知限制」登记（refresh 无吊销、jti 未消费——见 §5）

---

## 1. 要闭合的债（质量基线 2026-07-22 P1-2）

刷新令牌 7 天有效、**无法吊销**；`jti` 是装饰字段（生成但从不消费）。泄露的 refresh 令牌
在其整个有效期内可用。原审查给出的修法：「refresh 令牌 denylist / 版本号」。

## 2. 设计：**每用户吊销下限（revocation floor）**——版本号法的变体

吊销 = 在存储的 Config KV 里记录该用户的**下限时间戳**（吊销时刻，秒粒度）；
`/api/auth/refresh` 在验签与过期检查之外，**新增一条判据：令牌的 `iat` 不得早于（含等于）
该用户的 floor**。

```go
// auth/service.go（非冻结）
func (s *AuthService) RevokeRefresh(username string) error   // Set("auth:token-floor:<u>", now)
func (s *AuthService) refreshFloor(username string) (int64, bool, error)   // Get
// Refresh: claims.Iat <= floor  ⇒  ErrTokenRevoked
```

**为什么选 floor 而非 jti denylist**：denylist 需要**每令牌**状态（每个签发的 refresh 都要
登记，存储与清理都随签发量增长）；floor 是**每用户一个时间戳**，吊销一次即作废该用户
**全部**在途 refresh（「改密码后全端登出」的标准语义），存储 O(用户) 且无需清理。
jti 保持装饰字段——per-**token** 吊销是它未来的用途（登记为后续精化），本债的泄露场景
（「吊销这个用户的全部令牌」）由 floor 覆盖。

**粒度**：per-user（全端登出）。per-token 吊销登记为后续精化。

## 3. 语义与边界（逐条）

1. **秒粒度与 `<=`**：claims 的 `iat` 是秒粒度 ⇒ floor 比较取 `iat <= floor`（吊销秒内
   签发的令牌一并作废）——fail-closed 的标准吊销语义。**代价**：吊销后**同一秒内**的
   重新登录会被拒（用户一秒后重试即成功）；测试以 1.1s 睡眠跨越秒边界保证确定性。
2. **access 令牌不受影响（设计使然）**：access TTL 15 分钟，泄露的 access 至多多活 15 分钟；
   吊销针对的是**长生命周期的 refresh**。给 Authenticate 加 floor 检查 = 每请求一次
   storage 读（热路径代价）——拒绝（ADR-082 §2 的同一权衡）。
3. **吊销端点的鉴权方向（承重）**：`POST /api/auth/revoke` 要求**有效的 access 令牌**，
   且只吊销**调用者自己**的 refresh。**泄露的 refresh 令牌不能吊销**——否则攻击者反而能
   锁死真实用户（DoS）。真实用户持有效 access（≤15 分钟 TTL）总能在攻击者使用 refresh
   前吊销它。
4. **存储故障 = fail-closed**：floor 读取失败 ⇒ 拒绝该 refresh（无法读取的下限是无法
   遵守的下限）；floor 写入失败 ⇒ 吊销失败并 500（操作者可见）。
5. **存储位置**：Config KV 存储（`auth:token-floor:<username>`）——sqlite 部署下**持久**，
   重启后 floor 仍然生效；内存存储（demo）下 floor 随进程生命周期（与该存储的其余部分
   一致，登记为 demo 的自身限制）。
6. **对登录零影响**：Login 不检查 floor ⇒ 吊销后凭正确口令的重新登录照常（新令牌的
   `iat` 晚于 floor）——吊销的语义是「作废旧的」，不是「锁死账户」。

## 4. 与 ADR-081 窗口互斥的关系

正交且互补：窗口互斥串行化**族账本压缩**的销毁窗口（内部记账）；本 ADR 的 floor 串行化
的是**用户令牌的寿命**（外部凭证）。二者不共享任何锁（floor 只触 Config 存储），
也无语义耦合（一个管账本行，一个管 JWT 的 `iat`）。

## 5. 验收（证据）

| 验证 | 结果 |
|---|---|
| `TestAuthService_RefreshRevocationFloor`（auth 包，`-race` × 2） | 泄漏（pre-floor）refresh ⇒ `ErrTokenRevoked`；吊销后重新登录 ⇒ 正常，且**第二次吊销**再次杀之；floor 不触及 access 令牌；未知用户吊销 = 无害 no-op |
| `TestP1_2RevokeRefreshInvalidatesPreFloorTokens`（server 包，`-race` × 2，真 HTTP 面） | 无凭据 revoke ⇒ 401；有效 access revoke ⇒ 200；pre-floor refresh ⇒ 401；**access 不受影响**（/api/inventory 仍 200）；跨秒界后的重新登录 ⇒ 200 且新 refresh 可轮转 |
| server 全包 + auth 包 + `-race` | 全绿 |
| 全量门禁 | `GATE_all_OK` |

## 6. 后果

- 正面：P1-2 闭合——泄露的 refresh 可被用户一键作废（全端登出）；floor 持久于 sqlite。
- 代价：Config KV 里每用户一行（O(用户)）；吊销秒内的同秒令牌一并失效（§3.1，文档化）；
  per-token 吊销仍未提供（jti denylist，后续精化）。
- 登记：管理端**代用户吊销**（admin 面）与「登出其余会话」的会话语义，列为后续精化。

## 7. 更新（2026-10-12，P54 / ADR-087/088）

§2 与 §6 登记的两项后续精化已清偿：

- **per-token 吊销**：jti 拒绝名单落地（`auth:token-denylist:<jti>`；`Refresh` 在 floor
  之后查名单；「登出其余会话」的粒度——杀旧留新，T395 钉住；名单读错误 fail-closed）。
- **管理端代吊销**：`POST /api/auth/revoke` 扩展**可选** body——`{"username"}`（该用户
  floor，全端登出）与 `{"refresh_token"}`（按 jti 单枚，持有者自证）；目标非调用者时
  一律需 admin 角色，且**授权先于状态变更**（T398 钉住：403 后目标令牌必须仍可用）；
  无 body 的既有形态**逐字节/逐语义不变**（T396 + 本文档原测试）。
- **仍登记**：会话列表（已发 jti 登记表——盲杀 jti 需要它，未虚构）；名单 compaction
  （Config KV 无 Delete，条目过期后天然惰性、增长界如实声明）；access 侧即时吊销
  （§3.2 取舍沿用）。
