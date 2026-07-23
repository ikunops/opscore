import { useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'
import Card from '../components/Card'
import FirewallModule from './FirewallModule'

interface NetData {
  interfaces: { name: string; mtu: number; flags: string[]; addrs: string[] }[]
  ifaceError?: string
  listenError?: string
  listeners: {
    protocol: string
    local: string
    port: number
    pid: number
    process: string
    service: string
    category: string
    icon: string
    knownAs: string
    verified: boolean
  }[]
}

type NetTab = 'network' | 'firewall' | 'config'

export default function NetworkModule() {
  const [data, setData] = useState<NetData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<NetTab>('firewall')

  useEffect(() => {
    if (tab === 'firewall') return // 防火墙页自己拉数据
    const load = () =>
      getJSON<NetData>('/api/core/network')
        .then((d) => {
          setData(d)
          setError(null)
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : String(err))
        })
    load()
    const t = setInterval(load, 5000)
    return () => clearInterval(t)
  }, [tab])

  return (
    <div className="module">
      <div className="module-head">
        <h2>防火墙和网络</h2>
        <span className="pill">网络 · 防火墙</span>
      </div>

      <div className="tabs">
        <button className={`tab ${tab === 'network' ? 'tab-on' : ''}`} onClick={() => setTab('network')}>网络</button>
        <button className={`tab ${tab === 'firewall' ? 'tab-on' : ''}`} onClick={() => setTab('firewall')}>防火墙</button>
        <button className={`tab ${tab === 'config' ? 'tab-on' : ''}`} onClick={() => setTab('config')}>网络配置</button>
      </div>

      {tab === 'firewall' && <FirewallModule />}

      {tab === 'config' && <NetConfigSection />}

      {tab === 'network' &&
        (error ? (
          <div className="banner banner-error">请求失败: {error}</div>
        ) : data ? (
          <div className="grid grid-2">
            {(data.ifaceError || data.listenError) && (
              <div className="banner banner-warn small" style={{ gridColumn: '1 / -1' }}>
                后端采集出现错误(已尽量返回其余数据):
                {data.ifaceError && <div>· 网络接口: {data.ifaceError}</div>}
                {data.listenError && <div>· 监听端口: {data.listenError}</div>}
              </div>
            )}
            <Card title="网络接口" subtitle="interface / MTU / 地址">
              <div className="table-wrap">
                <table className="data-table net-table">
                  <thead>
                    <tr><th>接口</th><th>MTU</th><th>地址</th></tr>
                  </thead>
                  <tbody>
                    {data.interfaces.map((i) => (
                      <tr key={i.name}>
                        <td className="mono">{i.name}</td>
                        <td className="dim">{i.mtu}</td>
                        <td className="mono small">{i.addrs.join(' , ') || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </Card>

            <Card title="监听端口" subtitle="身份以真实进程为准 · 非端口假设">
              <div className="banner banner-info small">
                端口常见服务仅作提示；真实身份 = 占用该端口的进程(PID→进程名)。二者一致才标「已确认」。
              </div>
              <div className="table-wrap">
                <table className="data-table net-table">
                  <thead>
                    <tr><th>协议</th><th>本地地址</th><th>识别服务</th><th>真实进程 / PID</th><th>端口提示</th></tr>
                  </thead>
                  <tbody>
                    {data.listeners.slice(0, 40).map((l, idx) => (
                      <tr key={idx}>
                        <td><span className="badge badge-ok">{l.protocol}</span></td>
                        <td className="mono">{l.local}</td>
                        <td>
                          {l.service
                            ? <span className="svc-badge">{l.icon} {l.service}{l.verified && <span className="verified" title="端口提示与进程身份一致">✓</span>}</span>
                            : <span className="dim">未知</span>}
                        </td>
                        <td className="mono small">
                          {l.process || '—'}
                          <span className="dim"> · PID {l.pid || '?'}</span>
                        </td>
                        <td className="dim small">
                          {l.knownAs
                            ? (l.verified ? l.knownAs : `${l.knownAs}(进程不符)`)
                            : '—'}
                        </td>
                      </tr>
                    ))}
                    {data.listeners.length === 0 && (
                      <tr><td colSpan={5} className="dim">无监听端口或权限不足</td></tr>
                    )}
                  </tbody>
                </table>
              </div>
            </Card>
          </div>
        ) : (
          <div className="loading">加载网络信息中…</div>
        ))}
    </div>
  )
}

type NetConfig = {
  interfaces: string
  routes: string
  dns: string
  nm: string
  permission: 'root' | 'user'
}

type ConfigResult = {
  ok?: boolean
  error?: string
  note?: string
  output?: string
  permission: 'root' | 'user'
}

function NetConfigSection() {
  const [data, setData] = useState<NetConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionMsg, setActionMsg] = useState('')

  const load = async () => {
    setLoading(true)
    try { const d = await getJSON<NetConfig>('/api/core/network/config'); setData(d) } catch { /* ignore */ }
    setLoading(false)
  }

  useEffect(() => { load() }, [])

  const runAction = async (action: string, device: string, extra?: Record<string, any>) => {
    try {
      const body: any = { action, device, ...extra }
      const res = await postJSON<ConfigResult>('/api/core/network/config', body)
      if (res.ok) {
        setActionMsg(`✓ ${action} 成功${res.note ? ' · ' + res.note : ''}`)
        load()
      } else setActionMsg(`✗ ${res.error || '操作失败'}`)
    } catch { setActionMsg('✗ 请求失败') }
    setTimeout(() => setActionMsg(''), 5000)
  }

  const [editIP, setEditIP] = useState('')
  const [editMask, setEditMask] = useState(24)
  const [editDev, setEditDev] = useState('')
  const [editDNS, setEditDNS] = useState('')

  if (loading && !data) return <div className="loading">加载网络配置中…</div>
  if (!data) return <div className="banner banner-err">加载失败</div>

  const isRoot = data.permission === 'root'

  return (
    <>
      {actionMsg && <div className={`banner ${actionMsg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{actionMsg}</div>}

      <div className="grid grid-2">
        <Card title="网络接口" subtitle="ip addr show">
          <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap', maxHeight: 340, overflowY: 'auto' }}>{data.interfaces}</div>
        </Card>

        <Card title="路由表" subtitle="ip route show">
          <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap', maxHeight: 340, overflowY: 'auto' }}>{data.routes}</div>
        </Card>

        <Card title="DNS 配置" subtitle={data.permission === 'root' ? '' : '只读'}>
          <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap', maxHeight: 240, overflowY: 'auto' }}>{data.dns}</div>
        </Card>

        <Card title="NetworkManager" subtitle="nmcli dev status">
          <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap', maxHeight: 240, overflowY: 'auto' }}>{data.nm}</div>
        </Card>
      </div>

      {isRoot && (
        <Card title="操作" subtitle="root 权限">
          <div className="grid" style={{ gridTemplateColumns: '1fr 1fr', gap: 16 }}>
            <div>
              <div className="field-label">网卡重启</div>
              <div className="form-inline">
                <input className="input" placeholder="设备名 (如 ens160)" value={editDev} onChange={e => setEditDev(e.target.value)} />
                <button className="btn btn-danger" disabled={!editDev} onClick={() => { if (confirm('确认重启 ' + editDev + '？连接将断开')) runAction('restart', editDev) }}>重启</button>
                <button className="btn btn-accent" disabled={!editDev} onClick={() => runAction('dhcp', editDev)}>DHCP 续租</button>
              </div>
            </div>

            <div>
              <div className="field-label">修改 IP</div>
              <div className="form-inline">
                <input className="input" placeholder="设备" value={editDev} onChange={e => setEditDev(e.target.value)} style={{ width: 100 }} />
                <input className="input" placeholder="IP" value={editIP} onChange={e => setEditIP(e.target.value)} />
                <span className="dim">/</span>
                <input className="input" type="number" min={1} max={32} value={editMask} onChange={e => setEditMask(Number(e.target.value))} style={{ width: 60 }} />
                <button className="btn btn-accent" disabled={!editDev || !editIP} onClick={() => runAction('set-ip', editDev, { ip: editIP, mask: editMask })}>设置</button>
              </div>
            </div>

            <div style={{ gridColumn: '1 / -1' }}>
              <div className="field-label">修改 DNS</div>
              <div className="form-inline">
                <input className="input" placeholder="设备" value={editDev} onChange={e => setEditDev(e.target.value)} style={{ width: 100 }} />
                <input className="input" placeholder="DNS 服务器 (空格分隔多个)" value={editDNS} onChange={e => setEditDNS(e.target.value)} />
                <button className="btn btn-accent" disabled={!editDev || !editDNS} onClick={() => runAction('set-dns', editDev, { dns: editDNS })}>设置</button>
              </div>
            </div>
          </div>
        </Card>
      )}
    </>
  )
}
