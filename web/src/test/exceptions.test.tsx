import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ExceptionsList } from '../components/exceptions/ExceptionsList'
import { actionsForJob, type ExceptionItem } from '../features/exceptions/exceptions'

afterEach(() => {
  cleanup()
})

function item(id: string, status: string, label: string): ExceptionItem {
  return { id, title: `种子 ${id}`, downloader: 'qb-1', user_status: status, user_status_label: label }
}

describe('UI-10 Exceptions', () => {
  it('UNKNOWN 任务展示完整受控操作矩阵', () => {
    const actions = actionsForJob('UNKNOWN')
    expect(actions.map((action) => action.action)).toEqual([
      'CONTINUE_RECONCILIATION',
      'CONFIRM_EFFECT',
      'CONFIRM_NO_EFFECT_AND_RETRY',
      'EXPIRE_UNRESOLVED',
    ])
  })

  it('EXPIRED_UNKNOWN 仅允许两条人工闭环且无重试', () => {
    const actions = actionsForJob('EXPIRED_UNKNOWN')
    expect(actions.map((action) => action.action)).toEqual(['CONFIRM_EFFECT', 'CONFIRM_NO_EFFECT_AND_EXPIRE'])
    expect(actions.some((action) => action.action === 'CONFIRM_NO_EFFECT_AND_RETRY')).toBe(false)
  })

  it('每个操作先弹确认窗，确认后才执行', async () => {
    const onAction = vi.fn()
    render(
      <ExceptionsList
        items={[item('job-1', 'UNKNOWN', '需确认')]}
        uncertainInstances={[]}
        onAction={onAction}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: '人工确认已存在' }))
    const dialog = screen.getByRole('dialog', { name: '确认受控操作' })
    expect(dialog).toBeInTheDocument()
    expect(onAction).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '确认执行' }))
    await vi.waitFor(() => expect(onAction).toHaveBeenCalledWith('job-1', 'CONFIRM_EFFECT', expect.stringContaining('job-1')))
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('取消弹窗不执行任何操作', () => {
    const onAction = vi.fn()
    render(
      <ExceptionsList
        items={[item('job-1', 'EXPIRED_UNKNOWN', '需确认')]}
        uncertainInstances={[]}
        onAction={onAction}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: '确认无副作用并结束' }))
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(onAction).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('容量不可信实例以告警横幅展示，全页无再 Add 入口', () => {
    render(
      <ExceptionsList
        items={[item('job-2', 'UNKNOWN', '需确认')]}
        uncertainInstances={['qb-2']}
        onAction={() => {}}
      />,
    )
    expect(screen.getByRole('alert').textContent).toContain('容量不可信实例：qb-2')
    const buttons = screen.queryAllByRole('button').map((button) => button.textContent ?? '')
    expect(buttons.every((text) => !text.includes('Add') && !text.includes('再投'))).toBe(true)
  })
})
