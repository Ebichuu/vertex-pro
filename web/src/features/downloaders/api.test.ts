import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../mocks/server'
import { loadDownloaderDetail, parseDownloaderPeriod } from './api'

function stats(range = 'DAY') {
  return {
    instance_id: 'instance-live', range, download_bytes: 4096, upload_bytes: 2048,
    series: [
      { bucket_start: '2026-10-22T00:00:00Z', download_bytes: 4096, upload_bytes: 2048, missing: false },
      { bucket_start: '2026-10-22T01:00:00Z', download_bytes: 0, upload_bytes: 0, missing: true },
    ], next_cursor: '',
  }
}

describe('真实下载器详情适配', () => {
  it('按真实实例获取三个 UTC 周期，保留小时流量而非捏造速率', async () => {
    const queries: string[] = []
    server.use(
      http.get('/api/v1/dashboard', () => HttpResponse.json({ instances: [{ id: 'instance-live', name: '真实下载器', sync_state: 'READY', freshness: 'FRESH', realtime_freshness: 'FRESH', realtime_source: 'SYNC_SERVER_STATE', download_rate: 0, upload_rate: 10, physical_free_bytes: 100, effective_free_bytes: 80, capacity_state: 'TRUSTED', sampled_at: '2026-10-22T02:00:00Z' }] })),
      http.get('/api/v1/stats/downloaders/summary', ({ request }) => {
        const query = new URL(request.url).searchParams
        expect(query.get('instance_id')).toBe('instance-live')
        queries.push(query.get('range')!)
        return HttpResponse.json(stats(query.get('range')!))
      }),
    )
    const detail = await loadDownloaderDetail('instance-live')
    expect(queries.sort()).toEqual(['DAY', 'MONTH', 'WEEK'])
    expect(detail.name).toBe('真实下载器')
    expect(detail.realtime.downloadRate).toBe(0)
    expect(detail.realtime.activeCount).toBeUndefined()
    expect(detail.periods.day.points[0]).toMatchObject({ downloadedBytes: 4096, uploadedBytes: 2048, missing: false })
    expect(detail.periods.day.points[0].downloadRate).toBeUndefined()
    expect(detail.periods.day.points[1].downloadedBytes).toBeUndefined()
    expect(detail.periods.day.counterResets).toBeUndefined()
    expect(detail.periods.day.missingPoints).toBe(1)
    expect(detail.capacity.totalBytes).toBeUndefined()
  })

  it('全缺失桶不能以汇总零伪装成无流量，不以 UNKNOWN/负数作零值', () => {
    const summary = parseDownloaderPeriod({ ...stats(), download_bytes: 0, upload_bytes: 0, series: [{ bucket_start: '2026-10-22T00:00:00Z', download_bytes: 0, upload_bytes: 0, missing: true }] }, 'instance-live', 'day')
    expect(summary.uploadedBytes).toBeUndefined()
    expect(summary.downloadedBytes).toBeUndefined()
    expect(summary.ratio).toBeUndefined()
    expect(() => parseDownloaderPeriod({ ...stats(), instance_id: 'another-instance' }, 'instance-live', 'day')).toThrow('INVALID_RESPONSE')
    expect(() => parseDownloaderPeriod({ ...stats(), next_cursor: 'more' }, 'instance-live', 'day')).toThrow('INVALID_RESPONSE')
  })

  it('陈旧数据明确标记，未接实时源的占位零不显示，统计故障不丢失详情', async () => {
    server.use(
      http.get('/api/v1/dashboard', () => HttpResponse.json({ instances: [{ id: 'instance-live', sync_state: 'READY', freshness: 'STALE', realtime_freshness: 'NOT_FRESH', realtime_source: '', download_rate: 0, upload_rate: 0, capacity_state: 'CAPACITY_UNCERTAIN', physical_free_bytes: -1, effective_free_bytes: 10 }] })),
      http.get('/api/v1/stats/downloaders/summary', () => HttpResponse.json({ error: { code: 'INTERNAL_ERROR', message: 'unavailable' } }, { status: 503 })),
    )
    const detail = await loadDownloaderDetail('instance-live')
    expect(detail.freshness).toBe('STALE')
    expect(detail.realtime.downloadRate).toBeUndefined()
    expect(detail.capacity.freeBytes).toBeUndefined()
    expect(detail.capacity.effectiveFreeBytes).toBeUndefined()
    expect(detail.periods.day.available).toBe(false)
    expect(detail.reasonCodes).toContain('STATS_UNAVAILABLE')
    await expect(loadDownloaderDetail('qb-a')).rejects.toThrow('NOT_FOUND')
  })
})
