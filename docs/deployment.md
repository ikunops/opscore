# 部署（Deployment）

> **SSOT**：`cmd/opscore-server`（读边：唯一读组合根）+ `internal/harness` +
> `cmd/opscore serve`（控制面：证据平面的家）+ `deploy/`（参考部署物）。
> 单节点为规范（single-node normative）；多节点为预留 inert seam。

## 1. 二进制与部署面

单节点参考拓扑是**两个进程并置**（地址分配见 §4）：

- **`opscore-server`**（`cmd/opscore-server`）：**读边**。Phase 12.2 Deployment Harness 的
  唯一组合根；装配读图、挂载 `external/v1` 只读契约与独立运营探针。
- **`opscore serve`**（`cmd/opscore` 子命令）：**控制面**。执行 API 与**证据平面的家**：
  调度导出（快照、签名清单、chain ledger、发布锚定、验证存证、销毁问责、输入完整性、
  密钥生命周期）与 `:8082` 证据/保护读面 + 运维控制台。参考单元见
  `deploy/opscore-controlplane.service`，详见 §5。
- 其他 CLI（非部署面）：`opscore` 的 `list` / `plan` / `run`（Phase 0 执行 CLI）、
  `opscore-cli`（Phase 11.1 只读 CLI，只绑 `external/v1`）、`opscore-pkg`（打包辅助）。

> **为什么证据读面不在读边**：R21-1 / ADR-048 B1——保护/证据读面必须与 gate 同进程
> （计数才真实），且 `:8082` 恰好允许一个绑定者（R97）；harness 的 import 守卫
> （ADR-026 MUST-1）机械禁止读边引入控制面代码。判定记录见 ADR-083/084（Phase 52）。

## 2. 读边启动参数（flags）

| flag | 默认 | 说明 |
|------|------|------|
| `-config` | 空 | JSON 配置文件路径；提供则 fail-closed 校验 |
| `-addr` | `:8080` | external/v1 HTTP 监听地址（覆盖配置） |
| `-probe` | `:8081` | 运营探针 bind（覆盖配置） |

日志通过 `log/slog` 输出（json/text + level），由 `-config` 的 `log.*` 或默认值决定。

## 3. 读边配置文件（JSON，零新依赖）

示例见 `deploy/opscore.json.example`。Schema 版本由 `version` 字段戳记
（运营元数据，非 Runtime/Policy 语义开关，A-2）。

```json
{
  "version": "1",
  "server": { "listen": ":8080", "probe": ":8081" },
  "log": { "level": "info", "format": "json" },
  "storage": { "policyStoreDir": "/var/lib/opscore/policies" }
}
```

加载规则（`internal/harness/config_load.go`）：
- 用 `json.Decoder.DisallowUnknownFields` —— 未知键 **fail-closed** 拒绝。
- `version` 仅支持 `""` 或 `"1"`；否则拒绝。
- `log.level` ∈ {info, debug, warn}；`log.format` ∈ {json, text}；否则拒绝。
- 空 `policyStoreDir` 回落默认 `.opscore/policies`（具体、可追溯的磁盘 store，非隐式内存态）。
- 可选 `management.listen` / `management.principal`：**只在**设置了
  `OPSCORE_MANAGEMENT_TOKEN` 时才装配 management 写面；同机与控制面共处时须用它错开
  `:8082`（见 §4）。

**控制面不读这个 JSON**：`opscore serve` 的参数是 flag（+ 可选 KEY=VALUE 配置文件 + env），
见 §5。

## 4. bind 与地址分配（全机，单节点参考）

| bind | 进程 | 默认 | 语义 | 冲突规则 |
|------|------|------|------|---------|
| `:8080` | 读边 | `:8080` | external/v1 只读契约 | 控制面**不得**使用（参考单元显式 `127.0.0.1:8090`） |
| `:8081` | 读边 | `:8081` | 运营探针（A-10 独立） | — |
| `:8082` | **控制面** | `:8082` | 证据/保护读面 + 控制台（admin-only） | **恰好一个绑定者**（R97）：读边若同机启用 management 写面，必须在 `opscore.json` 用 `management.listen` 指定他址（如 `:8083`） |
| `:8090` | **控制面** | 参考产物显式 | 控制面操作 API | 参考绑定回环 `127.0.0.1`；对外暴露须显式修改 |

