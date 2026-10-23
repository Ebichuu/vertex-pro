import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/react'
import { duplicateRule } from '../../features/rules/rule-list'
import { RuleList } from './RuleList'
import { sortRules, updateRuleOptimistically, type RuleSummary } from '../../features/rules/rule-list'
import type { RuleUpdateResult } from '../../features/rules/rule-list'

afterEach(cleanup)
let copyID = 0
const persistence = { onUpdateRule: updateRuleOptimistically, onDuplicateRule: async (rule: RuleSummary) => duplicateRule(rule, `copy-${++copyID}`, `${rule.name} 副本`) }

const rules: RuleSummary[] = [
  { id: 'rule-b', name: '低优先级', priority: 10, enabled: true, revision: 1, matchCount: 7, target: 'qB-B', updatedAt: '2026-08-25T00:00:00Z' },
  { id: 'rule-a', name: '高优先级', priority: 100, enabled: true, revision: 1, matchCount: 42, target: '高速组', updatedAt: '2026-08-25T00:00:00Z' },
]

describe('RuleList', () => {
  it('shows match count and target, then toggles enabled state with a revision increment', async () => {
    let latest = rules
    render(<RuleList rules={rules} {...persistence} onChange={(nextRules) => { latest = nextRules }} />)
    expect(screen.getAllByRole('article')[0]).toHaveTextContent('高优先级')
    expect(screen.getAllByRole('article')[0]).toHaveTextContent('42')
    expect(screen.getAllByRole('article')[0]).toHaveTextContent('高速组')
    fireEvent.click(screen.getByRole('checkbox', { name: '高优先级 启用' }))
    await screen.findByText(/v2/)
    expect(latest.find((rule) => rule.id === 'rule-a')).toMatchObject({ enabled: false, revision: 2 })
  })

  it('duplicates a rule disabled with a fresh revision', async () => {
    let latest = rules
    render(<RuleList rules={rules} {...persistence} onChange={(nextRules) => { latest = nextRules }} />)
    fireEvent.click(screen.getAllByRole('button', { name: '复制' })[0])
    await waitFor(() => expect(latest).toHaveLength(3))
    expect(latest[2]).toMatchObject({ name: '高优先级 副本', enabled: false, revision: 1, matchCount: 0 })
  })

  it('returns a 409 result for a stale optimistic revision', () => {
    const result = updateRuleOptimistically(rules[0], 0, { enabled: false })
    expect(result).toMatchObject({ ok: false, status: 409, code: 'REVISION_CONFLICT' })
  })

  it('surfaces a 409 conflict and loads the current revision', async () => {
    const current = { ...rules[1], revision: 4, enabled: false }
    render(<RuleList rules={rules} onUpdateRule={() => ({ ok: false, status: 409, code: 'REVISION_CONFLICT', current })} />)
    fireEvent.click(screen.getByRole('checkbox', { name: '高优先级 启用' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('已加载当前 v4')
    expect(screen.getByRole('checkbox', { name: '高优先级 启用' })).not.toBeChecked()
  })

  it('reorders after a priority edit and creates unique duplicate identities', async () => {
    let latest = rules
    render(<RuleList rules={rules} {...persistence} onChange={(nextRules) => { latest = nextRules }} />)
    const priority = screen.getByRole('spinbutton', { name: '低优先级 优先级' })
    fireEvent.change(priority, { target: { value: '200' } })
    fireEvent.blur(priority)
    await screen.findByDisplayValue('200')
    await waitFor(() => expect(screen.getAllByRole('article')[0]).toHaveTextContent('低优先级'))
    fireEvent.click(screen.getAllByRole('button', { name: '复制' })[0])
    await waitFor(() => expect(latest).toHaveLength(3))
    fireEvent.click(screen.getAllByRole('button', { name: '复制' })[0])
    await waitFor(() => expect(latest).toHaveLength(4))
    expect(new Set(latest.map((rule) => rule.id)).size).toBe(latest.length)
  })

  it('uses the winner ordering as the stable list order', () => {
    expect(sortRules([{ ...rules[0], priority: 10, id: 'b' }, { ...rules[0], priority: 10, id: 'a' }]).map((rule) => rule.id)).toEqual(['a', 'b'])
  })

  it('shows qui policy status and removes an archived rule only after success', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const onArchiveRule = vi.fn().mockResolvedValue(undefined)
    const shown = [{ ...rules[0], policyStatus: '已同步' }]
    render(<RuleList rules={shown} onArchiveRule={onArchiveRule} />)
    expect(screen.getByText(/qui 策略：已同步/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '删除' }))
    await waitFor(() => expect(onArchiveRule).toHaveBeenCalledWith(shown[0]))
    await screen.findByText(/暂无规则/)
    expect(confirm).toHaveBeenCalledTimes(1)
    confirm.mockRestore()
  })

  it('keeps an in-flight edit while refreshing the policy status', async () => {
    let finish!: (result: RuleUpdateResult) => void
    const onUpdateRule = () => new Promise<RuleUpdateResult>(resolve => { finish = resolve })
    const { rerender } = render(<RuleList rules={rules} onUpdateRule={onUpdateRule} />)
    fireEvent.click(screen.getByRole('checkbox', { name: '高优先级 启用' }))
    const refreshed = rules.map(rule => rule.id === 'rule-a' ? { ...rule, policyStatus: 'qui 删除策略已启用' } : rule)
    rerender(<RuleList rules={refreshed} onUpdateRule={onUpdateRule} />)
    expect(screen.getByText(/qui 删除策略已启用/)).toBeInTheDocument()
    finish({ ok: true, rule: { ...refreshed[1], enabled: false, revision: 2 } })
    await waitFor(() => expect(screen.getByRole('checkbox', { name: '高优先级 启用' })).not.toBeChecked())
    expect(screen.getByText(/qui 删除策略已启用/)).toBeInTheDocument()
  })
})
