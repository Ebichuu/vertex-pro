import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { csrfHeaders } from '../../features/auth/auth'
import { items, record, ruleError, rulesClient } from '../../features/rules/api'
import type { AggregationPreviewData } from '../../features/rules/aggregation-preview'
import { parseGBInput } from '../../features/rules/size-input'
import { RuleAggregationPreview } from './RuleAggregationPreview'

type Evaluation = { id: string; name: string; priority: number; revision: number; state: string; reason: string; winner: boolean }
type Preview = { evaluatedAt: string; rules: Evaluation[]; aggregation: AggregationPreviewData | null; blockers: string[] }

function text(value: unknown): string { return typeof value === 'string' ? value : '' }
function strings(value: unknown): string[] { return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [] }

export function parseLivePreview(value: unknown): Preview {
  const raw = record(value)
  if (!Array.isArray(raw.rules) || typeof raw.evaluated_at !== 'string') throw new Error('预览响应格式无效。')
  const rules = raw.rules.map((value) => {
    const rule = record(value)
    if (typeof rule.id !== 'string' || typeof rule.name !== 'string' || !['TRUE', 'FALSE', 'UNKNOWN'].includes(String(rule.state))) throw new Error('规则判定响应格式无效。')
    const reason = typeof rule.reason === 'object' && rule.reason ? record(rule.reason) : {}
    return { id: rule.id, name: rule.name, priority: Number(rule.priority ?? 0), revision: Number(rule.revision ?? 0), state: String(rule.state), reason: text(reason.code) || text(rule.reason), winner: rule.winner === true }
  })
  let aggregation: AggregationPreviewData | null = null
  if (raw.aggregation) {
    const aggregate = record(raw.aggregation)
    const winner = aggregate.winner ? record(aggregate.winner) : {}
    const winnerID = text(winner.rule_id)
    const matched = rules.filter((rule) => rule.state === 'TRUE')
    const minutes = typeof aggregate.participation_window_minutes === 'number' ? aggregate.participation_window_minutes : typeof aggregate.participation_window === 'number' ? aggregate.participation_window / 60_000_000_000 : null
    const params = winner.add_params ? record(winner.add_params) : {}
    aggregation = {
      state: aggregate.rejected === true ? 'blocked' : matched.length ? 'matched' : 'empty',
      matches: matched.map((rule) => ({ id: rule.id, name: rule.name, priority: rule.priority, revision: rule.revision, winner: rule.winner || rule.id === winnerID, reason: rule.reason })),
      target: text(winner.target_instance_id) || text(winner.target_group_id) || null,
      category: text(winner.category) || null, tags: strings(aggregate.tags),
      savePathPolicy: text(winner.save_path) || null,
      sequentialDownload: typeof winner.sequential_download === 'boolean' ? winner.sequential_download : typeof params.sequentialDownload === 'string' ? params.sequentialDownload === 'true' : null,
      participationWindowMinutes: minutes,
      constraints: [...strings(aggregate.allowed_profiles), ...strings(aggregate.storage_labels)],
      blockers: aggregate.rejected === true ? [text(aggregate.reason) || '聚合约束未满足。'] : [],
    }
  }
  return { evaluatedAt: raw.evaluated_at, rules, aggregation, blockers: strings(raw.blockers) }
}

export function LiveDryRunPage() {
  const sites = useQuery({ queryKey: ['dry-run-sites'], queryFn: async () => items(await rulesClient.listSites()) })
  const [title, setTitle] = useState('')
  const [size, setSize] = useState('')
  const [site, setSite] = useState('')
  const [category, setCategory] = useState('')
  const [result, setResult] = useState<Preview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const run = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy) return
    const parsedSize = size.trim() ? parseGBInput(size) : undefined
    if (parsedSize?.error !== undefined) { setError(parsedSize.error); setResult(null); return }
    setBusy(true); setError(''); setResult(null)
    const facts: Record<string, unknown> = {}
    for (const [field, value] of Object.entries({ title, site, category })) if (value.trim()) facts[field] = { present: true, value: { type: 'string', string: value.trim() } }
    if (parsedSize) facts.size = { present: true, value: { type: 'number', number: parsedSize.bytes } }
    try { setResult(parseLivePreview(await rulesClient.dryRunRules({ body: { facts, site_id: site || undefined }, headers: csrfHeaders() }))) }
    catch (failure) { setError(`Dry-run 失败：${ruleError(failure)}`) }
    finally { setBusy(false) }
  }
  return <section className="dry-run-page" aria-labelledby="dry-run-page-title">
    <header className="dry-run-page-heading"><div><p className="eyebrow">SAFE PREVIEW</p><h1 id="dry-run-page-title">Dry-run 与聚合预览</h1><p className="muted">使用服务器已保存的启用规则评估输入，留空字段按缺失处理。</p></div></header>
    <div className="dry-run-layout"><form className="panel dry-run-controls" onSubmit={(event) => { void run(event) }}>
      <h2>预览输入</h2>
      <label>标题<input aria-label="预览标题" value={title} onChange={(event) => setTitle(event.target.value)} /></label>
      <label>大小（GB）<input aria-label="预览大小（GB）" type="text" inputMode="decimal" value={size} onChange={(event) => setSize(event.target.value)} /><span className="field-hint">1 GB = 1,000,000,000 bytes；留空表示字段缺失</span></label>
      <label>站点<select aria-label="预览站点" value={site} onChange={(event) => setSite(event.target.value)}><option value="">未提供（只评估全局规则）</option>{sites.data?.map((row) => <option key={text(row.id)} value={text(row.id)}>{text(row.name) || text(row.id)}</option>)}</select></label>
      {sites.isError && <p role="alert">站点加载失败。<button type="button" onClick={() => { void sites.refetch() }}>重试加载</button></p>}
      <label>分类<input aria-label="预览分类" value={category} onChange={(event) => setCategory(event.target.value)} /></label>
      <button type="submit" className="primary-button" disabled={busy}>{busy ? '正在预览…' : '运行 Dry-run'}</button>
      <p className="dry-run-safety">不下载、不创建 Reservation、不发送 Add。</p>
    </form><div className="dry-run-results" aria-live="polite" aria-busy={busy}>
      {error && <p role="alert" className="rule-list-error">{error}</p>}
      {!result && !error && <p className="empty-state">填写输入后运行预览。</p>}
      {result && <><section className="panel"><h2>规则判定</h2><p className="muted">服务端评估时间：{result.evaluatedAt}</p>{result.rules.length ? result.rules.map((rule) => <article key={rule.id}><strong>{rule.name}</strong><p>{rule.state === 'TRUE' ? '命中' : rule.state === 'FALSE' ? '未命中' : '字段缺失'}{rule.winner ? ' · Winner' : ''} · {rule.reason}</p></article>) : <p>没有适用的已启用规则。请先创建并启用规则。</p>}{result.blockers.map((blocker) => <p role="alert" key={blocker}>暂缓投递：{blocker}</p>)}</section>{result.aggregation && <RuleAggregationPreview data={result.aggregation} />}</>}
    </div></div>
  </section>
}
