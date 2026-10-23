import { describe, expect, it } from 'vitest'
import { ruleDraftFromAPI, ruleError, ruleSummary } from './api'
import { ApiRequestError } from '../../generated/api-client'
import { toRuleVersionPayload, validateRuleDraft } from './rule-editor'

describe('persisted action compatibility', () => {
  const actions = { target_mode: 'INSTANCE_GROUP', target_group_id: 'group-a', category: 'Test', tags: ['test'], upload_limit_bytes: 100_000_000, max_jobs: 10, seed_time_seconds: 3600, delete_mode: 'TORRENT_ONLY', sequential_download: true }
  const row = { id: 'r', name: 'Rule', enabled: false, revision: 1, priority: 10, updated_at: '2026-10-23T00:00:00Z', definition: { condition_tree: { version: 1, root: { kind: 'leaf', field: 'title', operator: 'exists' } }, action_set_json: actions } }
  it.each([actions, JSON.stringify(actions)])('preserves object and string actions while editing', action_set_json => {
    const draft = ruleDraftFromAPI({ ...row, definition: { ...row.definition, action_set_json } })
    expect(JSON.parse(toRuleVersionPayload(draft).action_set_json)).toMatchObject(actions)
  })
  it('requires explicit file deletion semantics', () => {
    const draft = ruleDraftFromAPI(row)
    draft.actions.deleteMode = ''
    expect(validateRuleDraft(draft).limits).toContain('是否删除下载文件')
  })
  it('shows a reported qui policy status without inventing one for older servers', () => {
    expect(ruleSummary({ ...row, policy_status: '同步中' }).policyStatus).toBe('同步中')
    expect(ruleSummary(row).policyStatus).toBeUndefined()
  })
  it('keeps the safe server message for an unconfirmed qui policy stop', () => {
    const error = new ApiRequestError(502, { code: 'QUI_UNREACHABLE', message: '本地已停用，但 qui 删除策略尚未确认', request_id: 'test', retryable: true })
    expect(ruleError(error)).toBe('本地已停用，但 qui 删除策略尚未确认')
  })
})
