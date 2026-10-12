# ADR-083 — Phase 52 Scope: 证据平面进入部署 SSOT（控制面参考部署 / :8082 拓扑纪律）

- **Status**: Accepted（Phase 52；owner 指定登记债清偿队列第 1 项）
- **Phase**: P52（部署整合清偿——**无新判据、无新机制**，R244 已裁定不构成新 Phase 级判据）
- **Base**: HEAD = `5e0860b`（origin/main，已推送）
- **Author**: judge（ZCode 代行），用户授权自主推进（2026-10-12：直达最后节点）

---

## 1. 要清偿的登记（R244 / quality-baseline 2026-10-11 §5 #5）

R244 收敛裁定的候选池第 2 条「**harness 证据读面接线**」被保留登记为**部署整合缺口**
（「判据已存在 ⇒ 只是接入」，D9 同款先例）。owner 指定队列将 P52 定为该项的清偿轮。

本 ADR 的任务：给这个「整合缺口」一个**诚实的、可断言的**闭合形态——包括在证据面前
**撤回其字面形态**（§3）。

## 2. 事实（逐行核对当前 HEAD 代码）

| # | 事实 | 证据 |
|---|---|---|
| F1 | 证据读面（`/management/v1/protection/alerts/history/export*`、`.../export`、`.../verify`、`.../coverage`、`.../anchor`、`.../verification`、`.../destruction`、`.../input-integrity`、`.../witness-reconcile`、`.../key-lifecycle`）**只注册在控制面** `ProtectionReadMux`，由 `cmd/opscore serve` 挂到 `--mgmt-addr`（默认 `:8082`） | `server.go:1674`（`ProtectionReadMux` 定义）、`server.go:1721-1765`（全部证据路由）、`cmd/opscore/main.go:783`（`muxSrv := &http.Server{Addr: *mgmtAddr, Handler: srv.ProtectionReadMux()}`） |
| F2 | harness **机械禁止** import controlplane/*：AST 守卫的 forbiddenImports 含 `controlplane/server`、`controlplane/hostregistry` | `internal/harness/harness_test.go:25-36, 96-99` |
| F3 | 读面逻辑**不可抽取**：冻结文件只有导出**类型**、无导出函数/方法外接口；实现全在未导出方法与 `*Server` 上 ⇒ 任何 harness 侧读面都将是证据验证的**第二实现**（第二真相源），禁止 | `history_export_manifest.go`（导出面仅 `ManifestListEntry`/`VerifyFormatResult`/`VerifyResult`）、`history_export_coverage.go`（仅 `Coverage*` 类型） |
| F4 | 「读面在控制面、不在 harness」是**已经裁决过的设计边界**，不是遗漏：R21-1/ADR-048 B1 原文「the Protection management read surface lives on :8082, colocated with the gate ... Binding it here — not on the harness :8082 read-surface — keeps the counters real」；并带 R97 拓扑警告「exactly one Control Plane instance may bind :8082」 | `cmd/opscore/main.go:769-776`（代码内注释原文） |
| F5 | 部署 SSOT 把证据平面的家**排除在外**：`docs/deployment.md` §1 将 `opscore`（含 `serve`）列为「其他 CLI（**非部署面**）」；`deploy/` 只有 `opscore-server` 的三件产物（service / Dockerfile / json.example）；证据 flag 无任何参考部署口径 | `docs/deployment.md:3, §1`；`deploy/` 目录清单 |
| F6 | 证据 flag 均为 CLI 字面默认（**不经 cfgVal 播种**，配置文件/env 喂不进去：`fs.Duration("export-interval", 0, ...)` 等）；secrets（`jwt-secret`/`admin-pass`）经 `OPESCORE_<KEY>` env 播种（`cfgVal`：file → `OPESCORE_...` env → def） | `cmd/opscore/main.go:490-560`（export-* 字面默认）；`cmd/opscore/main.go:395-417`（`cfgVal`/`cfgInt`/`cfgBool` 的 env 规则） |
| F7 | harness 部署（推荐入口 `opscore-server`）**不产生也不暴露任何证据**：`internal/harness/` 全文无 `HistoryExport` 引用 | `grep -rn HistoryExport internal/harness/` ⇒ 0 命中 |

⇒ **综合**：证据链（28 个 Phase，ADR-052~081）在部署口径中**无家可归**——跑着「推荐部署」
的操作者既得不到证据产出，也读不到证据读面；而承载证据的控制面进程被 SSOT 明文归类为
「非部署面 CLI」。

## 3. 裁定（R210 尺子）

1. **字面候选「harness 证据读面接线」驳回**：与 F2（机械守卫）、F3（不可抽取）、F4（已裁决的
   同进程共置边界 + :8082 单绑定警告）**三重矛盾**。它不是「缺一根线」，而是「**被裁决过
   的错误接线**」。R244 的保留登记以本条**撤回其字面形态**（把「待接线」改写为「不可接线，
   已裁决」）。
2. **可采形态（本 Phase）**：让证据平面的家**进入部署 SSOT**——把控制面进程（`cmd/opscore
   serve`）作为**一等部署面**给出参考部署：systemd 单元、secrets 契约、地址分配表、
   :8082 单绑定纪律、证据 flag 的 opt-in 口径；并以**机械漂移守卫**（T386~T388）把「部署
   产物不得发明不存在的 flag / 不得携带字面 secret / 地址纪律」变成测试。
   **无新判据、无新机制、零代码语义变更**（F1/F4 的边界原样保留——读面继续住在控制面）。

## 4. 交付物（D1~D6）

| # | 交付物 | 内容 |
|---|---|---|
| D1 | `deploy/opscore-controlplane.service` | 控制面 systemd 参考单元：`ExecStart=... opscore serve --addr 127.0.0.1:8090 --mgmt-addr :8082 --storage sqlite --db /var/lib/opscore/controlplane.db --export-interval 15m --export-dir /var/lib/opscore/exports --export-ledger-capacity 4096`（证据 opt-in flags 以注释示例给出）；强制 `EnvironmentFile`；缺 secret 拒启；加固与 `opscore.service` 同构 |
| D2 | `deploy/controlplane.env.example` | secrets 契约示例：仅 `OPESCORE_JWT_SECRET` / `OPESCORE_ADMIN_PASS` 两枚（占位值，绝无可用值）；注明 Phase 27 口令策略（≥12、非默认、非用户名） |
| D3 | `deploy/README.md`（改） | 两进程单节点拓扑 + 地址分配表 + 「:8082 恰好一个绑定者」规则 + 控制面升级说明（无 JSON schema 戳；证据 flag 仅 CLI 面） |
| D4 | `docs/deployment.md`（改） | SSOT 修正：删除「`opscore` = 非部署面 CLI」的过时分类；新增「控制面进程（证据平面的家）」节；§4 bind 表扩为**全机地址分配表**（含 :8090/:8082 归属与冲突规则） |
| D5 | `README.md`（改） | 部署面表补控制面行；快速开始补控制面启动方式；证据链的部署归属一句 |
| D6 | `cmd/opscore/deploy_artifacts_test.go`（新） | 机械守卫 T386~T388：产物存在与结构契约（serve 子命令、强制 env、无字面 secret、示例仅占位）；产物中每个 `--flag` ∈ `cmdServe` flag 集（AST 提取）；地址纪律（显式 `--addr` ∉ {8080,8081}、`--mgmt-addr` 显式） |

## 5. 非目标（明确不做）

1. **不给 harness 加任何证据路由/读面**（F2/F3/F4；本 Phase 撤回该形态）。
2. **不改任何默认地址或 flag 默认值**（R97 冲突以参考产物显式绑定 + 文档表消解；改默认 =
   行为变更 + 兼容风险，须另案）。
3. **不改 harness/controlplane 既有代码语义**（除新增守卫测试文件外的 `.go` 改动为零）。
4. **冻结面零 diff**（platform/ governance/ plugin/{runtime,isolation}/ controlplane/hostregistry/
   server 五文件 + appendonly_log.go + snapshot_anchor.go + snapshot_witness_reconcile.go + go.mod/go.sum）。
5. **不建控制面容器镜像**（systemd 是参考形态；容器化控制面登记为可选后续——需要
   distroless 无 `/bin/sh` 的 env 校验替代方案，非本轮范围）。
6. **不引入新的 secret 分发机制**（沿用 F6 既有的 `OPESCORE_<KEY>` env 通道）。

## 6. 验收

- D6 先红后绿（红证据留行：产物不存在 ⇒ T386~T388 红；落产物后绿）。
- 快速档门禁（build/vet/test）+ server 包不受影响（零 .go 语义面改动）。
- R245 归档（`bridge40/history/r245_p52_deployment_ssot.md`）。

## 7. 自对抗（预判评审会打的点，先自答）

| 预判质疑 | 自答 |
|---|---|
| **Q1「这只是文档/打包工作，配一个 Phase 编号与 ADR 对？」** | 本轮的**承重产出不是文档**，是三件事：① 对 R244 保留登记的字面形态做**撤回裁定**（F2/F3/F4 三重证伪——登记本身写错了方向）；② 把「证据平面的家在哪、:8082 谁绑定、两台进程怎么共处」变成**可断言的部署契约**（D1/D2/D3/D4）；③ 把该契约**机械化**（D6：产物引用不存在的 flag 会红、携带字面 secret 会红、地址冲突会红）。金额/风险都落在「部署产物 + 守卫」而非散文上。 |
| **Q2「为什么不干脆让 `opscore serve` 成为唯一推荐部署、废掉 harness？」** | 两者是**不同的面**，不是替代：`external/v1` 契约只由 harness 服务（F7 反面证据：控制面 mux 无 external/v1 路由——两处 grep 实证）；控制面服务的是执行 API + 保护/证据面。单节点参考拓扑是两个进程并置（D3/D4 的地址表就是为它写的）。 |
| **Q3「地址冲突（:8080/:8082）是不是应该改代码默认值一了百了？」** | 改默认 = 行为变更（`:8082` 是 ADR-048/050/051 系列文档化的面；`:8080` 是契约面），且不消除问题（两进程仍需显式分址）。参考产物显式绑定 + 文档表 + 守卫钉住，是零兼容风险的消解方式（§5.2）。 |
| **Q4「D6 的 AST 提取为什么放 `cmd/opscore` 而不是独立包？」** | 守卫对象是「引用该二进制 flag 的部署产物 ↔ 该二进制的 flag 集」的一致性；测试与被守卫的 flag 定义同包（读取同目录 `main.go`，与 harness 的 `go/parser` 守卫先例同款），依赖最小且不可能漂移。 |
| **Q5「这不还是 R210 说的『不产生新判据』吗——那为何消耗 083/084 编号？」** | 是，**不产生新判据**，本 ADR 从不声称产生判据。消耗编号是因为它是 owner 指定的登记债清偿队列的**工程交付单元**（有产物、有守卫、有验收），按项目纪律需要 Scope+Architecture 两纸与 T 编号测试；R244 已明文「083/084 编号未消耗」，此处正是它们的用途。**不把清偿冒充为 Phase**，也不把「无新判据」冒充为「无事可做」（F5/F7 是真实的部署缺口）。 |
