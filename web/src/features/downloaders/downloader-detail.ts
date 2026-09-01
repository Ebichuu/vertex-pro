export type DownloaderHealth = 'AVAILABLE' | 'DEGRADED' | 'OFFLINE'
export type DataFreshness = 'FRESH' | 'STALE' | 'UNKNOWN'
export type DownloaderPeriodKey = 'day' | 'week' | 'month'

export interface DownloaderPoint {
  label: string
  timestamp: string
  downloadRate?: number
  uploadRate?: number
  missing?: boolean
  counterReset?: boolean
}

export interface DownloaderPeriodSummary {
  key: DownloaderPeriodKey
  label: string
  uploadedBytes?: number
  downloadedBytes?: number
  ratio?: number
  points: ReadonlyArray<DownloaderPoint>
  missingPoints: number
  counterResets: number
}

export interface DownloaderRealtime {
  downloadRate?: number
  uploadRate?: number
  activeCount?: number
  downloadingCount?: number
  seedingCount?: number
  pausedCount?: number
  observedAt?: string
  source: string
}

export interface DownloaderCapacity {
  totalBytes?: number
  freeBytes?: number
  effectiveFreeBytes?: number
  state: string
  observedAt?: string
}

export interface DownloaderDetail {
  id: string
  name: string
  health: DownloaderHealth
  freshness: DataFreshness
  reasonCodes: ReadonlyArray<string>
  observedAt?: string
  realtime: DownloaderRealtime
  capacity: DownloaderCapacity
  periods: Readonly<Record<DownloaderPeriodKey, DownloaderPeriodSummary>>
}

export type DownloaderDetailLoader = (downloaderId: string) => Promise<DownloaderDetail>

export function formatDownloaderRate(bytesPerSecond?: number): string {
  if (bytesPerSecond === undefined || bytesPerSecond < 0) return '暂无数据'
  if (bytesPerSecond >= 1024 * 1024) return `${(bytesPerSecond / (1024 * 1024)).toFixed(1)} MiB/s`
  return `${Math.round(bytesPerSecond / 1024)} KiB/s`
}

export function formatDownloaderBytes(bytes?: number): string {
  if (bytes === undefined || bytes < 0) return '暂无数据'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }
  return `${value.toFixed(unitIndex === 0 ? 0 : 2)} ${units[unitIndex]}`
}

export function formatDownloaderTime(value?: string): string {
  if (!value) return '暂无数据'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
    hour12: false,
    timeZone: 'Asia/Shanghai',
  }).format(date)
}

export function healthLabel(health: DownloaderHealth): string {
  switch (health) {
    case 'AVAILABLE': return '在线'
    case 'DEGRADED': return '降级'
    case 'OFFLINE': return '离线'
  }
}

export function freshnessLabel(freshness: DataFreshness): string {
  switch (freshness) {
    case 'FRESH': return '数据新鲜'
    case 'STALE': return '数据已过期'
    case 'UNKNOWN': return '数据未知'
  }
}

export function healthTone(detail: Pick<DownloaderDetail, 'health' | 'freshness' | 'reasonCodes'>): 'ok' | 'warn' | 'bad' {
  if (detail.health === 'OFFLINE') return 'bad'
  if (detail.health !== 'AVAILABLE' || detail.freshness !== 'FRESH' || detail.reasonCodes.length > 0) return 'warn'
  return 'ok'
}

const points = (values: ReadonlyArray<number | null>, startHour: number): DownloaderPoint[] => values.map((downloadRate, index) => ({
  label: `${String((startHour + index) % 24).padStart(2, '0')}:00`,
  timestamp: `2026-08-29T${String((startHour + index) % 24).padStart(2, '0')}:00:00Z`,
  downloadRate: downloadRate ?? undefined,
  uploadRate: downloadRate === null ? undefined : Math.round(downloadRate * 0.32),
  missing: downloadRate === null,
  counterReset: index === 7,
}))

