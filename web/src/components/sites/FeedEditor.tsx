import { useState } from 'react'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '../ui/dialog'
import { Input } from '../ui/input'
import { Button } from '../ui/button'
import type { Feed, FeedPatch } from '../../features/sites/sites'
import { ApiRequestError } from '../../generated/api-client'

export function FeedEditor({ feed, save, close, restoreFocus }: { feed: Feed; save: (patch: FeedPatch) => Promise<void>; close: () => void; restoreFocus: () => void }) {
  const [name, setName] = useState(feed.name)
  const [baseline] = useState(feed)
  const [url, setUrl] = useState(feed.url)
  const [minutes, setMinutes] = useState(String(feed.pollMinutes))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return <Dialog open onOpenChange={open => { if (!open && !busy) close() }}><DialogContent busy={busy} onCloseAutoFocus={event => { event.preventDefault(); restoreFocus() }}>
    <DialogTitle>编辑 RSS</DialogTitle><DialogDescription>修改名称、订阅地址和轮询周期，启用状态保持当前设置。</DialogDescription>
    <form className="rss-edit-form" onSubmit={async event => {
      event.preventDefault()
      if (busy) return
      let valid = false
      try { valid = ['http:', 'https:'].includes(new URL(url).protocol) } catch { /* show validation below */ }
      if (!name.trim() || !valid || !Number.isInteger(Number(minutes)) || Number(minutes) < 1 || Number(minutes) > 1440) { setError('请填写名称、有效的 HTTP(S) 地址和 1–1440 分钟的轮询周期。'); return }
      const values = { name: name.trim(), url: url.trim(), pollMinutes: Number(minutes) }
      const patch: FeedPatch = {}
      for (const key of ['name', 'url', 'pollMinutes'] as const) {
        if (values[key] === baseline[key]) continue
        if (feed[key] !== baseline[key]) { setError('正在修改的字段已被其他操作更新，请保留草稿并重新打开编辑后核对。'); return }
        Object.assign(patch, { [key]: values[key] })
      }
      if (Object.keys(patch).length === 0) { close(); return }
      setBusy(true); setError('')
      try { await save(patch); close() } catch (failure) {
        setError(failure instanceof ApiRequestError && failure.body.code === 'REVISION_CONFLICT'
          ? 'RSS 已被其他操作更新，草稿已保留，请刷新后核对再保存。'
          : '保存失败，请检查配置或刷新后重试。')
      } finally { setBusy(false) }
    }}>
      <label>RSS 名称<Input value={name} onChange={event => setName(event.target.value)} required disabled={busy} /></label>
      <label>RSS 地址<Input value={url} onChange={event => setUrl(event.target.value)} type="url" required disabled={busy} /></label>
      <label>轮询周期（分钟）<Input value={minutes} onChange={event => setMinutes(event.target.value)} type="number" min="1" max="1440" required disabled={busy} /></label>
      {error && <p role="alert" className="auth-error">{error}</p>}
      <div className="confirm-actions"><Button type="button" disabled={busy} onClick={close}>取消</Button><Button variant="default" disabled={busy} type="submit">{busy ? '保存中…' : '保存修改'}</Button></div>
    </form>
  </DialogContent></Dialog>
}
