import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'

interface FWStatus {
  os: string
  backend: string
  running: boolean
  manageable: boolean
  message: string
}
interface FWRule {
  name: string
  direction: string
  action: string
  protocol: string
  localPort: string
  remoteIP: string
}
interface AuditEntry {
  ts: string
  actor: string
  role: string
  credential: string
  action: string
  params: string
  result: string
  dryRun: boolean
}

type Tab = 'port' | 'ip' | 'rules'

export default function FirewallModule({ embedded = false }: { embedded?: boolean }) {
  const [status, setStatus] = useState<FWStatus | null>(null)
  const [rules, setRules] = useState<FWRule[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [tab, setTab] = useState<Tab>('port')
  const [msg, setMsg] = useState<string>('')

  // 表单
  const [port, setPort] = useState('')
  const [proto, setProto] = useState('tcp')
  const [portAct, setPortAct] = useState<'allow' | 'deny'>('allow')
  const [cidr, setCidr] = useState('')
  const [ipAct, setIpAct] = useState<'allow' | 'deny'>('allow')

  // 确认弹窗
  const [confirm, setConfirm] = useState<{ open: boolean; payload: any; command: string; lockoutRisk: boolean }>({
    open: false,
    payload: null,
    command: '',
    lockoutRisk: false,
  })
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    getJSON<FWStatus>('/api/core/firewall').then(setStatus).catch(() => {})
    getJSON<{ rules: FWRule[] }>('/api/core/firewall/rules').then((d) => setRules(d.rules || [])).catch(() => {})
    getJSON<{ entries: AuditEntry[] }>('/api/core/firewall/audit').then((d) => setAudit(d.entries || [])).catch(() => {})
  }, [])

  useEffect(() => {
    load()
    const t = setInterval(load, 8000)
    return () => clearInterval(t)
  }, [load])

  const openConfirm = (payload: any) => {
    // 先预览命令:带 dryRun:true,后端只回命令、绝不执行(即使目标主机可写)。
    postJSON<{ command: string; lockoutRisk: boolean }>('/api/core/firewall/action', { ...payload, dryRun: true, reason: 'preview' })
      .then((r) => {
        setConfirm({ open: true, payload, command: r.command || '', lockoutRisk: !!r.lockoutRisk })
        setReason('')
      })
      .catch(() => setMsg('预览失败'))
  }

  const doConfirm = async () => {
    if (!reason.trim()) {
      setMsg('✗ 必须填写操作原因(审计要求)')
      return
    }
    setBusy(true)
    const res = await postJSON<any>('/api/core/firewall/action', { ...confirm.payload, reason: reason.trim() })
    setBusy(false)
    setConfirm({ ...confirm, open: false })
    if (res.dryRun) {
      setMsg(`⚠ 只读演示:未真正执行。预览命令 → ${res.command}`)
    } else if (res.ok) {
      setMsg(`✓ 已执行:${res.command}`)
    } else {
      setMsg(`✗ ${res.error || res.message || '操作失败'}`)
    }
    load()
  }

  if (!status) return <div className="loading">加载防火墙状态中…</div>

  const body = (
    <>
      {!status.manageable && (
        <div className="banner banner-warn">
          ⚠ {status.message}
        </div>
      )}
      {msg && <div className={`banner ${msg.startsWith('✗') ? 'banner-err' : msg.startsWith('⚠') ? 'banner-warn' : 'banner-ok'}`}>{msg}</div>}

      <Card title="防火墙状态" subtitle="高危操作 · 需二次确认 + 审计">
        <div className="status-row">
          <span className={`badge ${status.running ? 'badge-ok' : 'badge-danger'}`}>
            防火墙 {status.running ? '已开启' : '已关闭'}
          </span>
          <span className="badge badge-info">后端 {status.backend}</span>
          <span className={`badge ${status.manageable ? 'badge-ok' : 'badge-off'}`}>
            {status.manageable ? '可写入' : '只读演示'}
          </span>
          <div className="btn-row" style={{ marginLeft: 'auto' }}>
            <button className="btn btn-sm" disabled={busy} onClick={() => openConfirm({ action: 'start' })}>启动</button>
            <button className="btn btn-sm" disabled={busy} onClick={() => openConfirm({ action: 'stop' })}>停止</button>
            <button className="btn btn-sm btn-accent" disabled={busy} onClick={() => openConfirm({ action: 'restart' })}>重启</button>
          </div>
        </div>
      </Card>

      <div className="tabs">
        <button className={`tab ${tab === 'port' ? 'tab-on' : ''}`} onClick={() => setTab('port')}>端口规则</button>
        <button className={`tab ${tab === 'ip' ? 'tab-on' : ''}`} onClick={() => setTab('ip')}>IP 黑白名单</button>
        <button className={`tab ${tab === 'rules' ? 'tab-on' : ''}`} onClick={() => setTab('rules')}>现有规则 ({rules.length})</button>
      </div>

      {tab === 'port' && (
        <Card title="端口开关" subtitle="允许 / 拒绝 某端口+协议">
          <div className="form-inline">
            <input className="input" placeholder="端口,如 3306" value={port}
              onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ''))} />
            <select className="input" value={proto} onChange={(e) => setProto(e.target.value)}>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
            <select className="input" value={portAct} onChange={(e) => setPortAct(e.target.value as any)}>
              <option value="allow">允许</option>
              <option value="deny">拒绝</option>
            </select>
            <button className="btn btn-accent" disabled={!port}
              onClick={() => openConfirm({ action: portAct + '-port', port, proto })}>
              {portAct === 'allow' ? '开放端口' : '关闭端口'}
            </button>
          </div>
        </Card>
      )}

      {tab === 'ip' && (
        <Card title="IP 黑白名单" subtitle="按来源 IP / CIDR 放行或封禁">
          <div className="form-inline">
            <input className="input" placeholder="CIDR,如 10.0.0.0/24" value={cidr}
              onChange={(e) => setCidr(e.target.value)} />
            <select className="input" value={ipAct} onChange={(e) => setIpAct(e.target.value as any)}>
              <option value="allow">白名单(放行)</option>
              <option value="deny">黑名单(封禁)</option>
            </select>
            <button className="btn btn-accent" disabled={!cidr}
              onClick={() => openConfirm({ action: ipAct + '-ip', cidr })}>
              {ipAct === 'allow' ? '加入白名单' : '加入黑名单'}
            </button>
          </div>
        </Card>
      )}

      {tab === 'rules' && (
        <Card title="现有规则" subtitle="真实读取自后端(netsh / ufw)">
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr><th>名称</th><th>方向</th><th>动作</th><th>协议</th><th>本地端口</th><th>远端 IP</th></tr>
              </thead>
              <tbody>
                {rules.map((r, i) => (
                  <tr key={i}>
                    <td className="mono small">{r.name}</td>
                    <td className="dim">{r.direction || '—'}</td>
                    <td>
                      <span className={`badge ${/allow/i.test(r.action) ? 'badge-ok' : 'badge-danger'}`}>
                        {r.action || '—'}
                      </span>
                    </td>
                    <td className="dim">{r.protocol || '—'}</td>
                    <td className="mono small">{r.localPort || '—'}</td>
                    <td className="mono small">{r.remoteIP || '—'}</td>
                  </tr>
                ))}
                {rules.length === 0 && <tr><td colSpan={6} className="dim">无规则或当前环境不支持读取</td></tr>}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <Card title="审计链" subtitle="ADR-002:actor · action · params · result · ts">
        <div className="table-wrap">
          <table className="data-table">
            <thead><tr><th>时间</th><th>动作</th><th>命令</th><th>结果</th></tr></thead>
            <tbody>
              {audit.slice().reverse().map((a, i) => (
                <tr key={i}>
                  <td className="mono small dim">{a.ts}</td>
                  <td><span className={`badge ${a.dryRun ? 'badge-off' : 'badge-ok'}`}>{a.action}{a.dryRun ? ' · 预览' : ''}</span></td>
                  <td className="mono small">{a.params}</td>
                  <td className="small">{a.result}</td>
                </tr>
              ))}
              {audit.length === 0 && <tr><td colSpan={4} className="dim">暂无审计记录</td></tr>}
            </tbody>
          </table>
        </div>
      </Card>

      {confirm.open && (
        <div className="modal-overlay" onClick={() => !busy && setConfirm({ ...confirm, open: false })}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h3>确认防火墙操作</h3>
            {confirm.lockoutRisk && (
              <div className="lockout-warn">
                🔴 高危:此操作可能把自己锁死(关闭 SSH / RDP / 当前端口,或封禁全网)。请确认你有其他接入方式!
              </div>
            )}
            <div className="field-label">将执行的命令</div>
            <pre className="code-block">{confirm.command || '(无法预览)'}</pre>
            <div className="field-label">操作原因(必填,记入审计)</div>
            <input className="input" value={reason} onChange={(e) => setReason(e.target.value)}
              placeholder="例如:为应用 A 开放 3306 端口" />
            <div className="modal-actions">
              <button className="btn" disabled={busy} onClick={() => setConfirm({ ...confirm, open: false })}>取消</button>
              <button className="btn btn-danger" disabled={busy} onClick={doConfirm}>
                {busy ? '执行中…' : '确认执行'}
              </button>
            </div>
            {!status.manageable && <div className="dim small">本环境为只读演示,确认后仅记录审计、不真正改网络。</div>}
          </div>
        </div>
      )}
    </>
  )

  if (embedded) return body

  return (
    <div className="module">
      <div className="module-head">
        <h2>防火墙</h2>
        <span className="pill">{status.backend} · {status.os}</span>
      </div>
      {body}
    </div>
  )
}
