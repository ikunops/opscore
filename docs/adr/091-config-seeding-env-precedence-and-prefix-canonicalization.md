# ADR-091 — 维护轮：配置播种语义修正（env 优先）+ 环境变量前缀规范化（OPSCORE_ 规范名 / OPESCORE_ 兼容别名）

- **Status**: Accepted（维护轮，2026-10-12；无新判据、无新机制）
- **Base**: HEAD = `676cfdb`（P52~P55 清偿队列收官后）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12）

---

## 1. 缺陷 D2：契约注释与代码相反（且既有测试把错的一方钉死）

`loadConfigFile` 的契约（`cmd/opscore/main.go` 注释）明写：「Env vars (OPESCORE_`<KEY>`) take
precedence over the file, and explicit CLI flags take precedence over both」；而
`cfgVal`/`cfgInt`/`cfgBool` 的实际顺序是 **file 优先**（file → env → def）。更甚：既有
测试 `TestCfgVal_Precedence` 的子测试名就叫 *"file over env over default"*，把 file-first
钉死——**同一文件里的两个工件互相矛盾**，且没有任何一处声明过「以代码为准」。

**后果（承重）**：控制面的两枚 secrets（`--jwt-secret` / `--admin-pass`）正是经该通道
播种。配置文件里一条**陈旧的** `jwt-secret=` 行会**静默压制** env 注入的密钥（env 只能
填空缺）⇒「改 env 完成轮换」失效，且 P52 参考单元的 fail-closed 检查（ExecStartPre 验
env 存在）**不保证 env 被采用**——出现「检查通过、密钥没换」的静默失守。

而项目自己在读边写下了正确方向的理由（`harness/config.go` `EnvManagementToken` 注释）：
「config files get committed, baked into images, and copied between environments; the
process environment does not」。

**判定：env-first 为准**（契约注释 + harness 既有理由 + secrets 卫生）。修复代码 + 更新
被错误钉死的测试。**不采取**「把注释改成 file-first」——那会把错误语义合法化，并把
secrets 暴露在最不该信任的载体（会被提交的文件）里。

## 2. 瑕疵 D3：同产品两套环境变量前缀

读边（harness / `opscore-server`）文档化前缀是 `OPSCORE_`（`OPSCORE_MANAGEMENT_TOKEN`，
Phase 17 起）；控制面（`cmd/opscore`）历史拼写是 `OPESCORE_`（字母换位）。两侧各自自洽、
无文档写错，但**同产品两种拼写**是部署认知陷阱（运维设规范名 → 控制面静默落回
开发默认值）。**规范化**：`OPSCORE_<KEY>` 为规范名；`OPESCORE_<KEY>` 保留为兼容别名
（既有部署零破坏）；两者同设时**规范名胜**。

## 3. 设计

```go
// envValue: 规范名 -> 遗留别名 -> ""（空值视为缺席）
func envValue(key string) string {
    suffix := strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
    if v := os.Getenv("OPSCORE_" + suffix); v != "" { return v }
    return os.Getenv("OPESCORE_" + suffix)
}
// cfgVal/cfgInt/cfgBool 统一改为 env -> file -> def；
// 不可解析的数值/布尔值逐级回落（与原 file 分支同形）。
```

参考产物与文档改用规范名：`deploy/opscore-controlplane.service`（ExecStartPre 两行 +
注释）、`deploy/controlplane.env.example`（两键 + 前缀说明）、`docs/deployment.md` §5、
`deploy/README.md`、`README.md` 快速开始；T386 的期望键同步更新（契约演进，本条声明）。
`loadConfigFile` 的契约注释更新为双前缀表述（契约**变为真**）。

## 4. 声明的行为变更（穷举）

1. **env 覆盖 file**（此前相反）：受影响面 = 同时设置 env 与文件、且值不同的部署；新方向
   是更安全的那个，也是本文件一直**宣称**的契约。CLI 显式 flag 仍最高（不动——它整体
   替换默认值，不经这三个解析器）。
2. **`OPSCORE_<KEY>` 成为受支持名**（规范；此前完全无效）；`OPESCORE_<KEY>` 行为不变。
3. 参考产物/文档换用规范名；T386 期望键更新；`TestCfgVal_Precedence` 由「file over env」
   改判为 env-first 契约形状（**被错误钉死行为的更新**，作为判定的见证）。
其余零变化。

## 5. 测试契约（T401~T403）与变异

| T | 覆盖 | 修复前 |
|---|---|---|
| T401 | env 胜 file（string/int/bool 三型） | **实测红**（`env must beat the file, got "memory"`） |
| T402 | 规范前缀生效；规范胜别名；仅别名仍生效 | **实测红**（`canonical prefix must be honored, got "def"`） |
| T403 | 回落链：不可解析 env → file → def；空 env 视为缺席 | 绿（其规范名细分由 T402 覆盖） |

| MU | 变异 | 预期红 |
|---|---|---|
| MU1 | 回退 env-first（恢复 file 优先） | T401 |
| MU2 | 去掉规范名分支（只读遗留别名） | T402 |

## 6. 非目标

- 不改 harness 的 JSON 配置体系与 `OPSCORE_MANAGEMENT_TOKEN`（本就是规范拼写）；
- **不删除** `OPESCORE_` 别名支持（破坏性且无收益）；
- 不改 CLI flag 优先级（显式 flag 永远最高，本就不经这三个解析器）；
- 不追溯重写历史 ADR 的事实段落（083/084 的 F 项是当时实况；§9/本条记录演进）。

## 7. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「file 作为『部署设置』的第一来源会不会更合理？」** | file 是部署设置的载体不假，但这三个解析器同时播种 **secrets**——恰恰是最不该进 file 的一类（会被提交/烤进镜像/复制传播）。契约注释与 harness 先例都选 env-first；如需 file-first 应另案改契约与文档（无此需求）。 |
| **Q2「双前缀共存会不会让运维困惑？」** | 文档只教**一个**规范名；别名仅作兼容存在；优先级由 T402 钉死（规范名胜）。相比「静默落回开发默认值」的现状，这是净减陷阱。 |
| **Q3「这算新判据吗？配一个 ADR？」** | 不算新判据——配置层语义修正；配 ADR 是因为它**改变被测试钉死过的行为**并触及 secrets 采纳路径，需要声明与见证（与 ADR-079/082 同族的维护轮）。 |
| **Q4「为什么测试以前是绿的？」** | 因为测试把错误行为当契约钉住了（§1 原文），而契约注释无人核对——**注释与测试互相矛盾时，两者都不是证据**；本次以「哪一方正确」重新判定，而非「哪一方存在」。 |
