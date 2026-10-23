import type { SettingsValues } from '../../generated/models'
import type { SettingsErrors } from './settings'

export function validateSettings(values: SettingsValues): SettingsErrors {
  const errors: SettingsErrors = {}
  const zone = values.timezone.trim()
  try {
    if (!zone || zone === 'Local' || zone.length > 100) throw new Error('invalid timezone')
    new Intl.DateTimeFormat('zh-CN', { timeZone: zone }).format()
  } catch {
    errors.timezone = '请输入有效的 IANA 时区，例如 Asia/Shanghai。'
  }
  const limits = [
    ['rss_poll_interval_minutes', 1440, 'RSS 轮询间隔', '分钟'],
    ['rate_limit_per_minute', 600, '站点请求限流', '次/分钟'],
    ['max_concurrent_rss_fetches', 32, '并发 RSS 请求数', ''],
    ['session_ttl_hours', 720, '管理员会话有效期', '小时'],
    ['stats_raw_retention_days', 3650, '原始统计保留期', '天'],
    ['stats_hourly_retention_days', 3650, '小时统计保留期', '天'],
  ] as const
  for (const [key, max, label, unit] of limits) {
    if (!Number.isInteger(values[key]) || values[key] < 1 || values[key] > max) errors[key] = `${label}必须是 1–${max}${unit ? ` ${unit}` : ''}。`
  }
  if (!errors.stats_raw_retention_days && !errors.stats_hourly_retention_days && values.stats_hourly_retention_days < values.stats_raw_retention_days) {
    errors.retention = '小时统计保留期不能短于原始统计保留期。'
  }
  return errors
}
