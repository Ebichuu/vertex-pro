import { http, HttpResponse } from 'msw'
import type { DiagnosticReport, SettingsResponse, SettingsValues } from '../generated/models'
import { validateSettings } from '../features/settings/validation'

export function settingsFixture(values: Partial<SettingsValues> = {}): SettingsResponse {
  const defaults: SettingsValues = {
    timezone: 'UTC', rss_poll_interval_minutes: 60, rate_limit_per_minute: 30,
    max_concurrent_rss_fetches: 4, session_ttl_hours: 24, stats_raw_retention_days: 30,
    stats_hourly_retention_days: 180, ...values,
  }
  return { values: { ...defaults }, active: { ...defaults }, revision: 1, updated_at: '2026-09-06T08:00:00Z', restart_required: false,
    security: { csrf_protection: true, secure_cookies: true, diagnostics_redacted: true } }
}

export const diagnosticFixture: DiagnosticReport = {
  generated_at: '2026-09-06T08:00:00Z', version: '0.0.0-test', build: { Available: false, Revision: '', Clean: false },
  config: { timezone: 'UTC', settings_revision: 1 }, checks: [{ name: 'database', ok: true, detail: 'ping ok' }], digest: `sha256:${'0'.repeat(64)}`,
}

const error = (code: string, status: number) => HttpResponse.json({ error: { code, message: 'settings request failed', request_id: 'settings-test', retryable: status === 409 } }, { status })

export function settingsHandlers(initial = settingsFixture()) {
  let current = structuredClone(initial)
  return [
    http.get('/api/v1/settings', () => HttpResponse.json(current, { headers: { ETag: `"${current.revision}"`, 'Cache-Control': 'no-store' } })),
    http.put('/api/v1/settings', async ({ request }) => {
      if (!request.headers.get('X-CSRF-Token')) return error('FORBIDDEN', 403)
      if (request.headers.get('If-Match') !== `"${current.revision}"`) return error('REVISION_CONFLICT', 409)
      const values = await request.json() as SettingsValues
      if (Object.keys(validateSettings(values)).length) return error('VALIDATION_FAILED', 422)
      current = { ...current, values: { ...values }, revision: current.revision + 1, updated_at: '2026-09-06T09:00:00Z', restart_required: JSON.stringify(values) !== JSON.stringify(current.active) }
      return HttpResponse.json(current, { headers: { ETag: `"${current.revision}"` } })
    }),
    http.get('/api/v1/settings/diagnostics', () => HttpResponse.json(diagnosticFixture, { headers: { 'Content-Disposition': 'attachment; filename="vertex-pro-diagnostics.json"', 'Cache-Control': 'no-store' } })),
  ]
}
