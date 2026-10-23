import { ApiClient, ApiRequestError } from '../../generated/api-client'
import type { DataFreshness, DownloaderDetail, DownloaderDetailLoader, DownloaderPeriodKey, DownloaderPeriodSummary } from './downloader-detail'

const client = new ApiClient(window.location.origin)
type JsonObject = Record<string, unknown>
const PERIODS: ReadonlyArray<{ key: DownloaderPeriodKey; range: string; label: string }> = [
  { key: 'day', range: 'DAY', label: '本日（UTC）' },
  { key: 'week', range: 'WEEK', label: '本周（UTC）' },
  { key: 'month', range: 'MONTH', label: '本月（UTC）' },
]

function object(value: unknown): JsonObject | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as JsonObject : undefined
}

function text(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function number(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function time(value: unknown): string | undefined {
  const result = text(value)
  return result && Number.isFinite(Date.parse(result)) ? result : undefined
}

function freshness(value: unknown): DataFreshness {
  if (value === 'FRESH') return 'FRESH'
  if (value === 'STALE' || value === 'NOT_FRESH') return 'STALE'
  return 'UNKNOWN'
}

export function parseDownloaderPeriod(raw: unknown, instanceId: string, key: DownloaderPeriodKey): DownloaderPeriodSummary {
  const data = object(raw)
  const period = PERIODS.find((item) => item.key === key)!
  if (!data || data.instance_id !== instanceId || data.range !== period.range || !Array.isArray(data.series) || text(data.next_cursor)) {
    throw new Error('DOWNLOADER_STATS_INVALID_RESPONSE')
  }
  const points = data.series.flatMap((value) => {
    const point = object(value)
    const timestamp = time(point?.bucket_start)
    if (!point || !timestamp) throw new Error('DOWNLOADER_STATS_INVALID_RESPONSE')
    const downloadedBytes = number(point.download_bytes)
    const uploadedBytes = number(point.upload_bytes)
    const missing = point.missing !== false || downloadedBytes === undefined || uploadedBytes === undefined
    return [{
      timestamp,
      label: new Intl.DateTimeFormat('zh-CN', { timeZone: 'UTC', month: 'numeric', day: 'numeric', hour: '2-digit', hour12: false }).format(new Date(timestamp)),
      downloadedBytes: missing ? undefined : downloadedBytes,
      uploadedBytes: missing ? undefined : uploadedBytes,
      missing,
      counterReset: typeof point.counter_reset === 'boolean' ? point.counter_reset : undefined,
    }]
  })
  // A server total of 0 over entirely missing buckets is not a measured zero.
  const measured = points.some((point) => !point.missing)
  const uploadedBytes = measured ? number(data.upload_bytes) : undefined
  const downloadedBytes = measured ? number(data.download_bytes) : undefined
  return {
    key, label: period.label, metric: 'bytes', available: true,
    uploadedBytes, downloadedBytes,
    ratio: uploadedBytes !== undefined && downloadedBytes !== undefined && downloadedBytes > 0 ? uploadedBytes / downloadedBytes : undefined,
    points,
    missingPoints: points.filter((point) => point.missing).length,
    counterResets: number(data.counter_resets) ?? (points.length > 0 && points.every((point) => point.counterReset !== undefined) ? points.filter((point) => point.counterReset).length : undefined),
  }
}

function unavailablePeriod(key: DownloaderPeriodKey): DownloaderPeriodSummary {
  return { key, label: PERIODS.find((period) => period.key === key)!.label, metric: 'bytes', available: false, points: [], missingPoints: 0 }
}

export const loadDownloaderDetail: DownloaderDetailLoader = async (downloaderId) => {
  if (!downloaderId.trim()) throw new Error('DOWNLOADER_DETAIL_NOT_FOUND')
  const dashboard = object(await client.getDashboard())
  if (!dashboard || !Array.isArray(dashboard.instances)) throw new Error('DOWNLOADER_DETAIL_INVALID_RESPONSE')
  const card = dashboard.instances.map(object).find((value) => value?.id === downloaderId)
  if (!card) throw new Error('DOWNLOADER_DETAIL_NOT_FOUND')
  const anchor = new Date().toISOString()
  const periods = await Promise.all(PERIODS.map(async (period) => {
    try {
      return parseDownloaderPeriod(await client.getDownloaderSummary({ query: { instance_id: downloaderId, range: period.range, anchor } }), downloaderId, period.key)
    } catch (error) {
      if (error instanceof ApiRequestError && (error.status === 401 || error.status === 403)) throw error
      return unavailablePeriod(period.key)
    }
  }))
  const reasons = Array.isArray(card.reason_codes) ? card.reason_codes.filter((value): value is string => typeof value === 'string') : []
  const realtimeFreshness = text(card.realtime_source) ? freshness(card.realtime_freshness) : 'UNKNOWN'
  const snapshotFreshness = freshness(card.freshness)
  const sampledAt = time(card.sampled_at ?? card.realtime_observed_at)
  const capacityState = text(card.capacity_state) ?? 'UNKNOWN'
  const detail: DownloaderDetail = {
    id: downloaderId,
    name: text(card.name) ?? downloaderId,
    health: card.sync_state === 'OFFLINE' ? 'OFFLINE' : card.sync_state === 'READY' && snapshotFreshness === 'FRESH' && realtimeFreshness === 'FRESH' ? 'AVAILABLE' : card.sync_state ? 'DEGRADED' : 'UNKNOWN',
    freshness: snapshotFreshness === 'STALE' || realtimeFreshness === 'STALE' ? 'STALE' : snapshotFreshness === 'FRESH' && realtimeFreshness === 'FRESH' ? 'FRESH' : 'UNKNOWN',
    observedAt: sampledAt,
    reasonCodes: [...new Set([...reasons, ...(periods.some((period) => period.available === false) ? ['STATS_UNAVAILABLE'] : [])])],
    realtime: {
      // NOT_FRESH/UNKNOWN rates are still historical observations only when the
      // API names their source; default placeholder zeros must stay unknown.
      downloadRate: text(card.realtime_source) ? number(card.download_rate) : undefined,
      uploadRate: text(card.realtime_source) ? number(card.upload_rate) : undefined,
      downloadingCount: text(card.realtime_source) ? number(card.downloading_count) : undefined,
      seedingCount: text(card.realtime_source) ? number(card.seeding_count) : undefined,
      activeCount: number(card.active_count),
      pausedCount: number(card.paused_count),
      observedAt: sampledAt,
      source: text(card.realtime_source) ?? 'UNKNOWN',
    },
    capacity: {
      totalBytes: number(card.total_bytes),
      freeBytes: number(card.physical_free_bytes),
      effectiveFreeBytes: capacityState === 'TRUSTED' ? number(card.effective_free_bytes) : undefined,
      state: capacityState,
      observedAt: time(card.capacity_observed_at),
    },
    periods: { day: periods[0], week: periods[1], month: periods[2] },
  }
  return detail
}
