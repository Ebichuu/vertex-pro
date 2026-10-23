import { ApiClient, ApiRequestError } from '../../generated/api-client'
import { csrfHeaders } from '../auth/auth'
import { defaultTree, type VertexCondition, type VertexConditionTree } from './model'
import { fromRuleVersionPayload, type RuleEditorDraft, type RuleVersionPayload } from './rule-editor'
import type { RuleSummary } from './rule-list'

export const rulesClient = new ApiClient(window.location.origin)
export type TargetOption = { id: string; label: string }

export function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('服务器响应格式无效。')
  return value as Record<string, unknown>
}

export function items(value: unknown): Record<string, unknown>[] {
  const payload = record(value)
  if (!Array.isArray(payload.items)) throw new Error('服务器列表格式无效。')
  return payload.items.map(record)
}

export function ruleSummary(value: unknown): RuleSummary {
  const raw = record(value)
  if (typeof raw.id !== 'string' || typeof raw.name !== 'string' || typeof raw.enabled !== 'boolean' || !Number.isSafeInteger(raw.revision) || Number(raw.revision) < 1 || !Number.isSafeInteger(raw.priority) || typeof raw.updated_at !== 'string') throw new Error('规则响应格式无效。')
  return { id: raw.id, name: raw.name, enabled: raw.enabled, revision: Number(raw.revision), priority: Number(raw.priority), updatedAt: raw.updated_at, matchCount: typeof raw.match_count === 'number' ? raw.match_count : 0, target: typeof raw.target === 'string' ? raw.target : '未设置', policyStatus: typeof raw.policy_status === 'string' && raw.policy_status ? raw.policy_status : undefined }
}

function treeFromAPI(value: unknown): VertexConditionTree {
  const tree = record(value)
  if (tree.version !== 1) throw new Error('不支持的规则条件版本。')
  const visit = (input: unknown): VertexCondition => {
    const node = record(input)
    if (node.kind === 'group') {
      const operator = String(node.operator).toLowerCase()
      if ((operator !== 'and' && operator !== 'or') || !Array.isArray(node.children)) throw new Error('规则条件组格式无效。')
      return { kind: 'group', operator, children: node.children.map(visit) }
    }
    if (node.kind !== 'leaf' || typeof node.field !== 'string' || typeof node.operator !== 'string') throw new Error('规则条件格式无效。')
    return node as VertexCondition
  }
  return { version: 1, root: visit(tree.root) }
}

function treeToAPI(tree: VertexConditionTree): unknown {
  const visit = (node: VertexCondition): unknown => node.kind === 'group' ? { ...node, operator: node.operator.toUpperCase(), children: node.children.map(visit) } : node
  return { version: 1, root: visit(tree.root) }
}

export function newRuleDraft(): RuleEditorDraft {
  const tree = defaultTree()
  tree.root = { kind: 'group', operator: 'and', children: [{ kind: 'leaf', field: 'title', operator: 'exists' }] }
  return { id: '', siteId: '', name: '', enabled: false, priority: 100, revision: 0, conditionTree: tree, actions: { targetMode: 'FIXED_INSTANCE', targetInstanceId: '', targetGroupId: '', category: '', tags: [], savePathPolicy: 'INSTANCE_DEFAULT', sequentialDownload: false, participationWindowMinutes: 60 } }
}

export function ruleDraftFromAPI(value: unknown): RuleEditorDraft {
  const raw = record(value)
  const summary = ruleSummary(raw)
  const definition = record(raw.definition)
  const tree = treeFromAPI(definition.condition_tree ?? definition)
  const base = newRuleDraft()
  if (definition.action_set_json == null || definition.action_set_json === '') {
    return { ...base, ...summary, siteId: typeof raw.site_id === 'string' ? raw.site_id : '', conditionTree: tree }
  }
  const actions = record(typeof definition.action_set_json === 'string' ? JSON.parse(definition.action_set_json) : definition.action_set_json)
  const normalized = { target_mode: 'FIXED_INSTANCE', target_instance_id: null, target_group_id: null, category: '', tags: [], save_path_policy: 'INSTANCE_DEFAULT', sequential_download: false, participation_window_minutes: 60, ...actions }
  if (!['FIXED_INSTANCE', 'INSTANCE_GROUP'].includes(String(normalized.target_mode)) || !Array.isArray(normalized.tags)) throw new Error('规则动作格式无效。')
  return { ...fromRuleVersionPayload({ rule_id: summary.id, name: summary.name, enabled: summary.enabled, priority: summary.priority, revision: summary.revision, condition_schema_version: 1, condition_tree_json: JSON.stringify(tree), action_schema_version: 1, action_set_json: JSON.stringify(normalized) }), siteId: typeof raw.site_id === 'string' ? raw.site_id : '' }
}

export function ruleBody(payload: RuleVersionPayload) {
  return { name: payload.name, enabled: payload.enabled, priority: payload.priority, definition: { condition_tree: treeToAPI(JSON.parse(payload.condition_tree_json) as VertexConditionTree), action_set_json: payload.action_set_json } }
}

export async function saveRule(payload: RuleVersionPayload): Promise<RuleEditorDraft> {
  const result = payload.rule_id
    ? await rulesClient.updateRule(payload.rule_id, { body: ruleBody(payload), headers: { ...csrfHeaders(), 'If-Match': `"${payload.revision - 1}"` } })
    : await rulesClient.createRule({ body: { ...ruleBody(payload), site_id: payload.site_id || undefined }, headers: csrfHeaders() })
  return ruleDraftFromAPI(result)
}

export function ruleError(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.body.code === 'QUI_UNREACHABLE' && error.body.message) return error.body.message
    if (error.status === 409) return '规则已被其他窗口修改，草稿已保留。请返回列表重新载入后修改。'
    if (error.status === 401 || error.status === 403) return '登录已失效或没有操作权限，请重新登录后重试。'
    if (error.status === 404) return '规则或目标已不存在，请刷新后重试。'
    if (error.body.code === 'VALIDATION_FAILED') return '规则未通过服务器校验，请检查条件、目标与参数。'
  }
  return '请求失败，请检查连接后重试。'
}
