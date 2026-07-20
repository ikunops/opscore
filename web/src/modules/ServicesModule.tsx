import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
  logSource?: string
  logPaths?: string[]
  logCommand?: string
}

interface LogLine {
  line: string
  num: number
}

interface LogResponse {
  source: string
  target: string
  lines: LogLine[]
  total: number
  warnings?: string[]
}

export default function ServicesModule() {
  const [data, setData] = useState<{ os: string; managed: boolean; services: ServiceInfo[]; note?: string } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [msg, setMsg] = useState<string>('')
  const [logTarget, setLogTarget] = useState<ServiceInfo | null>(null)
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'running' | 'exited' | 'failed'>('all')
  const [sortKey, setSortKey] = useState<'cpu' | 'mem' | null>(null)
  const [sortDir, setSortDir] = useState<'desc' | 'asc'>('desc')

  const toggleSort = (key: 'cpu' | 'mem') => {
    if (sortKey !== key) {
      setSortKey(key)
      setSortDir('desc')
    } else if (sortDir === 'desc') {
      setSortDir('asc')
    } else {
      setSortKey(null)
      setSortDir('desc')
    }
  }

  const sortIndicator = (key: 'cpu' | 'mem') => {
    if (sortKey !== key) return ''
    return sortDir === 'desc' ? ' ▼' : ' ▲'
  }

  const visible = useMemo(() => {
    if (!data) return []
    const q = search.trim().toLowerCase()
    const list = data.services.filter((s) => {
      if (q) {
        const hay = `${s.name} ${s.recognized || ''} ${s.category || ''}`.toLowerCase()
        if (!hay.includes(q)) return false
      }
      switch (statusFilter) {
        case 'running': return s.subStatus === 'running'
        case 'exited': return s.subStatus === 'exited'
        case 'failed': return s.status === 'failed'
        default: return true
      }
    })
    // 点击 CPU/内存 列头时按数值排序;否则按名称稳定排序(避免轮询时行跳动)
    if (sortKey) {
      const dir = sortDir === 'desc' ? -1 : 1
      list.sort((a, b) => {
        const av = sortKey === 'cpu' ? (a.cpuPercent || 0) : (a.memPercent || 0)
        const bv = sortKey === 'cpu' ? (b.cpuPercent || 0) : (b.memPercent || 0)
        return (av - bv) * dir
      })
    } else {
      list.sort((a, b) => a.name.localeCompare(b.name))
    }
    return list
  }, [data, search, statusFilter, sortKey, sortDir])

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
        <h2>服务发现 <span className="pill pill-sub">{activeCount}/{data.services.length} 活跃 · {data.os}</span></h2>
        <div className="head-tools">
          <div className="search-box">
            <span className="search-ico">🔍</span>
            <input className="ipt search-ipt" placeholder="搜索服务，如 nginx" value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
        </div>
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
                <th>
                  状态
                  <select className="sel sel-xs" value={statusFilter} onChange={(e) => setStatusFilter(e.target.value as 'all' | 'running' | 'exited' | 'failed')}>
                    <option value="all">全部</option>
                    <option value="running">运行中</option>
                    <option value="exited">已退出</option>
                    <option value="failed">失败</option>
                  </select>
                </th>
                <th>说明</th>
                <th className="sortable" onClick={() => toggleSort('cpu')}>CPU %{sortIndicator('cpu')}</th>
                <th className="sortable" onClick={() => toggleSort('mem')}>内存 %{sortIndicator('mem')}</th>
                <th>单元文件 / PID</th>
                <th>日志命令</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((s) => (
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
                  <td className="mono small">{fmtPct(s.cpuPercent)}</td>
                  <td className="mono small">{fmtPct(s.memPercent)}</td>
                  <td className="mono small">{s.isProcess ? `PID ${s.pid}` : (s.unitFile || (s.pid ? `PID ${s.pid}` : '—'))}</td>
                  <td className="mono small dim">
                    {s.logCommand
                      ? <><button className="btn btn-sm btn-log" onClick={() => setLogTarget(s)}>查看</button> <span style={{ marginLeft: 6 }}>{s.logCommand}</span></>
                      : '—'}
                  </td>
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

      {logTarget && (
        <LogModal service={logTarget} onClose={() => setLogTarget(null)} />
      )}
    </div>
  )
}

function LogModal({ service, onClose }: { service: ServiceInfo; onClose: () => void }) {
  const [logLines, setLogLines] = useState<LogLine[]>([])
  const [warnings, setWarnings] = useState<string[]>([])
  const [loadingLog, setLoadingLog] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [tab, setTab] = useState<'journalctl' | 'file'>('journalctl')
  const [filePath, setFilePath] = useState<string>('')
  const [filter, setFilter] = useState('')
  const refreshTimer = useRef<number | null>(null)
  const logBodyRef = useRef<HTMLDivElement | null>(null)
  const followBottom = useRef(true)

  const hasJournal = service.logSource === 'journalctl' || service.logSource === 'both'
  const hasFile = (service.logPaths && service.logPaths.length > 0) || service.logSource === 'file'

  const fetchLog = useCallback(async () => {
    setLoadingLog(true)
    let url = ''
    if (tab === 'journalctl') {
      url = `/api/core/services/logs?source=journalctl&target=${encodeURIComponent(service.name)}&lines=100`
    } else {
      const p = filePath || (service.logPaths && service.logPaths[0]) || ''
      if (!p) {
        setLoadingLog(false)
        return
      }
      url = `/api/core/services/logs?source=file&path=${encodeURIComponent(p)}&lines=100`
    }
    try {
      const res = (await getJSON(url)) as LogResponse
      setLogLines(res.lines || [])
      setWarnings(res.warnings || [])
    } catch {
      setWarnings(['日志获取失败'])
    }
    setLoadingLog(false)
  }, [tab, filePath, service.name, service.logPaths])

  useEffect(() => {
    fetchLog()
    if (autoRefresh) {
      refreshTimer.current = window.setInterval(() => fetchLog(), 5000)
    }
    return () => {
      if (refreshTimer.current) window.clearInterval(refreshTimer.current)
    }
  }, [autoRefresh, fetchLog])

  // 新日志到达后:若用户停留在底部(或首次打开),自动滚到最新;
  // 若用户已向上翻看历史,则不强制打断。
  useEffect(() => {
    const el = logBodyRef.current
    if (el && followBottom.current) {
      el.scrollTop = el.scrollHeight
    }
  }, [logLines])

  const onLogScroll = () => {
    const el = logBodyRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    followBottom.current = atBottom
  }

  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal log-modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div className="modal-title">
            {service.icon && <span className="svc-icon">{service.icon}</span>}
            <b>{service.recognized || service.name}</b>
            <span className="dim small"> · {service.name}</span>
          </div>
          <button className="btn btn-sm btn-ghost" onClick={onClose}>✕ 关闭</button>
        </div>

        <div className="log-panel-head">
          {hasJournal && hasFile ? (
            <div className="tab-row">
              <button className={`tab ${tab === 'journalctl' ? 'tab-active' : ''}`} onClick={() => setTab('journalctl')}>📋 journalctl</button>
              <button className={`tab ${tab === 'file' ? 'tab-active' : ''}`} onClick={() => setTab('file')}>📁 文件日志</button>
            </div>
          ) : hasJournal ? (
            <span className="log-srclabel">📋 journalctl 日志</span>
          ) : hasFile ? (
            <span className="log-srclabel">📁 文件日志</span>
          ) : (
            <span className="log-srclabel">⚠ 无可识别日志来源</span>
          )}
          <div className="log-tools">
            {tab === 'file' && service.logPaths && service.logPaths.length > 1 && (
              <select className="sel sel-sm" value={filePath || service.logPaths[0]} onChange={(e) => setFilePath(e.target.value)}>
                {service.logPaths.map((p) => <option key={p} value={p}>{p}</option>)}
              </select>
            )}
            <input className="ipt ipt-sm" placeholder="过滤关键词…" value={filter} onChange={(e) => setFilter(e.target.value)} />
            <label className="chk"><input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} /> 自动刷新</label>
            <button className="btn btn-sm btn-ghost" onClick={() => fetchLog()}>刷新</button>
          </div>
        </div>

        {warnings.length > 0 && (
          <div className="log-warn">{warnings.map((w, i) => <div key={i}>⚠ {w}</div>)}</div>
        )}

        <div className="log-body" ref={logBodyRef} onScroll={onLogScroll}>
          {loadingLog ? (
            <div className="log-loading">加载中…</div>
          ) : logLines.length === 0 ? (
            <div className="log-empty">（无日志内容）</div>
          ) : (
            logLines
              .filter((l) => !filter || l.line.toLowerCase().includes(filter.toLowerCase()))
              .map((l) => (
                <div key={l.num} className={`log-line ${lineLevel(l.line)}`}>
                  <span className="log-num">{l.num}</span>
                  <span className="log-text">{l.line}</span>
                </div>
              ))
          )}
        </div>
      </div>
    </div>
  )
}

function lineLevel(line: string): string {
  const l = line.toLowerCase()
  if (l.includes('error') || l.includes('err') || l.includes('fail') || l.includes('fatal')) return 'lvl-err'
  if (l.includes('warn') || l.includes('warning')) return 'lvl-warn'
  if (l.includes('info') || l.includes('notice')) return 'lvl-info'
  return 'lvl-default'
}

function fmtPct(v?: number): string {
  if (v === undefined || v === null || v === 0) return '—'
  return v.toFixed(1) + '%'
}
