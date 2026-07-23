import { useCallback, useEffect, useState } from 'react'
import { getJSON, postJSON } from '../api/client'

type Permission = 'root' | 'user'
type Crontab = { content: string; error?: string; permission: Permission }
type SaveResult = { ok?: boolean; error?: string; permission: Permission }
type Disks = { lsblk: string; mounts: string; df: string; permission: Permission }
type DiskActionResult = { ok?: boolean; error?: string; output?: string; permission: Permission }

export default function TasksModule() {
  const [tab, setTab] = useState('crontab')
  const [perm, setPerm] = useState<Permission>('user')

  useEffect(() => {
    getJSON<{ permission: Permission }>('/api/core/tasks/disks').then(d => setPerm(d.permission)).catch(() => {})
  }, [])

  const tabs = [
    { id: 'crontab', label: '定时任务' },
    { id: 'disks', label: '磁盘挂载' },
    { id: 'smart', label: 'SMART 健康' },
  ]

  return (
    <div className="module">
      <div className="module-head">
        <h2>任务与存储</h2>
        <span className="pill">{perm === 'root' ? 'root 权限' : '受限模式'}</span>
      </div>

      <div className="tabs">
        {tabs.map(t => (
          <button key={t.id} className={`tab ${tab === t.id ? 'tab-on' : ''}`} onClick={() => setTab(t.id)}>{t.label}</button>
        ))}
      </div>

      {tab === 'crontab' && <CrontabSection />}
      {tab === 'disks' && <DisksSection />}
      {tab === 'smart' && <SmartSection />}
    </div>
  )
}

function CrontabSection() {
  const [user, setUser] = useState('root')
  const [data, setData] = useState<Crontab | null>(null)
  const [edit, setEdit] = useState('')
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [msg, setMsg] = useState('')

  const load = useCallback(() => {
    getJSON<Crontab>(`/api/core/tasks/crontab?user=${user}`).then(d => { setData(d); setEdit(d.content || '') }).catch(() => {})
  }, [user])

  useEffect(() => { load() }, [load])

  const save = async () => {
    setSaving(true)
    try {
      const res = await postJSON<SaveResult>('/api/core/tasks/crontab', { user, content: edit })
      if (res.ok) { setMsg('✓ 已保存'); setEditing(false); load() }
      else setMsg(`✗ ${res.error || '保存失败'}`)
    } catch { setMsg('✗ 保存失败') }
    setSaving(false)
    setTimeout(() => setMsg(''), 3000)
  }

  if (!data) return <div className="loading">加载中…</div>

  return (
    <Card title="定时任务" subtitle={data.permission === 'root' ? 'root 权限' : '只读（非 root）'}>
      {data.error && <div className="banner banner-err">{data.error}</div>}
      <div className="form-inline" style={{ marginBottom: 12 }}>
        <span className="field-label" style={{ margin: 0 }}>用户</span>
        <select className="sel" value={user} onChange={e => { setUser(e.target.value); setEditing(false) }}>
          <option value="root">root</option>
          <option value={typeof window !== 'undefined' ? (window as any).__user || '' : ''} disabled>——</option>
        </select>
        <div style={{ flex: 1 }} />
        {!editing ? (
          <button className="btn btn-accent" disabled={data.permission !== 'root'} onClick={() => setEditing(true)}>编辑</button>
        ) : (
          <>
            <button className="btn" onClick={() => { setEditing(false); setEdit(data.content || '') }}>取消</button>
            <button className="btn btn-accent" disabled={saving} onClick={save}>{saving ? '保存中…' : '保存'}</button>
          </>
        )}
      </div>
      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}
      <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>
        {editing ? (
          <textarea className="input" style={{ width: '100%', minHeight: 240, fontFamily: 'ui-monospace,monospace', fontSize: 12.5, resize: 'vertical', whiteSpace: 'pre' }}
            value={edit} onChange={e => setEdit(e.target.value)} />
        ) : data.content || '（无定时任务）'}
      </div>
    </Card>
  )
}

