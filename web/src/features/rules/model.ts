export const VERTEX_RULE_VERSION = 1 as const

export type VertexValueType = 'string' | 'number' | 'boolean' | 'string_set' | 'time' | 'number_set' | 'time_set' | 'number_range' | 'time_range'
export type VertexOperator = 'equals' | 'not_equals' | 'contains' | 'not_contains' | 'regex' | 'greater_than' | 'greater_or_equal' | 'less_than' | 'less_or_equal' | 'between' | 'in' | 'not_in' | 'exists' | 'not_exists'
export type VertexField = 'site' | 'rss' | 'title' | 'size' | 'category' | 'published_at' | 'discovery_delay' | 'promotion' | 'tags' | 'publisher' | 'group' | 'qb_profile' | 'instance_labels' | 'storage_labels'

export type VertexValue = {
  type: VertexValueType
  string?: string
  number?: number
  boolean?: boolean
  strings?: string[]
  time?: string
  numbers?: number[]
  times?: string[]
}

export type VertexCondition =
  | { kind: 'group'; operator: 'and' | 'or'; children: VertexCondition[] }
  | { kind: 'leaf'; field: VertexField; operator: VertexOperator; value?: VertexValue }

export type VertexConditionTree = { version: typeof VERTEX_RULE_VERSION; root: VertexCondition }

export type VertexFieldSpec = { field: VertexField; label: string; valueType: VertexValueType; operators: VertexOperator[] }

export const VERTEX_FIELDS: VertexFieldSpec[] = [
  { field: 'site', label: '站点', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'rss', label: 'RSS 源', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'title', label: '标题', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'regex', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'size', label: '大小（GB）', valueType: 'number', operators: ['equals', 'not_equals', 'greater_than', 'greater_or_equal', 'less_than', 'less_or_equal', 'between', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'category', label: '分类', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'published_at', label: '发布时间', valueType: 'time', operators: ['equals', 'not_equals', 'greater_than', 'greater_or_equal', 'less_than', 'less_or_equal', 'between', 'exists', 'not_exists'] },
  { field: 'discovery_delay', label: '发现延迟（秒）', valueType: 'number', operators: ['equals', 'not_equals', 'greater_than', 'greater_or_equal', 'less_than', 'less_or_equal', 'between', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'promotion', label: '促销状态', valueType: 'boolean', operators: ['equals', 'not_equals', 'exists', 'not_exists'] },
  { field: 'tags', label: 'Tags', valueType: 'string_set', operators: ['contains', 'not_contains', 'exists', 'not_exists'] },
  { field: 'publisher', label: '发布者', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'regex', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'group', label: '分组', valueType: 'string', operators: ['equals', 'not_equals', 'contains', 'not_contains', 'regex', 'in', 'not_in', 'exists', 'not_exists'] },
  { field: 'qb_profile', label: 'qB Profile', valueType: 'string_set', operators: ['contains', 'not_contains', 'exists', 'not_exists'] },
  { field: 'instance_labels', label: '实例标签', valueType: 'string_set', operators: ['contains', 'not_contains', 'exists', 'not_exists'] },
  { field: 'storage_labels', label: '存储标签', valueType: 'string_set', operators: ['contains', 'not_contains', 'exists', 'not_exists'] },
]

export const OPERATOR_LABELS: Record<VertexOperator, string> = {
  equals: '等于', not_equals: '不等于', contains: '包含', not_contains: '不包含', regex: '匹配正则',
  greater_than: '大于', greater_or_equal: '大于等于', less_than: '小于', less_or_equal: '小于等于',
  between: '介于', in: '在集合中', not_in: '不在集合中', exists: '字段存在', not_exists: '字段不存在',
}

export function fieldSpec(field: VertexField): VertexFieldSpec {
  return VERTEX_FIELDS.find((spec) => spec.field === field) ?? VERTEX_FIELDS[0]
}

export function operatorValueType(spec: VertexFieldSpec, operator: VertexOperator): VertexValueType {
  if (operator === 'in' || operator === 'not_in') {
    if (spec.valueType === 'string') return 'string_set'
    if (spec.valueType === 'number') return 'number_set'
    if (spec.valueType === 'time') return 'time_set'
  }
  if (operator === 'between') {
    if (spec.valueType === 'number') return 'number_range'
    if (spec.valueType === 'time') return 'time_range'
  }
  if ((operator === 'contains' || operator === 'not_contains') && spec.valueType === 'string_set') return 'string'
  return spec.valueType
}
export function defaultValue(valueType: VertexValueType): VertexValue {
  if (valueType === 'number') return { type: valueType, number: 0 }
  if (valueType === 'number_range' || valueType === 'number_set') return { type: valueType, numbers: [0, 0] }
  if (valueType === 'boolean') return { type: valueType, boolean: false }
  if (valueType === 'time') return { type: valueType, time: '' }
  if (valueType === 'time_range' || valueType === 'time_set') return { type: valueType, times: ['', ''] }
  if (valueType === 'string_set') return { type: valueType, strings: [''] }
  return { type: 'string', string: '' }
}

export function defaultLeaf(field: VertexField = 'title'): Extract<VertexCondition, { kind: 'leaf' }> {
  const spec = fieldSpec(field)
  const operator = spec.operators[0]
  return { kind: 'leaf', field, operator, ...(operator === 'exists' || operator === 'not_exists' ? {} : { value: defaultValue(spec.valueType) }) }
}

export function defaultTree(): VertexConditionTree {
  return { version: VERTEX_RULE_VERSION, root: { kind: 'group', operator: 'and', children: [defaultLeaf()] } }
}





export type SimpleRuleDraft = {
  conditions: Extract<VertexCondition, { kind: 'leaf' }>[]
}

const SIMPLE_FIELDS: VertexField[] = ['site', 'rss', 'title', 'size', 'category', 'promotion']
const SIMPLE_OPERATORS: VertexOperator[] = ['equals', 'not_equals', 'contains', 'not_contains', 'greater_than', 'greater_or_equal', 'less_than', 'less_or_equal', 'between', 'exists', 'not_exists']

export function isSimpleConditionTree(tree: VertexConditionTree): boolean {
  if (tree.root.kind !== 'group' || tree.root.operator !== 'and') return false
  return tree.root.children.every((child) => child.kind === 'leaf' && SIMPLE_FIELDS.includes(child.field) && SIMPLE_OPERATORS.includes(child.operator))
}

export function toSimpleDraft(tree: VertexConditionTree): SimpleRuleDraft | null {
  if (!isSimpleConditionTree(tree) || tree.root.kind !== 'group') return null
  return { conditions: tree.root.children.filter((child): child is Extract<VertexCondition, { kind: 'leaf' }> => child.kind === 'leaf').map((child) => ({ ...child, value: child.value ? { ...child.value } : undefined })) }
}

export function fromSimpleDraft(draft: SimpleRuleDraft): VertexConditionTree {
  return { version: VERTEX_RULE_VERSION, root: { kind: 'group', operator: 'and', children: draft.conditions.map((condition) => ({ ...condition, value: condition.value ? { ...condition.value } : undefined })) } }
}

