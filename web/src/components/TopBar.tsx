import { useTheme } from '../theme'

export default function TopBar() {
  const { theme, toggle } = useTheme()
  return (
    <header className="topbar">
      <div className="topbar-title">单一控制台 · 按钮替命令</div>
      <button className="theme-toggle" onClick={toggle} aria-label="切换主题">
        {theme === 'light' ? '🌙 暗色' : '☀ 亮色'}
      </button>
    </header>
  )
}
