import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../mocks/server'
import { loadHistoryDetail, parseHistoryDetail } from './api'

describe('真实历史详情适配', () => {
  it('请求真实 job ID，保留已删除事实和最后观测数据，内部状态仅进入诊断', async () => {
    let requestedId: unknown
    server.use(http.get('/api/v1/jobs/:id', ({ params }) => {
      requestedId = params.id
      return HttpResponse.json({
        id: params.id, title: '真实任务', site: '测试站点', downloader: 'instance-42',
        user_status: 'DELETED', user_status_label: '服务端标签', discovered_at: '2026-10-22T01:00:00Z',
        deleted_at: '2026-10-22T02:00:00Z', deletion_source: 'SYNC_REMOVED', job_state: 'RECONCILED_CONFIRMED',
        last_transfer: { state_label: '做种中', uploaded_bytes: 2048, downloaded_bytes: null, ratio: 2.5 },
        timeline: [{ event_id: 4, stage: '确认', to_state: 'HASH_SEEN', occurred_at: '2026-10-22T01:01:00Z' }],
      })
    }))
    const detail = await loadHistoryDetail('live-job')
    expect(requestedId).toBe('live-job')
    expect(detail).toMatchObject({ id: 'live-job', title: '真实任务', downloader: 'instance-42', userStatus: { user_status: 'DELETED', user_status_label: '已删除' } })
    expect(detail.transfer.uploadedBytes).toBe(2048)
    expect(detail.transfer.downloadedBytes).toBeUndefined()
    expect(detail.deletion?.confirmedAt).toBe('2026-10-22T02:00:00Z')
    expect(detail.timeline[0].label).toBe('下载器已确认种子存在')
    expect(detail.diagnostics).toContainEqual({ label: '事件 4 目标状态', value: 'HASH_SEEN' })
  })

  it('缺失、null、非法数字与未知状态不会变成 fixture 或成功事实', () => {
    const detail = parseHistoryDetail({ id: 'unknown', job_state: 'UNKNOWN', timeline: null, last_transfer: { uploaded_bytes: -1, downloaded_bytes: '100', ratio: null } }, 'unknown')
    expect(detail.title).toBe('未命名种子')
    expect(detail.userStatus.user_status).toBe('NEEDS_CONFIRMATION')
    expect(detail.transfer).toEqual({ stateLabel: '暂无数据', uploadedBytes: undefined, downloadedBytes: undefined, ratio: undefined, observedAt: undefined })
    expect(detail.rulesKnown).toBe(false)
    expect(detail.finalDecision.tags).toBeUndefined()
    expect(detail.timeline).toEqual([])
    expect(detail.deletion).toBeUndefined()
    expect(() => parseHistoryDetail({ id: 'different' }, 'unknown')).toThrow('INVALID_RESPONSE')
  })

  it('404 保留失败，不用本地示例兜底', async () => {
    server.use(http.get('/api/v1/jobs/:id', () => HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'job not found', retryable: false } }, { status: 404 })))
    await expect(loadHistoryDetail('history-job-1')).rejects.toThrow()
  })
})
