import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '../../mocks/server'
import { diagnosticFixture, settingsFixture, settingsHandlers } from '../../mocks/settings'
import { downloadDiagnostics, loadSettings, parseSettings, saveSettings, validateSettings } from './settings'
import { formatHistoryTime } from '../history/history-detail'
import { formatDownloaderTime } from '../downloaders/downloader-detail'

beforeEach(() => {
  server.use(...settingsHandlers())
  document.cookie = 'vertex_csrf=settings-csrf; path=/'
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); document.cookie = 'vertex_csrf=; Max-Age=0; path=/' })

describe('Settings API contract', () => {
  it('通过生成客户端携带 revision/CSRF 保存，并重新加载服务端值', async () => {
    const initial = await loadSettings()
    const changed = { ...initial, values: { ...initial.values, timezone: 'Asia/Shanghai', rate_limit_per_minute: 45 } }
    const saved = await saveSettings(changed)
    expect(saved.revision).toBe(2)
    expect(saved.restart_required).toBe(true)
    expect(saved.active.timezone).toBe('UTC')
    expect((await loadSettings()).values.timezone).toBe('Asia/Shanghai')
    await expect(saveSettings(changed)).rejects.toMatchObject({ status: 409, body: { code: 'REVISION_CONFLICT' } })
  })

  it('拒绝无效合同响应，并仅保留白名单字段', () => {
    const input = settingsFixture()
    expect(() => parseSettings({ ...input, revision: 0 })).toThrow()
    expect(() => parseSettings({ ...input, values: { ...input.values, timezone: '' } })).toThrow()
    expect(JSON.stringify(parseSettings({ ...input, credentials: 'private', values: { ...input.values, proxy_key: 'private' } }))).not.toContain('private')
    for (const [field, invalid] of Object.entries({ rss_poll_interval_minutes: 1441, rate_limit_per_minute: 601, max_concurrent_rss_fetches: 33, session_ttl_hours: 721, stats_raw_retention_days: 3651, stats_hourly_retention_days: 3651 })) {
      expect(validateSettings({ ...input.values, [field]: invalid })).toHaveProperty(field)
    }
  })

  it('下载服务器生成的报告，不向端点发送浏览器草稿', async () => {
    const createObjectURL = vi.fn<(blob: Blob) => string>(() => 'blob:settings-test')
    const revokeObjectURL = vi.fn()
    vi.stubGlobal('URL', class extends URL { static createObjectURL = createObjectURL; static revokeObjectURL = revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    await downloadDiagnostics()
    expect(click).toHaveBeenCalledOnce()
    const blob = createObjectURL.mock.calls[0][0] as Blob
    expect(blob.type).toBe('application/json')
    const reader = new FileReader()
    const text = await new Promise<string>((resolve) => { reader.onload = () => resolve(String(reader.result)); reader.readAsText(blob) })
    expect(JSON.parse(text)).toEqual(diagnosticFixture)
    expect(document.querySelector('a[download]')).toBeNull()
  })

  it('未认证时下载失败，不产生本地伪报告', async () => {
    server.use(http.get('/api/v1/settings/diagnostics', () => HttpResponse.json({ error: { code: 'UNAUTHENTICATED', message: 'session expired', request_id: 'test', retryable: false } }, { status: 401 })))
    await expect(downloadDiagnostics()).rejects.toMatchObject({ status: 401 })
  })

  it('历史与下载器时间显示采用配置时区，UTC 时间事实不变', () => {
    const instant = '2026-09-06T08:00:00Z'
    expect(formatHistoryTime(instant, 'UTC')).toContain('08:00:00')
    expect(formatHistoryTime(instant, 'Asia/Shanghai')).toContain('16:00:00')
    expect(formatDownloaderTime(instant, 'Asia/Shanghai')).toContain('16:00')
    expect(formatDownloaderTime(instant, 'UTC')).toContain('08:00')
  })
})
