import { useState } from 'react'
import type { DryRunEvaluationState, DryRunExecutor, DryRunPreviewResult, RecentRssItem } from '../../features/rules/dry-run'
import { RuleAggregationPreview } from './RuleAggregationPreview'

type RuleDryRunPageProps = {
  items: ReadonlyArray<RecentRssItem>
  runDryRun: DryRunExecutor
}

const evaluationLabel: Record<DryRunEvaluationState, string> = { TRUE: '命中', FALSE: '未命中', UNKNOWN: '字段缺失' }

function formatSize(sizeBytes: number | null) {
  if (sizeBytes === null) return '未提供'
  return `${(sizeBytes / 1024 ** 3).toFixed(1)} GiB`
}

export function RuleDryRunPage({ items, runDryRun }: RuleDryRunPageProps) {
  const [selectedItemId, setSelectedItemId] = useState(items[0]?.id ?? '')
  const [result, setResult] = useState<DryRunPreviewResult | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const selectedItem = items.find((item) => item.id === selectedItemId)

  const run = async () => {
    if (!selectedItemId) return
    setRunning(true)
    setError(null)
    try {
      setResult(await runDryRun(selectedItemId))
    } catch (runError) {
      setResult(null)
      setError(`Dry-run 失败：${String(runError)}`)
    } finally {
      setRunning(false)
    }
  }

  return <section className="dry-run-page" aria-labelledby="dry-run-page-title">
    <header className="dry-run-page-heading">
      <div>
        <p className="eyebrow">UI-06 · SAFE PREVIEW</p>
        <h1 id="dry-run-page-title">Dry-run 与聚合预览</h1>
        <p className="muted">逐条查看规则判定，再确认 Winner、最终 qB 和受控参数。</p>
      </div>
      <span className="page-badge">LOCAL ONLY</span>
    </header>

    <div className="dry-run-layout">
      <aside className="panel dry-run-controls" aria-labelledby="dry-run-input-title">
        <div><p className="eyebrow">RECENT RSS</p><h2 id="dry-run-input-title">选择预览项目</h2></div>
        {items.length > 0 ? <>
          <label>近期 RSS 项<select aria-label="近期 RSS 项" value={selectedItemId} disabled={running} onChange={(event) => { setSelectedItemId(event.target.value); setResult(null); setError(null) }}>{items.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label>
          {selectedItem && <dl className="dry-run-item-meta">
            <div><dt>站点</dt><dd>{selectedItem.site}</dd></div>
            <div><dt>RSS 源</dt><dd>{selectedItem.feed}</dd></div>
            <div><dt>大小</dt><dd>{formatSize(selectedItem.sizeBytes)}</dd></div>
            <div><dt>发布时间</dt><dd>{selectedItem.publishedAt.slice(0, 16).replace('T', ' ')}</dd></div>
          </dl>}
          <button type="button" className="primary-button" disabled={running} onClick={() => { void run() }}>{running ? '正在预览…' : '运行 Dry-run'}</button>
        </> : <p className="empty-state">暂无可用于预览的近期 RSS 项。</p>}
        <p className="dry-run-safety">只读取预览输入，不下载、不创建 Reservation、不发送 Add。</p>
      </aside>

      <div className="dry-run-results" aria-live="polite" aria-busy={running}>
        {error && <p className="rule-list-error" role="alert">{error}</p>}
        {!result && !error && <section className="dry-run-empty" aria-label="预览等待中"><strong>等待运行</strong><p>选择一条近期 RSS 项后运行 Dry-run，结果会显示全部规则和每个条件的判定。</p></section>}
        {result && <>
          <section aria-labelledby="dry-run-evaluations-title">
            <div className="dry-run-section-heading"><div><p className="eyebrow">RULE EVALUATION</p><h2 id="dry-run-evaluations-title">规则判定</h2></div><span>{result.rules.length} 条规则 · {result.evaluatedAt.slice(11, 16)}</span></div>
            <div className="dry-run-rule-list">{result.rules.map((rule) => <article className={`dry-run-rule dry-run-rule-${rule.state.toLowerCase()}`} key={rule.id}>
              <header><div><strong>{rule.name}</strong><span>优先级 {rule.priority} · v{rule.revision}</span></div><div className="dry-run-rule-status"><span className={`evaluation-badge evaluation-${rule.state.toLowerCase()}`}>{evaluationLabel[rule.state]}</span>{rule.winner && <span className="winner-badge">Winner</span>}</div></header>
              <p>{rule.reason}</p>
              <ul>{rule.conditions.map((condition) => <li key={condition.id}><span className={`condition-state condition-${condition.state.toLowerCase()}`}>{condition.state}</span><div><strong>{condition.label}</strong><span>{condition.detail}{condition.missingField && <> · 缺失字段：<code>{condition.missingField}</code></>}</span></div></li>)}</ul>
            </article>)}</div>
          </section>
          <RuleAggregationPreview data={result.aggregation} />
        </>}
      </div>
    </div>
  </section>
}
