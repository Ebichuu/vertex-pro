import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { HistoryTable } from '../components/history/HistoryTable'
import { mergePages, type HistoryPageData } from '../features/history/history'

afterEach(() => {
  cleanup()
})

function entry(id: string, overrides: Partial<HistoryPageData['items'][number]> = {}): HistoryPageData['items'][number] {
  return {
    id,
    title: `种子 ${id}`,
    site: 'HDSky',
    downloader: 'qb-1',
    user_status: 'SEEDING',
    user_status_label: '做种中',
    discovered_at: '2026-08-29T07:00:00Z',
    ...overrides,
  }
}

describe('UI-07 History', () => {
  it('表格呈现种子、来源、目标下载器、用户状态', () => {
    render(
      <HistoryTable
        pages={[{ items: [entry('job-1'), entry('job-2')], next_cursor: '' }]}
        statusFilter=""
        onStatusFilterChange={() => {}}
        hasMore={false}
        loading={false}
        onLoadMore={() => {}}
        onOpen={() => {}}
      />,
    )
    const table = screen.getByRole('table', { name: '投递历史' })
    expect(table.querySelectorAll('tbody tr')).toHaveLength(2)
    expect(screen.getByText('种子 job-1')).toBeInTheDocument()
    expect(screen.getAllByText('HDSky')).toHaveLength(2)
    expect(screen.getAllByText('qb-1')).toHaveLength(2)
    expect(within(table).getAllByText('做种中')).toHaveLength(2)
  })

  it('已删除条目保留并显示删除时间与来源', () => {
    render(
      <HistoryTable
        pages={[{ items: [entry('job-del', { user_status: 'DELETED', user_status_label: '已删除', deleted_at: '2026-08-29T09:00:00Z', deletion_source: 'QB_USER_REMOVED' })], next_cursor: '' }]}
        statusFilter=""
        onStatusFilterChange={() => {}}
        hasMore={false}
        loading={false}
        onLoadMore={() => {}}
        onOpen={() => {}}
      />,
    )
    expect(screen.getByText('已删除', { selector: '.status-chip' })).toBeInTheDocument()
    expect(screen.getByText(/删除于 2026-08-29（手动删除）/)).toBeInTheDocument()
  })

  it('状态筛选触发回调且合并翻页去重', () => {
    const onStatusFilterChange = vi.fn()
    render(
      <HistoryTable
        pages={[
          { items: [entry('job-1')], next_cursor: 'cursor-1' },
          { items: [entry('job-1'), entry('job-2')], next_cursor: '' },
        ]}
        statusFilter=""
        onStatusFilterChange={onStatusFilterChange}
        hasMore={false}
        loading={false}
        onLoadMore={() => {}}
        onOpen={() => {}}
      />,
    )
    expect(screen.getAllByText('种子 job-1')).toHaveLength(1)
    fireEvent.change(screen.getByLabelText('状态筛选'), { target: { value: 'SEEDING' } })
    expect(onStatusFilterChange).toHaveBeenCalledWith('SEEDING')
    const filter = screen.getByLabelText('状态筛选')
    expect(within(filter).getAllByRole('option').map(option => option.textContent)).toEqual(['全部状态', '准备中', '下载中', '做种中', '已结束', '需关注'])
    fireEvent.change(filter, { target: { value: 'COMPLETED,DELETED' } })
    expect(onStatusFilterChange).toHaveBeenLastCalledWith('COMPLETED,DELETED')
  })

  it('hasMore 时渲染加载更多并触发回调', () => {
    const onLoadMore = vi.fn()
    render(
      <HistoryTable
        pages={[{ items: [entry('job-1')], next_cursor: 'cursor-1' }]}
        statusFilter=""
        onStatusFilterChange={() => {}}
        hasMore
        loading={false}
        onLoadMore={onLoadMore}
        onOpen={() => {}}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(onLoadMore).toHaveBeenCalledTimes(1)
  })

  it('mergePages 按 id 去重且保持顺序', () => {
    const merged = mergePages([
      { items: [entry('a'), entry('b')], next_cursor: 'c' },
      { items: [entry('b'), entry('c')], next_cursor: '' },
    ])
    expect(merged.map((item) => item.id)).toEqual(['a', 'b', 'c'])
  })

  it('点击种子标题进入对应详情', () => {
    const onOpen = vi.fn()
    render(
      <HistoryTable
        pages={[{ items: [entry('job-1')], next_cursor: '' }]}
        statusFilter=""
        onStatusFilterChange={() => {}}
        hasMore={false}
        loading={false}
        onLoadMore={() => {}}
        onOpen={onOpen}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: '查看 种子 job-1 详情' }))
    expect(onOpen).toHaveBeenCalledWith('job-1')
  })
})
