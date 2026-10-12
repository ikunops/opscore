# ADR-093 — 维护轮：SSH 口令 flag 的 env 通道补齐（argv 可见性，D5）

- **Status**: Accepted（维护轮，2026-10-12；无新判据、无新机制）
- **Base**: HEAD = `956384d`（ADR-092 后）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12）

---

## 1. 事实（对抗巡航第三笔）

| # | 事实 | 证据 |
|---|---|---|
| F1 | argv 对本机**所有**用户可见（`ps`）；env 对本机**不同 uid** 的进程不可见（`/proc/<pid>/environ` 属主-only）——env 是严格更优的口令通道 | — |
| F2 | 本项目自己已立此纪律：P52/T386 为参考单元断言「secret 绝不上 argv」（负例：unit 里出现 `--jwt-secret=` 即红） | `deploy_artifacts_test.go` T386 |
| F3 | 但 CLI 侧三处不对等：`serve --target-pass` 经 `cfgVal`（ADR-091 后 env-first）**已有** env 通道；`plan`/`run --target-pass`（2 处）与 `host add --password`（1 处）用**字面空默认** ⇒ 内联目标的口令**只能走 argv** | `main.go` 三处注册 |
| F4 | 非 argv 通道**存在但不全**：named host + hosts-file（0600，`os.WriteFile(..., 0o600)`）覆盖 `--target <name>` 路径；内联 `--target-host` 路径没有 | `saveHostStoreToFile` |
| F5 | `host list` 输出不含口令（NAME/ADDRESS/PORT/USER/GROUPS）——**显示层已脱敏**，缺的只是输入通道 | `cmdHost list` |

## 2. 修法（最小、与 F2 同源）

三处 flag 默认值改走 `envValue`（ADR-091 的规范名/别名规则一并适用）：

```go
// plan / run:
tPass := fs.String("target-pass", envValue("target-pass"), "…（prefer env OPSCORE_TARGET_PASS; argv is visible to local users）")
// host add:
pass := fs.String("password", envValue("password"), "…（prefer env OPSCORE_PASSWORD; …）")
```

help 文案教学 env 名；`printUsage` 增「Password discipline」段。**显式 flag 仍覆盖
默认**（优先级不变）。

## 3. 声明的行为变更（穷举）

1. 未显式传 flag 时，`OPSCORE_TARGET_PASS` / `OPSCORE_PASSWORD`（含遗留 `OPESCORE_`
   别名）现在可提供默认值——**纯增量**（此前该场景下这两个 flag 恒为空）。
2. 显式 flag 优先级不变；无 env 时行为**逐字节不变**。
3. `host list` 输出、hosts-file 格式、serve 侧行为：零变化。

## 4. 测试契约（T405）与变异

| 形态 | 覆盖 |
|---|---|
| T405（AST 守卫，`go/parser`） | **每一个**口令 flag（`target-pass` / `password`）的注册，其默认值必须来自 `envValue`/`cfgVal`——不许字面值；计数下限（≥3 target-pass、≥1 password）防提取失效空转 |

| MU | 变异 | 预期红 |
|---|---|---|
| MU4 | 任一注册回退为字面默认（本处用 plan 的一处演示） | T405 |

**红证据**：T405 是对「修复前形态」的静态断言——把 plan 的注册临时回退为
`fs.String("target-pass", "", …)`（即修复前原文）实测红，随后还原（sha256 核验）。

## 5. 非目标

- 不改 flag 名与语义；不做 stdin/交互式口令提示（登记为可选增强）；
- 不改 hosts-file 设计（0600 已核，明文存储是既有设计取舍，与 API 侧一致）；
- 不追溯改 `cmd/opscore-cli` / `opscore-pkg`（无口令 flag，已核）。

## 6. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「CLI 口令走 flag 是行业惯例，值得修吗？」** | 成本三行 + 一个守卫；收益是**与项目自己的 argv 纪律（F2）对等**，并为内联目标提供 F4 缺失的通道。惯例不是理由——本项目刚在 P52 把 argv 纪律写进守卫。 |
| **Q2「env 同样会泄露吗？」** | 不同 uid 的进程读不到彼此的 `/proc/<pid>/environ`；argv 默认对所有用户可见（F1）。env 严格更优，不是等价。 |
| **Q3「这算新判据吗？」** | 不算：CLI 输入通道补齐，与证据链/判定无关。配 ADR 因声明了新的受支持 env 名。 |
