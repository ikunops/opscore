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

// ================= Cron 解析/生成工具 =================

type CronTask = {
  id: string
  minute: string
  hour: string
  dayOfMonth: string
  month: string
  dayOfWeek: string
  command: string
}

// 解析单行 crontab
function parseCronLine(line: string): CronTask | null {
  const trimmed = line.trim()
  if (!trimmed || trimmed.startsWith('#')) return null
  const parts = trimmed.split(/\s+/)
  if (parts.length < 6) return null
  return {
    id: crypto.randomUUID(),
    minute: parts[0],
    hour: parts[1],
    dayOfMonth: parts[2],
    month: parts[3],
    dayOfWeek: parts[4],
    command: parts.slice(5).join(' '),
  }
}

// 解析完整 crontab 文本
function parseCrontab(text: string): CronTask[] {
  return text.split('\n').map(l => parseCronLine(l)).filter((t): t is CronTask => t !== null)
}

// 构建单行 crontab
function buildCronLine(task: CronTask): string {
  return [task.minute, task.hour, task.dayOfMonth, task.month, task.dayOfWeek, task.command].join(' ')
}

// 构建完整 crontab 文本
function buildCrontab(tasks: CronTask[]): string {
  return tasks.map(buildCronLine).join('\n')
}

// 将 cron 字段转为人话
function cronToHuman(t: CronTask): string {
  const parts: string[] = []

  // 分钟
  if (t.minute === '*') parts.push('每分钟')
  else if (t.minute.startsWith('*/')) parts.push(`每 ${t.minute.slice(2)} 分钟`)
  else if (t.minute.includes(',')) parts.push(`分钟 ${t.minute}`)
  else parts.push(`${t.minute} 分`)

  // 小时
  if (t.hour === '*') parts.push('每小时')
  else if (t.hour.startsWith('*/')) parts.push(`每 ${t.hour.slice(2)} 小时`)
  else if (t.hour.includes(',')) parts.push(`小时 ${t.hour}`)
  else parts.push(`${t.hour} 时`)

  // 日期
  if (t.dayOfMonth === '*' && t.dayOfWeek === '*') parts.push('每天')
  else if (t.dayOfMonth !== '*') {
    if (t.dayOfMonth.startsWith('*/')) parts.push(`每 ${t.dayOfMonth.slice(2)} 天`)
    else if (t.dayOfMonth.includes(',')) parts.push(`每月 ${t.dayOfMonth} 号`)
    else parts.push(`每月 ${t.dayOfMonth} 号`)
  } else if (t.dayOfWeek !== '*') {
    const weekMap: Record<string, string> = { '0': '日', '7': '日', '1': '一', '2': '二', '3': '三', '4': '四', '5': '五', '6': '六' }
    if (t.dayOfWeek.startsWith('*/')) parts.push(`每 ${t.dayOfWeek.slice(2)} 周`)
    else if (t.dayOfWeek.includes(',')) parts.push(`周 ${t.dayOfWeek.split(',').map(w => weekMap[w] || w).join(',')}`)
    else parts.push(`周${weekMap[t.dayOfWeek] || t.dayOfWeek}`)
  }

  // 月份
  if (t.month !== '*') {
    if (t.month.startsWith('*/')) parts.push(`每 ${t.month.slice(2)} 个月`)
    else if (t.month.includes(',')) parts.push(`月份 ${t.month}`)
    else parts.push(`${t.month} 月`)
  }

  return parts.join(' · ') || '每分钟'
}

