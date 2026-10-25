import { useRef, useState } from 'react'
import { actionsForJob, type ExceptionItem } from '../../features/exceptions/exceptions'
import { Button } from '../ui/button'
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '../ui/dialog'

type ExceptionsListProps = {
  items: ReadonlyArray<ExceptionItem>
  uncertainInstances: ReadonlyArray<string>
  onAction: (jobID: string, action: string, commandID: string) => Promise<void> | void
}

// ExceptionsList 展示需人工处理的异常与受控操作（UI-10）。
// 每个操作必须经过确认弹窗；不提供任何形式的"再 Add"。
export function ExceptionsList({ items, uncertainInstances, onAction }: ExceptionsListProps) {
  const [pending, setPending] = useState<{ jobID: string; action: string; label: string } | null>(null)
  const [executing, setExecuting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const cancelRef = useRef<HTMLButtonElement>(null)
  const confirm = (jobID: string, action: string, label: string, trigger: HTMLButtonElement) => { triggerRef.current = trigger; setError(''); setPending({ jobID, action, label }) }
  return (
    <section aria-labelledby="exceptions-list-title">
      <h2 id="exceptions-list-title">待处理异常</h2>
      {error && !pending && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      {uncertainInstances.length > 0 && (
        <div className="panel exceptions-banner" role="alert">
          容量不可信实例：{uncertainInstances.join('、')}——新投递已暂停，等待容量对账。
        </div>
      )}
      <ul className="exceptions-list" aria-label="异常列表">
        {items.map((item) => {
          const actions = actionsForJob(item.user_status).filter((action) => item.allowed_actions === undefined || item.allowed_actions.includes(action.action))
          return (
            <li key={item.id} className="panel exception-item" aria-label={`异常 ${item.title || item.id}`}>
              <div className="exception-head">
                <span className="dashboard-card-name">{item.title || item.id}</span>
                <span className="status-chip tone-wait">{item.user_status_label}</span>
              </div>
              <p className="muted">目标下载器：{item.downloader || '—'}</p>
              {item.action_unavailable_reason && <p className="muted">{item.action_unavailable_reason}</p>}
              <div className="exception-actions" role="group" aria-label={`受控操作 ${item.id}`}>
                {actions.map((action) => (
                  <Button
                    key={action.action}
                    type="button"
                    className={action.danger ? 'ghost-button danger' : 'ghost-button'}
                    disabled={executing}
                    onClick={event => confirm(item.id, action.action, action.label, event.currentTarget)}
                  >
                    {action.label}
                  </Button>
                ))}
              </div>
            </li>
          )
        })}
        {items.length === 0 && uncertainInstances.length === 0 && (
          <li className="muted">当前没有需要人工处理的异常。</li>
        )}
      </ul>
      <Dialog open={Boolean(pending)} onOpenChange={open => { if (!open && !executing) setPending(null) }}>
        {pending && <DialogContent busy={executing} onOpenAutoFocus={event => { event.preventDefault(); cancelRef.current?.focus() }} onCloseAutoFocus={event => { event.preventDefault(); if (triggerRef.current?.isConnected) triggerRef.current.focus() }}>
            <DialogTitle>确认受控操作</DialogTitle>
            <DialogDescription>
              即将对 {pending.jobID} 执行「{pending.label}」。该操作会写入审计记录，请确认你已核对最后新鲜快照。
            </DialogDescription>
            {error && <p role="alert" className="auth-error">{error}</p>}
            <div className="confirm-actions">
              <Button ref={cancelRef} type="button" disabled={executing} onClick={() => setPending(null)}>
                取消
              </Button>
              <Button
                type="button"
                variant="destructive"
                disabled={executing}
                onClick={async () => {
                  setExecuting(true)
                  setError(''); setNotice('')
                  try {
                    await onAction(pending.jobID, pending.action, `cmd-${pending.action.toLowerCase()}-${pending.jobID}-${Date.now()}`)
                    setPending(null)
                    setNotice('服务器已处理操作，列表已刷新。')
                  } catch {
                    setError('操作未完成，请检查状态与新鲜快照后重试。')
                  } finally {
                    setExecuting(false)
                  }
                }}
              >
                {executing ? '执行中…' : '确认执行'}
              </Button>
            </div>
        </DialogContent>}
      </Dialog>
    </section>
  )
}
