import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { useEffect, useMemo, useRef, useState } from 'react'
import {
  sortRules,
  type RulePatch,
  type RuleSummary,
  type RuleUpdateResult,
} from '../../features/rules/rule-list'
import { ruleError } from '../../features/rules/api'

type RuleListProps = {
  rules: RuleSummary[]
  onChange?: (rules: RuleSummary[]) => void
  onUpdateRule?: (rule: RuleSummary, expectedRevision: number, patch: RulePatch) => RuleUpdateResult | Promise<RuleUpdateResult>
  onEdit?: (rule: RuleSummary) => void
  onArchiveRule?: (rule: RuleSummary) => Promise<void>
  onDuplicateRule?: (rule: RuleSummary) => Promise<RuleSummary>
}

type Conflict = { ruleName: string; currentRevision: number }

export function RuleList({ rules: initialRules, onChange, onUpdateRule, onEdit, onDuplicateRule, onArchiveRule }: RuleListProps) {
  const [rules, setRules] = useState(initialRules)
  const rulesRef = useRef(initialRules)
  useEffect(() => { rulesRef.current = initialRules; setRules(initialRules) }, [initialRules])
  const [conflict, setConflict] = useState<Conflict | null>(null)
  const [requestError, setRequestError] = useState<string | null>(null)
  const [busyRuleId, setBusyRuleId] = useState<string | null>(null)
  const sortedRules = useMemo(() => sortRules(rules), [rules])
  const enabledCount = rules.filter((rule) => rule.enabled).length
  const totalMatches = rules.reduce((total, rule) => total + rule.matchCount, 0)
  const commit = (nextRules: RuleSummary[]) => { rulesRef.current = nextRules; setRules(nextRules); onChange?.(nextRules) }

  const applyPatch = async (rule: RuleSummary, patch: RulePatch) => {
    if (!onUpdateRule || busyRuleId !== null) return
    setBusyRuleId(rule.id)
    setConflict(null)
    setRequestError(null)
    try {
      const result = await onUpdateRule(rule, rule.revision, patch)
      if (!result.ok) {
        setConflict({ ruleName: rule.name, currentRevision: result.current.revision })
        commit(rulesRef.current.map((item) => item.id === rule.id ? result.current : item))
        return
      }
      commit(rulesRef.current.map((item) => item.id === rule.id ? result.rule : item))
    } catch (error) {
      setRequestError(`规则更新失败：${ruleError(error)}`)
    } finally {
      setBusyRuleId(null)
    }
  }

  const changePriority = (rule: RuleSummary, value: string) => {
    const priority = Number(value)
    if (!Number.isInteger(priority) || priority < 0 || priority === rule.priority) return
    void applyPatch(rule, { priority })
  }

  const archive = async (rule: RuleSummary) => {
    if (!onArchiveRule || busyRuleId !== null || !window.confirm(`删除规则“${rule.name}”？将停用其自动策略，已有任务和历史记录会保留。`)) return
    setBusyRuleId(rule.id); setRequestError(null)
    try { await onArchiveRule(rule); commit(rulesRef.current.filter(item => item.id !== rule.id)) }
    catch (error) { setRequestError(`删除失败：${ruleError(error)}`) }
    finally { setBusyRuleId(null) }
  }
  const copy = async (rule: RuleSummary) => {
    if (!onDuplicateRule || busyRuleId !== null) return
    setBusyRuleId(rule.id)
    setConflict(null)
    setRequestError(null)
    try { const duplicate = await onDuplicateRule(rule); commit([...rulesRef.current.filter(item => item.id !== duplicate.id), duplicate]) }
    catch (error) { setRequestError(`规则复制失败：${ruleError(error)}`) }
    finally { setBusyRuleId(null) }
  }

  return <section className="rule-list" aria-label="规则列表">
    <div className="rule-list-header">
      <div><p className="eyebrow">AUTOMATION RULES</p><h2>投递规则</h2></div>
      <div className="rule-list-summary"><strong>{enabledCount}/{rules.length}</strong> 已启用 · <strong>{totalMatches}</strong> 次命中<span>优先级高 → 低</span></div>
    </div>
    {conflict && <div className="rule-mode-warning" role="alert">“{conflict.ruleName}”已被其他编辑者修改，已加载当前 v{conflict.currentRevision}，请重新操作。</div>}
    {requestError && <div className="rule-list-error" role="alert">{requestError}</div>}
    {rules.length === 0 && <p className="empty-state">暂无规则。创建首条规则后，它会按优先级显示在这里。</p>}
    {rules.length > 0 && <>
      <div className="rule-list-columns" aria-hidden="true"><span>规则</span><span>命中</span><span>目标 qB</span><span>优先级</span><span>启用</span><span>操作</span></div>
      <div className="rule-list-items">{sortedRules.map((rule) => <article className="rule-list-item" key={rule.id}>
        <div className="rule-list-main"><strong>{rule.name}</strong><span>v{rule.revision} · 更新于 {rule.updatedAt.slice(0, 10)}{rule.policyStatus ? ` · qui 策略：${rule.policyStatus}` : ''}</span></div>
        <div className="rule-list-field"><span>命中</span><strong>{rule.matchCount}</strong></div>
        <div className="rule-list-field rule-list-target"><span>目标 qB</span><strong>{rule.target}</strong></div>
        <label className="rule-list-priority"><span>优先级</span><Input key={`${rule.id}-${rule.revision}`} aria-label={`${rule.name} 优先级`} type="number" min="0" defaultValue={rule.priority} disabled={busyRuleId !== null || !onUpdateRule} onBlur={(event) => changePriority(rule, event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur() }} /></label>
        <label className="rule-list-enabled"><span>启用</span><Input aria-label={`${rule.name} 启用`} type="checkbox" checked={rule.enabled} disabled={busyRuleId !== null || !onUpdateRule} onChange={() => { void applyPatch(rule, { enabled: !rule.enabled }) }} /></label>
        <div className="rule-list-actions">{onEdit && <Button type="button" className="ghost-button" onClick={() => onEdit(rule)}>编辑</Button>}<Button type="button" className="ghost-button" onClick={() => { void copy(rule) }} disabled={busyRuleId !== null || !onDuplicateRule}>复制</Button>{onArchiveRule && <Button type="button" className="ghost-button" disabled={busyRuleId !== null} onClick={() => { void archive(rule) }}>删除</Button>}</div>
      </article>)}</div>
    </>}
  </section>
}
