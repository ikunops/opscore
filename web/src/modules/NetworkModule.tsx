import { useEffect, useState } from 'react'
import { getJSON } from '../api/client'
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

type NetTab = 'network' | 'firewall'

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
      </div>

      {tab === 'firewall' && <FirewallModule />}

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
