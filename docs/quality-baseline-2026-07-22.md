# OpsCore 代码质量基线报告（Phase 1.9.1）

> 审查人：Senior Developer（高级开发工程师）　|　日期：2026-07-22　|　范围：core / controlplane / builtin / demo / cmd
> 目的：建立团队质量把控基线。本报告按 **安全 → 正确性/健壮性 → 工程质量/流程 → 测试** 四个维度列出问题，并给出可执行的修复项。

---

## 一、总体结论

**架构底子扎实，工程纪律有漏点。** 安全关键不变量（命令注入防护、RBAC 失败即拒绝、JWT 双密钥、主机密钥 fail-closed）都做对了，说明团队"设计能力"在线。但**发布前缺少质量门禁**，导致本可避免的低级 bug 反复出现（如已修的 `.gitignore` 误屏蔽 `cmd/opscore/`、`UI 注入多留 {}`）。本报告的问题，多数属于"应被门禁挡住"而非"不会写"。

---

## 二、亮点（值得保留，作为团队规范范本）

- ✅ **无 shell 注入**：本地 `exec.Command(args...)`（step.go:48）、远程 `buildRemoteCommand` 逐参单引号转义（ssh.go:130-148），杜绝 word-splitting / 注入。这是教科书级做法。
- ✅ **RBAC 失败即拒绝**：`rbac.Authorize` 未知用户 → `false,nil`（rbac.go:27），`handleRun` 先鉴权再执行（server.go:417）。
- ✅ **JWT 访问/刷新双密钥 + jti**：`accessSecret/refreshSecret` 分离（token.go:39-43），`newJTI` 用 `crypto/rand`（token.go:26）。
- ✅ **主机密钥 fail-closed**：`InsecureIgnoreHostKey=false` 时直接拒绝（ssh.go:39-41），非 insecure 路径不连真机。
- ✅ **能力驱动设计意图清晰**，`Operation as Code` 抽象到位，ADR/宪法冻结流程成熟。

---

## 三、问题清单

### P0 —— 上线前必须修（安全）

**P0-1 开放注册接口**
- 位置：`server.go:308 handleRegister`
- 现象：任何人只要能访问 HTTP API 即可 `POST /api/auth/register` 创建账号，且默认无角色（最小权限，这点好），但**毫无门槛**。
- 风险：结合默认 `admin/admin`（见 P0-2），本地/暴露环境可被随意建号；生产必须由管理员审批。
- 修复：`register` 增加"仅管理员可调用"或"默认关闭、需 `--allow-register` 显式开启"；代码注释已承认 "Phase 4 will replace open registration"，但现在是活的。
- 教学点：*公开端点 = 攻击面，默认应 deny。*

**P0-2 默认 JWT 签名密钥 `change-me-in-prod`**
- 位置：`main.go:108`（`--jwt-secret` 默认值）、`main.go:180-181`（`secret+":access"`）
- 现象：若部署时不传 `--jwt-secret`，token 用已知字符串签名 → **任何人可本地伪造任意用户 token**。
- 修复：非 `demo` / 非 `memory` 模式下，若 `--jwt-secret` 仍为默认值则**启动即报错退出**；或启动随机生成并打到日志。
- 教学点：*密钥绝不能"有默认值且可用"。*

### P1 —— 应修（安全/健壮性）

**P1-1 默认管理员凭据 `admin/admin`**
- 位置：`main.go:106-107`
- 修复：首次启动强制改密，或在非 demo 模式下打印醒目警告；bootstrap 后标记"需改密"。

**P1-2 刷新令牌无吊销、jti 未被消费**
- 位置：`token.go:18-23`（jti 生成但不校验）、`auth.Refresh`（service.go:95）
- 现象：刷新令牌 7 天有效，泄露后无法作废；`jti` 目前是装饰字段（无服务端 denylist）。
- 修复（后续）：引入 refresh 令牌 denylist / 版本号；短期可接受，但需在文档标注为已知限制。

**P1-3 登录无频率限制 / 暴力破解防护**
- 现象：`/login` 对失败无次数限制（虽已用统一 `ErrInvalidCredentials` 防枚举，这点好）。
- 修复：加固定窗口/令牌桶限流（中间件），或 fail2ban 前置。