探针端点（`internal/harness/probe.go`）：
- `GET /healthz` → 永远 `ok`，无读、无副作用。
- `GET /readyz` → 调 `polRepo.List()` 纯读验证可达性；不可达返回 `503 not ready`
  （**绝不修复/重跑**，A-3）。
- `GET /versionz` → 暴露构建元数据（Version / GoVersion / Module / Schema）。

## 5. 控制面进程（证据平面的家）

参考单元：`deploy/opscore-controlplane.service`（加固与读边单元同构：non-root、
`NoNewPrivileges`、`ProtectSystem=strict`、`ReadWritePaths=/var/lib/opscore`）。

- **secrets 契约**：`/etc/opscore/controlplane.env`（**强制**；缺文件 = 单元拒启）只含
  `OPESCORE_JWT_SECRET` 与 `OPESCORE_ADMIN_PASS` 两枚，示例见
  `deploy/controlplane.env.example`。**secret 绝不上 argv**（argv 对本机所有用户可见）。
  缺 secret 时 `ExecStartPre` 拒启——绝不落回二进制的开发默认值；`admin-pass` 须满足
  Phase 27 策略（≥12 字符、非默认口令、非用户名）。
- **证据 flag 全部 opt-in，且只经 CLI 传入**：`--export-interval` 等是字面默认注册
  （配置文件/env 喂不进去）；`--export-interval` 与 `--export-dir` 必须成对
  （P34-I5 fail-fast）；签名/锚定/生命周期/验证/销毁/输入各面需要操作者提供
  密钥或端点才有意义，参考单元以注释给出全部选项。未启用的面一律 503
  （不回退、不假装）。
- **生命周期**：SIGTERM → serve 上下文取消 → HTTP 面 drain，导出调度器在飞 tick
  落定后退出（`TimeoutStopSec=30` 足够）。
- **能力缓存提示（opt-in，Phase 53 / ADR-085）**：`--capability-cache-hints` 让命名主机
  的成功探针把快照哈希发布到该主机的注册表 `labels["capability_snapshot_hash"]`
  （经 `GET /api/hosts/{name}` 可见），并在该主机探针**失败**时重放其**上次已知**的
  能力集（`source=cache` + 原始 `CollectedAt` 如实标注；新鲜探针永远优先）。删除该
  label 即放弃缓存。默认关闭：未启用时行为逐字节不变。
- **机械守卫**：单元引用不存在的 flag、携带字面 secret、或地址越界，都会使
  `cmd/opscore` 的 T386~T388 变红（ADR-084）——部署产物不得发明二进制的参数。

## 6. 单节点部署（systemd 参考）

- `deploy/opscore.service`（读边）：
  - `ExecStart=/usr/local/bin/opscore-server -config /etc/opscore/opscore.json`
  - `Restart=on-failure`，`TimeoutStopSec=30`；non-root：`User=opscore`
  - 加固：`NoNewPrivileges=true`、`ProtectSystem=strict`、`ReadWritePaths=/var/lib/opscore`
- `deploy/opscore-controlplane.service`（控制面 / 证据平面）：
  - `ExecStart=/usr/local/bin/opscore serve --addr 127.0.0.1:8090 --mgmt-addr :8082
    --storage sqlite --db /var/lib/opscore/controlplane.db
    --export-interval 15m --export-dir /var/lib/opscore/exports --export-ledger-capacity 4096`
  - `EnvironmentFile=/etc/opscore/controlplane.env`（强制）+ 缺值拒启（见 §5）
  - 两个单元可同机部署：地址已按 §4 错开（`:8082` 单绑定者 = 控制面）。

## 7. 容器部署（distroless 参考）

`deploy/Dockerfile`：多阶段构建 → distroless，non-root，`EXPOSE 8080 8081`，
ENTRYPOINT 仍是 `cmd/opscore-server`（组合根唯一，A-1）。

**只打包读边**：控制面容器化登记为后续可选扩展——distroless 无 `/bin/sh`，其
secrets 校验需要替代方案（如 `AssertEnvironment=` 或启动期自检），不在本轮范围
（ADR-083 §5.5）。

## 8. 优雅关闭

`signal.NotifyContext` 驱动；读边 `Harness.Shutdown` 幂等（`sync.Once`）：依次关闭
external HTTP、探针 HTTP，并对 policy store 做 drain/flush（**绝不映射成 Execution
Cancel/Apply**，A-4）。控制面：SIGTERM 取消 serve 上下文，两个 HTTP 面 drain，
导出调度器在飞 tick 落定后退出（见 §5）。
