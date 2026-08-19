# 部署参考物（Reference Deployment Artifacts）

本目录包含 OpsCore `opscore-server` 的单节点参考部署物。所有产物均 **不重新实现 Deployment**
（R70 硬边界①）：它们只描述/打包既有 `cmd/opscore-server` 组合根。

## 文件

| 文件 | 用途 |
|------|------|
| `opscore.service` | systemd 单元（single-node, non-root, 加固） |
| `Dockerfile` | 多阶段 → distroless，non-root |
| `opscore.json.example` | JSON 配置示例 |

## 单节点部署（systemd）

1. 构建：`make build`（产出 `opscore-server`）。
2. 安装二进制到 `/usr/local/bin/opscore-server`。
3. 准备配置 `/etc/opscore/opscore.json`（参考 `opscore.json.example`）。
4. 建数据目录并赋权：`mkdir -p /var/lib/opscore && chown opscore:opscore /var/lib/opscore`。
5. 安装单元：`cp opscore.service /etc/systemd/system/ && systemctl daemon-reload && systemctl enable --now opscore`。

加固要点：`User=opscore`、`NoNewPrivileges=true`、`ProtectSystem=strict`、
`ReadWritePaths=/var/lib/opscore`、`Restart=on-failure`、`TimeoutStopSec=30`。

## 容器部署（distroless）

```bash
docker build -f deploy/Dockerfile -t opscore:latest .
docker run -p 8080:8080 -p 8081:8081 -v /var/lib/opscore:/var/lib/opscore opscore:latest
```

`EXPOSE 8080 8081` 分别对应 external/v1 与运营探针。

## 升级说明（Upgrade）

- **配置 schema 版本戳记**：`version` 字段当前为 `"1"`。未知键或被拒绝的 `version` 会
  **fail-closed**（进程拒绝启动，而非静默采用错误配置）。
- 升级二进制即可；配置向后兼容同一 `version`。升 `version` 属破坏性变更，须走 Scope ADR。
- 单节点优先；多节点共享状态（`Storage` seam）当前为 **inert 预留**，请勿依赖。