**P1-4 错误处理把内部细节外泄**
- 位置：`server.go:404`（`"plan failed: "+err.Error()`）、`server.go:444` 执行失败返回 500
- 现象：内部错误原文回给客户端，且"操作执行失败"与"服务器错误"都用了 500，语义混淆（操作失败应是 200+success=false 或 422）。
- 修复：服务端 `logger.Error` 记详情，客户端返回通用文案；区分 4xx/5xx 语义。

**P1-5 能力探测在"控制平面本机"而非"目标机"**
- 位置：`capability.go:25 DetectCapability()` 探测 `runtime.GOOS`（本机）
- 现象：当命令经 SSH 跑到 `192.168.94.20` 时，真正该探测的是**远程主机**的能力，但当前 capability 探测的是控制平面自己的环境。remote 路径上该闸已被放宽（记忆：restart 跳过本机 `HasSystemctl` 判定）。
- 风险：能力模型对远程目标失真，是架构债。
- 修复（后续）：`CapabilityContext` 改为在目标机上经 SSH 探测（`cat /proc/1/comm`、`which systemctl` 等），纳入 "Phase 2/3 主机注册表"。
- 教学点：*能力要"在正确的机器上探测"，上下文错位是分布式系统的经典坑。*

**P1-6 脆弱的字符串错误比较**
- 位置：`server.go:573`（`err.Error() == "EOF"`）
- 现象：依赖 `json.Decoder` 的具体错误文案，库实现变了就静默失效。
- 修复：`errors.Is(err, io.EOF)`。

### P2 —— 建议（打磨）

- **P2-1** `ssh.go:92` `time.After(timeout)` 在成功路径不停止定时器，短命令下轻微泄漏 → 用 `time.NewTimer` + `Stop()`。
- **P2-2** `decodeJSON` 在 `r.Body==nil` 时静默返回，后续 `credentials{}` 触发 401 但无明确日志 → 显式 400。
- **P2-3** `handleRun` 操作失败统一 500 → 语义上应为 200+`success:false`（操作结果是业务数据，不是服务器故障）。
- **P2-4** 注释语言混用（代码英文 / UI 与 ADR 中文）→ 团队统一约定（建议代码注释英文、用户文案中文）。
- **P2-5** `demo` 假主机 `PasswordCallback` 仅本机 127.0.0.1 监听（好），但 `InsecureIgnoreHostKey:true` 写死 —— 仅 demo 用，OK，需在注释强调"绝不用于真机"。

---

## 四、工程质量 / 流程问题（团队纪律）

**Q1（高）缺自动化质量门禁**——本次会话两次事故（gitignore 屏蔽 CLI、UI 注入 `{}`）都可在提交前用 1 分钟脚本挡住。**这是团队最需要补的短板**，对应任务 #31。

**Q2（中）测试覆盖不均**——已有 `ssh_cmd_test`、`service_test`、`services_test`（demo 整链）做得不错；但缺：`auth.Login` 失败路径、`rbac.Can` 性能、`token` 边界、`audit sink` 落库校验。建议补关键路径单测。

**Q3（低）无 PR/提交规范与 Review checklist**——对应任务 #32，把"门禁 + 审查清单"固化成文，让质量不依赖个人。

---

## 五、下一步（与四项任务对应）

| 任务 | 动作 | 交付物 |
|---|---|---|
| #30（本报告） | 全面 Code Review | 本文档 |
| #31 自动化门禁 | pre-commit（文件真进库、不被 ignore 误杀）+ UI/API 冒烟脚本（node --check + curl 整链）+ `go vet/build/test` 一键 | 仓库内脚本 |
| #32 工程规范手册 | review checklist / commit 规范 / ADR 流程 / 测试标准 | `docs/ENGINEERING.md` |
| #33 实战带教 | 后续 Phase 2/3/4 开发中嵌入"为什么这样写"，并定期 PR 级 review | 持续 |

**P0 修复建议作为 #33 的首批带教实例**（open-registration 关门、JWT 密钥启动校验）——改动小、教学价值高，可在下一轮顺手做掉。

---
*审查基于当前工作树（Phase 1.9.1，commit 597ce3b）。未推送，所有结论仅用于本地改进。*
