import type { AggregationPreviewData } from '../../features/rules/aggregation-preview'

type Props = { data: AggregationPreviewData }

export function RuleAggregationPreview({ data }: Props) {
  const stateLabel = data.state === 'matched' ? '可投递' : data.state === 'blocked' ? '已阻止' : '无命中'
  const stateTone = data.state === 'matched' ? 'READY' : data.state === 'blocked' ? 'BLOCKED' : 'WAITING'
  const hasWinner = data.matches.some((match) => match.winner)

  return (
    <aside className={`aggregation-preview aggregation-preview-${data.state}`} aria-label="竞速投递预览">
      <div className="aggregation-preview-header">
        <div><p className="eyebrow">RACE PREVIEW</p><h2>竞速投递</h2></div>
        <span className="preview-state"><strong>{stateTone}</strong><small>{stateLabel}</small></span>
      </div>
      <div className="race-summary" aria-label="规则聚合摘要">
        <div><strong>{data.matches.length}</strong><span>命中规则</span></div>
        <div><strong>{hasWinner ? '已选定' : '未选定'}</strong><span>Winner</span></div>
        <div><strong>{data.constraints.length}</strong><span>安全约束</span></div>
      </div>
      <section className="preview-section" aria-labelledby="matched-rules-title">
        <div className="preview-section-heading"><h3 id="matched-rules-title">命中规则</h3><span>按优先级排序</span></div>
        {data.matches.length > 0 ? <div className="preview-match-list">{data.matches.map((match) => <article className={`preview-match${match.winner ? ' is-winner' : ''}`} key={`${match.id}-${match.revision}`}>
          <div className="preview-match-title"><strong>{match.name}</strong>{match.winner && <span className="winner-badge">Winner</span>}</div>
          <span className="preview-match-meta">优先级 {match.priority} · v{match.revision}</span>
          <span className="preview-match-reason">{match.reason}</span>
        </article>)}</div> : <p className="preview-muted">没有规则进入聚合。</p>}
      </section>
      <section className="preview-section" aria-labelledby="final-result-title">
        <div className="preview-section-heading"><h3 id="final-result-title">最终投递目标</h3><span>聚合结果</span></div>
        <dl className="preview-result-grid">
          <div><dt>目标 qB</dt><dd>{data.target ?? '未确定'}</dd></div>
          <div><dt>分类</dt><dd>{data.category ?? '未确定'}</dd></div>
          <div><dt>Tags</dt><dd>{data.tags.length ? data.tags.map((tag) => <span className="tag-chip" key={tag}>{tag}</span>) : '无'}</dd></div>
          <div><dt>保存路径</dt><dd>{data.savePathPolicy ?? '未确定'}</dd></div>
          <div><dt>顺序下载</dt><dd>{data.sequentialDownload === null ? '未确定' : data.sequentialDownload ? '启用' : '关闭'}</dd></div>
          <div><dt>参与窗口</dt><dd>{data.participationWindowMinutes === null ? '未确定' : `${data.participationWindowMinutes} 分钟`}</dd></div>
          <div><dt>约束</dt><dd>{data.constraints.length ? data.constraints.join(' · ') : '无'}</dd></div>
        </dl>
      </section>
      {data.blockers.length > 0 && <section className="preview-blockers" role="alert"><strong>暂缓投递</strong>{data.blockers.map((blocker) => <p key={blocker}>{blocker}</p>)}</section>}
      <p className="preview-footnote">聚合结果只读取 dry-run 输入。不下载、不创建 Reservation、不发送 Add。</p>
    </aside>
  )
}
