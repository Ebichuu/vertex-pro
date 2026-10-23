import type { HistoryEntry } from './history'

export interface HistoryRuleMatch {
  id: string
  name: string
  priority?: number
  winner: boolean
}

export interface HistoryFinalDecision {
  downloader: string
  category: string
  tags?: ReadonlyArray<string>
  savePath: string
}

export interface HistoryTransferSnapshot {
  stateLabel: string
  uploadedBytes?: number
  downloadedBytes?: number
  ratio?: number
  observedAt?: string
}

export interface HistoryTimelineItem {
  id: string
  stage: string
  label: string
  occurredAt: string
  detail?: string
  tone: 'complete' | 'current' | 'warning'
}

export interface HistoryDeletionConfirmation {
  confirmedAt: string
  sourceLabel: string
}

export interface HistoryDiagnosticItem {
  label: string
  value: string
}

export interface HistoryDetail {
  id: string
  title: string
  site: string
  downloader: string
  userStatus: Pick<HistoryEntry, 'user_status' | 'user_status_label'>
  discoveredAt?: string
  addedAt?: string
  confirmedAt?: string
  finishedAt?: string
  failureReason?: string
  rules: ReadonlyArray<HistoryRuleMatch>
  rulesKnown?: boolean
  winnerReason: string
  finalDecision: HistoryFinalDecision
  transfer: HistoryTransferSnapshot
  deletion?: HistoryDeletionConfirmation
  timeline: ReadonlyArray<HistoryTimelineItem>
  diagnostics: ReadonlyArray<HistoryDiagnosticItem>
}

export type HistoryDetailLoader = (jobId: string) => Promise<HistoryDetail>

export const HIDDEN_HISTORY_TERMS = [
  'Candidate',
  'Placement',
  'Reservation',
  'Reflection',
  'SubmitIntent',
  'Capacity Epoch',
  'RESERVED',
  'REFLECTING',
  'UNKNOWN_SIDE_EFFECT',
  'job.state.changed',
  'attempt.state.changed',
  'Snapshot revision',
  'Evidence digest',
] as const

const deletedFixture: HistoryDetail = {
  id: 'history-job-1',
  title: 'The.Last.Horizon.2026.2160p.UHD.BluRay',
  site: 'HDSky',
  downloader: 'qB-A · NVMe',
  userStatus: { user_status: 'DELETED', user_status_label: '已删除' },
  discoveredAt: '2026-08-29T07:00:00Z',
  addedAt: '2026-08-29T07:00:08Z',
  confirmedAt: '2026-08-29T07:00:12Z',
  finishedAt: '2026-08-29T07:42:19Z',
  rules: [
    { id: 'rule-4k', name: '4K 电影', priority: 100, winner: true },
    { id: 'rule-free', name: 'Freeleech', priority: 60, winner: false },
  ],
  winnerReason: '优先级 100，高于其他命中规则',
  finalDecision: {
    downloader: 'qB-A · NVMe',
    category: 'movie/4k',
    tags: ['vertex-pro', '4k', 'freeleech'],
    savePath: '按分类创建子目录',
  },
  transfer: {
    stateLabel: '做种中',
    uploadedBytes: 128_849_018_880,
    downloadedBytes: 31_287_918_592,
    ratio: 4.12,
    observedAt: '2026-08-29T08:59:48Z',
  },
  deletion: {
    confirmedAt: '2026-08-29T09:00:00Z',
    sourceLabel: 'qB 中手动删除，经同步确认',
  },
  timeline: [
    { id: 'event-1', stage: '发现', label: '从 HDSky RSS 发现', occurredAt: '2026-08-29T07:00:00Z', tone: 'complete' },
    { id: 'event-2', stage: '准备', label: '规则聚合完成', occurredAt: '2026-08-29T07:00:03Z', detail: '命中 2 条规则，由“4K 电影”胜出', tone: 'complete' },
    { id: 'event-3', stage: '添加', label: '已发送到 qB-A · NVMe', occurredAt: '2026-08-29T07:00:08Z', tone: 'complete' },
    { id: 'event-4', stage: '确认', label: '下载器已确认种子存在', occurredAt: '2026-08-29T07:00:12Z', tone: 'complete' },
    { id: 'event-5', stage: '上传', label: '最后记录上传 120.00 GiB', occurredAt: '2026-08-29T08:59:48Z', tone: 'complete' },
    { id: 'event-6', stage: '删除', label: '删除已确认', occurredAt: '2026-08-29T09:00:00Z', detail: '保留最后上传量与分享率，不会自动重新添加', tone: 'current' },
  ],
  diagnostics: [
    { label: 'Job state', value: 'RECONCILED_CONFIRMED' },
    { label: 'Placement state', value: 'COMMITTED' },
    { label: 'Reservation', value: 'RELEASED' },
    { label: 'SubmitIntent', value: 'DISPATCHED' },
    { label: 'Event type', value: 'attempt.state.changed' },
    { label: 'Snapshot revision', value: '1842' },
    { label: 'Evidence digest', value: 'sha256:0f62d9c6…8c7d' },
  ],
}