const dayPoints = points([1_600_000, 1_820_000, 1_540_000, 1_960_000, 2_240_000, null, 2_080_000, 1_120_000, 1_760_000, 2_420_000, 2_180_000, 2_560_000], 0)
const weekPoints = points([1_220_000, 1_460_000, 1_880_000, 1_640_000, 2_120_000, 2_360_000, 2_040_000], 0)
const monthPoints = points([920_000, 1_140_000, 1_380_000, 1_260_000, 1_720_000, 1_560_000], 0)

const localDetails: Record<string, DownloaderDetail> = {
  'qb-a': {
    id: 'qb-a',
    name: 'qB-A · NVMe',
    health: 'AVAILABLE',
    freshness: 'FRESH',
    reasonCodes: [],
    observedAt: '2026-08-29T09:05:00Z',
    realtime: {
      downloadRate: 2_560_000,
      uploadRate: 820_000,
      activeCount: 18,
      downloadingCount: 6,
      seedingCount: 11,
      pausedCount: 1,
      observedAt: '2026-08-29T09:05:00Z',
      source: 'SYNC_SERVER_STATE',
    },
    capacity: {
      totalBytes: 4 * 1024 ** 4,
      freeBytes: 1.46 * 1024 ** 4,
      effectiveFreeBytes: 1.21 * 1024 ** 4,
      state: 'TRUSTED',
      observedAt: '2026-08-29T09:04:52Z',
    },
    periods: {
      day: { key: 'day', label: '最近 24 小时', uploadedBytes: 18.4 * 1024 ** 3, downloadedBytes: 62.7 * 1024 ** 3, ratio: 0.29, points: dayPoints, missingPoints: 1, counterResets: 1 },
      week: { key: 'week', label: '最近 7 天', uploadedBytes: 142.8 * 1024 ** 3, downloadedBytes: 512.4 * 1024 ** 3, ratio: 0.28, points: weekPoints, missingPoints: 0, counterResets: 1 },
      month: { key: 'month', label: '最近 30 天', uploadedBytes: 620.1 * 1024 ** 3, downloadedBytes: 2.1 * 1024 ** 4, ratio: 0.30, points: monthPoints, missingPoints: 0, counterResets: 1 },
    },
  },
  'qb-b': {
    id: 'qb-b',
    name: 'qB-B · Bulk',
    health: 'DEGRADED',
    freshness: 'STALE',
    reasonCodes: ['SNAPSHOT_STALE', 'CAPACITY_REFRESH_PENDING'],
    observedAt: '2026-08-29T08:52:00Z',
    realtime: { downloadRate: 420_000, uploadRate: 110_000, activeCount: 9, downloadingCount: 2, seedingCount: 6, pausedCount: 1, observedAt: '2026-08-29T08:52:00Z', source: 'TRANSFER_INFO_FALLBACK' },
    capacity: { totalBytes: 8 * 1024 ** 4, freeBytes: undefined, effectiveFreeBytes: undefined, state: 'RECONCILING', observedAt: '2026-08-29T08:40:00Z' },
    periods: {
      day: { key: 'day', label: '最近 24 小时', uploadedBytes: 8.1 * 1024 ** 3, downloadedBytes: 26.3 * 1024 ** 3, ratio: 0.31, points: weekPoints, missingPoints: 2, counterResets: 0 },
      week: { key: 'week', label: '最近 7 天', uploadedBytes: 58.2 * 1024 ** 3, downloadedBytes: 180.4 * 1024 ** 3, ratio: 0.32, points: weekPoints, missingPoints: 2, counterResets: 0 },
      month: { key: 'month', label: '最近 30 天', uploadedBytes: 250.0 * 1024 ** 3, downloadedBytes: 820.0 * 1024 ** 3, ratio: 0.30, points: monthPoints, missingPoints: 3, counterResets: 0 },
    },
  },
}

export const loadLocalDownloaderDetail: DownloaderDetailLoader = async (downloaderId) => {
  const detail = localDetails[downloaderId]
  if (!detail) throw new Error('DOWNLOADER_DETAIL_NOT_FOUND')
  return detail
}
