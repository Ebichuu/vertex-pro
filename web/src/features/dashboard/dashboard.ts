// Dashboard 卡片的展示模型与格式化辅助（UI-02）。
// 卡片字段对应 GET /api/v1/dashboard 的实例汇总投影（§18.2 最低字段）。
import { formatRateMBps, formatTerabytes } from '../shared/units'
import type { DailySiteStats } from '../../generated/models'

export interface InstanceCard {
  id: string
  sync_state: string
  snapshot_revision: number
  freshness: string
  capacity_state: string
  physical_free_bytes: number
  effective_free_bytes: number
  storage_state: string
  reason_codes: ReadonlyArray<string>
  download_rate: number
  upload_rate: number
  downloading_count: number
  seeding_count: number
  realtime_freshness: string
  realtime_source: string
}

export interface DashboardData {
  jobs_total: number
  user_status_counts: Record<string, number>
  instances: ReadonlyArray<InstanceCard>
  today_date?: string
  today_timezone?: string
  today_sites?: ReadonlyArray<DailySiteStats>
}

export function formatRate(bytesPerSecond: number): string {
  return formatRateMBps(bytesPerSecond)
}

export function formatBytes(bytes: number): string {
  return formatTerabytes(bytes)
}

// freshnessLabel 把后端 freshness/reason 映射为用户可见文案。
export function freshnessLabel(card: InstanceCard): string {
  if (card.freshness !== 'FRESH' || card.sync_state !== 'READY') {
    return '数据不新鲜'
  }
  return '数据新鲜'
}

// cardTone 决定卡片边框语气：正常 / 提示 / 异常。
export function cardTone(card: InstanceCard): 'ok' | 'warn' | 'bad' {
  if (card.sync_state === 'OFFLINE') {
    return 'bad'
  }
  if (card.freshness !== 'FRESH' || card.sync_state !== 'READY' || card.realtime_freshness !== 'FRESH' || card.capacity_state !== 'TRUSTED' || card.storage_state !== 'VERIFIED' || card.reason_codes.length > 0) {
    return 'warn'
  }
  return 'ok'
}

// 汇总只使用仍然有效的投影。未知或过期值不能成为看似正常的零值。
export function summarizeInstances(instances: ReadonlyArray<InstanceCard>) {
  const fresh = instances.filter(card => card.sync_state === 'READY' && card.freshness === 'FRESH')
  const rates = fresh.filter(card => card.realtime_freshness === 'FRESH')
  const capacities = fresh.filter(card => card.capacity_state === 'TRUSTED' && card.storage_state === 'VERIFIED')
  const sum = (cards: ReadonlyArray<InstanceCard>, key: 'download_rate' | 'upload_rate' | 'effective_free_bytes') => {
    const valid = cards.filter(card => Number.isFinite(card[key]) && card[key] >= 0)
    return { value: valid.length ? valid.reduce((total, card) => total + card[key], 0) : -1, count: valid.length }
  }
  return { online: fresh.length, total: instances.length, download: sum(rates, 'download_rate'), upload: sum(rates, 'upload_rate'), capacity: sum(capacities, 'effective_free_bytes') }
}
