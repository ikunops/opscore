import { useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { getJSON } from './api/client'
import TopBar from './components/TopBar'
import ResourcesModule from './modules/ResourcesModule'
import ServicesModule from './modules/ServicesModule'
import NetworkModule from './modules/NetworkModule'
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
            <Route path="/plugins" element={<PluginsModule />} />
            <Route path="*" element={<Navigate to="/resources" replace />} />
          </Routes>
        </main>
      </div>
    </div>
  )
}

function icon(name: string) {
  const map: Record<string, string> = {
    cpu: '▦',
    server: '⬢',
    network: '◈',
    puzzle: '✦',
  }
  return map[name] || '•'
}