// 下拉选项生成
const MINUTE_OPTS = ['*', '0', '1', '2', '3', '4', '5', '10', '15', '20', '30', '*/5', '*/10', '*/15', '*/30']
const HOUR_OPTS = ['*', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '13', '14', '15', '16', '17', '18', '19', '20', '21', '22', '23', '*/2', '*/4', '*/6', '*/12']
const DOM_OPTS = ['*', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '13', '13', '14', '15', '16', '17', '18', '19', '20', '21', '22', '23', '24', '25', '26', '27', '28', '29', '30', '31', '*/2', '*/3', '*/5', '*/7']
const MONTH_OPTS = ['*', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12', '*/2', '*/3', '*/4', '*/6']
const DOW_OPTS = ['*', '0', '1', '2', '3', '4', '5', '6', '7', '*/2', '*/3']

// ================= 定时任务可视化编辑器 =================

function CrontabSection() {
  const [user, setUser] = useState('root')
  const [tasks, setTasks] = useState<CronTask[]>([])
  const [rawContent, setRawContent] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [form, setForm] = useState<CronTask | null>(null)
  const [msg, setMsg] = useState('')
  const [loading, setLoading] = useState(true)
  const [showRaw, setShowRaw] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    getJSON<Crontab>(`/api/core/tasks/crontab?user=${user}`).then(d => {
      setRawContent(d.content || '')
      setTasks(parseCrontab(d.content || ''))
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [user])

  useEffect(() => { load() }, [load])

  const save = async () => {
    const content = buildCrontab(tasks)
    try {
      const res = await postJSON<{ ok?: boolean; error?: string; permission: Permission }>('/api/core/tasks/crontab', { user, content })
      if (res.ok) { setMsg('✓ 已保存') }
      else setMsg(`✗ ${res.error || '保存失败'}`)
    } catch { setMsg('✗ 保存失败') }
    setTimeout(() => setMsg(''), 3000)
  }

  const startEdit = (task: CronTask) => setForm({ ...task })
  const cancelEdit = () => setForm(null)

  const handleChange = (field: keyof CronTask, value: string) => {
    setForm(f => f ? { ...f, [field]: value } : null)
  }

  const submitEdit = () => {
    if (!form) return
    setTasks(tasks.map(t => t.id === form.id ? form : t))
    setForm(null)
  }

  const deleteTask = (id: string) => {
    if (!confirm('确定删除该任务？')) return
    setTasks(tasks.filter(t => t.id !== id))
  }

  const addTask = () => {
    const newTask: CronTask = {
      id: crypto.randomUUID(),
      minute: '0',
      hour: '3',
      dayOfMonth: '*',
      month: '*',
      dayOfWeek: '*',
      command: '',
    }
    setTasks([...tasks, newTask])
    setTimeout(() => setForm({ ...newTask, command: '' }), 0)
  }

  const toggleRaw = () => setShowRaw(!showRaw)

  if (loading) return <div className="loading">加载中…</div>

  return (
    <Card title="定时任务" subtitle={showRaw ? '原始文本模式' : '可视化编辑'}>
      {showRaw && <button className="btn btn-sm" style={{ marginBottom: 12 }} onClick={toggleRaw}>切换到可视化</button>}
      {!showRaw && <button className="btn btn-sm" style={{ marginBottom: 12 }} onClick={toggleRaw}>切换原始文本</button>}

      <div className="form-inline" style={{ marginBottom: 12, flexWrap: 'wrap', gap: 8 }}>
        <span className="field-label" style={{ margin: 0 }}>用户</span>
        <select className="sel" value={user} onChange={e => { setUser(e.target.value); setShowRaw(false) }}>
          <option value="root">root</option>
        </select>
        <div style={{ flex: 1 }} />
        <button className="btn btn-accent" onClick={addTask}>+ 新增任务</button>
        <button className="btn" onClick={toggleRaw}>{showRaw ? '可视化' : '原始文本'}</button>
        <button className="btn btn-accent" onClick={save}>保存</button>
      </div>

      {msg && <div className={`banner ${msg.startsWith('✓') ? 'banner-ok' : 'banner-err'}`}>{msg}</div>}

      {showRaw ? (
        <div className="code-block" style={{ fontSize: 12.5, whiteSpace: 'pre-wrap' }}>
          <textarea className="input" style={{ width: '100%', minHeight: 240, fontFamily: 'ui-monospace,monospace', fontSize: 12.5, resize: 'vertical' }}
            value={rawContent} onChange={e => setRawContent(e.target.value)} />
        </div>
      ) : (
        <>
          {tasks.length === 0 && (
            <div className="banner banner-info" style={{ textAlign: 'center', padding: 24 }}>
              暂无定时任务，点击「+ 新增任务」创建
            </div>
          )}
          {tasks.map(task => (
            <CronCard
              key={task.id}
              task={task}
              isEditing={form?.id === task.id}
              onEdit={startEdit}
              onDelete={deleteTask}
              onCancel={cancelEdit}
              onSubmit={submitEdit}
              form={form}
              onChange={handleChange}
            />
          ))}
        </>
      )}
    </Card>
  )
}

function CronCard({ task, isEditing, onEdit, onDelete, onCancel, onSubmit, form, onChange }: {
  task: CronTask
  isEditing: boolean
  onEdit: (t: CronTask) => void
  onDelete: (id: string) => void
  onCancel: () => void
  onSubmit: () => void
  form: CronTask | null
  onChange: (field: keyof CronTask, value: string) => void
}) {
  return (
    <Card style={{ marginBottom: 12 }}>
      {isEditing ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div className="grid" style={{ gridTemplateColumns: 'repeat(5, 1fr)', gap: 8 }}>
            <SelectField label="分" value={form!.minute} onChange={v => onChange('minute', v)} options={MINUTE_OPTS} />
            <SelectField label="时" value={form!.hour} onChange={v => onChange('hour', v)} options={HOUR_OPTS} />
            <SelectField label="日" value={form!.dayOfMonth} onChange={v => onChange('dayOfMonth', v)} options={DOM_OPTS} />
            <SelectField label="月" value={form!.month} onChange={v => onChange('month', v)} options={MONTH_OPTS} />
            <SelectField label="周" value={form!.dayOfWeek} onChange={v => onChange('dayOfWeek', v)} options={DOW_OPTS} />
          </div>
          <InputField label="命令" value={form!.command} onChange={v => onChange('command', v)} placeholder="如 /usr/local/bin/backup.sh" />
          <div className="form-inline" style={{ justifyContent: 'flex-end', marginTop: 8 }}>
            <button className="btn" onClick={onCancel}>取消</button>
            <button className="btn btn-accent" onClick={onSubmit}>保存</button>
          </div>
        </div>
      ) : (
        <>
          <div className="flex" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <div>
              <span className="pill" style={{ marginRight: 8, fontSize: 12 }}>{cronToHuman(task)}</span>
              <span className="mono dim" style={{ fontSize: 13 }}>{task.command}</span>
            </div>
            <div className="btn-row">
              <button className="btn btn-sm" onClick={() => onEdit(task)}>编辑</button>
              <button className="btn btn-sm btn-danger" onClick={() => onDelete(task.id)}>删除</button>
            </div>
          </div>
        </>
      )}
    </Card>
  )
}

function SelectField({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: string[] }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <label className="field-label" style={{ fontSize: 11 }}>{label}</label>
      <select className="sel" value={value} onChange={e => onChange(e.target.value)} style={{ fontSize: 12.5 }}>
        {options.map(o => <option key={o} value={o}>{o}</option>)}
      </select>
    </div>
  )
}

function InputField({ label, value, onChange, placeholder }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1 }}>
      <label className="field-label" style={{ fontSize: 11 }}>{label}</label>
      <input className="input" value={value} onChange={e => onChange(e.target.value)} placeholder={placeholder} style={{ fontSize: 12.5 }} />
    </div>
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

// ================= Disks / Smart 保持不变 =================

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