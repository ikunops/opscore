import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'

interface ServiceInfo {
  id: string
  name: string
  status: string
  subStatus: string
  description: string
  unitFile: string
  logHint: string
  isProcess: boolean
  pid?: number
  cpuPercent?: number
  memPercent?: number
  recognized?: string
  category?: string
  icon?: string
}

export default function ServicesModule() {
  const [data, setData] = useState<{ os: string; managed: boolean; services: ServiceInfo[]; note?: string } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [msg, setMsg] = useState<string>('')

  const load = useCallback(() => {
    getJSON('/api/core/services').then(setData).catch(() => setMsg('加载失败'))
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [load])

  const act = async (id: string, action: string) => {
    setBusy(`${id}:${action}`)
    setMsg('')
    const res = await postJSON('/api/core/services/action', { id, action })
    setBusy(null)
    if (res.ok) {
      setMsg(`✓ ${action} ${id} 成功`)
      load()
    } else {
      setMsg(`✗ ${res.error || '操作失败'}`)
    }
  }

  if (!data) return <div className="loading">加载服务中…</div>

  const activeCount = data.services.filter((s) => /active|running/i.test(s.status)).length

  return (
    <div className="module">
      <div className="module-head">
        <h2>服务发现</h2>
        <span className="pill">{activeCount}/{data.services.length} 活跃 · {data.os}</span>
      </div>

      {!data.managed && (
        <div className="banner">⚠ {data.note}（按钮为可视化占位，真实启停需在 Linux/systemd 环境运行）</div>
      )}
      {msg && <div className={`banner ${msg.startsWith('✗') ? 'banner-err' : 'banner-ok'}`}>{msg}</div>}

      <Card title="运行中的服务 / 进程" subtitle="启停 / 重启 · 位置 / 日志">
        <div className="table-wrap">
          <table className="data-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>状态</th>
                <th>说明</th>
                <th>单元文件 / PID</th>
                <th>日志</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {data.services.map((s) => (
                <tr key={s.id}>
                  <td className="mono">
                    {s.icon && <span className="svc-icon">{s.icon}</span>}
                    {s.recognized
                      ? <><b>{s.recognized}</b><div className="dim small">{s.name}</div></>
                      : s.name}
                  </td>
                  <td>
                    <span className={`badge ${/active|running/i.test(s.status) ? 'badge-ok' : 'badge-off'}`}>
                      {s.status}
                    </span>
                    {s.subStatus && <span className="dim"> · {s.subStatus}</span>}
                  </td>
                  <td className="dim">
                    {s.recognized && s.category && <span className="tag">{s.category}</span>}
                    <span>{s.description}</span>
                  </td>
                  <td className="mono small">{s.isProcess ? `PID ${s.pid}` : s.unitFile || '—'}</td>
                  <td className="mono small dim">{s.logHint || '—'}</td>
                  <td>
                    <div className="btn-row">
                      <button className="btn btn-sm" disabled={!data.managed || busy !== null} onClick={() => act(s.id, 'start')}>启动</button>
                      <button className="btn btn-sm" disabled={!data.managed || busy !== null} onClick={() => act(s.id, 'stop')}>停止</button>
                      <button className="btn btn-sm btn-accent" disabled={!data.managed || busy !== null} onClick={() => act(s.id, 'restart')}>重启</button>
                      {busy === `${s.id}:start` || busy === `${s.id}:stop` || busy === `${s.id}:restart` ? <span className="spinner" /> : null}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  )
}
