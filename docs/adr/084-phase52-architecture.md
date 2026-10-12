# ADR-084 — Phase 52 Architecture: 证据平面进入部署 SSOT（Architecture）

- **Status**: Accepted（配套 ADR-083）
- **Base**: HEAD = `5e0860b`
- **Author**: judge（ZCode 代行）

---

## 1. 文件清单

| 文件 | 动作 | 说明 |
|---|---|---|
| `deploy/opscore-controlplane.service` | 新增 | 控制面参考单元（证据平面的家） |
| `deploy/controlplane.env.example` | 新增 | secrets 契约示例（仅占位；两枚 env） |
| `deploy/README.md` | 修改 | 两进程拓扑 + 地址表 + :8082 单绑定 + 控制面升级说明 |
| `docs/deployment.md` | 修改 | SSOT 修正 + 控制面进程节 + 全机地址分配表 |
| `README.md` | 修改 | 部署面表 + 快速开始（控制面启动） |
| `cmd/opscore/deploy_artifacts_test.go` | 新增 | 机械守卫 T386~T388 |

**零 `.go` 语义面改动**：唯一新增 Go 文件是测试；既有一切 `.go`（harness/controlplane/cmd）
字节不变。冻结面（§ADR-083 §5.4 全清单）零 diff。

## 2. 控制面参考单元（D1，逐行语义）

```ini
ExecStart=/usr/local/bin/opscore serve \
  --addr 127.0.0.1:8090 \          # 操作 API：参考绑定回环，避开 edge 的 :8080；暴露须显式改
  --mgmt-addr :8082 \              # 证据/保护读面 + 控制台：保留 ADR-048 文档默认面
  --storage sqlite \
  --db /var/lib/opscore/controlplane.db \
  --export-interval 15m \          # 证据管线 opt-in（P34-I5：interval 与 dir 必须成对）
  --export-dir /var/lib/opscore/exports \
  --export-ledger-capacity 4096
EnvironmentFile=/etc/opscore/controlplane.env   # 强制（无 '-' 前缀）：缺文件 = 拒启
ExecStartPre=/bin/sh -c 'test -n "${OPESCORE_JWT_SECRET}" -a -n "${OPESCORE_ADMIN_PASS}"'
#                            ↑ systemd 先做变量展开；空值 ⇒ test 失败 ⇒ 单元拒启（fail-closed，
#                              绝不给二进制 change-me-in-prod / admin 兜底的机会）
```

**为什么这些 flag 在 ExecStart 而不在配置文件**：F6——证据 flag 是**字面默认注册**
（`fs.Duration("export-interval", 0, ...)`），配置文件（KEY=VALUE）与 env 只能播种
`cfgVal` 类 flag；证据 flag 只有 CLI 一条通道。参考产物必须按实际机制书写。

**证据 opt-in flags（单元内注释示例，逐项对应真实 flag）**：
签名（`--export-sign-key`/`--export-trust-keys`）、锚定（`--export-anchor-endpoint`/
`--export-anchor-timeout`/`--export-anchor-max-attempts`/`--export-anchor-capacity`）、
密钥生命周期（`--export-key-authority`/`--export-key-authority-trust`/
`--export-key-lifecycle-capacity`）、验证存证（`--export-verify-attest`/
`--export-verify-interval`/`--export-verify-capacity`/`--export-verifier-key`/
`--export-verifier-trust`）、销毁问责（`--export-destruction-log`/
`--export-destruction-capacity`）、输入完整性（`--export-acceptance-log`/
`--export-acceptance-capacity`）、格式/保留（`--export-formats`/`--export-retain`）。
**全部保持注释（不默认开启）**：每一项都需要操作者提供密钥/端点/路径才有意义，
空配置各面的既有语义是「未启用 ⇒ 503 不回退」——参考部署不给任何面「看似开启」的假象。

**加固**：与 `opscore.service` 同构——`User=opscore`/`Group=opscore`、`NoNewPrivileges=true`、
`ProtectSystem=strict`、`ProtectHome=true`、`ReadWritePaths=/var/lib/opscore`、
`StateDirectory=opscore`（0750）、`Restart=on-failure`、`RestartSec=2`、`TimeoutStopSec=30`、
`WorkingDirectory=/var/lib/opscore`。

## 3. secrets 契约（D2）

- **仅两枚**：`OPESCORE_JWT_SECRET`、`OPESCORE_ADMIN_PASS`（F6 的 `OPESCORE_<KEY>` 规则：
  key = flag 名去 `--`、连字符转下划线、全大写）。
- **示例文件只含占位**（值为空或 `<...>` 占位），绝不携带可用值；`admin-pass` 注明 Phase 27
  策略（≥12 字符、非默认口令、非用户名——非 demo 持久化部署强制）。
