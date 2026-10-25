import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { PreparationPanel } from './PreparationPanel'
import type { Preparation, PreparationPage } from '../../features/sites/sites'

const item: Preparation = { id: '1', title: 'Test torrent', size_bytes: 1000000000, state: 'RATE_WAIT', attempts: 1, max_attempts: 5, rate_wait_ms: 2500, discovered_at: '', next_attempt_at: '', deadline_at: '', last_error_code: '', job_id: '' }
describe('逐条准备状态', () => {
  it('shows GB, waiting time and paginated results; switching aborts stale requests', async () => {
    let resolveOld!: (value: PreparationPage) => void
    const calls: AbortSignal[] = []
    const load = vi.fn(async (_id: string, cursor: string, signal: AbortSignal): Promise<PreparationPage> => {
      calls.push(signal)
      if (cursor) return new Promise(resolve => { resolveOld = resolve })
      return { items: [item], next_cursor: '1' }
    })
    const view = render(<PreparationPanel feedId="f" name="RSS" load={load} onClose={() => {}} />)
    expect(await screen.findByText('等待站点限流许可')).toBeInTheDocument()
    expect(screen.getByText('1.00 GB')).toBeInTheDocument()
    expect(screen.getByText('2.5 秒')).toBeInTheDocument()
    fireEvent.click(screen.getByText('下一页'))
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2))
    view.unmount()
    expect(calls.every(signal => signal.aborted)).toBe(true)
    await act(async () => resolveOld({ items: [{ ...item, title: 'Late response' }], next_cursor: '' }))
    expect(screen.queryByText('Late response')).not.toBeInTheDocument()
  })
  it('reports loading failure and supports retry', async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error('offline')).mockResolvedValue({ items: [], next_cursor: '' })
    render(<PreparationPanel feedId="f" name="RSS" load={load} onClose={() => {}} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('读取失败')
    fireEvent.click(screen.getByText('重新读取'))
    expect(await screen.findByText('暂无准备记录。')).toBeInTheDocument()
  })
})
