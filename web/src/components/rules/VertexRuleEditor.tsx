import { createContext, useContext, useState } from 'react'
import { parseSizeValueInput, sizeValueInput } from '../../features/rules/size-input'
import {
  defaultLeaf,
  defaultTree,
  defaultValue,
  fieldSpec,
  isSimpleConditionTree,
  operatorValueType,
  OPERATOR_LABELS,
  type VertexCondition,
  type VertexConditionTree,
  type VertexField,
  type VertexFieldSpec,
  type VertexOperator,
  type VertexValue,
  type VertexValueType,
  VERTEX_FIELDS,
} from '../../features/rules/model'

type RuleEditorProps = { value?: VertexConditionTree; onChange?: (value: VertexConditionTree) => void }
type LeafCondition = Extract<VertexCondition, { kind: 'leaf' }>
type LeafProps = { condition: LeafCondition; onChange: (condition: LeafCondition) => void; onRemove: () => void; onCopy: () => void; onMove: (offset: -1 | 1) => void; canMoveUp: boolean; canMoveDown: boolean }
type GroupProps = { condition: Extract<VertexCondition, { kind: 'group' }>; onChange: (condition: Extract<VertexCondition, { kind: 'group' }>) => void; onRemove?: () => void; onCopy?: () => void; onMove?: (offset: -1 | 1) => void; canMoveUp?: boolean; canMoveDown?: boolean; simple?: boolean }

const SizeInputDrafts = createContext<WeakMap<VertexValue, string>>(new WeakMap())

function copyCondition(condition: VertexCondition, drafts: WeakMap<VertexValue, string>): VertexCondition {
  if (condition.kind === 'group') return { ...condition, children: condition.children.map((child) => copyCondition(child, drafts)) }
  const copy = structuredClone(condition)
  if (condition.value && copy.value && drafts.has(condition.value)) drafts.set(copy.value, drafts.get(condition.value)!)
  return copy
}

function SizeValueEditor({ condition, valueType, onChange }: { condition: LeafCondition; valueType: VertexValueType; onChange: (condition: LeafCondition) => void }) {
  const drafts = useContext(SizeInputDrafts)
  const input = condition.value && drafts.has(condition.value) ? drafts.get(condition.value)! : sizeValueInput(condition.value)
  const parsed = parseSizeValueInput(input, valueType)
  return <input aria-label="值" aria-invalid={Boolean(parsed.error)} type="text" inputMode={valueType === 'number' ? 'decimal' : 'text'} value={input} placeholder={valueType === 'number' ? 'GB' : 'GB，以逗号分隔'} onChange={(event) => {
    const text = event.target.value
    // Keep incomplete input outside the AST; an invalid draft has no numeric value to submit.
    const value = parseSizeValueInput(text, valueType).value ?? { type: valueType }
    drafts.set(value, text)
    onChange(setValue(condition, value))
  }} />
}

function setValue(condition: LeafCondition, value: VertexValue): LeafCondition {
  return { ...condition, value }
}

function updateValue(condition: LeafCondition, valueType: VertexValueType, value: string): LeafCondition {
  if (valueType === 'number') return setValue(condition, { type: valueType, number: Number(value) || 0 })
  if (valueType === 'boolean') return setValue(condition, { type: valueType, boolean: value === 'true' })
  if (valueType === 'string_set') return setValue(condition, { type: valueType, strings: value.split(',').map((item) => item.trim()).filter(Boolean) })
  if (valueType === 'number_range' || valueType === 'number_set') return setValue(condition, { type: valueType, numbers: value.split(',').map((item) => Number(item.trim())).filter(Number.isFinite) })
  if (valueType === 'time') return setValue(condition, { type: valueType, time: value })
  if (valueType === 'time_range' || valueType === 'time_set') return setValue(condition, { type: valueType, times: value.split(',').map((item) => item.trim()).filter(Boolean) })
  return setValue(condition, { type: valueType, string: value })
}

