# OpsCore 代码质量基线复审（2026-10-11）

> 复审人：judge（ZCode 代行）　|　日期：2026-10-11　|　基线：`quality-baseline-2026-07-22.md`（Phase 1.9.1，commit 597ce3b）
> 范围：逐项复审原报告的全部 P0/P1/P2/Q 条目的**当前状态** + 记录 2026-07-22 之后新增的验证基础设施。
> 方法：每项以当前工作树为准逐条核对（grep/Read 到行号），不沿用旧结论。

---

## 一、原 P0/P1 项复审（逐项）

| 项 | 原内容 | 现状（2026-10-11） | 证据 |
|---|---|---|---|
| **P0-1** 开放注册 | 默认无门槛 | **✅ 已修**：`AllowRegister`（默认 false）⇒ 未开启时 `POST /api/auth/register` 返回 403 "registration is disabled" | server.go:131-136、:531-533 |
| **P0-2** JWT 密钥默认值 `change-me-in-prod` | 建议非 demo/memory 启动即拒 | **✅ 已修**：`cmd/opscore/main.go:546-550` —— 非 demo 且非 memory 模式下检测到默认密钥即 `logger.Error + os.Exit(1)` | main.go:544-551 |
| **P1-1** 默认 admin/admin | 建议强制改密或拒启 | **✅ 已修**：`main.go:553` 起 "Security gate (P1-1)" —— 非平凡模式下默认 bootstrap 口令**拒启**；且 `BootstrapAdmin` 改为显式 Config 字段（不设即不引导） | main.go:552-556、server.go:124-140 |
| **P1-2** refresh 无吊销、jti 未消费 | 登记为已知限制 | **⚠ 仍开放**：`token.go:22` 的 `Jti` 仍是装饰字段，无 denylist | token.go:22 |
| **P1-3** 登录无速率限制 | 建议限流或 fail2ban | **⚠ 仍开放**：`handleLogin`（server.go:496-510）无 limiter；统一 `ErrInvalidCredentials` 防枚举仍在 | server.go:496-510 |
| **P1-4** 内部错误细节外泄 | 建议区分 4xx/5xx | **⚠ 部分改善**：通用文案（"invalid credentials"/"invalid JSON body"）已成惯例；个别 handler 仍拼 `err.Error()` | server.go:496-510 等 |
| **P1-5** 能力探测在本机而非目标机 | 架构债，建议纳入主机注册表 | **⚠ 仍开放**：`core.DetectCapability()`（capability.go:71）仍探本机；`plugin/isolation/helper.go:52` 明确 "helper NEVER calls DetectCapability"；主机注册表（controlplane/hostregistry）已建成但**探测未迁移** | capability.go:67-71 |
| **P1-6** 脆弱的字符串错误比较 | 建议 `errors.Is(err, io.EOF)` | **✅ 基本已修**：证据路径 `history_export_manifest.go:160` 已用 `errors.Is`；残留 `server.go:1786` 一处 `err.Error() == "EOF"` | history_export_manifest.go:160 |
| **P2-1** ssh.go time.After 定时器 | 建议 Timer+Stop | **⚠ 仍开放**：ssh.go:91/:259 仍为 `time.After`（成功路径定时器滞留至超时点；量级小） | ssh.go:91/:259 |

**小结**：P0 两项**全修**；P1 六项中 1 修、1 基本修、4 项仍开放（均为登记过的已知限制或架构债）。

## 二、原 Q 条目复审（团队纪律）

| 项 | 原内容 | 现状 |
|---|---|---|
| **Q1（高）缺自动化质量门禁** | pre-commit + 冒烟 + go vet/build/test 一键 | **✅ 已超越**：`_zcode_gate.sh`（build/vet/快速档/server 重包档，独占锁 + 串行包 + 临时卷 + `RACE=1` 竞态档）+ **冻结面哈希钉死测试**（P46/P49/P50/P51 四层）+ mktree 提交脚本（bytes 端到端）+ 三级 ADR 签收制度 + 桥接档案 |
| **Q2（中）测试覆盖不均** | 缺 auth.Login 失败路径、token 边界、audit sink 校验 | **✅ 大幅改善**：`auth_test.go`（TokenRoundTrip/RegisterLoginRefresh）、`storage_sink_test.go`/`sink_hash_test.go`（audit 落库 + 能力哈希 ✓ 当年点名的两项均已补）+ 全仓 54 包、~2000 用例、`-race` 全绿 |
| **Q3（低）无 PR/提交规范** | 建议固化成文 | **✅ 已固化**：`docs/ENGINEERING.md` + `docs/operations.md`（演进章程/继电器铁律/机械守卫/Drift Log）+ `bridge40/WORKFLOW_RULES.md`（workflow 铁律七条） |

## 三、2026-07-22 之后新增的验证基础设施（原报告时尚不存在）

