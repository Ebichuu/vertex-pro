import { VERTEX_RULE_VERSION, type VertexConditionTree } from './model'

export type RuleTargetMode = 'FIXED_INSTANCE' | 'INSTANCE_GROUP'
export type SavePathPolicy = 'INSTANCE_DEFAULT' | 'CATEGORY_SUBDIRECTORY'

export type RuleActionDraft = {
  targetMode: RuleTargetMode
  targetInstanceId: string
  targetGroupId: string
  category: string
  tags: string[]
  savePathPolicy: SavePathPolicy
  sequentialDownload: boolean
  participationWindowMinutes: number
}

export type RuleEditorDraft = {
  id: string
  siteId?: string
  name: string
  enabled: boolean
  priority: number
  revision: number
  conditionTree: VertexConditionTree
  actions: RuleActionDraft
  extraActions?: Record<string, unknown>
}

export type RuleEditorErrors = Partial<Record<'name' | 'priority' | 'target' | 'participationWindowMinutes', string>>

export type RuleVersionPayload = {
  rule_id: string
  site_id?: string
  name: string
  enabled: boolean
  priority: number
  revision: number
  condition_schema_version: number
  condition_tree_json: string
  action_schema_version: number
  action_set_json: string
}

type RuleActionPayload = {
  target_mode: RuleTargetMode
  target_instance_id: string | null
  target_group_id: string | null
  category: string
  tags: string[]
  save_path_policy: SavePathPolicy
  sequential_download: boolean
  participation_window_minutes: number
}

export function validateRuleDraft(draft: RuleEditorDraft): RuleEditorErrors {
  const errors: RuleEditorErrors = {}
  if (!draft.name.trim()) errors.name = '请输入规则名称。'
  if (!Number.isInteger(draft.priority) || draft.priority < 0 || draft.priority > 10_000) errors.priority = '优先级需为 0 到 10000 的整数。'
  if (draft.actions.targetMode === 'FIXED_INSTANCE' && !draft.actions.targetInstanceId) errors.target = '请选择目标 qB。'
  if (draft.actions.targetMode === 'INSTANCE_GROUP' && !draft.actions.targetGroupId) errors.target = '请选择目标 qB 组。'
  if (!Number.isInteger(draft.actions.participationWindowMinutes) || draft.actions.participationWindowMinutes < 1 || draft.actions.participationWindowMinutes > 1440) {
    errors.participationWindowMinutes = '参与窗口需为 1 到 1440 分钟。'
  }
  return errors
}

export function toRuleVersionPayload(draft: RuleEditorDraft, revision = draft.revision + 1): RuleVersionPayload {
  const actions: RuleActionPayload = {
    ...draft.extraActions,
    target_mode: draft.actions.targetMode,
    target_instance_id: draft.actions.targetMode === 'FIXED_INSTANCE' ? draft.actions.targetInstanceId : null,
    target_group_id: draft.actions.targetMode === 'INSTANCE_GROUP' ? draft.actions.targetGroupId : null,
    category: draft.actions.category.trim(),
    tags: draft.actions.tags.map((tag) => tag.trim()).filter(Boolean),
    save_path_policy: draft.actions.savePathPolicy,
    sequential_download: draft.actions.sequentialDownload,
    participation_window_minutes: draft.actions.participationWindowMinutes,
  }
  return {
    rule_id: draft.id,
    site_id: draft.siteId,
    name: draft.name.trim(),
    enabled: draft.enabled,
    priority: draft.priority,
    revision,
    condition_schema_version: VERTEX_RULE_VERSION,
    condition_tree_json: JSON.stringify(draft.conditionTree),
    action_schema_version: 1,
    action_set_json: JSON.stringify(actions),
  }
}

export function fromRuleVersionPayload(payload: RuleVersionPayload): RuleEditorDraft {
  const conditionTree = JSON.parse(payload.condition_tree_json) as VertexConditionTree
  const actions = JSON.parse(payload.action_set_json) as RuleActionPayload
  if (conditionTree.version !== VERTEX_RULE_VERSION || payload.condition_schema_version !== VERTEX_RULE_VERSION || payload.action_schema_version !== 1) {
    throw new Error('不支持的规则版本。')
  }
  return {
    id: payload.rule_id,
    siteId: payload.site_id,
    name: payload.name,
    enabled: payload.enabled,
    priority: payload.priority,
    revision: payload.revision,
    conditionTree,
    extraActions: actions as unknown as Record<string, unknown>,
    actions: {
      targetMode: actions.target_mode,
      targetInstanceId: actions.target_instance_id ?? '',
      targetGroupId: actions.target_group_id ?? '',
      category: actions.category,
      tags: [...actions.tags],
      savePathPolicy: actions.save_path_policy,
      sequentialDownload: actions.sequential_download,
      participationWindowMinutes: actions.participation_window_minutes,
    },
  }
}

export function parseTags(value: string): string[] {
  return [...new Set(value.split(',').map((tag) => tag.trim()).filter(Boolean))]
}
