import { ApiClient, ApiRequestError } from '../../generated/api-client'
import type { DiagnosticReport, SettingsResponse, SettingsValues } from '../../generated/models'
import { csrfHeaders } from '../auth/auth'
import { validateSettings } from './validation'
export { validateSettings } from './validation'

export type AppSettings = SettingsResponse
export type { SettingsValues }
export type SettingsErrors = Partial<Record<keyof SettingsValues | 'retention', string>>
export type SettingsLoader = (signal?: AbortSignal) => Promise<AppSettings>
export type SettingsSaver = (settings: AppSettings, signal?: AbortSignal) => Promise<AppSettings>
export type DiagnosticsDownloader = (signal?: AbortSignal) => Promise<void>

const client = new ApiClient(window.location.origin)

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('invalid settings response')
  return value as Record<string, unknown>
}

function parseValues(raw: unknown): SettingsValues {
  const value = object(raw)
  if (typeof value.timezone !== 'string') throw new Error('invalid settings response')
  const values = {
    timezone: value.timezone,
    rss_poll_interval_minutes: value.rss_poll_interval_minutes,
    rate_limit_per_minute: value.rate_limit_per_minute,
    max_concurrent_rss_fetches: value.max_concurrent_rss_fetches,
    session_ttl_hours: value.session_ttl_hours,
    stats_raw_retention_days: value.stats_raw_retention_days,
    stats_hourly_retention_days: value.stats_hourly_retention_days,
  } as SettingsValues
  if (Object.keys(validateSettings(values)).length) throw new Error('invalid settings response')
  return values
}

export function parseSettings(raw: unknown): AppSettings {
  const value = object(raw)
  const security = object(value.security)
  if (!Number.isSafeInteger(value.revision) || Number(value.revision) < 1 || typeof value.updated_at !== 'string' || !Number.isFinite(Date.parse(value.updated_at)) || typeof value.restart_required !== 'boolean' || typeof security.csrf_protection !== 'boolean' || typeof security.secure_cookies !== 'boolean' || typeof security.diagnostics_redacted !== 'boolean') throw new Error('invalid settings response')
  return {
    values: parseValues(value.values), active: parseValues(value.active), revision: Number(value.revision),
    updated_at: value.updated_at, restart_required: value.restart_required,
    security: { csrf_protection: security.csrf_protection, secure_cookies: security.secure_cookies, diagnostics_redacted: security.diagnostics_redacted },
  }
}

export const loadSettings: SettingsLoader = async (signal) => parseSettings(await client.getSettings({ signal }))

export const saveSettings: SettingsSaver = async (settings, signal) => parseSettings(await client.updateSettings({
  body: { ...settings.values, timezone: settings.values.timezone.trim() },
  headers: { ...csrfHeaders(), 'If-Match': `"${settings.revision}"` }, signal,
}))

export const downloadDiagnostics: DiagnosticsDownloader = async (signal) => {
  const report = await client.getSettingsDiagnostics<DiagnosticReport>({ signal })
  if (!report || typeof report.generated_at !== 'string' || typeof report.digest !== 'string' || !/^sha256:[a-f0-9]{64}$/.test(report.digest) || !Array.isArray(report.checks)) throw new Error('invalid diagnostic response')
  const blob = new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' })
  const objectURLs = URL
  const url = objectURLs.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'vertex-pro-diagnostics.json'
  document.body.append(anchor)
  try { anchor.click() } finally {
    anchor.remove()
    window.setTimeout(() => objectURLs.revokeObjectURL(url), 1000)
  }
}

export function isSettingsConflict(error: unknown): boolean {
  return error instanceof ApiRequestError && error.body.code === 'REVISION_CONFLICT'
}

export function settingsErrorMessage(error: unknown, action: 'load' | 'save' | 'download'): string {
  if (error instanceof ApiRequestError) {
    if (error.body.code === 'UNAUTHENTICATED') return '登录已失效，请重新登录后重试。'
    if (error.status === 403) return '当前请求没有操作权限，请重新登录后重试。'
    if (isSettingsConflict(error)) return '设置已在另一窗口更新。草稿已保留，请载入最新设置后重新修改。'
    if (error.body.code === 'VALIDATION_FAILED') return '设置未通过服务器校验，请检查输入范围。'
  }
  return { load: '设置加载失败，请重试。', save: '设置保存失败，草稿已保留，请重试。', download: '诊断包下载失败，请稍后重试。' }[action]
}