- **secret 绝不出现在 argv**：单元不写 `--jwt-secret=...`/`--admin-pass=...`（argv 对同机
  任何用户可见），只经 env 通道（`cfgVal` 的 env 播种）。T386 对此有负例断言。

## 4. 全机地址分配表（D3/D4 共用；I6 的守卫对象）

| bind | 进程 | 默认 | 冲突规则 |
|---|---|---|---|
| `:8080` external/v1 只读契约 | harness（`opscore-server`） | 契约面 | 控制面**不得**使用（参考单元显式 :8090） |
| `:8081` 运营探针 | harness | A-10 独立 | — |
| `:8082` 证据/保护读面 + 控制台 | **控制面** | 单绑定：**恰好一个**控制面实例可绑定（R97） | harness 若同机启用 management 写面（需 `OPSCORE_MANAGEMENT_TOKEN`），必须在 `opscore.json` `management.listen` 指定他址（示例 :8083） |
| `:8090` 控制面操作 API | **控制面** | 参考产物显式 | 参考默认回环；暴露须显式 |

## 5. 不变量（I1~I6）

| # | 不变量 |
|---|---|
| I1 | 冻结面零 diff（ADR-083 §5.4 全清单；blob 哈希自证） |
| I2 | 零 `.go` 语义面改动：除 `cmd/opscore/deploy_artifacts_test.go` 外无新增/修改 `.go` 文件 |
| I3 | 单元文件**不携带任何字面 secret**、不在 argv 传 secret（T386 负例：出现 `--jwt-secret=`/`--admin-pass=` 即红） |
| I4 | `EnvironmentFile` **必须强制**（无 `-` 前缀）+ `ExecStartPre` 缺值拒启（T386：`-` 前缀即红） |
| I5 | 部署产物引用的**每个** `--flag` 必须存在于 `cmdServe` 的 flag 集（T387，AST 提取；产物**包括注释行**——注释里的示例 flag 同样是产物承诺） |
| I6 | 地址纪律：单元显式传 `--addr`（∉ {8080,8081}）与 `--mgmt-addr`（T388） |

## 6. 测试映射（T386~T388）

| T | 测试 | 判别力（先红后绿的「红」是什么） |
|---|---|---|
| T386 | `TestT386ControlplaneUnitContract` | 产物不存在即红（初始红证据）；结构断言：ExecStart 以 `/usr/local/bin/opscore` + `serve` 开头、强制 env（无 `-`）、无字面 secret、示例定义恰好两枚 `OPESCORE_*` 且值为空/占位 |
| T387 | `TestT387ArtifactFlagsExistInServeFlagSet` | 单元（含注释）内每个 `--flag` 经 AST 提取的 `cmdServe` flag 集校验；负例自证：`--export-typo` 形式的引用会红 |
| T388 | `TestT388ControlplaneAddressDiscipline` | 单元必须显式给 `--addr` 且 ∉ {8080,8081}、给 `--mgmt-addr`；改为 8080/8081/缺省即红 |

**AST 提取法**（与 `harness_test.go` 的 go/parser 守卫同款）：读同目录 `main.go`，定位
`func cmdServe`，遍历其体内 `SelectorExpr` 调用 `fs.String/Int/Bool/Duration`，取首参
`BasicLit` 字符串为 flag 名；单元引用的 flag（去 `--`）必须 ∈ 该集合。

## 7. 实现步骤（每步跑门禁）

1. 写 T386~T388（**先红**：产物缺 ⇒ 三测全红；留一行红证据）。
2. 落 D1（单元）。
3. 落 D2（env 示例）。
4. 重跑 T386~T388 ⇒ 绿。
5. 文档 D3/D4/D5（deploy/README、docs/deployment.md、README）。
6. 快速档门禁（build/vet/test）全绿 → 提交（docs 提交 + 实现提交，mktree 流程）。
7. R245 归档 + 推送核验。

## 8. 容量与成本（诚实声明）

- 平台成本：**零**——产物是文本文件与一个测试；无新依赖、无新进程、无运行时开销。
- 守卫成本：T386~T388 是纯文件+AST 检查，毫秒级，不触网不落盘（除读文件）。
- 维护成本：证据 flag 增删时，若参考产物不同步，T387 会红——这正是守卫的目的（把
  「文档不得发明不存在的 flag」的部署版本机械化，见 operations.md §6 Drift 纪律）。
- **不声称**覆盖面：参考单元是 systemd/Linux 形态；容器、Windows、多节点拓扑不在本轮
  （ADR-083 §5.5/§5.4 已登记非目标）。
