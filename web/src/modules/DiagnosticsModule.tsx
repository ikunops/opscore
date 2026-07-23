import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'

type Permission = 'root' | 'user'

type DiagnosticInfo = {
  permission: Permission
  features: { id: string; name: string; available: boolean }[]
}

type DiagResult = { output?: string; error?: string; permission: Permission }
type LoginAudit = { last: string; lastb: string; sshd_logs: string; permission: Permission }
type Updates = { updates: string; needs_restart: boolean; restart_detail: string; error?: string; permission: Permission }

const NET_TOOLS = [
  { id: 'ping', label: 'Ping', needsTarget: true },
  { id: 'traceroute', label: '路由追踪', needsTarget: true },
  { id: 'mtr', label: 'MTR 路径', needsTarget: true },
  { id: 'port', label: '端口检测', needsTarget: true },
  { id: 'dns', label: 'DNS 查询', needsTarget: true },
  { id: 'dns-detail', label: 'DNS 详情', needsTarget: true },
  { id: 'http', label: 'HTTP 探测', needsTarget: true },
  { id: 'route', label: '路由表', needsTarget: false },
  { id: 'arp', label: 'ARP 邻居', needsTarget: false },
]

export default function DiagnosticsModule() {
  const [info, setInfo] = useState<DiagnosticInfo | null>(null)
  const [tab, setTab] = useState('network')

  useEffect(() => {
    getJSON<DiagnosticInfo>('/api/core/diagnostics').then(setInfo).catch(() => {})
  }, [])

  if (!info) return <div className="loading">加载中…</div>

  const tabs = [
    { id: 'network', label: '网络诊断', avail: true },
    { id: 'login', label: '登录审计', avail: true },
    { id: 'updates', label: '系统更新', avail: info.features.find(f => f.id === 'updates')?.available ?? false },
  ]

  return (
    <div className="module">
      <div className="module-head">
        <h2>系统诊断</h2>
        <span className="pill">{info.permission === 'root' ? 'root 权限' : '受限模式'}</span>
      </div>

      <div className="tabs">
        {tabs.filter(t => t.avail).map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>

      {tab === 'network' && <NetworkSection />}
      {tab === 'login' && <LoginSection />}
      {tab === 'updates' && <UpdatesSection />}
    </div>
  )
}

function NetworkSection() {
  const [tool, setTool] = useState('ping')
  const [target, setTarget] = useState('')
  const [port, setPort] = useState(80)
  const [count, setCount] = useState(4)
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<DiagResult | null>(null)

  const cur = NET_TOOLS.find(t => t.id === tool) || NET_TOOLS[0]
  const isRoot = true // server is root, but permission label shown in result

  const run = async () => {
    if (cur.needsTarget && !target.trim()) return
    setLoading(true)
    setResult(null)
    try {
      const body: any = { tool }
      if (cur.needsTarget) body.target = target.trim()
      if (tool === 'ping') body.count = count
      if (tool === 'port') body.port = port
      const res = await postJSON<DiagResult>('/api/core/diagnostics/network', body)
      setResult(res)
    } catch { setResult({ error: '请求失败' } as DiagResult) }
    setLoading(false)
  }

  return (
    <Card title="网络诊断" subtitle="多工具诊断">
      <div className="tabs" style={{ marginBottom: 12 }}>
        {NET_TOOLS.map(t => (
          <button key={t.id} className={`tab ${tool === t.id ? 'tab-on' : ''}`} onClick={() => setTool(t.id)}>{t.label}</button>
        ))}
      </div>

      <div className="form-inline" style={{ marginBottom: 14 }}>
        {cur.needsTarget && tool !== 'http' && (
          <input className="input" placeholder="目标地址 (IP 或域名)" value={target} onChange={e => setTarget(e.target.value)} onKeyDown={e => e.key === 'Enter' && run()} />
        )}
        {tool === 'port' && (
          <>
            <span className="field-label" style={{ margin: '0 0 0 8px' }}>端口</span>
            <input className="input" type="number" min={1} max={65535} style={{ width: 90 }} value={port} onChange={e => setPort(Number(e.target.value))} onKeyDown={e => e.key === 'Enter' && run()} />
          </>
        )}
        {tool === 'http' && (
          <input className="input" placeholder="URL (如 https://example.com)" value={target} onChange={e => setTarget(e.target.value)} onKeyDown={e => e.key === 'Enter' && run()} />
        )}
        {tool === 'ping' && (
          <select className="sel" value={count} onChange={e => setCount(Number(e.target.value))}>
            <option value={2}>2 次</option>
            <option value={4}>4 次</option>
            <option value={6}>6 次</option>
            <option value={10}>10 次</option>
          </select>
        )}
        <button className="btn btn-accent" onClick={run} disabled={loading || (cur.needsTarget && !target.trim())}>{loading ? '诊断中…' : '执行'}</button>
      </div>

      {result && (
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontFamily: 'ui-monospace,monospace', fontSize: 12.5 }}>
          {result.error && <div className="banner banner-err">{result.error}</div>}
          {result.output}
        </div>
      )}
    </Card>
  )
}

function LoginSection() {
  const [data, setData] = useState<LoginAudit | null>(null)
  useEffect(() => { getJSON<LoginAudit>('/api/core/diagnostics/login-audit').then(setData).catch(() => {}) }, [])
  if (!data) return <div className="loading">加载中…</div>

  return (
    <>
      <Card title="最近登录" subtitle="last -F -n 30">
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.last || '（无记录）'}</div>
      </Card>
      {data.lastb && (
        <Card title="失败登录尝试" subtitle="lastb">
          <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.lastb}</div>
        </Card>
      )}
      {data.sshd_logs && (
        <Card title="SSHD 日志" subtitle="journalctl -u sshd (7天)">
          <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.sshd_logs}</div>
        </Card>
      )}
    </>
  )
}

function UpdatesSection() {
  const [data, setData] = useState<Updates | null>(null)
  useEffect(() => { getJSON<Updates>('/api/core/diagnostics/updates').then(setData).catch(() => {}) }, [])
  if (!data) return <div className="loading">加载中…</div>
  if (data.error) return <div className="banner banner-err">{data.error}</div>

  return (
    <>
      <Card title="安全更新" subtitle="dnf check-update --security">
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5 }}>{data.updates || '（无待安装安全更新）'}</div>
      </Card>
      <Card title="重启状态" subtitle="needs-restarting">
        <div className="banner" style={{ background: data.needs_restart ? '#ef44441f' : '#22c55e1f', borderColor: data.needs_restart ? '#ef44444d' : '#22c55e4d' }}>
          {data.needs_restart ? '⚠ 系统需要重启以应用更新' : '✓ 系统不需要重启'}
        </div>
        <div className="code-block" style={{ whiteSpace: 'pre-wrap', fontSize: 12.5, marginTop: 8 }}>{data.restart_detail}</div>
      </Card>
    </>
  )
}

function Card({ title, subtitle, children }: { title: string; subtitle?: string; children: React.ReactNode }) {
  return (
    <div className="card glass" style={{ marginBottom: 16 }}>
      <div className="card-head">
        <h3>{title}</h3>
        {subtitle && <span className="card-sub">{subtitle}</span>}
      </div>
      {children}
    </div>
  )
}