| 能力 | 内容 | 依据 |
|---|---|---|
| **竞态检测** | `-race` 全仓 54/54 包零数据竞争（便携 mingw-w64 + `RACE=1` 门禁档）；并发子集 × 多变 GOMAXPROCS 压力 48+ 次 | ADR-079、r236/r240 |
| **冻结面机械守卫** | 四层 sha256 哈希钉死测试（P46 I9 / P49 T317 / P50 / P51）——冻结面任何变化**响亮拦截**并强制显式决策（本轮实测拦截两次） | r238 |
| **原生模糊测试** | 七族日志行分类器 × 解码器成对 + 共享读取原语；累计 ~90 万次执行零崩溃 | `fuzz_log_classifiers_test.go` |
| **并发压力形状** | T248（并发追加无缺口）、P47 T289（每流串行化）、P43 死锁回归、ADR-080/081（并发族销毁 + 窗口串行化）、Watcher soak | 各 ADR |
| **证据链（Phase 24~51）** | 签名/链/账本/锚定/见证对账/投递/判定留痕/兑现/授权生命周期/主体域——十二个问题面，两次独立对抗挑战判定收敛 | ADR-052~081 |

## 四、遗留（按优先级）

1. **P1-5 能力探测上下文错位**（架构债）：探测未迁移到目标机；主机注册表已建成，迁移是「接线」而非「新机制」——建议纳入下一轮主机面工作。
2. **P1-2 refresh 令牌无吊销**：登记为已知限制；如需闭合，方案（denylist/版本号）已在原报告给出。
3. **P1-3 登录限流**：方案已给出（固定窗口/令牌桶）。
4. **P1-6 残留一处** `server.go:1786` 的 `err.Error() == "EOF"`（一处字符串比较，`errors.Is(err, io.EOF)` 一行替换）。
5. **P2-1 ssh.go 定时器**（量级小，两处 `time.After` → `Timer+Stop`）。

> 以上均为**加固/打磨**，无 P0 级残留；证据链与竞态检测两大体系已闭环。

---

## 五、补记（同日 Phase 52 启动评估亲核，R210 尺子）

Phase 52 启动评估对本表逐项亲核后补记（证据均为当日工作树实测）：

1. **遗留 #1（P1-5）的表述已过时，以本节为准**：「探测未迁移到目标机」与代码实况不符——
   目标机探测**已存在且已接线**：`internal/core/ssh.go` 的 `probeRemoteCapability`（经 SSH
   逐项 `command -v *` 探测 + `uname` / `/etc/os-release` 主机身份，`Source=ssh`）在
   `internal/core/dispatcher.go:82`（内核派发路径）与 `internal/controlplane/server/server.go:385`
   （控制面派发前）两处接线；`internal/platform/resolver.go:100-113` 的 remote 解析消费该快照；
   本机版 `DetectCapability()`（`capability.go:71`）**生产代码零调用方**（仅测试/注释引用，
   `grep -rn "DetectCapability()" --include="*.go" internal/ cmd/` 仅命中定义处）。
2. **P1-5 的真实残差（本轮清偿）**：SSH 探测的 systemd 判定命令曾是 `command -v systemd`
   （修复前 `ssh.go:409`），而 systemd 守护进程二进制不在标准发行版 PATH（Debian:
   `/usr/lib/systemd/systemd`）⇒ 对几乎所有 systemd 主机误报 `Available=false`，remote
   解析因此系统性绕开 systemctl（`resolver.go:108` 首选 `snap.Available("systemd")`）。
   三套探测两套正确：本机版探 `LookPath("systemctl")`（`capability.go:85`）、富探测版跑
   `systemctl --version`（`capability/probe.go` systemdProbe）。**已修**：SSH 版改探
   `command -v systemctl`；判别用例 `TestProbeRemoteCapability_SystemdBySystemctlBinary`
   （伪造 bin 目录只含 systemctl ⇒ 修复前红：`Available=false`，`ssh_probe_env_test.go:161`；
   修复后绿；core + platform 定向回归绿）。
3. **P1-5 其余残差（保留登记，均非 Phase）**：① 探测失败时 resolver 静默回退主导工具
   （`resolver.go` 包注释声明的取舍）——决策依据可追责：无快照时执行审计 `capability_hash`
   为空、有快照时为快照哈希（`executor.go:238-243` + `audit.go` CapabilityHash）；改
   fail-closed 属姿态加固，非新判据（R210）。② 能力集经主机注册表分发（`SourceCache`）
   是纯可用性特性——重放已采集的判据，不产生新判据。③ `cat /proc/1/comm`（按 init 名
   判定）不采纳：与另两套探测的 systemctl 判据不同源，会引入第三种语义。
4. **遗留 #2~#5 已在本表落笔后当日清偿**（以各自提交为准）：P1-2 → ADR-082（`4f0d7a0`）；
   P1-3 → 每用户名登录限流（`4dac468`）；P1-6 残留 → `c16d897`；P2-1 → Timer+Stop
   （`b54b636`）。**当前基线唯一的开放 P1 残差即本节 #3（P1-5 修正后的登记）。**
5. **R210 裁定（Phase 52 启动轮）**：候选池六条（P1-5、harness 证据读面接线、P1-2 后续
   精化、P47 A8-⑧、ADR-081 §8 尾、P2 打磨）均不产生「此前给不出的新可断言判据」——
   P1-5 具名修法的前半已存在（本节 #1），后半（注册表分发）与 harness 读面接线属整合
   （D9 同款，ADR-077 §9 ⑦），其余为加固/呈现/已登记的诚实边界 ⇒ **Phase 52 收敛，
   不开 Phase**；本补记与 ssh.go 修复为维护轮清偿（不消耗 ADR-083/084 编号）。
