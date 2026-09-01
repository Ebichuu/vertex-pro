import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RuleEditorPage } from './RuleEditorPage'
import { defaultTree } from '../../features/rules/model'
import { fromRuleVersionPayload, toRuleVersionPayload, type RuleEditorDraft } from '../../features/rules/rule-editor'

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
  it('renders rule identity, shared condition AST, target and controlled parameters', () => {
    render(<RuleEditorPage initialDraft={draft} instances={instances} groups={groups} />)
    expect(screen.getByRole('heading', { name: '编辑规则' })).toBeInTheDocument()
    expect(screen.getByLabelText('规则名称')).toHaveValue('4K 电影')
    expect(screen.getByLabelText('投递目标')).toHaveValue('qb-a')
    expect(screen.getByRole('region', { name: 'Vertex 规则编辑器' })).toBeInTheDocument()
    expect(screen.getByLabelText('保存路径策略')).toHaveValue('CATEGORY_SUBDIRECTORY')
  })

  it('saves an immutable next revision payload and preserves the AST round-trip', async () => {
    const onSave = vi.fn()
    render(<RuleEditorPage initialDraft={draft} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.change(screen.getByLabelText('Tags'), { target: { value: 'vertex-pro, 4k, 4k, free' } })
    fireEvent.click(screen.getByRole('button', { name: '保存本地版本' }))
    expect(await screen.findByRole('status')).toHaveTextContent('本地 v4')
    const payload = onSave.mock.calls[0][0]
    expect(payload).toMatchObject({ rule_id: 'rule-4k', revision: 4, condition_schema_version: 1, action_schema_version: 1 })
    expect(JSON.parse(payload.action_set_json)).toMatchObject({ target_instance_id: 'qb-a', target_group_id: null, tags: ['vertex-pro', '4k', 'free'] })
    expect(fromRuleVersionPayload(payload).conditionTree).toEqual(draft.conditionTree)
  })

  it('validates the target XOR and participation window before saving', async () => {
    const invalid = { ...draft, actions: { ...draft.actions, targetInstanceId: '', participationWindowMinutes: 0 } }
    const onSave = vi.fn()
    render(<RuleEditorPage initialDraft={invalid} instances={instances} groups={groups} onSave={onSave} />)
    fireEvent.click(screen.getByRole('button', { name: '保存本地版本' }))
    expect(await screen.findByText('请选择目标 qB。')).toBeInTheDocument()
    expect(screen.getByText('参与窗口需为 1 到 1440 分钟。')).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('round-trips an instance-group action without retaining a fixed target', () => {
    const groupDraft = { ...draft, actions: { ...draft.actions, targetMode: 'INSTANCE_GROUP' as const, targetInstanceId: 'qb-a', targetGroupId: 'fast' } }
    expect(fromRuleVersionPayload(toRuleVersionPayload(groupDraft, 7))).toMatchObject({ revision: 7, actions: { targetMode: 'INSTANCE_GROUP', targetInstanceId: '', targetGroupId: 'fast' } })
  })
})
