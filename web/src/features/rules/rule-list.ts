export type RuleSummary = {
  id: string
  name: string
  priority: number
  enabled: boolean
  revision: number
  matchCount: number
  target: string
  policyStatus?: string
  updatedAt: string
}

export type RulePatch = Partial<Pick<RuleSummary, 'name' | 'priority' | 'enabled'>>
export type RuleUpdateResult = { ok: true; rule: RuleSummary } | { ok: false; status: 409; code: 'REVISION_CONFLICT'; current: RuleSummary }

export function sortRules(rules: RuleSummary[]): RuleSummary[] {
  return [...rules].sort((left, right) => right.priority - left.priority || right.revision - left.revision || left.id.localeCompare(right.id))
}

export function updateRuleOptimistically(rule: RuleSummary, expectedRevision: number, patch: RulePatch): RuleUpdateResult {
  if (rule.revision !== expectedRevision) return { ok: false, status: 409, code: 'REVISION_CONFLICT', current: { ...rule } }
  return { ok: true, rule: { ...rule, ...patch, revision: rule.revision + 1 } }
}

export function duplicateRule(rule: RuleSummary, id: string, name: string): RuleSummary {
  return { ...rule, id, name, enabled: false, revision: 1, matchCount: 0, updatedAt: new Date().toISOString() }
}
