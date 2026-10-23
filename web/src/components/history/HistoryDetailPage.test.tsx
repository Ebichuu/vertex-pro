import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { HistoryDetailPage } from './HistoryDetailPage'
import { HIDDEN_HISTORY_TERMS, loadLocalHistoryDetail, sortHistoryTimeline, type HistoryDetail, type HistoryDetailLoader } from '../../features/history/history-detail'
import { parseHistoryDetail } from '../../features/history/api'

afterEach(cleanup)

describe('UI-08 History Detail', () => {
  it('真实详情缺失事实时显示未知，添加请求不冒充下载确认', async () => {
    const detail = parseHistoryDetail({
      id: 'real-job', title: '真实待确认任务', user_status: 'NEEDS_CONFIRMATION', job_state: 'UNKNOWN',
      timeline: [{ event_id: 3, stage: '添加', to_state: 'ADD_SENT', occurred_at: '2026-10-22T01:00:00Z' }],
      last_transfer: { uploaded_bytes: null, downloaded_bytes: -1, ratio: null },
    }, 'real-job')
    render(<HistoryDetailPage jobId="real-job" loadDetail={async () => detail} onBack={() => {}} />)
    const ordinary = await screen.findByTestId('ordinary-history-detail')
    expect(ordinary).toHaveTextContent('已发起添加请求，等待确认')
    expect(ordinary).toHaveTextContent('暂无规则命中信息')
    expect(ordinary).not.toHaveTextContent('0 条规则命中')
    expect(ordinary).not.toHaveTextContent('下载器已确认种子存在')
    expect(ordinary).not.toHaveTextContent('0 B')
    expect(ordinary).not.toHaveTextContent('NaN')
    expect(ordinary).not.toHaveTextContent('UNKNOWN')
    expect(screen.queryByText('LOCAL ONLY')).not.toBeInTheDocument()
  })
  it('展示最后状态、上传分享率、规则聚合和删除确认', async () => {
    render(<HistoryDetailPage jobId="history-job-1" loadDetail={loadLocalHistoryDetail} onBack={() => {}} />)

    expect(await screen.findByRole('heading', { name: 'The.Last.Horizon.2026.2160p.UHD.BluRay' })).toBeInTheDocument()
    expect(screen.getByText('已删除', { selector: '.status-chip' })).toBeInTheDocument()
    expect(screen.getByText('120.00 GiB')).toBeInTheDocument()
    expect(screen.getByText('4.12')).toBeInTheDocument()
    expect(screen.getByText('qB 中手动删除，经同步确认')).toBeInTheDocument()
    expect(screen.getByText('4K 电影：优先级 100，高于其他命中规则')).toBeInTheDocument()
    expect(screen.getByText('movie/4k')).toBeInTheDocument()
  })

  it('按顺序展示用户语言时间线并隔离内部诊断术语', async () => {
    render(<HistoryDetailPage jobId="history-job-1" loadDetail={loadLocalHistoryDetail} onBack={() => {}} />)
    const ordinary = await screen.findByTestId('ordinary-history-detail')
    const timeline = within(ordinary).getByRole('list', { name: '投递时间线事件' })
    expect(within(timeline).getAllByRole('listitem').map((item) => item.querySelector('.history-timeline-copy span')?.textContent)).toEqual([
      '发现', '准备', '添加', '确认', '上传', '删除',
    ])
    for (const term of HIDDEN_HISTORY_TERMS) {
      expect(ordinary.textContent?.toLocaleLowerCase()).not.toContain(term.toLocaleLowerCase())
    }
    const diagnostics = screen.getByText('高级诊断').closest('details')
    expect(diagnostics).not.toHaveAttribute('open')
    expect(diagnostics).toHaveTextContent('Placement state')
    expect(diagnostics).toHaveTextContent('Reservation')
    expect(diagnostics).toHaveTextContent('SubmitIntent')
  })

  it('加载失败后可重新加载且返回历史回调可用', async () => {
    const onBack = vi.fn()
    const loader: HistoryDetailLoader = vi.fn()
      .mockRejectedValueOnce(new Error('RESERVED fixture unavailable'))
      .mockImplementation(loadLocalHistoryDetail)
    render(<HistoryDetailPage jobId="history-job-2" loadDetail={loader} onBack={onBack} />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('未找到或暂时无法读取这条投递记录')
    expect(alert).not.toHaveTextContent('RESERVED')
    fireEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect(await screen.findByRole('heading', { name: 'Planet.Earth.III.S01.1080p.BluRay' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '← 返回历史' }))
    expect(onBack).toHaveBeenCalledTimes(1)
    expect(loader).toHaveBeenCalledTimes(2)
  })

  it('活动 fixture 的摘要、规则和时间线事实一致，缺失事实明确显示暂无数据', async () => {
    render(<HistoryDetailPage jobId="history-job-2" loadDetail={loadLocalHistoryDetail} onBack={() => {}} />)
    const ordinary = await screen.findByTestId('ordinary-history-detail')
    expect(ordinary).toHaveTextContent('qB-B · Bulk')
    expect(ordinary).toHaveTextContent('Freeleech')
    expect(ordinary).toHaveTextContent('18.25 GiB')
    expect(ordinary).not.toHaveTextContent('qB-A · NVMe')
    expect(ordinary).not.toHaveTextContent('4K 电影')
    expect(within(ordinary).getAllByText('暂无数据')).toHaveLength(3)
  })

  it('未知 jobId 不伪造成功历史，并按时间与事件 id 稳定排序', async () => {
    await expect(loadLocalHistoryDetail('not-a-real-job')).rejects.toThrow('HISTORY_DETAIL_NOT_FOUND')
    const base = await loadLocalHistoryDetail('history-job-1')
    const sorted = sortHistoryTimeline([
      { ...base.timeline[2], id: 'event-10', occurredAt: '2026-08-29T07:00:08Z' },
      { ...base.timeline[0], id: 'event-1', occurredAt: '2026-08-29T07:00:00Z' },
      { ...base.timeline[1], id: 'event-2', occurredAt: '2026-08-29T07:00:08Z' },
    ])
    expect(sorted.map((item) => item.id)).toEqual(['event-1', 'event-2', 'event-10'])
  })

  it('切换 jobId 时立即隐藏旧记录，直到新记录加载完成', async () => {
    const first = await loadLocalHistoryDetail('history-job-1')
    const second = await loadLocalHistoryDetail('history-job-2')
    let resolveSecond: ((detail: HistoryDetail) => void) | undefined
    const loader: HistoryDetailLoader = vi.fn((jobId: string): Promise<HistoryDetail> => jobId === first.id
      ? Promise.resolve(first)
      : new Promise<HistoryDetail>((resolve) => { resolveSecond = resolve }))
    const { rerender } = render(<HistoryDetailPage jobId={first.id} loadDetail={loader} onBack={() => {}} />)
    expect(await screen.findByRole('heading', { name: first.title })).toBeInTheDocument()
    rerender(<HistoryDetailPage jobId={second.id} loadDetail={loader} onBack={() => {}} />)
    expect(screen.queryByRole('heading', { name: first.title })).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('正在加载投递记录')
    resolveSecond?.(second)
    expect(await screen.findByRole('heading', { name: second.title })).toBeInTheDocument()
  })
})
