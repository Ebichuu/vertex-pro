import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { InstanceGroups, type InstanceGroup } from '../components/rules/InstanceGroups'
import { RuleEditorPage } from '../components/rules/RuleEditorPage'
import { RuleList } from '../components/rules/RuleList'
import { ApiRequestError } from '../generated/api-client'
import { csrfHeaders } from '../features/auth/auth'
import { items, newRuleDraft, ruleDraftFromAPI, ruleError, ruleSummary, rulesClient, saveRule, type TargetOption } from '../features/rules/api'
import { toRuleVersionPayload } from '../features/rules/rule-editor'
import type { RulePatch, RuleSummary } from '../features/rules/rule-list'

export function RequestState({ loading, error, retry }: { loading?: boolean; error?: unknown; retry?: () => void }) {
  return <section className="panel" aria-live="polite">{loading ? <p role="status">正在加载…</p> : <><p role="alert">{ruleError(error)}</p>{retry && <button type="button" className="ghost-button" onClick={retry}>重试加载</button>}</>}</section>
}

export function RulesPage({ onEdit, onCreate }: { onEdit: (id: string) => void; onCreate: () => void }) {
  const client = useQueryClient()
  const query = useQuery({ queryKey: ['rules'], queryFn: async () => items(await rulesClient.listRules()).map(ruleSummary), refetchInterval: 15_000 })
  if (query.isPending) return <RequestState loading />
  if (query.isError) return <RequestState error={query.error} retry={() => { void query.refetch() }} />
  const update = async (rule: RuleSummary, expectedRevision: number, patch: RulePatch) => {
    try {
      const saved = ruleSummary(await rulesClient.updateRule(rule.id, { body: patch, headers: { ...csrfHeaders(), 'If-Match': `"${expectedRevision}"` } }))
      return { ok: true as const, rule: saved }
    } catch (error) {
      if (error instanceof ApiRequestError && error.status === 409) return { ok: false as const, status: 409 as const, code: 'REVISION_CONFLICT' as const, current: ruleSummary(await rulesClient.getRule(rule.id)) }
      throw error
    } finally {
      void query.refetch()
    }
  }
  const duplicate = async (rule: RuleSummary) => {
    try {
      const existing = await rulesClient.getRule(rule.id)
      const draft = ruleDraftFromAPI(existing)
      const body = { ...draft, id: '', name: `${draft.name} 副本 ${crypto.randomUUID().slice(0, 6)}`, enabled: false, revision: 0 }
      const saved = await saveRule(toRuleVersionPayload(body))
      return ruleSummary(await rulesClient.getRule(saved.id))
    } finally {
      void query.refetch()
    }
  }
  return <section className="rules-page" aria-labelledby="rules-title">
    <div className="rules-page-heading"><div><p className="eyebrow">AUTOMATION RULES</p><h1 id="rules-title">规则配置</h1><p className="muted">管理已保存的规则，再通过 Dry-run 核对条件和聚合决策。</p></div><div className="rules-page-actions"><button type="button" className="primary-button" onClick={onCreate}>新建规则</button><Link to="/aggregation" className="ghost-button">Dry-run 预览</Link></div></div>
    <RuleList rules={query.data} onChange={next => client.setQueryData(['rules'], next)} onUpdateRule={update} onDuplicateRule={duplicate} onArchiveRule={async rule => { try { await rulesClient.archiveRule(rule.id, { headers: { ...csrfHeaders(), 'If-Match': `"${rule.revision}"` } }) } finally { void query.refetch() } }} onEdit={(rule) => onEdit(rule.id)} />
  </section>
}

export function SavedRuleEditor({ ruleId, onBack }: { ruleId?: string; onBack: () => void }) {
  const client = useQueryClient()
  const query = useQuery({ queryKey: ['rule-editor', ruleId ?? 'new'], queryFn: async () => {
    const [rule, instances, sites, groups] = await Promise.all([
      ruleId ? rulesClient.getRule(ruleId) : Promise.resolve(undefined), rulesClient.listInstances(), rulesClient.listSites(), rulesClient.listInstanceGroups<{items: InstanceGroup[]}>(),
    ])
    const options = (rows: Record<string, unknown>[]): TargetOption[] => rows.map((row) => {
      if (typeof row.id !== 'string') throw new Error('目标响应格式无效。')
      return { id: row.id, label: typeof row.name === 'string' ? row.name : row.id }
    })
    return { draft: rule ? ruleDraftFromAPI(rule) : newRuleDraft(), instances: options(items(instances)), sites: options(items(sites)), groups:groups.items }
  } })
  if (query.isPending) return <RequestState loading />
  if (query.isError) return <RequestState error={query.error} retry={() => { void query.refetch() }} />
  return <><InstanceGroups groups={query.data.groups} instances={query.data.instances} onSaved={() => query.refetch()} /><RuleEditorPage key={ruleId ?? 'new'} initialDraft={query.data.draft} instances={query.data.instances} sites={query.data.sites} groups={query.data.groups.map(group => ({id:group.id,label:group.name}))} onCancel={onBack} onSave={async (payload) => {
    const saved = await saveRule(payload)
    await client.invalidateQueries({ queryKey: ['rules'] })
    return saved
  }} /></>
}
