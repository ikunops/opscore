import { useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { getJSON } from './api/client'
import TopBar from './components/TopBar'
import ResourcesModule from './modules/ResourcesModule'
import ServicesModule from './modules/ServicesModule'
import NetworkModule from './modules/NetworkModule'
import DiagnosticsModule from './modules/DiagnosticsModule'
import TasksModule from './modules/TasksModule'
import PluginsModule from './modules/PluginsModule'

interface Manifest {
  id: string
  name: string
  icon: string
  routePath: string
  group: string
  description: string
}

export default function App() {
  const [modules, setModules] = useState<Manifest[]>([])

  useEffect(() => {
    getJSON<Manifest[]>('/api/manifest').then(setModules).catch(() => setModules([]))
  }, [])

  const core = modules.filter((m) => m.group === 'core')
  const plugins = modules.filter((m) => m.group === 'plugin')

  return (
    <div className="layout">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-dot" />
          <div>
            <div className="brand-name">OpsCore</div>
            <div className="brand-sub">运维控制台 · Demo</div>
          </div>
        </div>

        <div className="nav-group-label">核心模块</div>
        <nav>
          {core.map((m) => (
            <NavLink key={m.id} to={m.routePath} className="nav-item">
              <span className="nav-icon">{icon(m.icon)}</span>
              <span className="nav-text">
                <span className="nav-title">{m.name}</span>
                <span className="nav-desc">{m.description}</span>
              </span>
            </NavLink>
          ))}
        </nav>

        {plugins.length > 0 && (
          <>
            <div className="nav-group-label">插件</div>
            <nav>
              {plugins.map((m) => (
                <NavLink key={m.id} to={m.routePath} className="nav-item nav-item-plugin">
                  <span className="nav-icon">{icon(m.icon)}</span>
                  <span className="nav-text">
                    <span className="nav-title">{m.name}</span>
                    <span className="nav-desc">{m.description}</span>
                  </span>
                </NavLink>
              ))}
            </nav>
          </>
        )}
        <div className="sidebar-foot">编译期内置 · 其余可插拔</div>
      </aside>

      <div className="main">
        <TopBar />
        <main className="content">
          <Routes>
            <Route path="/" element={<Navigate to="/resources" replace />} />
            <Route path="/resources" element={<ResourcesModule />} />
            <Route path="/services" element={<ServicesModule />} />
            <Route path="/network" element={<NetworkModule />} />
            <Route path="/diagnostics" element={<DiagnosticsModule />} />
            <Route path="/tasks" element={<TasksModule />} />
            <Route path="/plugins" element={<PluginsModule />} />
            <Route path="*" element={<Navigate to="/resources" replace />} />
          </Routes>
        </main>
      </div>
    </div>
  )
}

function icon(name: string) {
  const icons: Record<string, JSX.Element> = {
    cpu: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="4" y="4" width="16" height="16" rx="2" />
        <rect x="9" y="9" width="6" height="6" />
        <path d="M15 2v2M9 2v2M15 20v2M9 20v2M2 15h2M2 9h2M20 15h2M20 9h2" />
      </svg>
    ),
    server: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="2" y="2" width="20" height="8" rx="2" />
        <rect x="2" y="14" width="20" height="8" rx="2" />
        <circle cx="6" cy="6" r="1" fill="currentColor" />
        <circle cx="6" cy="18" r="1" fill="currentColor" />
      </svg>
    ),
    activity: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
      </svg>
    ),
    database: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <ellipse cx="12" cy="5" rx="9" ry="3" />
        <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" />
        <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
      </svg>
    ),
    network: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" />
        <path d="M9 12l2 2 4-4" />
      </svg>
    ),
    puzzle: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M19.439 7.85c-.049.322.059.648.289.878l1.568 1.568c.47.47.706 1.087.706 1.704s-.235 1.233-.706 1.704l-1.611 1.611a.98.98 0 0 1-.837.276c-.47-.07-.802-.48-.968-.925a2.501 2.501 0 1 0-3.214 3.214c.446.166.855.497.925.968a.979.979 0 0 1-.276.837l-1.61 1.61a2.404 2.404 0 0 1-1.705.707 2.402 2.402 0 0 1-1.704-.706l-1.568-1.568a1.026 1.026 0 0 0-.877-.29c-.493.074-.84.504-1.02.968a2.5 2.5 0 1 1-3.237-3.237c.464-.18.894-.527.967-1.02a1.026 1.026 0 0 0-.289-.877l-1.568-1.568A2.402 2.402 0 0 1 1.998 12c0-.617.236-1.234.706-1.704L4.315 8.685a.98.98 0 0 1 .837-.276c.47.07.802.48.968.925a2.501 2.501 0 1 0 3.214-3.214c-.446-.166-.855-.497-.925-.968a.979.979 0 0 1 .276-.837l1.61-1.61a2.404 2.404 0 0 1 1.705-.706c.617 0 1.234.236 1.704.706l1.568 1.568c.23.23.556.338.877.29.493-.074.84-.504 1.02-.968a2.5 2.5 0 1 1 3.237 3.237c-.464.18-.894.527-.967 1.02Z" />
      </svg>
    ),
  }
  return icons[name] || <span>•</span>
}