const activeFixture: HistoryDetail = {
  ...deletedFixture,
  id: 'history-job-2',
  title: 'Planet.Earth.III.S01.1080p.BluRay',
  downloader: 'qB-B · Bulk',
  userStatus: { user_status: 'SEEDING', user_status_label: '做种中' },
  discoveredAt: '2026-08-29T08:12:00Z',
  addedAt: '2026-08-29T08:12:08Z',
  confirmedAt: '2026-08-29T08:12:15Z',
  finishedAt: undefined,
  rules: [{ id: 'rule-free', name: 'Freeleech', priority: 60, winner: true }],
  winnerReason: '唯一命中的已启用规则',
  finalDecision: {
    downloader: 'qB-B · Bulk',
    category: 'documentary',
    tags: ['vertex-pro', 'freeleech'],
    savePath: '按分类创建子目录',
  },
  transfer: {
    stateLabel: '做种中',
    uploadedBytes: 19_595_657_216,
    downloadedBytes: 18_253_611_008,
    ratio: 1.07,
    observedAt: '2026-08-29T09:05:00Z',
  },
  deletion: undefined,
  timeline: [
    { id: 'active-event-1', stage: '发现', label: '从 HDSky RSS 发现', occurredAt: '2026-08-29T08:12:00Z', tone: 'complete' },
    { id: 'active-event-2', stage: '准备', label: '规则聚合完成', occurredAt: '2026-08-29T08:12:03Z', detail: '唯一命中的规则是“Freeleech”', tone: 'complete' },
    { id: 'active-event-3', stage: '添加', label: '已发送到 qB-B · Bulk', occurredAt: '2026-08-29T08:12:08Z', tone: 'complete' },
    { id: 'active-event-4', stage: '确认', label: '下载器已确认种子存在', occurredAt: '2026-08-29T08:12:15Z', tone: 'complete' },
    { id: 'active-event-5', stage: '上传', label: '最后记录上传 18.25 GiB', occurredAt: '2026-08-29T09:05:00Z', tone: 'current' },
  ],
}

const localDetails: Record<string, HistoryDetail> = {
  [deletedFixture.id]: deletedFixture,
  [activeFixture.id]: activeFixture,
}

export const loadLocalHistoryDetail: HistoryDetailLoader = async (jobId) => {
  const fixture = localDetails[jobId]
  if (!fixture) {
    throw new Error('HISTORY_DETAIL_NOT_FOUND')
  }
  return fixture
}

export function sortHistoryTimeline(timeline: ReadonlyArray<HistoryTimelineItem>): HistoryTimelineItem[] {
  return [...timeline].sort((left, right) => {
    const leftTime = Date.parse(left.occurredAt)
    const rightTime = Date.parse(right.occurredAt)
    const timeOrder = Number.isNaN(leftTime) || Number.isNaN(rightTime)
      ? left.occurredAt.localeCompare(right.occurredAt)
      : leftTime - rightTime
    return timeOrder || left.id.localeCompare(right.id, 'zh-CN', { numeric: true })
  })
}

export function formatHistoryBytes(bytes?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) {
    return '暂无数据'
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }
  return `${value.toFixed(unitIndex === 0 ? 0 : 2)} ${units[unitIndex]}`
}

export function formatHistoryTime(value?: string, timezone = 'UTC'): string {
  if (!value) {
    return '暂无数据'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'medium',
    hour12: false,
    timeZone: timezone,
  }).format(date)
}
