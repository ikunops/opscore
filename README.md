# OpsCore · 核心模块 Demo

从最核心做起的最小可运行运维控制台。三个**内置核心模块** + 可插拔插件契约。

## 技术栈
- 后端:Go + gopsutil(v4) 采集系统指标,标准库 net/http,前端静态资源运行时读取目录(不再 embed)
8: - 前端:React 18 + Vite + TypeScript + ECharts 5(含 echarts-liquidfill 波浪图)
8: - 单二进制交付,无外部依赖

## 核心模块
1. **系统资源** — 内存(波浪 liquidfill)、CPU(仪表盘 + 实时折线)、磁盘(饼图 + **可点击下钻**:点挂载点展开顶层目录占用与百分比、显示总容量)、每核(柱状)、网络吞吐、系统负载
11: 2. **服务发现** — Linux 下 `systemctl` 列出运行单元,支持 启动/停止/重启 按钮 + 单元文件位置 + 日志查看命令;非 Linux 自动降级为进程列表。常见服务(MySQL/Nginx/Redis/PostgreSQL…)按**单元名/进程名**识别并标注图标+分类(名称即事实来源)
12: 3. **网络** — 网络接口、监听端口(LISTEN sockets)。端口身份以**真实进程(PID→进程名)**为准,端口常见服务表仅作提示;二者一致才标「已确认」,绝不"占 3306 就说是 MySQL"
13: 4. **防火墙** — 并入「防火墙和网络」模块(顶部 tab 之一)。状态卡(后端 ufw/firewalld/netsh 探测 + running + 可写性);端口开关(允许/拒绝 端口+协议)、IP 黑白名单、现有规则列表(真实读取)。**高危操作二次确认弹窗(展示将执行命令 + 锁定警告 + 原因输入)+ 审计链**。参考 ADR-002:写入仅在「受支持 + 特权」环境真正执行,否则只读预览(dryRun)

## 磁盘下钻 API
17: - `GET /api/core/disk/children?path=<挂载点>` → 返回该盘总容量 + 顶层子目录/文件大小(`size`/`isDir`),10s 超时管控遍历,`partial` 标识是否未扫全

## 构建与运行

### 方式一:裸金属部署(推荐)
```bash
# 1. 克隆
git clone <repo-url>
cd opscore

# 2. 构建前端 + 编译二进制(前端改动只需重跑 `make web`,后端无需重编译)
make build

# 3. 直接运行(默认监听 :8088)
./opscore

# 或显式指定: ./opscore -addr 127.0.0.1:8088  (配合 nginx 反代)
# 可选: -dist ./web/dist (自定义前端资源目录)
```

### 方式二:Docker 部署
```bash
docker compose up -d --build
```

### 方式三:Windows
```bash
build.bat
opscore.exe
```

### 开机自启(可选,裸金属)
```bash
# 1. 二进制与前端产物放到指定位置
sudo mkdir -p /opt/opscore/web
sudo cp opscore /opt/opscore/opscore
sudo cp -r web/dist /opt/opscore/web/dist

# 2. 安装 systemd 单元(运行用户不绑定,默认以启动者身份运行)
sudo cp deploy/opscore.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now opscore
```
> 运行用户**不绑定特定账号**:service 不写 `User=/Group=`,默认以启动者(通常 root)运行。
> 想用普通用户跑: `systemctl edit opscore` 加 `User=/Group=` 即可;K8s/CICD 容器内非 root 用户也能直接跑,无需预建账号。

> **端口对接说明**
> - 默认监听 `:8088` (避开 Prometheus 9090 / nginx 8080/8081)
> - 如需 nginx 反代:OpsCore 起 `-addr 127.0.0.1:8088`, nginx 反代 `http://127.0.0.1:8088` (参考 `deploy/nginx-opscore.conf`)
> - 反代配置因环境而异,本仓库只提供示例配置

> **前后端分离说明**
> - 前端构建产物 `web/dist` 运行时从目录读取，**不再 embed 进二进制**
> - 前端改动只需 `cd web && npm run build`,后端二进制**无需重编译**
> - 二进制可直接运行,前提是同目录下有 `web/dist` 目录(或用 `-dist` 指定)

> 注意:服务启停需要 Linux + systemd 环境且有相应权限;在 Windows/macOS 上服务模块会降级为进程列表展示。
> 防火墙:本机 Windows 上 Windows Defender 防火墙服务未运行,且为安全起见演示为**只读**(写入仅预览命令、记审计不执行);真实开关/端口/黑白名单需在 **Linux + 特权** 主机(ufw/firewalld)上生效。拒绝 SSH(22)/RDP(3389)/当前端口、封禁全网会触发红色锁定警告。
> 系统资源在 Windows 上内存"已用%"含系统缓存,gopsutil 行为如此;在 Linux 上数值准确。
> Windows 挂载点为盘符(如 `C:`),下钻端点会自动归一化为 `C:\` 读取盘根。
