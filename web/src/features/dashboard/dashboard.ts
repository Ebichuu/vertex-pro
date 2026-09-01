// Dashboard 卡片的展示模型与格式化辅助（UI-02）。
// 卡片字段对应 GET /api/v1/dashboard 的实例汇总投影（§18.2 最低字段）。

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
}

// formatRate 以 KiB/s / MiB/s 呈现速率；0 显示为 0 KiB/s。
export function formatRate(bytesPerSecond: number): string {
  if (bytesPerSecond >= 1024 * 1024) {
    return `${(bytesPerSecond / (1024 * 1024)).toFixed(1)} MiB/s`
  }
  return `${Math.round(bytesPerSecond / 1024)} KiB/s`
}

// formatBytes 以 GiB 呈现容量；-1 表示未知（保持未知，不美化）。
export function formatBytes(bytes: number): string {
  if (bytes < 0) {
    return '未知'
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GiB`
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
  if (card.freshness !== 'FRESH' || card.sync_state !== 'READY' || card.reason_codes.length > 0) {
    return 'warn'
  }
  return 'ok'
}
