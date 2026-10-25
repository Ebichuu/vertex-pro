import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { DashboardCards } from '../components/dashboard/DashboardCards'
import { formatBytes, formatRate, type DashboardData } from '../features/dashboard/dashboard'
import { DashboardSummary } from '../components/dashboard/DashboardSummary'

afterEach(() => {
  cleanup()
})

function card(overrides: Partial<DashboardData['instances'][number]> = {}): DashboardData['instances'][number] {
  return {
    id: 'qb-1',
    sync_state: 'READY',
    snapshot_revision: 5,
    freshness: 'FRESH',
    capacity_state: 'TRUSTED',
    physical_free_bytes: 100 * 1024 * 1024 * 1024,
    effective_free_bytes: 90 * 1024 * 1024 * 1024,
    storage_state: 'VERIFIED',
    reason_codes: [],
    download_rate: 2048,
    upload_rate: 256,
    downloading_count: 3,
    seeding_count: 7,
    realtime_freshness: 'FRESH',
    realtime_source: 'SYNC_SERVER_STATE',
    ...overrides,
  }
}

describe('UI-02 Dashboard', () => {
  it('汇总排除过期、未知和不可信容量，显示实际覆盖范围', () => {
    const data: DashboardData = { jobs_total: 1, user_status_counts: {}, instances: [
      card({ id: 'live', download_rate: 100_000_000, upload_rate: 200_000_000, effective_free_bytes: 2_000_000_000_000 }),
      card({ id: 'old', freshness: 'STALE', download_rate: 900_000_000, effective_free_bytes: 8_000_000_000_000 }),
      card({ id: 'untrusted', realtime_freshness: 'STALE', capacity_state: 'UNCERTAIN', download_rate: 900_000_000, effective_free_bytes: 8_000_000_000_000 }),
    ] }
    render(<DashboardSummary data={data} />)
    expect(screen.getByText('100.00 MB/s')).toBeInTheDocument()
    expect(screen.getByText('200.00 MB/s')).toBeInTheDocument()
    expect(screen.getByText('2.00 TB')).toBeInTheDocument()
    expect(screen.getAllByText('仅 1/3 个实例有有效数据')).toHaveLength(3)
  })
  it('没有有效监控值时显示未知而不是零速度或零容量', () => {
    render(<DashboardSummary data={{ jobs_total: 0, user_status_counts: {}, instances: [card({ download_rate: -1, upload_rate: Number.NaN, effective_free_bytes: -1 })] }} />)
    expect(screen.getAllByText('未知')).toHaveLength(3)
    expect(screen.getAllByText('暂无有效数据')).toHaveLength(3)
  })
  it('优先显示在线实例，支持筛选且不丢失异常原因', () => {
    render(<DashboardCards data={{ jobs_total: 0, user_status_counts: {}, instances: [card({ id: 'off', sync_state: 'OFFLINE', reason_codes: ['SNAPSHOT_STALE'] }), card({ id: 'live' })] }} />)
    const grid = screen.getByRole('list', { name: '实例卡片' })
    expect(grid.firstElementChild).toHaveAttribute('aria-label', '实例 live')
    fireEvent.change(screen.getByRole('combobox', { name: '筛选下载器' }), { target: { value: 'attention' } })
    expect(within(grid).queryByText('实例 live')).not.toBeInTheDocument()
    expect(within(grid).getByText('SNAPSHOT_STALE')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('combobox', { name: '筛选下载器' }), { target: { value: 'all' } })
    expect(within(grid).getByText('实例 live')).toBeInTheDocument()
  })
  it('一屏渲染 20 个实例卡片', () => {
    const instances = Array.from({ length: 20 }, (_, index) => card({ id: `qb-${index + 1}` }))
    render(<DashboardCards data={{ jobs_total: 42, user_status_counts: {}, instances }} />)
    const grid = screen.getByRole('list', { name: '实例卡片' })
    expect(grid.querySelectorAll('li')).toHaveLength(20)
    expect(screen.getByText('实例 20 个 · 任务 42 个')).toBeInTheDocument()
  })

  it('stale/offline 实例显示数据不新鲜与原因码', () => {
    const data: DashboardData = {
      jobs_total: 0,
      user_status_counts: {},
      instances: [
        card({ id: 'qb-stale', sync_state: 'STALE', freshness: 'NOT_FRESH', reason_codes: ['SNAPSHOT_STALE'] }),
        card({ id: 'qb-off', sync_state: 'OFFLINE', freshness: 'NOT_FRESH', reason_codes: ['SNAPSHOT_STALE'] }),
      ],
    }
    render(<DashboardCards data={data} />)
    expect(screen.getAllByText('数据不新鲜')).toHaveLength(2)
    expect(screen.getAllByText('SNAPSHOT_STALE')).toHaveLength(2)
    expect(screen.getByText('离线')).toHaveAttribute('title', 'OFFLINE')
  })

  it('速率与容量按口径格式化，未知容量不美化', () => {
    expect(formatRate(2048)).toBe('<0.01 MB/s')
    expect(formatRate(3 * 1024 * 1024)).toBe('3.15 MB/s')
    expect(formatBytes(100 * 1024 * 1024 * 1024)).toBe('0.11 TB')
    expect(formatBytes(-1)).toBe('未知')
    const { container } = render(<DashboardCards data={{ jobs_total: 0, user_status_counts: {}, instances: [card()] }} />)
    expect(container.textContent).toContain('<0.01 MB/s')
    expect(container.textContent).toContain('0.11 TB')
  })
})
