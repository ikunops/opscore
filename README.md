# OpsCore 前端布局修复记录

> 问题：14 寸非 4:3 屏幕上，表格列挤压、换行、表头筛选器拥挤；CPU 拱桥遮挡数字
> 时间：2026-07-23

---

## 一、发现的问题

### 1. 防火墙与网络 - 监听端口表格
- 5 列（协议、本地地址、识别服务、真实进程/PID、端口提示）没有固定比例
- 窄屏下内容换行或相互挤压，布局混乱
- 期望比例：**10 : 25 : 25 : 25 : 15**

### 2. 服务发现 - 运行中服务/进程表格
- 状态筛选原本嵌在"状态"表头 `<th>` 里，和文本挤在同一行
- 改为独立筛选栏后用户反馈不符合整体格局
- 最终方案：**表头内嵌 `<select>` 下拉框**（状态/运行中/已退出/失败），默认显示"状态"不过滤

### 3. 网络/防火墙表格溢出行为
- 前列内容过长时，自动换行把后面列"推"下去
- 期望：**后列遮挡前列溢出**（推拉门效果）
- 保持固定列宽，超出部分用 `...` 省略

### 4. CPU 仪表盘拱桥遮挡 & 底部文字换行
- 拱桥宽度 16px 时，`fontSize: 28` 的数字左侧被拱桥裁切
- 底部 `stat-row` 使用 `display: flex; justify-content: space-between`，长 CPU 型号把 `2 核` 推到第二行
- 缩放 100% 时，flex item 亚像素舍入导致 CPU 卡片占比异常（70-80% 正常）

---

## 二、改动内容

### CSS (`web/src/index.css`)

#### 1. 全局表格溢出处理
```css
.data-table th, .data-table td {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.data-table td { max-width: 0; }
```

#### 2. 网络/防火墙表格列宽 (`.net-table`)
```css
.net-table th:nth-child(1) { width: 10%; }  /* 协议 */
.net-table th:nth-child(2) { width: 25%; }  /* 本地地址 */
.net-table th:nth-child(3) { width: 25%; }  /* 识别服务 */
.net-table th:nth-child(4) { width: 25%; }  /* 真实进程/PID */
.net-table th:nth-child(5) { width: 15%; }  /* 端口提示 */
```

#### 3. 服务发现表格列宽微调
```css
.data-table th:nth-child(1) { width: 16%; }
.data-table th:nth-child(2) { width: 10%; }
.data-table th:nth-child(3) { width: 22%; }
.data-table th:nth-child(4) { width: 6%; }
.data-table th:nth-child(5) { width: 6%; }
.data-table th:nth-child(6) { width: 18%; }
.data-table th:nth-child(7) { width: 14%; }
.data-table th:nth-child(8) { width: 8%; }
```

#### 4. 状态筛选 + 复制 Toast（新增）
```css
.sel-xs { font-size: 11px; padding: 2px 6px; margin-left: 6px; border-radius: 6px; }

.toast-copy { position: fixed; bottom: 28px; left: 50%; transform: translateX(-50%); background: var(--text); color: var(--bg); padding: 8px 22px; border-radius: 10px; font-size: 13px; z-index: 100; animation: toast-in 0.25s ease; }
@keyframes toast-in { from { opacity: 0; transform: translateX(-50%) translateY(12px); } to { opacity: 1; transform: translateX(-50%) translateY(0); } }
```

### 组件改动

#### 1. `ResourcesModule.tsx` - CPU 仪表盘 & 底部文字
仅调整 gauge 数字大小和底部布局，**不改变卡片结构**：
```tsx
// 仪表数字缩小，约束在拱桥内: 28px → 22px
detail: { valueAnimation: true, fontSize: 22, color: txt, offsetCenter: [0, 0], formatter: (value) => value.toFixed(2) + '%' },

// stat-row 改为自然文本流（消除 flex 亚像素舍入导致的缩放比例异常）
<div className="stat-row" style={{ display: 'block' }}>
  <span style={{ fontSize: 13, fontWeight: 700 }}>{snap.cpu.cores} 核</span>{' '}
  <span className="dim" style={{ fontSize: 11.5 }}>{snap.cpu.model || '—'}</span>
</div>
```

#### 2. `NetworkModule.tsx`
- 网络接口表格：增加 `className="data-table net-table"`
- 监听端口表格：增加 `className="data-table net-table"`

#### 3. `FirewallModule.tsx`
- 现有规则表格：增加 `className="data-table net-table"`

#### 4. `ServicesModule.tsx`
- **移除**：独立筛选栏 `<div className="filter-bar">`
- **表头状态列改为下拉**：
  ```tsx
  <th>
    <select className="sel sel-xs" value={statusFilter}
      onChange={(e) => setStatusFilter(e.target.value as ...)}>
      <option value="all">状态</option>
      <option value="running">运行中</option>
      <option value="exited">已退出</option>
      <option value="failed">失败</option>
    </select>
  </th>
  ```
- **新增**：日志命令双击复制功能 `copyCmd()` + `cursor:copy` + Toast 提示"已复制"

---

## 三、效果验证

| 检查项 | 结果 |
|--------|------|
| 前端构建成功 | ✅ `.net-table` 和 `.toast-copy` 已进入构建产物 |
| 服务重启成功 | ✅ systemd `opscore.service` active |
| 页面访问正常 | ✅ http://192.168.207.10:8081 |
| 表格列宽固定 | ✅ `table-layout: fixed` + 百分比宽度 |
| 溢出隐藏 | ✅ 后列遮挡前列，无换行 |
| 状态筛选 | ✅ 表头内嵌 `<select>` 下拉，默认"状态" |
| CPU 数字在拱桥内 | ✅ `fontSize: 22`，居中不裁切 |
| CPU 底部不换行 | ✅ `display: block` 自然文本流，型号自动截断 |
| 卡片布局一致 | ✅ 未改动 `grid-5` / Card 结构 |
| 缩放 100% / 80% 比例一致 | ✅ 消除 flex 亚像素舍入问题 |
| 日志命令双击复制 | ✅ `onDoubleClick` + Toast "已复制" |

---

## 四、修改文件清单

```
/opt/opscore/web/src/index.css
  - 全局表格溢出隐藏
  - .net-table 列宽比例 10:25:25:25:15
  - 服务发现表格列宽微调
  - .sel-xs 下拉框样式（已有）
  - .toast-copy / @keyframes toast-in（新增双‑击复制提示）
  - 注意：未改动 .grid / .card 等布局类

/opt/opscore/web/src/modules/ResourcesModule.tsx
  - cpuOption: detail.fontSize 28→22
  - cpu stat-row: display:flex → display:block (inline style)
  - 底部两 span 缩小字号 + 自然文本流

/opt/opscore/web/src/modules/NetworkModule.tsx
  - 接口表格 + 监听端口表格增加 net-table class

/opt/opscore/web/src/modules/FirewallModule.tsx
  - 现有规则表格增加 net-table class

/opt/opscore/web/src/modules/ServicesModule.tsx
  - 状态筛选从表头内嵌 select 改为独立 filter-bar → 最终改回表头 `<select>` 下拉
  - 日志命令双击复制（copyCmd + Toast）
```

---

## 五、未改动（保持原样）

- `.grid-5` / `.grid-2` 网格布局
- `.card` / `.card-head` / `.stat-row` 卡片结构
- EChart 容器高度（`height={240}` / `height={260}`）
- 内存波浪图、磁盘饼图等其他卡片
- gauge `progress` / `axisLine` width 保持 16px 不变
- `.filter-bar` / `.filter-btn` / `.filter-on` 已删除（不再使用）
