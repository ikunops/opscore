# 部署（Deployment）

> **SSOT**：`cmd/opscore-server`（唯一组合根）+ `internal/harness` + `deploy/`（参考部署物）。
> 单节点为规范（single-node normative）；多节点为预留 inert seam。

## 1. 二进制与入口

- **`opscore-server`**（`cmd/opscore-server`）：推荐的部署入口，Phase 12.2 Deployment Harness
  的唯一组合根。它装配读图、挂载 `external/v1` 只读契约与一个独立的运营探针。
- 其他 CLI（非部署面）：
  - `opscore`（Phase 0 执行 CLI，子命令 `list` / `plan` / `run`）
  - `opscore-cli`（Phase 11.1 只读 CLI，只绑 `external/v1`）
  - `opscore-pkg`（打包辅助）

## 2. 启动参数（flags）

| flag | 默认 | 说明 |
|------|------|------|
| `-config` | 空 | JSON 配置文件路径；提供则 fail-closed 校验 |
| `-addr` | `:8080` | external/v1 HTTP 监听地址（覆盖配置） |
| `-probe` | `:8081` | 运营探针 bind（覆盖配置） |

日志通过 `log/slog` 输出（json/text + level），由 `-config` 的 `log.*` 或默认值决定。

## 3. 配置文件（JSON，零新依赖）

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

## 4. 两个独立 bind

| bind | 路径 | 语义 | 是否契约 |
|------|------|------|---------|
| external/v1 | `:8080` | 只读公共读契约 | 是（external/v1） |
| 运营探针 | `:8081` | 健康/就绪/版本（观测用） | 否（A-10 独立） |

探针端点（`internal/harness/probe.go`）：
- `GET /healthz` → 永远 `ok`，无读、无副作用。
- `GET /readyz` → 调 `polRepo.List()` 纯读验证可达性；不可达返回 `503 not ready`
  （**绝不修复/重跑**，A-3）。
- `GET /versionz` → 暴露构建元数据（Version / GoVersion / Module / Schema）。

## 5. 单节点部署（systemd 参考）

`deploy/opscore.service`：
- `ExecStart=/usr/local/bin/opscore-server -config /etc/opscore/opscore.json`
- `Restart=on-failure`，`TimeoutStopSec=30`
- non-root：`User=opscore`
- 加固：`NoNewPrivileges=true`、`ProtectSystem=strict`、`ReadWritePaths=/var/lib/opscore`

## 6. 容器部署（distroless 参考）

`deploy/Dockerfile`：多阶段构建 → distroless，non-root，`EXPOSE 8080 8081`，
ENTRYPOINT 仍是 `cmd/opscore-server`（组合根唯一，A-1）。

## 7. 优雅关闭

`signal.NotifyContext` 驱动；`Harness.Shutdown` 幂等（`sync.Once`）：依次关闭 external HTTP、
探针 HTTP，并对 policy store 做 drain/flush（**绝不映射成 Execution Cancel/Apply**，A-4）。
