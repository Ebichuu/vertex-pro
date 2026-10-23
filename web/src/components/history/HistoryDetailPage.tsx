import { useEffect, useState } from 'react'
import {
  formatHistoryBytes,
  formatHistoryTime as formatTimeInZone,
  sortHistoryTimeline,
  type HistoryDetail,
  type HistoryDetailLoader,
} from '../../features/history/history-detail'
import { statusTone } from '../../features/history/history'
import { useDisplayTimezone } from '../../features/settings/timezone'

type HistoryDetailPageProps = {
  jobId: string
  loadDetail: HistoryDetailLoader
  onBack: () => void
}

type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; jobId: string; detail: HistoryDetail }
  | { status: 'error'; jobId: string }

const timelineToneLabel: Record<HistoryDetail['timeline'][number]['tone'], string> = {
  complete: '已完成',
  current: '当前阶段',
  warning: '需关注',
}

export function HistoryDetailPage({ jobId, loadDetail, onBack }: HistoryDetailPageProps) {
  const timezone = useDisplayTimezone()
  const formatHistoryTime = (value?: string) => formatTimeInZone(value, timezone)
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<LoadState>({ status: 'loading' })

  useEffect(() => {
    let active = true
    setState({ status: 'loading' })
    void loadDetail(jobId).then(
      (detail) => {
        if (active) setState({ status: 'ready', jobId, detail })
      },
      () => {
        if (active) setState({ status: 'error', jobId })
      },
    )
    return () => {
      active = false
    }
  }, [attempt, jobId, loadDetail])

  if (state.status === 'loading' || state.jobId !== jobId) {
    return (
      <section className="history-detail-page" aria-labelledby="history-detail-loading-title">
        <button type="button" className="history-back-button" onClick={onBack}>← 返回历史</button>
        <div className="panel">
          <h1 id="history-detail-loading-title">历史详情</h1>
          <p className="muted" role="status" aria-live="polite">正在加载投递记录…</p>
        </div>
      </section>
    )
  }

  if (state.status === 'error') {
    return (
      <section className="history-detail-page" aria-labelledby="history-detail-error-title">
        <button type="button" className="history-back-button" onClick={onBack}>← 返回历史</button>
        <div className="panel" role="alert">
          <h1 id="history-detail-error-title">历史详情加载失败</h1>
          <p className="muted">未找到或暂时无法读取这条投递记录。请返回历史列表或重新加载。</p>
          <button type="button" className="ghost-button" onClick={() => setAttempt((value) => value + 1)}>重新加载</button>
        </div>
      </section>
    )
  }

  const { detail } = state
  const winner = detail.rules.find((rule) => rule.winner)
  const timeline = sortHistoryTimeline(detail.timeline)
  return (
    <article className="history-detail-page" aria-labelledby="history-detail-title">
      <div className="history-detail-heading">
        <div>
          <button type="button" className="history-back-button" onClick={onBack}>← 返回历史</button>
          <p className="eyebrow">HISTORY DETAIL</p>
          <h1 id="history-detail-title">{detail.title}</h1>
          <p className="muted">查看这次投递的最终结果、传输记录和关键时间。</p>
        </div>
        <div className="history-detail-heading-status">
          <span className={`status-chip tone-${statusTone(detail.userStatus.user_status)}`}>{detail.userStatus.user_status_label}</span>
          <button type="button" className="ghost-button" onClick={() => setAttempt((value) => value + 1)}>刷新记录</button>
        </div>
      </div>

      <div data-testid="ordinary-history-detail" className="history-detail-ordinary">
        <section className="history-detail-summary" aria-labelledby="history-summary-title">
          <h2 id="history-summary-title">投递摘要</h2>
          <dl className="history-detail-summary-grid">
            <SummaryItem label="来源站点" value={detail.site} />
            <SummaryItem label="目标下载器" value={detail.downloader} />
            <SummaryItem label="最后状态" value={detail.transfer.stateLabel} />
            <SummaryItem label="发现时间" value={formatHistoryTime(detail.discoveredAt)} />
            <SummaryItem label="添加时间" value={formatHistoryTime(detail.addedAt)} />
            <SummaryItem label="确认时间" value={formatHistoryTime(detail.confirmedAt)} />
            <SummaryItem label="完成时间" value={formatHistoryTime(detail.finishedAt)} />
            <SummaryItem label="删除确认" value={detail.deletion ? formatHistoryTime(detail.deletion.confirmedAt) : '暂无数据'} />
            <SummaryItem label="失败原因" value={detail.failureReason || '暂无数据'} />
          </dl>
        </section>

        {detail.deletion && (
          <section className="history-deletion-confirmation" aria-labelledby="history-deletion-title">
            <div>
              <p className="eyebrow">DELETION CONFIRMED</p>
              <h2 id="history-deletion-title">删除已确认</h2>
            </div>
            <dl>
              <div><dt>确认时间</dt><dd>{formatHistoryTime(detail.deletion.confirmedAt)}</dd></div>
              <div><dt>证据来源</dt><dd>{detail.deletion.sourceLabel}</dd></div>
            </dl>
            <p>这条历史会继续保留最后上传量与分享率，系统不会自动重新添加。</p>
          </section>
        )}

        <section className="history-detail-section" aria-labelledby="history-transfer-title">
          <div className="history-detail-section-heading">
            <div>
              <p className="eyebrow">LAST KNOWN TRANSFER</p>
              <h2 id="history-transfer-title">最后传输数据</h2>
            </div>
            <span>记录于 {formatHistoryTime(detail.transfer.observedAt)}</span>
          </div>
          <dl className="history-transfer-grid">
            <Metric label="累计上传" value={formatHistoryBytes(detail.transfer.uploadedBytes)} />
            <Metric label="累计下载" value={formatHistoryBytes(detail.transfer.downloadedBytes)} />
            <Metric label="分享率" value={detail.transfer.ratio === undefined ? '暂无数据' : detail.transfer.ratio.toFixed(2)} />
          </dl>
        </section>

        <section className="history-detail-section history-rule-result" aria-labelledby="history-rules-title">
          <div className="history-detail-section-heading">
            <div>
              <p className="eyebrow">RULE RESULT</p>
              <h2 id="history-rules-title">规则与最终结果</h2>
            </div>
            <span>{detail.rulesKnown === false ? '命中信息暂无数据' : `${detail.rules.length} 条规则命中`}</span>
          </div>
          <div className="history-rule-layout">
            <div>
              <h3>命中规则</h3>
              <ul className="history-rule-matches">
                {detail.rules.map((rule) => (
                  <li key={rule.id} className={rule.winner ? 'is-winner' : undefined}>
                    <span><strong>{rule.name}</strong><small>优先级 {rule.priority ?? '暂无数据'}</small></span>
                    {rule.winner && <span className="winner-badge">胜出</span>}
                  </li>
                ))}
              </ul>
              {detail.rules.length === 0 && <p className="muted">{detail.rulesKnown === false ? '暂无规则命中信息' : '没有记录命中规则'}</p>}
              <p className="history-winner-reason">{winner ? `${winner.name}：${detail.winnerReason}` : detail.winnerReason}</p>
            </div>
            <dl className="history-final-decision">
              <SummaryItem label="最终下载器" value={detail.finalDecision.downloader} />
              <SummaryItem label="分类" value={detail.finalDecision.category} />
              <div>
                <dt>标签</dt>
                <dd>{detail.finalDecision.tags?.length ? detail.finalDecision.tags.map((tag) => <span key={tag} className="tag-chip">{tag}</span>) : detail.finalDecision.tags ? '无标签' : '暂无数据'}</dd>
              </div>
              <SummaryItem label="保存路径" value={detail.finalDecision.savePath} />
            </dl>
          </div>
        </section>

        {detail.failureReason && (
          <section className="history-failure" role="note" aria-labelledby="history-failure-title">
            <h2 id="history-failure-title">失败原因</h2>
            <p>{detail.failureReason}</p>
          </section>
        )}

        <section className="history-detail-section" aria-labelledby="history-timeline-title">
          <div className="history-detail-section-heading">
            <div>
              <p className="eyebrow">TIMELINE</p>
              <h2 id="history-timeline-title">投递时间线</h2>
            </div>
            <span>按发生时间排序</span>
          </div>
          <ol className="history-timeline" aria-label="投递时间线事件">
            {timeline.map((item) => (
              <li key={item.id} className={`timeline-${item.tone}`}>
                <div className="history-timeline-marker" aria-hidden="true" />
                <div className="history-timeline-copy">
                  <div>
                    <span>{item.stage}</span>
                    <span className="visually-hidden">阶段状态：{timelineToneLabel[item.tone]}</span>
                    <time dateTime={item.occurredAt}>{formatHistoryTime(item.occurredAt)}</time>
                  </div>
                  <strong>{item.label}</strong>
                  {item.detail && <p>{item.detail}</p>}
                </div>
              </li>
            ))}
          </ol>
          {timeline.length === 0 && <p className="muted">暂无时间线记录</p>}
        </section>
      </div>

      <details className="history-diagnostics">
        <summary>高级诊断</summary>
        <p>以下字段用于排障，不代表普通用户状态。</p>
        <dl>
          {detail.diagnostics.map((item) => (
            <div key={item.label}><dt>{item.label}</dt><dd><code>{item.value}</code></dd></div>
          ))}
        </dl>
        {detail.diagnostics.length === 0 && <p className="muted">暂无诊断记录</p>}
      </details>
    </article>
  )
}

function SummaryItem({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value || '暂无数据'}</dd></div>
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}