function DisksSection() {
  const [data, setData] = useState<Disks | null>(null)
  const [mountDev, setMountDev] = useState('')
  const [mountPoint, setMountPoint] = useState('')
  const [mountFstype, setMountFstype] = useState('')
  const [mountMsg, setMountMsg] = useState('')

  const load = useCallback(() => {
    getJSON<Disks>('/api/core/tasks/disks').then(setData).catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const mountAction = async (action: 'mount' | 'umount', device: string, mountpoint?: string) => {
    try {
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', { action, device, mountpoint })
      if (res.ok) { setMountMsg(`✓ ${action} 成功`); load() }
      else setMountMsg(`✗ ${res.error || '操作失败'}`)
    } catch { setMountMsg('✗ 请求失败') }
    setTimeout(() => setMountMsg(''), 3000)
  }

  if (!data) return <div className="loading">加载中…</div>

  const isRoot = data.permission === 'root'

  return (
    <>
      {mountMsg && <div className={`banner ${mountMsg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{mountMsg}</div>}

      <Card title="块设备" subtitle="lsblk">
        <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>{data.lsblk}</div>
      </Card>

      <Card title="挂载点" subtitle="mount">
        <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>{data.mounts}</div>
      </Card>

      <Card title="磁盘使用" subtitle="df -h">
        <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>{data.df}</div>
      </Card>

      {isRoot && (
        <Card title="挂载操作" subtitle="root">
          <div className="form-inline">
            <input className="input" placeholder="设备 (如 /dev/sdb1)" value={mountDev} onChange={e => setMountDev(e.target.value)} />
            <input className="input" placeholder="挂载点 (如 /mnt/data)" value={mountPoint} onChange={e => setMountPoint(e.target.value)} />
            <select className="sel" value={mountFstype} onChange={e => setMountFstype(e.target.value)}>
              <option value="">自动</option>
              <option value="ext4">ext4</option>
              <option value="xfs">xfs</option>
              <option value="ntfs">ntfs</option>
              <option value="vfat">vfat</option>
            </select>
            <button className="btn btn-accent" disabled={!mountDev || !mountPoint}
              onClick={() => mountAction('mount', mountDev, mountPoint)}>挂载</button>
            <button className="btn btn-danger" disabled={!mountDev && !mountPoint}
              onClick={() => mountAction('umount', mountDev, mountPoint)}>卸载</button>
          </div>
        </Card>
      )}
    </>
  )
}

function SmartSection() {
  const [device, setDevice] = useState('sda')
  const [output, setOutput] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [perm, setPerm] = useState<Permission>('user')

  const load = async () => {
    if (!device.trim()) return
    setLoading(true)
    setErr('')
    try {
      const res = await postJSON<DiskActionResult>('/api/core/tasks/disks/action', { action: 'smart', device: device.trim() })
      if (res.error) setErr(res.error)
      else setOutput(res.output || '')
      setPerm(res.permission)
    } catch { setErr('请求失败') }
    setLoading(false)
  }

  useEffect(() => { load() }, [])

  return (
    <Card title="SMART 健康" subtitle="smartctl -a">
      {perm !== 'root' && <div className="banner banner-err">需要 root 权限</div>}
      <div className="form-inline" style={{ marginBottom: 12 }}>
        <span className="field-label" style={{ margin: 0 }}>设备</span>
        <select className="sel" value={device} onChange={e => setDevice(e.target.value)}>
          {['sda', 'sdb', 'sdc', 'sdd', 'nvme0n1', 'nvme1n1'].map(d => <option key={d} value={d}>{d}</option>)}
        </select>
        <button className="btn btn-accent" disabled={loading || perm !== 'root'} onClick={load}>{loading ? '读取中…' : '读取 SMART'}</button>
      </div>
      {err && <div className="banner banner-err">{err}</div>}
      {output && <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap', maxHeight: 500, overflowY: 'auto' }}>{output}</div>}
    </Card>
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