function ValueEditor({ condition, spec, onChange }: { condition: LeafCondition; spec: VertexFieldSpec; onChange: (condition: LeafCondition) => void }) {
  if (condition.operator === 'exists' || condition.operator === 'not_exists') return null
  const valueType = operatorValueType(spec, condition.operator)
  if (condition.field === 'size') return <SizeValueEditor condition={condition} valueType={valueType} onChange={onChange} />
  const value = condition.value ?? defaultValue(valueType)
  if (valueType === 'boolean') {
    return <select aria-label="值" value={String(value.boolean ?? false)} onChange={(event) => onChange(updateValue(condition, valueType, event.target.value))}><option value="true">是</option><option value="false">否</option></select>
  }
  const inputType = valueType === 'number' ? 'number' : valueType === 'time' ? 'datetime-local' : 'text'
  const currentValue = valueType === 'number' ? String(value.number ?? 0) : valueType === 'number_range' || valueType === 'number_set' ? (value.numbers ?? []).join(', ') : valueType === 'string_set' ? (value.strings ?? []).join(', ') : valueType === 'time' || valueType === 'time_range' || valueType === 'time_set' ? (value.time ?? value.times?.join(', ') ?? '') : String(value.string ?? '')
  const collectionValue = valueType === 'string_set' || valueType === 'number_range' || valueType === 'number_set' || valueType === 'time_range' || valueType === 'time_set'
  return <input aria-label="值" type={inputType} value={currentValue} placeholder={collectionValue ? '以逗号分隔' : undefined} onChange={(event) => onChange(updateValue(condition, valueType, event.target.value))} />
}

function LeafEditor({ condition, onChange, onRemove, onCopy, onMove, canMoveUp, canMoveDown }: LeafProps) {
  const spec = fieldSpec(condition.field)
  const changeField = (field: VertexField) => onChange(defaultLeaf(field))
  const changeOperator = (operator: VertexOperator) => {
    const nextValueType = operatorValueType(spec, operator)
    const nextValue = condition.value?.type === nextValueType ? condition.value : defaultValue(nextValueType)
    onChange({ ...condition, operator, ...(operator === 'exists' || operator === 'not_exists' ? { value: undefined } : { value: nextValue }) })
  }
  return <div className="rule-leaf" data-testid="rule-leaf">
    <select aria-label="字段" value={condition.field} onChange={(event) => changeField(event.target.value as VertexField)}>{VERTEX_FIELDS.map((field) => <option key={field.field} value={field.field}>{field.label}</option>)}</select>
    <select aria-label="运算符" value={condition.operator} onChange={(event) => changeOperator(event.target.value as VertexOperator)}>{spec.operators.map((operator) => <option key={operator} value={operator}>{OPERATOR_LABELS[operator]}</option>)}</select>
    <ValueEditor condition={condition} spec={spec} onChange={onChange} />
    <div className="rule-node-actions">
      <button type="button" aria-label="上移条件" title="上移条件" disabled={!canMoveUp} onClick={() => onMove(-1)}>↑</button>
      <button type="button" aria-label="下移条件" title="下移条件" disabled={!canMoveDown} onClick={() => onMove(1)}>↓</button>
      <button type="button" aria-label="复制条件" onClick={onCopy}>复制</button>
      <button type="button" aria-label="删除条件" onClick={onRemove}>删除</button>
    </div>
  </div>
}

