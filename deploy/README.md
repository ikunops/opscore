# 部署参考物（Reference Deployment Artifacts）

本目录包含 OpsCore 的单节点参考部署物：**读边**（`opscore-server`，唯一读组合根）与
**控制面**（`opscore serve`，证据平面的家）。所有产物均 **不重新实现 Deployment**
（R70 硬边界①）：它们只描述/打包既有二进制，不引入第二条生产接线。

## 文件

| 文件 | 用途 |
|------|------|
| `opscore.service` | 读边 systemd 单元（single-node, non-root, 加固） |
| `opscore.json.example` | 读边 JSON 配置示例 |
| `opscore-controlplane.service` | **控制面 systemd 单元**（执行 API + 证据平面 + `:8082` 证据/保护读面） |
| `controlplane.env.example` | 控制面 secrets 示例（仅占位；安装为 `/etc/opscore/controlplane.env`） |
| `Dockerfile` | 读边容器（多阶段 → distroless，non-root） |

## 单节点拓扑（两进程 + 地址分配）

单节点参考是**两个进程并置**；地址互不冲突、`:8082` 恰好一个绑定者：

| bind | 进程 | 语义 |
|------|------|------|
| `:8080` | 读边（`opscore-server`） | external/v1 只读契约 |
| `:8081` | 读边 | 运营探针（健康/就绪/版本） |
| `:8082` | **控制面** | 证据/保护读面 + 运维控制台（admin-only）——**单绑定**（R97） |
| `:8090` | **控制面** | 控制面操作 API（参考单元绑定回环 `127.0.0.1`） |

> 若读边在同一主机启用 management 写面（需要 `OPSCORE_MANAGEMENT_TOKEN`），必须在
> `opscore.json` 用 `management.listen` 指定 `:8082` 以外的地址（如 `:8083`）——
> 「正确的代码、错误的拓扑」是 R97 记录在案的部署陷阱。

## 单节点部署（systemd）

### 读边

1. 构建：`make build`（产出 `opscore-server`）。
2. 安装二进制到 `/usr/local/bin/opscore-server`。
3. 准备配置 `/etc/opscore/opscore.json`（参考 `opscore.json.example`）。
4. 建数据目录并赋权：`mkdir -p /var/lib/opscore && chown opscore:opscore /var/lib/opscore`。
5. 安装单元：`cp opscore.service /etc/systemd/system/ && systemctl daemon-reload && systemctl enable --now opscore`。

加固要点：`User=opscore`、`NoNewPrivileges=true`、`ProtectSystem=strict`、
`ReadWritePaths=/var/lib/opscore`、`Restart=on-failure`、`TimeoutStopSec=30`。

### 控制面（证据平面的家）

1. 安装同一 `make build` 产出的 `opscore` 二进制到 `/usr/local/bin/opscore`。
2. 安装 secrets：`install -m600 controlplane.env.example /etc/opscore/controlplane.env`，
   填入两枚值——`OPESCORE_JWT_SECRET`（≥32 随机字节，hex/base64；轮换即作废全部已签发
   令牌）与 `OPESCORE_ADMIN_PASS`（Phase 27 策略：≥12 字符、非默认、非用户名）。
3. 安装单元：`cp opscore-controlplane.service /etc/systemd/system/ && systemctl daemon-reload && systemctl enable --now opscore-controlplane`。
4. 证据管线出厂**全关**（每个面都需要操作者提供密钥/端点/路径才有意义；未启用的面一律
   `503`）。按单元内注释逐项开启需要的面（签名 `--export-sign-key`/`--export-trust-keys`、
   锚定 `--export-anchor-endpoint`、生命周期、验证存证、销毁问责、输入完整性……）。
   注意 `--export-interval` 与 `--export-dir` 必须成对（P34-I5 fail-fast）。

加固与读边同构：non-root、`NoNewPrivileges`、`ProtectSystem=strict`、
`ReadWritePaths=/var/lib/opscore`、强制 `EnvironmentFile`（缺文件或缺 secret 一律拒启）。

## 容器部署（distroless）

```bash
docker build -f deploy/Dockerfile -t opscore:latest .
docker run -p 8080:8080 -p 8081:8081 -v /var/lib/opscore:/var/lib/opscore opscore:latest
```

`EXPOSE 8080 8081` 分别对应 external/v1 与运营探针。**本镜像只打包读边**；控制面
容器化登记为后续可选扩展（distroless 无 `/bin/sh`，secrets 校验需替代方案）。

## 升级说明（Upgrade）

- **读边配置 schema 版本戳记**：`version` 字段当前为 `"1"`。未知键或被拒绝的 `version`
  会 **fail-closed**（进程拒绝启动，而非静默采用错误配置）。
- 升级二进制即可；配置向后兼容同一 `version`。升 `version` 属破坏性变更，须走 Scope ADR。
- **控制面无 JSON schema 戳记**：参数是 flag（+ 可选 KEY=VALUE 配置文件 + env）；
  证据 flag 只经 CLI 传入（字面默认注册，配置文件/env 喂不进去）。
- **升级后核对绑定**：控制面必须占 `:8082`，读边不得与之同址（见拓扑表）。
- 单节点优先；多节点共享状态（`Storage` seam）当前为 **inert 预留**，请勿依赖。
- 参考单元与二进制的参数一致性由机械守卫钉住（`cmd/opscore` 的 T386~T388，ADR-084）：
  单元引用不存在的 flag 会直接测红。
