import Card from '../components/Card'

export default function PluginsModule() {
  return (
    <div className="module">
      <div className="module-head">
        <h2>插件中心</h2>
        <span className="pill">可插拔 · 编译期注册</span>
      </div>

      <Card title="模块契约(ModuleManifest)">
        <p className="dim">
          未来所有扩展模块(如 AlertFusion 告警、容器管理、数据库)都通过一个 Manifest 注册:
        </p>
        <pre className="code-block">{`type Manifest struct {
    ID          string  // 唯一标识
    Name        string  // 侧栏显示名
    Icon        string  // 图标
    RoutePath   string  // 前端路由
    Group       string  // "core" | "plugin"
    Description string  // 描述
}`}</pre>
        <p className="dim">
          Host Shell 启动时扫描 Manifest 动态生成侧栏与路由;模块只需提供一组 <code>/api/...</code> 与对应前端页面即可被宿主发现。
          当前已内置三核心:系统资源 / 服务发现 / 网络。其余为可插拔插件。
        </p>
      </Card>
    </div>
  )
}
