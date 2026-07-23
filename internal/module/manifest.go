package module

// Manifest 是 Host Shell 用来动态生成侧栏与路由的模块契约。
// 核心模块在代码中直接注册;未来插件也只需实现一个 Manifest + 一组 API 即可被宿主发现。
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	RoutePath   string `json:"routePath"`
	Group       string `json:"group"` // "core" | "plugin"
	Description string `json:"description"`
}

// CoreModules 返回三个核心模块 + 插件中心占位。
// 这是当前 demo 的地基:资源 / 服务发现 / 防火墙和网络 内置,其余走插件。
// 防火墙已并入「防火墙和网络」模块,作为其内部的标签页之一。
func CoreModules() []Manifest {
	return []Manifest{
		{ID: "resources", Name: "系统资源", Icon: "cpu", RoutePath: "/resources", Group: "core", Description: "CPU / 内存 / 磁盘 / 网络 实时多图式可视化"},
		{ID: "services", Name: "服务发现", Icon: "server", RoutePath: "/services", Group: "core", Description: "运行服务启停 / 重启,查看单元文件与日志位置"},
		{ID: "network", Name: "防火墙和网络", Icon: "network", RoutePath: "/network", Group: "core", Description: "网络接口 / 监听端口 / 防火墙状态与规则(高危,需确认+审计)"},
		{ID: "diagnostics", Name: "系统诊断", Icon: "activity", RoutePath: "/diagnostics", Group: "core", Description: "网络诊断(Ping/Trace) / 登录审计 / 系统更新"},
		{ID: "tasks", Name: "任务与存储", Icon: "database", RoutePath: "/tasks", Group: "core", Description: "定时任务(Crontab) / 磁盘挂载与SMART健康"},
		{ID: "plugins", Name: "插件中心", Icon: "puzzle", RoutePath: "/plugins", Group: "plugin", Description: "可插拔模块(待接入)"},
	}
}
