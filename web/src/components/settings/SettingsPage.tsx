import { useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  type AppSettings, type DiagnosticsDownloader, type SettingsErrors, type SettingsLoader,
  type SettingsSaver, type SettingsValues, isSettingsConflict, settingsErrorMessage, validateSettings,
} from '../../features/settings/settings'

type SettingsPageProps = {
  loadSettings: SettingsLoader
  saveSettings?: SettingsSaver
  downloadDiagnostics?: DiagnosticsDownloader
  canEdit?: boolean
}

type SettingsState = {
  status: 'loading' | 'ready' | 'error'
  saved?: AppSettings
  draft?: SettingsValues
  dirty: boolean
  conflict: boolean
  error?: string
}

export function SettingsPage({ loadSettings, saveSettings, downloadDiagnostics, canEdit = true }: SettingsPageProps) {
  const [state, setState] = useState<SettingsState>({ status: 'loading', dirty: false, conflict: false })
  const [attempt, setAttempt] = useState({ id: 0, discard: true })
  const [errors, setErrors] = useState<SettingsErrors>({})
  const [notice, setNotice] = useState<{ text: string; error: boolean } | null>(null)
  const [saving, setSaving] = useState(false)
  const [downloading, setDownloading] = useState(false)
  const [offline, setOffline] = useState(!navigator.onLine)
  const saveInFlight = useRef(false)
  const mounted = useRef(false)
  const writeController = useRef<AbortController>()
  const downloadController = useRef<AbortController>()
  const canSave = canEdit && Boolean(saveSettings)

  useEffect(() => {
    mounted.current = true
    const disconnected = () => setOffline(true)
    const connected = () => {
      setOffline(false)
      if (!saveInFlight.current) setAttempt((current) => ({ id: current.id + 1, discard: false }))
    }
    window.addEventListener('offline', disconnected)
    window.addEventListener('online', connected)
    return () => {
      mounted.current = false
      writeController.current?.abort()
      downloadController.current?.abort()
      window.removeEventListener('offline', disconnected)
      window.removeEventListener('online', connected)
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    setState((current) => ({ ...current, status: 'loading', error: undefined }))
    void loadSettings(controller.signal).then((loaded) => {
      if (controller.signal.aborted) return
      setState((current) => {
        const keepDraft = !attempt.discard && current.dirty && current.saved && current.draft
        return {
          status: 'ready', saved: keepDraft ? { ...current.saved!, active: loaded.active } : loaded,
          draft: keepDraft ? current.draft : { ...loaded.values }, dirty: Boolean(keepDraft),
          conflict: Boolean(keepDraft && loaded.revision !== current.saved!.revision),
        }
      })
    }, (error: unknown) => {
      if (!controller.signal.aborted) setState((current) => ({ ...current, status: 'error', error: settingsErrorMessage(error, 'load') }))
    })
    return () => controller.abort()
  }, [loadSettings, attempt])

  const reload = (discard: boolean) => {
    setNotice(null)
    setErrors({})
    setAttempt((current) => ({ id: current.id + 1, discard }))
  }
  const update = <K extends keyof SettingsValues>(key: K, value: SettingsValues[K]) => {
    setState((current) => ({ ...current, draft: { ...current.draft!, [key]: value }, dirty: true }))
    setErrors((current) => ({ ...current, [key]: undefined, retention: undefined }))
    setNotice(null)
  }
  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!canSave || !saveSettings || !state.saved || !state.draft || offline || state.conflict || state.status !== 'ready' || saveInFlight.current) return
    const nextErrors = validateSettings(state.draft)
    setErrors(nextErrors)
    setNotice(null)
    if (Object.keys(nextErrors).length > 0) {
      const first = Object.keys(nextErrors)[0]
      document.getElementById(`settings-${first === 'retention' ? 'stats_hourly_retention_days' : first}`)?.focus()
      return
    }
    saveInFlight.current = true
    setSaving(true)
    const controller = new AbortController()
    writeController.current = controller
    try {
      const saved = await saveSettings({ ...state.saved, values: state.draft }, controller.signal)
      if (controller.signal.aborted || !mounted.current) return
      setState({ status: 'ready', saved, draft: { ...saved.values }, dirty: false, conflict: false })
      setNotice({ text: saved.restart_required ? '设置已保存，重启服务后生效。' : '设置已保存。', error: false })
    } catch (error) {
      if (controller.signal.aborted || !mounted.current) return
      if (isSettingsConflict(error)) setState((current) => ({ ...current, conflict: true }))
      setNotice({ text: settingsErrorMessage(error, 'save'), error: true })
    } finally {
      saveInFlight.current = false
      if (mounted.current) setSaving(false)
    }
  }
  const download = async () => {
    if (!downloadDiagnostics || downloading || offline) return
    setDownloading(true)
    setNotice(null)
    const controller = new AbortController()
    downloadController.current = controller
    try {
      await downloadDiagnostics(controller.signal)
      if (!controller.signal.aborted && mounted.current) setNotice({ text: '诊断包已下载，敏感信息已脱敏。', error: false })
    } catch (error) {
      if (!controller.signal.aborted && mounted.current) setNotice({ text: settingsErrorMessage(error, 'download'), error: true })
    } finally {
      if (mounted.current) setDownloading(false)
    }
  }

  const values = state.draft
  const saved = state.saved
  const input = (key: keyof SettingsValues, label: string, hint: string, max?: number) => {
    const id = `settings-${key}`
    const error = errors[key] || (key === 'stats_hourly_retention_days' ? errors.retention : undefined)
    return <Field id={id} label={label} hint={hint} error={error}>
      <input id={id} type={max ? 'number' : 'text'} min={max ? 1 : undefined} max={max} step={max ? 1 : undefined}
        value={Number.isNaN(values![key]) ? '' : values![key]} onChange={(event) => update(key, max ? (event.target.value === '' ? NaN : Number(event.target.value)) : event.target.value)}
        aria-invalid={Boolean(error)} aria-describedby={`${id}-hint${error ? ` ${id}-error` : ''}`} />
    </Field>
  }

  return (
    <section className="settings-page" aria-labelledby="settings-title" aria-busy={state.status === 'loading'}>
      <header className="settings-heading">
        <div><p className="eyebrow">SYSTEM SETTINGS</p><h1 id="settings-title">系统设置</h1><p className="muted">管理时间显示、RSS 请求、数据保留和会话安全。</p></div>
        <span className="page-badge">{saved?.restart_required ? '待重启生效' : '系统配置'}</span>
      </header>
      {notice && <p className={notice.error ? 'settings-permission' : 'settings-notice'} role={notice.error ? 'alert' : 'status'}>{notice.text}</p>}
      {offline && <p className="settings-permission" role="status">连接已断开，草稿保留在当前页面。恢复连接后会检查最新版本。</p>}
      {state.status === 'loading' && <p className="muted" role="status">{saved ? '正在检查最新设置…' : '正在加载设置…'}</p>}
      {state.error && <div className="settings-permission" role="alert"><p>{state.error}{saved ? '当前显示上次加载的设置。' : ''}</p><button type="button" className="ghost-button" onClick={() => reload(false)} disabled={offline || saving}>重试加载</button></div>}
      {state.conflict && <div className="settings-permission" role="alert"><p>服务器上有更新的设置。请先记录需要保留的修改，再载入最新版本。</p><button type="button" className="ghost-button" disabled={offline || saving} onClick={() => reload(true)}>放弃草稿并载入最新设置</button></div>}
      {!canSave && <p className="settings-permission" role="status">{canEdit ? '当前设置只能查看，保存暂不可用。' : '当前账号只有查看权限。'}</p>}
      {values && saved && <div className="settings-layout">
        <form className="panel settings-form" onSubmit={(event) => { void submit(event) }} aria-label="全局设置表单" noValidate>
          <p className="settings-apply-note">保存后重启服务生效。缩短保留期会在重启后的清理中删除超期统计；已有登录会话保持原到期时间。</p>
          <fieldset disabled={saving || !canSave}>
            <legend className="settings-section-title">运行策略</legend>
            <div className="settings-form-grid">
              {input('timezone', '时区', '用于历史和详情时间显示，统计仍按 UTC 分桶')}
              {input('rss_poll_interval_minutes', 'RSS 轮询间隔（分钟）', '启用 RSS 调度后的全局轮询周期', 1440)}
            </div>
          </fieldset>
          <fieldset disabled={saving || !canSave}>
            <legend className="settings-section-title">限流</legend>
            <div className="settings-form-grid">
              {input('rate_limit_per_minute', '站点请求上限（次/分钟）', '同一目标主机的轮询和手动测试共享预算', 600)}
              {input('max_concurrent_rss_fetches', '并发 RSS 请求数', '所有站点共享上限，达到上限后等待空位', 32)}
            </div>
          </fieldset>
          <fieldset disabled={saving || !canSave}>
            <legend className="settings-section-title">数据保留</legend>
            <div className="settings-form-grid">
              {input('stats_raw_retention_days', '原始统计保留（天）', '原始采样数据的保留时间', 3650)}
              {input('stats_hourly_retention_days', '小时统计保留（天）', '不可短于原始统计保留期', 3650)}
            </div>
          </fieldset>
          <fieldset disabled={saving || !canSave}>
            <legend className="settings-section-title">安全</legend>
            <div className="settings-form-grid">
              {input('session_ttl_hours', '管理员会话有效期（小时）', '重启后新登录的会话采用此期限', 720)}
              <div className="settings-security-status" aria-label="安全状态">
                <div><span>CSRF 防护</span><strong>{saved.security.csrf_protection ? '已启用' : '未启用'}</strong></div>
                <div><span>安全 Cookie</span><strong>{saved.security.secure_cookies ? '已启用' : '未启用'}</strong></div>
                <div><span>qui 凭据</span><strong aria-label="qui 凭据已遮罩">••••••••</strong><small>仅服务端托管</small></div>
              </div>
            </div>
            <p className="field-hint settings-security-note">安全防护保持启用。凭据不会传入设置页面。</p>
          </fieldset>
          <div className="settings-actions">
            <button type="submit" className="primary-button" disabled={saving || !canSave || offline || state.status !== 'ready' || state.conflict}>{saving ? '保存中…' : '保存设置'}</button>
            <span className="muted">{state.dirty ? '有未保存修改' : `上次更新：${new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', timeZone: saved.active.timezone, hour12: false }).format(new Date(saved.updated_at))}`}</span>
          </div>
        </form>
        <aside className="panel settings-diagnostics" aria-labelledby="settings-diagnostics-title">
          <p className="eyebrow">DIAGNOSTICS</p><h2 id="settings-diagnostics-title">诊断下载</h2>
          <p className="muted">导出当前服务版本、运行配置摘要和数据库检查结果，供排查问题时使用。</p>
          <button type="button" className="ghost-button" onClick={() => { void download() }} disabled={downloading || !downloadDiagnostics || offline}>{downloading ? '准备中…' : '下载诊断包'}</button>
          <p className="settings-diagnostics-note">服务端生成并脱敏，不包含凭据或本页未保存的修改。</p>
          <dl className="settings-active-summary"><dt>当前显示时区</dt><dd>{saved.active.timezone}</dd><dt>当前 RSS 轮询</dt><dd>{saved.active.rss_poll_interval_minutes} 分钟</dd><dt>当前请求上限</dt><dd>{saved.active.rate_limit_per_minute} 次/分钟</dd></dl>
        </aside>
      </div>}
    </section>
  )
}

function Field({ id, label, hint, error, children }: { id: string; label: string; hint: string; error?: string; children: ReactNode }) {
  return <div className="settings-field"><label htmlFor={id}>{label}</label>{children}<small id={`${id}-hint`}>{hint}</small>{error && <em id={`${id}-error`} className="field-error" role="alert">{error}</em>}</div>
}
