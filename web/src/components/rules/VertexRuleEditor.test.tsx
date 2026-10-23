import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { VertexRuleEditor } from './VertexRuleEditor'
import userEvent from '@testing-library/user-event'
import { validateSizeConditions } from '../../features/rules/size-input'
import { defaultTree, fromSimpleDraft, isSimpleConditionTree, toSimpleDraft, type VertexConditionTree } from '../../features/rules/model'

afterEach(cleanup)

function childrenOf(tree: ReturnType<typeof defaultTree>) {
  if (tree.root.kind !== 'group') throw new Error('expected group root')
  return tree.root.children
}

describe('VertexRuleEditor', () => {
  it('shows existing bytes as precise GB without mutating the original AST', () => {
    const initial: VertexConditionTree = { version: 1, root: { kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number', number: 1_073_741_824 } } }
    let changed = false
    render(<VertexRuleEditor value={initial} onChange={() => { changed = true }} />)
    expect(screen.getByLabelText('值')).toHaveValue('1.073741824')
    fireEvent.click(screen.getByRole('tab', { name: '高级模式' }))
    expect(changed).toBe(false)
    expect(initial.root).toMatchObject({ value: { number: 1_073_741_824 } })
  })

  it('allows typing decimal GB without swallowing the decimal point', async () => {
    const user = userEvent.setup()
    let latest = defaultTree()
    render(<VertexRuleEditor value={latest} onChange={(value) => { latest = value }} />)
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'size' } })
    await user.clear(screen.getByLabelText('值'))
    await user.type(screen.getByLabelText('值'), '1.5')
    expect(screen.getByLabelText('值')).toHaveValue('1.5')
    expect(childrenOf(latest)[0]).toMatchObject({ value: { number: 1_500_000_000 } })
  })

  it.each(['between', 'in', 'not_in'])('retains comma drafts while typing %s GB values', async (operator) => {
    const user = userEvent.setup()
    let latest = defaultTree()
    render(<VertexRuleEditor value={latest} onChange={(value) => { latest = value }} />)
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'size' } })
    fireEvent.change(screen.getByLabelText('运算符'), { target: { value: operator } })
    await user.clear(screen.getByLabelText('值'))
    await user.type(screen.getByLabelText('值'), '1,')
    expect(screen.getByLabelText('值')).toHaveValue('1,')
    expect(validateSizeConditions(latest.root)).toBeDefined()
    await user.type(screen.getByLabelText('值'), '10')
    expect(screen.getByLabelText('值')).toHaveValue('1,10')
    expect(childrenOf(latest)[0]).toMatchObject({ value: { type: operator === 'between' ? 'number_range' : 'number_set', numbers: [1_000_000_000, 10_000_000_000] } })
    expect(validateSizeConditions(latest.root)).toBeUndefined()
  })

  it('preserves an invalid size draft through copying, moving and comparator changes', () => {
    render(<VertexRuleEditor />)
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'size' } })
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1.' } })
    fireEvent.click(screen.getByRole('button', { name: '复制条件' }))
    fireEvent.change(screen.getAllByLabelText('值')[0], { target: { value: 'invalid' } })
    fireEvent.click(screen.getAllByRole('button', { name: '下移条件' })[0])
    fireEvent.change(screen.getAllByLabelText('运算符')[1], { target: { value: 'greater_than' } })
    expect(screen.getAllByLabelText('值')[0]).toHaveValue('1.')
    expect(screen.getAllByLabelText('值')[1]).toHaveValue('invalid')
    expect(screen.getAllByLabelText('值')[1]).toHaveAttribute('aria-invalid', 'true')
  })

  it('renders Vertex fields and changes the operator/value without changing AST shape', () => {
    let latest = defaultTree()
    render(<VertexRuleEditor value={latest} onChange={(nextValue) => { latest = nextValue }} />)
    expect(screen.getByRole('option', { name: '标题' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'size' } })
    expect(latest.root.kind).toBe('group')
    expect(childrenOf(latest)[0]).toMatchObject({ kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number', number: 0 } })
    fireEvent.change(screen.getByLabelText('运算符'), { target: { value: 'greater_than' } })
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1024' } })
    expect(childrenOf(latest)[0]).toMatchObject({ field: 'size', operator: 'greater_than', value: { type: 'number', number: 1_024_000_000_000 } })
  })

  it('maps set operators to typed set values', () => {
    let latest = defaultTree()
    render(<VertexRuleEditor value={latest} onChange={(nextValue) => { latest = nextValue }} />)
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'site' } })
    fireEvent.change(screen.getByLabelText('运算符'), { target: { value: 'in' } })
    expect(childrenOf(latest)[0]).toMatchObject({ value: { type: 'string_set' } })
  })

  it('supports nested group composition and removal', () => {
    render(<VertexRuleEditor />)
    fireEvent.click(screen.getAllByRole('button', { name: '添加条件' })[0])
    expect(screen.getAllByTestId('rule-leaf')).toHaveLength(2)
    fireEvent.click(screen.getAllByRole('button', { name: '删除条件' })[0])
    expect(screen.getAllByTestId('rule-leaf')).toHaveLength(1)
  })

  it('creates nested groups and copies conditions in advanced mode', () => {
    render(<VertexRuleEditor />)
    fireEvent.click(screen.getByRole('tab', { name: '高级模式' }))
    fireEvent.click(screen.getByRole('button', { name: '添加条件组' }))
    expect(screen.getAllByTestId('rule-group')).toHaveLength(2)
    fireEvent.click(screen.getAllByRole('button', { name: '复制条件' })[0])
    expect(screen.getAllByTestId('rule-leaf')).toHaveLength(3)
  })

  it('reorders sibling conditions with keyboard-reachable controls', () => {
    const initial = defaultTree()
    if (initial.root.kind !== 'group') throw new Error('expected group root')
    initial.root.children.push({ kind: 'leaf', field: 'site', operator: 'equals', value: { type: 'string', string: 'HDSky' } })
    let latest = initial
    render(<VertexRuleEditor value={initial} onChange={(nextValue) => { latest = nextValue }} />)
    fireEvent.click(screen.getAllByRole('button', { name: '下移条件' })[0])
    expect(childrenOf(latest)[0]).toMatchObject({ field: 'site' })
    expect(childrenOf(latest)[1]).toMatchObject({ field: 'title' })
  })

  it('keeps a shared AST when switching between simple and advanced modes', () => {
    const initial = defaultTree()
    let latest: VertexConditionTree = initial
    render(<VertexRuleEditor value={initial} onChange={(nextValue) => { latest = nextValue }} />)
    fireEvent.click(screen.getByRole('tab', { name: '高级模式' }))
    fireEvent.change(screen.getByLabelText('字段'), { target: { value: 'site' } })
    const advancedTree = latest
    fireEvent.click(screen.getByRole('tab', { name: '简单模式' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(latest).toEqual(advancedTree)
    expect(isSimpleConditionTree(latest)).toBe(true)
  })

  it('warns and preserves advanced semantics that simple mode cannot express', () => {
    const source = defaultTree()
    if (source.root.kind !== 'group') throw new Error('expected group root')
    const advanced: VertexConditionTree = { version: 1, root: { kind: 'group', operator: 'or', children: [source.root.children[0]] } }
    render(<VertexRuleEditor value={advanced} />)
    fireEvent.click(screen.getByRole('tab', { name: '简单模式' }))
    expect(screen.getByRole('status')).toHaveTextContent('高级语义')
  })

  it('round-trips simple drafts without changing leaf semantics', () => {
    const tree = defaultTree()
    const draft = toSimpleDraft(tree)
    expect(draft).not.toBeNull()
    expect(fromSimpleDraft(draft!)).toEqual(tree)
  })
})









