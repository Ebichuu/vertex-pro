import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { useState } from 'react'
import { rulesClient, ruleError, type TargetOption } from '../../features/rules/api'
import { csrfHeaders } from '../../features/auth/auth'

export type InstanceGroup = { id: string; name: string; members: string[]; revision: number }
export function InstanceGroups({ groups, instances, onSaved }: { groups: InstanceGroup[]; instances: TargetOption[]; onSaved: () => Promise<unknown> }) {
  const [selected, setSelected] = useState('')
  const [name, setName] = useState('')
  const [members, setMembers] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const select = (id: string) => {
    const group = groups.find(g => g.id === id)
    setSelected(id); setName(group?.name ?? ''); setMembers(group?.members ?? []); setNotice('')
  }
  const save = async () => {
    if (!name.trim() || members.length === 0 || busy) return
    setBusy(true); setNotice('')
    try {
      await rulesClient.saveInstanceGroup({ body: { id: selected || `group-${crypto.randomUUID()}`, name: name.trim(), members, revision: groups.find(g => g.id === selected)?.revision ?? 0 }, headers: csrfHeaders() })
      await onSaved(); select(''); setNotice('分流组已保存，可在投递目标中选择。')
    } catch (error) { setNotice(ruleError(error)) } finally { setBusy(false) }
  }
  return <details className="panel group-management"><summary>管理 qB 分流组</summary>
    <label>编辑分流组<select aria-label="编辑分流组" value={selected} onChange={e => select(e.target.value)}><option value="">新建分流组</option>{groups.map(g => <option key={g.id} value={g.id}>{g.name}</option>)}</select></label>
    <label>分流组名称<Input value={name} onChange={e => setName(e.target.value)} /></label>
    <fieldset><legend>成员下载器</legend>{instances.map(instance => <label key={instance.id}><Input type="checkbox" checked={members.includes(instance.id)} onChange={e => setMembers(current => e.target.checked ? [...current, instance.id] : current.filter(id => id !== instance.id))} />{instance.label}</label>)}</fieldset>
    <p className="muted">从可用成员中按容量和下载负载选址。不可用或容量不足时等待，不会绕过验证。</p>
    <Button type="button" disabled={busy || !name.trim() || members.length === 0} onClick={() => { void save() }}>{busy ? '保存中…' : '保存分流组'}</Button>
    {notice && <p role="status">{notice}</p>}
  </details>
}
