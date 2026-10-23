import { ApiClient } from '../../generated/api-client'
import { USER_STATUS_FILTERS } from './history'
import type { HistoryDetail, HistoryDetailLoader, HistoryRuleMatch, HistoryTimelineItem } from './history-detail'

const client = new ApiClient(window.location.origin)
type JsonObject = Record<string, unknown>

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

export function parseHistoryDetail(raw: unknown, jobId: string): HistoryDetail {
  const data = object(raw)
  if (!data || data.id !== jobId) throw new Error('HISTORY_DETAIL_INVALID_RESPONSE')
  const status = USER_STATUS_FILTERS.find((item) => item.value && item.value === data.user_status)
  const userStatus = { user_status: status?.value ?? 'NEEDS_CONFIRMATION', user_status_label: status?.label ?? '需确认' }
  const decision = object(data.final_decision) ?? {}
  const transfer = object(data.last_transfer) ?? {}
  const rules: HistoryRuleMatch[] = Array.isArray(data.rules) ? data.rules.flatMap((value) => {
    const rule = object(value)
    if (!rule || !text(rule.id)) return []
    return [{ id: String(rule.id), name: text(rule.name) ?? '未命名规则', priority: typeof rule.priority === 'number' && Number.isFinite(rule.priority) ? rule.priority : undefined, winner: rule.winner === true }]
  }) : []
  const diagnostics: HistoryDetail['diagnostics'][number][] = []
  if (text(data.job_state)) diagnostics.push({ label: 'Job state', value: String(data.job_state) })
  const timeline: HistoryTimelineItem[] = Array.isArray(data.timeline) ? data.timeline.flatMap((value, index) => {
    const event = object(value)
    const occurredAt = time(event?.occurred_at)
    if (!event || !occurredAt) return []
    const stage = ['发现', '准备', '添加', '确认', '上传', '删除', '已存在', '失败', '待确认', '状态变更'].includes(String(event.stage)) ? String(event.stage) : '状态变更'
    const id = typeof event.event_id === 'number' || typeof event.event_id === 'string' ? String(event.event_id) : `event-${index}`
    // Raw transitions are diagnostic facts; they do not prove download success.
    for (const [key, label] of [['from_state', '原状态'], ['to_state', '目标状态'], ['reason_code', '原因码']] as const) {
      if (text(event[key])) diagnostics.push({ label: `事件 ${id} ${label}`, value: String(event[key]) })
    }
    const warning = stage === '失败' || stage === '待确认'
    const waiting = ['RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION'].includes(String(event.to_state))
    const labels: Record<string, string> = {
      RESERVED: '准备向下载器添加',
      ADD_SENT: '已发起添加请求，等待确认',
      AWAITING_CONFIRMATION: '正在等待下载器确认',
      HASH_SEEN: '下载器已确认种子存在',
      RECONCILED_CONFIRMED: '下载器已确认种子存在',
      ALREADY_PRESENT: '下载器中已有相同种子',
      UNKNOWN: '添加结果需确认',
      EXPIRED_UNKNOWN: '添加结果需人工确认',
      ADD_FAILED: '本次添加未完成',
      RECONCILED_NO_EFFECT: '已确认本次添加未生效',
    }
    return [{ id, stage, label: labels[String(event.to_state)] ?? stage, occurredAt, tone: warning ? 'warning' : waiting ? 'current' : 'complete' }]
  }) : []
  const deletedAt = time(data.deleted_at)
  const downloader = text(data.downloader) ?? '暂无数据'
  const tags = Array.isArray(decision.tags) ? decision.tags.filter((value): value is string => typeof value === 'string') : undefined
  return {
    id: jobId,
    title: text(data.title) ?? '未命名种子',
    site: text(data.site) ?? '暂无数据',
    downloader,
    userStatus,
    discoveredAt: time(data.discovered_at),
    addedAt: time(data.added_at),
    confirmedAt: time(data.confirmed_at),
    finishedAt: time(data.finished_at),
    // Only a server-provided user-facing explanation is shown outside diagnostics.
    failureReason: text(data.failure_reason),
    rules,
    rulesKnown: Array.isArray(data.rules),
    winnerReason: text(data.winner_reason) ?? '暂无规则聚合信息',
    finalDecision: {
      downloader: text(decision.downloader) ?? downloader,
      category: text(decision.category) ?? '暂无数据',
      tags,
      savePath: text(decision.save_path) ?? '暂无数据',
    },
    transfer: {
      stateLabel: text(transfer.state_label) ?? '暂无数据',
      uploadedBytes: number(transfer.uploaded_bytes),
      downloadedBytes: number(transfer.downloaded_bytes),
      ratio: number(transfer.ratio),
      observedAt: time(transfer.observed_at),
    },
    deletion: deletedAt ? { confirmedAt: deletedAt, sourceLabel: deletionSource(data.deletion_source) } : undefined,
    timeline,
    diagnostics,
  }
}

function deletionSource(value: unknown): string {
  switch (value) {
    case 'QUI_DELETE': return 'qui 删除，经同步确认'
    case 'QB_DELETE': return 'qB 删除，经同步确认'
    case 'QB_USER_REMOVED': return 'qB 中手动删除，经同步确认'
    case 'SYNC_REMOVED':
    case 'SYNC_TORRENTS_REMOVED': return '下载器同步确认删除'
    default: return '删除已确认，未提供来源信息'
  }
}

export const loadHistoryDetail: HistoryDetailLoader = async (jobId) => {
  if (!jobId.trim()) throw new Error('HISTORY_DETAIL_NOT_FOUND')
  return parseHistoryDetail(await client.getJob(jobId), jobId)
}
