import { useState } from 'react'
import { actionsForJob, type ExceptionItem } from '../../features/exceptions/exceptions'

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
  const confirm = (jobID: string, action: string, label: string) => setPending({ jobID, action, label })
  return (
    <section aria-labelledby="exceptions-list-title">
      {uncertainInstances.length > 0 && (
        <div className="panel exceptions-banner" role="alert">
          容量不可信实例：{uncertainInstances.join('、')}——新投递已暂停，等待容量对账。
        </div>
      )}
      <ul className="exceptions-list" aria-label="异常列表">
        {items.map((item) => {
          const actions = actionsForJob(item.user_status)
          return (
            <li key={item.id} className="panel exception-item" aria-label={`异常 ${item.title || item.id}`}>
              <div className="exception-head">
                <span className="dashboard-card-name">{item.title || item.id}</span>
                <span className="status-chip tone-wait">{item.user_status_label}</span>
              </div>
              <p className="muted">目标下载器：{item.downloader || '—'}</p>
              <div className="exception-actions" role="group" aria-label={`受控操作 ${item.id}`}>
                {actions.map((action) => (
                  <button
                    key={action.action}
                    type="button"
                    className={action.danger ? 'ghost-button danger' : 'ghost-button'}
                    disabled={executing}
                    onClick={() => confirm(item.id, action.action, action.label)}
                  >
                    {action.label}
                  </button>
                ))}
              </div>
            </li>
          )
        })}
        {items.length === 0 && uncertainInstances.length === 0 && (
          <li className="muted">当前没有需要人工处理的异常。</li>
        )}
      </ul>
      {pending && (
        <div className="confirm-backdrop" role="dialog" aria-modal="true" aria-label="确认受控操作">
          <div className="panel confirm-dialog">
            <h2>确认受控操作</h2>
            <p>
              即将对 {pending.jobID} 执行「{pending.label}」。该操作会写入审计记录，请确认你已核对最后新鲜快照。
            </p>
            <div className="confirm-actions">
              <button type="button" className="ghost-button" onClick={() => setPending(null)}>
                取消
              </button>
              <button
                type="button"
                className="ghost-button danger"
                onClick={async () => {
                  setExecuting(true)
                  try {
                    await onAction(pending.jobID, pending.action, `cmd-${pending.action.toLowerCase()}-${pending.jobID}-${Date.now()}`)
                    setPending(null)
                  } finally {
                    setExecuting(false)
                  }
                }}
              >
                {executing ? '执行中…' : '确认执行'}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}