function GroupEditor({ condition, onChange, onRemove, onCopy, onMove, canMoveUp = false, canMoveDown = false, simple = false }: GroupProps) {
  const sizeDrafts = useContext(SizeInputDrafts)
  const updateChild = (index: number, child: VertexCondition) => onChange({ ...condition, children: condition.children.map((item, itemIndex) => itemIndex === index ? child : item) })
  const removeChild = (index: number) => onChange({ ...condition, children: condition.children.filter((_, itemIndex) => itemIndex !== index) })
  const copyChild = (index: number) => onChange({ ...condition, children: condition.children.flatMap((item, itemIndex) => itemIndex === index ? [item, copyCondition(item, sizeDrafts)] : [item]) })
  const moveChild = (index: number, offset: -1 | 1) => {
    const target = index + offset
    if (target < 0 || target >= condition.children.length) return
    const children = [...condition.children]
    ;[children[index], children[target]] = [children[target], children[index]]
    onChange({ ...condition, children })
  }
  return <div className="rule-group" data-testid="rule-group">
    <div className="rule-group-header">
      {simple ? <span className="rule-simple-combinator">全部满足（AND）</span> : <select aria-label="组合运算符" value={condition.operator} onChange={(event) => onChange({ ...condition, operator: event.target.value as 'and' | 'or' })}><option value="and">全部满足（AND）</option><option value="or">任一满足（OR）</option></select>}
      <div className="rule-group-actions">
        <button type="button" onClick={() => onChange({ ...condition, children: [...condition.children, defaultLeaf()] })}>添加条件</button>
        {!simple && <button type="button" onClick={() => onChange({ ...condition, children: [...condition.children, { kind: 'group', operator: 'and', children: [defaultLeaf()] }] })}>添加条件组</button>}
        {onMove && <button type="button" aria-label="上移条件组" title="上移条件组" disabled={!canMoveUp} onClick={() => onMove(-1)}>↑</button>}
        {onMove && <button type="button" aria-label="下移条件组" title="下移条件组" disabled={!canMoveDown} onClick={() => onMove(1)}>↓</button>}
        {onCopy && <button type="button" aria-label="复制条件组" onClick={onCopy}>复制组</button>}
        {onRemove && <button type="button" aria-label="删除条件组" onClick={onRemove}>删除组</button>}
      </div>
    </div>
    {condition.children.map((child, index) => child.kind === 'leaf'
      ? <LeafEditor key={index} condition={child} onChange={(nextChild) => updateChild(index, nextChild)} onRemove={() => removeChild(index)} onCopy={() => copyChild(index)} onMove={(offset) => moveChild(index, offset)} canMoveUp={index > 0} canMoveDown={index < condition.children.length - 1} />
      : <GroupEditor key={index} condition={child} onChange={(nextChild) => updateChild(index, nextChild)} onRemove={() => removeChild(index)} onCopy={() => copyChild(index)} onMove={(offset) => moveChild(index, offset)} canMoveUp={index > 0} canMoveDown={index < condition.children.length - 1} simple={simple} />)}
  </div>
}

export function VertexRuleEditor({ value = defaultTree(), onChange }: RuleEditorProps) {
  const [tree, setTree] = useState(value)
  const [sizeDrafts] = useState(() => new WeakMap<VertexValue, string>())
  const [mode, setMode] = useState<'simple' | 'advanced'>(isSimpleConditionTree(value) ? 'simple' : 'advanced')
  const updateTree = (nextTree: VertexConditionTree) => { setTree(nextTree); onChange?.(nextTree) }
  const simpleCompatible = isSimpleConditionTree(tree)
  const root = tree.root.kind === 'group' ? tree.root : { kind: 'group' as const, operator: 'and' as const, children: [tree.root] }
  const simpleView = mode === 'simple' && simpleCompatible
  return <section className="vertex-rule-editor" aria-label="Vertex 规则编辑器">
    <div className="rule-editor-caption"><span>Vertex 条件树</span><span>v{tree.version}</span></div>
    <div className="rule-mode-switcher" role="tablist" aria-label="规则编辑模式">
      <button type="button" role="tab" aria-selected={mode === 'simple'} className={mode === 'simple' ? 'active' : ''} onClick={() => setMode('simple')}>简单模式</button>
      <button type="button" role="tab" aria-selected={mode === 'advanced'} className={mode === 'advanced' ? 'active' : ''} onClick={() => setMode('advanced')}>高级模式</button>
    </div>
    {mode === 'simple' && !simpleCompatible && <div className="rule-mode-warning" role="status">当前条件包含简单模式无法表达的高级语义，已保留原 AST；请切换到高级模式编辑。</div>}
    <p className="field-hint">大小按 GB 输入，1 GB = 1,000,000,000 bytes；区间和集合使用逗号分隔。</p>
    <SizeInputDrafts.Provider value={sizeDrafts}><GroupEditor condition={root} simple={simpleView} onChange={(nextRoot) => updateTree({ ...tree, root: nextRoot })} /></SizeInputDrafts.Provider>
  </section>
}
