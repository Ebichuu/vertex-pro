import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RuleEditorPage } from './RuleEditorPage'
import { defaultTree } from '../../features/rules/model'
import { fromRuleVersionPayload, toRuleVersionPayload, validateRuleDraft, type RuleEditorDraft } from '../../features/rules/rule-editor'

afterEach(cleanup)

const draft: RuleEditorDraft = {
  id: 'rule-4k',
  name: '4K 电影',
  enabled: true,
  priority: 100,
  revision: 3,
  conditionTree: defaultTree(),
  actions: {
    targetMode: 'FIXED_INSTANCE',
    targetInstanceId: 'qb-a',
    targetGroupId: '',
    category: 'movie/4k',
    tags: ['vertex-pro', '4k'],
    savePathPolicy: 'CATEGORY_SUBDIRECTORY',
    sequentialDownload: false,
    participationWindowMinutes: 20,
  },
}

const instances = [{ id: 'qb-a', label: 'qB-A · NVMe' }]
const groups = [{ id: 'fast', label: '高速组' }]

describe('UI-05 Rule Editor', () => {
  const sizeDraft: RuleEditorDraft = { ...draft, conditionTree: { version: 1, root: { kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number', number: 1_073_741_824 } } } }

  it('preserves an unedited legacy byte threshold when saving', async () => {
    const onSave = vi.fn((payload) => fromRuleVersionPayload(payload))
    render(<RuleEditorPage initialDraft={sizeDraft} instances={instances} groups={groups} onSave={onSave} />)
    expect(screen.getByLabelText('值')).toHaveValue('1.073741824')
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    await screen.findByText('规则已保存为 v4。')
    expect(JSON.parse(onSave.mock.calls[0][0].condition_tree_json)).toEqual(sizeDraft.conditionTree)
  })

  it('saves decimal GB as bytes in the immutable payload', async () => {
    const onSave = vi.fn((payload) => fromRuleVersionPayload(payload))
    render(<RuleEditorPage initialDraft={sizeDraft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1.5' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    await screen.findByText('规则已保存为 v4。')
    expect(JSON.parse(onSave.mock.calls[0][0].condition_tree_json).root.children[0].value).toEqual({ type: 'number', number: 1_500_000_000 })
    expect(sizeDraft.conditionTree.root).toMatchObject({ value: { number: 1_073_741_824 } })
  })

  it.each(['', '-1', 'abc', '0.0000000001', '9007199.254740992'])('rejects invalid size %j instead of submitting zero or the old value', async (input) => {
    const onSave = vi.fn()
    render(<RuleEditorPage initialDraft={sizeDraft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('值'), { target: { value: input } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('整数字节')
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.getByLabelText('值')).toHaveValue(input)
  })

  it('rejects incomplete ranges and allows saving after correction', async () => {
    const onSave = vi.fn((payload) => fromRuleVersionPayload(payload))
    render(<RuleEditorPage initialDraft={sizeDraft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('运算符'), { target: { value: 'between' } })
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1,' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1,10' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    await screen.findByText('规则已保存为 v4。')
    expect(JSON.parse(onSave.mock.calls[0][0].condition_tree_json).root.children[0].value).toEqual({ type: 'number_range', numbers: [1_000_000_000, 10_000_000_000] })
  })

  it('rejects invalid size ASTs at validation and serialization boundaries', () => {
    const invalid: RuleEditorDraft = { ...draft, conditionTree: { version: 1, root: { kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number' } } } }
    expect(validateRuleDraft(invalid).conditions).toContain('整数字节')
    expect(() => toRuleVersionPayload(invalid)).toThrow('整数字节')
  })

  it('blocks saving conditions on fields without a live data source', () => {
    const dead: RuleEditorDraft = { ...draft, conditionTree: { version: 1, root: { kind: 'group', operator: 'and', children: [
      { kind: 'leaf', field: 'promotion', operator: 'equals', value: { type: 'boolean', boolean: true } },
      { kind: 'leaf', field: 'storage_labels', operator: 'exists' },
    ] } } }
    expect(validateRuleDraft(dead).conditions).toContain('促销状态、存储标签')
    expect(validateRuleDraft(draft).conditions).toBeUndefined()
  })

  it('saves a size set in bytes and excludes UI input drafts from the payload', async () => {
    const onSave = vi.fn((payload) => fromRuleVersionPayload(payload))
    render(<RuleEditorPage initialDraft={sizeDraft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('运算符'), { target: { value: 'in' } })
    fireEvent.change(screen.getByLabelText('值'), { target: { value: '1,1.5,10' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    await screen.findByText('规则已保存为 v4。')
    expect(JSON.parse(onSave.mock.calls[0][0].condition_tree_json).root.children[0]).toEqual({ kind: 'leaf', field: 'size', operator: 'in', value: { type: 'number_set', numbers: [1_000_000_000, 1_500_000_000, 10_000_000_000] } })
  })

  it('renders rule identity, shared condition AST, target and controlled parameters', () => {
    render(<RuleEditorPage initialDraft={draft} instances={instances} groups={groups} />)
    expect(screen.getByRole('heading', { name: '编辑规则' })).toBeInTheDocument()
    expect(screen.getByLabelText('规则名称')).toHaveValue('4K 电影')
    expect(screen.getByLabelText('投递目标')).toHaveValue('qb-a')
    expect(screen.getByRole('region', { name: 'Vertex 规则编辑器' })).toBeInTheDocument()
    expect(screen.getByLabelText('保存路径策略')).toHaveValue('CATEGORY_SUBDIRECTORY')
  })

  it('saves an immutable next revision payload and preserves the AST round-trip', async () => {
    const onSave = vi.fn((payload) => ({ ...fromRuleVersionPayload(payload), revision: 4 }))
    render(<RuleEditorPage initialDraft={draft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('Tags'), { target: { value: 'vertex-pro, 4k, 4k, free' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByRole('status')).toHaveTextContent('v4')
    const payload = onSave.mock.calls[0][0]
    expect(payload).toMatchObject({ rule_id: 'rule-4k', revision: 4, condition_schema_version: 1, action_schema_version: 1 })
    expect(JSON.parse(payload.action_set_json)).toMatchObject({ target_instance_id: 'qb-a', target_group_id: null, tags: ['vertex-pro', '4k', 'free'] })
    expect(fromRuleVersionPayload(payload).conditionTree).toEqual(draft.conditionTree)
  })

  it('validates the target XOR and participation window before saving', async () => {
    const invalid = { ...draft, actions: { ...draft.actions, targetInstanceId: '', participationWindowMinutes: 0 } }
    const onSave = vi.fn((payload) => ({ ...fromRuleVersionPayload(payload), revision: 4 }))
    render(<RuleEditorPage initialDraft={invalid} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByText('请选择目标 qB。')).toBeInTheDocument()
    expect(screen.getByText('参与窗口需为 1 到 1440 分钟。')).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('round-trips an instance-group action without retaining a fixed target', () => {
    const groupDraft = { ...draft, actions: { ...draft.actions, targetMode: 'INSTANCE_GROUP' as const, targetInstanceId: 'qb-a', targetGroupId: 'fast' } }
    expect(fromRuleVersionPayload(toRuleVersionPayload(groupDraft, 7))).toMatchObject({ revision: 7, actions: { targetMode: 'INSTANCE_GROUP', targetInstanceId: '', targetGroupId: 'fast' } })
  })

  it('saves upload limit, batch cap and timed deletion in server units', async () => {
    const onSave = vi.fn((payload) => fromRuleVersionPayload(payload))
    render(<RuleEditorPage initialDraft={draft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByRole('spinbutton', { name: /上传限速/ }), { target: { value: '100' } })
    fireEvent.change(screen.getByRole('spinbutton', { name: /累计任务上限/ }), { target: { value: '10' } })
    fireEvent.change(screen.getByRole('spinbutton', { name: /做种超过多少小时删除/ }), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByText('启用定时删除前，请明确是否删除下载文件。')).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
    fireEvent.change(screen.getByRole('combobox', { name: '到期删除方式' }), { target: { value: 'TORRENT_ONLY' } })
    expect(screen.queryByText('启用定时删除前，请明确是否删除下载文件。')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存规则' }))
    expect(await screen.findByText('规则已保存为 v4。')).toBeInTheDocument()
    expect(JSON.parse(onSave.mock.calls[0][0].action_set_json)).toMatchObject({ upload_limit_bytes: 100_000_000, max_jobs: 10, seed_time_seconds: 3600, delete_mode: 'TORRENT_ONLY', start_inactive: false })
  })
})
