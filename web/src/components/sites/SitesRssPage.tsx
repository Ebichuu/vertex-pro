import { useEffect, useRef, useState } from 'react'
import { ApiRequestError } from '../../generated/api-client'
import { PreparationPanel } from './PreparationPanel'
import {
  EMPTY_SITE_FORM,
  formatFeedHealth,
  formatPollInterval,
  localPreview,
  siteIdForName,
  validateSiteForm,
  type Feed,
  type FeedPatch,
  type FeedPreview,
  type PreparationLoader,
  type Site,
  type SiteFormValues,
} from '../../features/sites/sites'

type SiteCreationResult = {
  site: Site
  warning?: string
}

type SitesRssPageProps = {
  initialSites: ReadonlyArray<Site>
  onChange?: (sites: ReadonlyArray<Site>) => void
  onCreateSite?: (form: SiteFormValues) => Promise<SiteCreationResult>
  onUpdateFeed?: (feed: Feed, patch: FeedPatch) => Promise<Feed>
  onTestFeed?: (feed: Feed) => Promise<FeedPreview>
  onLoadPreparations?: PreparationLoader
  onArchiveSite?: (site: Site) => Promise<void>
  onArchiveFeed?: (feed: Feed) => Promise<void>
  onUpdateSite?: (site: Site, enabled: boolean, credentialRef?: string) => Promise<Site>
}

function cloneSites(sites: ReadonlyArray<Site>): Site[] {
  return sites.map((site) => ({ ...site, feeds: site.feeds.map((feed) => ({ ...feed })) }))
}

function configurationError(error: unknown, fallback: string): string {
  return error instanceof ApiRequestError && error.body.message ? error.body.message : fallback
}

function CredentialRefInput({ site, onSave }: { site: Site; onSave: (site: Site, value: string) => Promise<void> }) {
  const [value, setValue] = useState(site.credentialRef)
  const [focused, setFocused] = useState(false)
  const [saving, setSaving] = useState(false)
  useEffect(() => { if (!focused && !saving) setValue(site.credentialRef) }, [site.credentialRef, focused, saving])
  return <label>凭据引用<input value={value} placeholder="env:VERTEX_SITE_ZMPT" disabled={saving} onFocus={() => setFocused(true)} onChange={event => setValue(event.target.value)} onBlur={() => {
    setFocused(false)
    if (value === site.credentialRef) return
    setSaving(true)
    void onSave(site, value).finally(() => setSaving(false))
  }} /></label>
}

export function SitesRssPage({ initialSites, onChange, onCreateSite, onUpdateFeed, onTestFeed, onUpdateSite, onArchiveSite, onArchiveFeed, onLoadPreparations }: SitesRssPageProps) {
  const [sites, setSites] = useState(() => cloneSites(initialSites))
  const sitesRef = useRef(sites)
  useEffect(() => { sitesRef.current = cloneSites(initialSites); setSites(sitesRef.current) }, [initialSites])
  const [selectedSiteId, setSelectedSiteId] = useState(initialSites[0]?.id ?? '')
  const [form, setForm] = useState<SiteFormValues>(EMPTY_SITE_FORM)
  const [errors, setErrors] = useState<ReturnType<typeof validateSiteForm>>({})
  const [notice, setNotice] = useState<string | null>(null)
  const [preview, setPreview] = useState<FeedPreview | null>(null)
  const [preparationFeedId, setPreparationFeedId] = useState<string | null>(null)
  const [testingFeedId, setTestingFeedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [updatingFeedId, setUpdatingFeedId] = useState<string | null>(null)

  const selectedSite = sites.find((site) => site.id === selectedSiteId) ?? sites[0]
  const commit = (nextSites: Site[]) => {
    sitesRef.current = nextSites
    setSites(nextSites)
    onChange?.(nextSites)
  }

  const updateForm = <K extends keyof SiteFormValues>(key: K, value: SiteFormValues[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
    setErrors((current) => ({ ...current, [key]: undefined }))
    setNotice(null)
  }

  const createSite = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const nextErrors = validateSiteForm(form)
    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors)
      setNotice(null)
      return
    }
    setCreating(true)
    setNotice(null)
    try {
      const siteId = siteIdForName(form.siteName, sites.length)
      const feed: Feed = {
        id: `${siteId}-feed-1`,
        siteId,
        name: form.feedName.trim(),
        url: form.feedUrl.trim(),
        enabled: form.enabled,
        pollMinutes: Number(form.pollMinutes),
        lastSuccess: null,
        lastError: null,
        discoveredCount: 0,
      }
      const result = onCreateSite ? await onCreateSite(form) : { site: {
        id: siteId,
        name: form.siteName.trim(),
        baseUrl: form.siteUrl.trim(),
        credentialRef: form.credentialRef.trim(),
        enabled: form.enabled,
        feeds: [feed],
      } }
      commit([...sitesRef.current.filter(site => site.id !== result.site.id), result.site])
      setSelectedSiteId(result.site.id)
      setForm(EMPTY_SITE_FORM)
      setErrors({})
      setPreview(null)
      setNotice(result.warning ?? (onCreateSite ? '站点和 RSS 已保存。' : '站点和 RSS 已加入列表。'))
    } catch (error) {
      setNotice(`站点保存失败：${String(error)}`)
    } finally {
      setCreating(false)
    }
  }

  const toggleSite = async (siteId: string, enabled: boolean) => {
    const site = sites.find(item => item.id === siteId)
    if (!site) return
    try {
      const updated = onUpdateSite ? await onUpdateSite(site, enabled) : { ...site, enabled }
      commit(sitesRef.current.map(item => item.id === siteId ? updated : item))
    } catch (error) { setNotice(configurationError(error, '站点更新失败，请刷新后重试。')) }
  }

  const archive = async (site: Site, feed?: Feed) => {
    if (!window.confirm(feed ? `删除 RSS“${feed.name}”？历史记录会保留。` : `删除站点“${site.name}”及所属 RSS 和规则？历史记录会保留。`)) return
    try {
      if (feed && onArchiveFeed) { await onArchiveFeed(feed); commit(sitesRef.current.map(item => item.id === site.id ? { ...item, feeds: item.feeds.filter(f => f.id !== feed.id) } : item)) }
      else if (onArchiveSite) { await onArchiveSite(site); const remaining = sitesRef.current.filter(item => item.id !== site.id); commit(remaining); if (selectedSiteId === site.id) setSelectedSiteId(remaining[0]?.id ?? '') }
      setPreview(null); setNotice('配置已删除，历史记录已保留。')
    } catch (error) { setNotice(configurationError(error, '删除失败，配置可能已更新，请刷新后重试。')) }
  }
  const saveCredential = async (site: Site, credentialRef: string) => {
    if (!onUpdateSite) return
    try { const saved = await onUpdateSite(site, site.enabled, credentialRef); commit(sitesRef.current.map(item => item.id === site.id ? saved : item)); setNotice('凭据引用已保存。') }
    catch (error) { setNotice(configurationError(error, '凭据引用保存失败，请检查格式或刷新后重试。')) }
  }
  const toggleFeed = async (feedId: string, enabled: boolean) => {
    const feed = sites.flatMap((site) => site.feeds).find((candidate) => candidate.id === feedId)
    if (!feed) return
    setUpdatingFeedId(feedId)
    setNotice(null)
    try {
      const updated = onUpdateFeed ? await onUpdateFeed(feed, { enabled }) : { ...feed, enabled }
      commit(sitesRef.current.map((site) => ({ ...site, feeds: site.feeds.map((candidate) => candidate.id === feedId ? updated : candidate) })))
      setNotice('RSS 已更新。')
    } catch (error) {
      setNotice(`RSS 更新失败：${String(error)}`)
    } finally {
      setUpdatingFeedId(null)
    }
  }

  const testFeed = async (feed: Feed) => {
    setTestingFeedId(feed.id)
    setPreview(null)
    setNotice(null)
    try {
      setPreview(await (onTestFeed ? onTestFeed(feed) : Promise.resolve(localPreview(feed))))
    } catch (error) {
      setNotice(`RSS 测试失败：${String(error)}`)
    } finally {
      setTestingFeedId(null)
    }
  }

  return (
    <section className="sites-rss-page" aria-labelledby="sites-rss-title">
      <header className="sites-rss-heading">
        <div>
          <p className="eyebrow">SITES & RSS</p>
          <h1 id="sites-rss-title">站点与 RSS</h1>
          <p className="muted">管理站点来源、轮询节奏和只读测试结果。</p>
        </div>
        <span className="page-badge">RSS</span>
      </header>

      {notice && <p className="sites-rss-notice" role="status">{notice}</p>}

      <div className="sites-rss-layout">
        <form className="panel sites-rss-form" onSubmit={createSite} aria-labelledby="sites-rss-form-title">
          <div className="sites-rss-section-heading">
            <div>
              <p className="eyebrow">NEW SOURCE</p>
              <h2 id="sites-rss-form-title">添加站点 RSS</h2>
            </div>
            <span className="muted">不会添加种子</span>
          </div>
          <div className="sites-rss-form-grid">
            <label>站点名称<input value={form.siteName} onChange={(event) => updateForm('siteName', event.target.value)} aria-invalid={Boolean(errors.siteName)} aria-describedby={errors.siteName ? 'site-name-error' : undefined} /></label>
            {errors.siteName && <p id="site-name-error" className="field-error" role="alert">{errors.siteName}</p>}
            <label>站点地址<input type="url" value={form.siteUrl} onChange={(event) => updateForm('siteUrl', event.target.value)} placeholder="https://example.test" aria-invalid={Boolean(errors.siteUrl)} aria-describedby={errors.siteUrl ? 'site-url-error' : undefined} /></label>
            {errors.siteUrl && <p id="site-url-error" className="field-error" role="alert">{errors.siteUrl}</p>}
            <label>RSS 名称<input value={form.feedName} onChange={(event) => updateForm('feedName', event.target.value)} aria-invalid={Boolean(errors.feedName)} /></label>
            {errors.feedName && <p className="field-error" role="alert">{errors.feedName}</p>}
            <label>RSS 地址<input type="url" value={form.feedUrl} onChange={(event) => updateForm('feedUrl', event.target.value)} placeholder="https://example.test/rss.xml" aria-invalid={Boolean(errors.feedUrl)} /></label>
            {errors.feedUrl && <p className="field-error" role="alert">{errors.feedUrl}</p>}
            <label>凭据引用<span className="field-hint">引用专用环境变量；内容为 Cookie / Authorization 的 JSON 对象</span><input value={form.credentialRef} onChange={(event) => updateForm('credentialRef', event.target.value)} placeholder="env:VERTEX_SITE_ZMPT" /></label>
            <label>轮询周期（分钟）<input type="number" min="1" max="1440" value={form.pollMinutes} onChange={(event) => updateForm('pollMinutes', event.target.value)} aria-invalid={Boolean(errors.pollMinutes)} /></label>
            {errors.pollMinutes && <p className="field-error" role="alert">{errors.pollMinutes}</p>}
          </div>
          <label className="checkbox-field"><input type="checkbox" checked={form.enabled} onChange={(event) => updateForm('enabled', event.target.checked)} />创建后启用</label>
          <button type="submit" className="primary-button" disabled={creating}>{creating ? '保存中…' : '添加 RSS'}</button>
        </form>

        <section className="panel sites-rss-data" aria-labelledby="sites-rss-data-title">
          <div className="sites-rss-section-heading">
            <div>
              <p className="eyebrow">SOURCES</p>
              <h2 id="sites-rss-data-title">已配置来源</h2>
            </div>
            <label className="site-picker">当前站点<select aria-label="选择站点" value={selectedSite?.id ?? ''} onChange={(event) => { setSelectedSiteId(event.target.value); setPreview(null) }} disabled={sites.length === 0}>
              {sites.length === 0 && <option value="">暂无站点</option>}
              {sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}
            </select></label>
          </div>

          {!selectedSite && <p className="empty-state">暂无站点，先添加一个 RSS 来源。</p>}
          {selectedSite && <>
            <div className="site-meta">
              <div><span>站点地址</span><strong>{selectedSite.baseUrl}</strong></div>
              <CredentialRefInput key={selectedSite.id} site={selectedSite} onSave={saveCredential} />
              <label className="checkbox-field"><input type="checkbox" checked={selectedSite.enabled} onChange={(event) => toggleSite(selectedSite.id, event.target.checked)} />站点启用</label>{onArchiveSite && <button type="button" onClick={() => { void archive(selectedSite) }}>删除站点</button>}
            </div>
            <div className="sites-rss-table-wrap">
              <table className="sites-rss-table" aria-label="站点与 RSS 列表">
                <thead><tr><th scope="col">RSS</th><th scope="col">状态</th><th scope="col">轮询</th><th scope="col">最近拉取</th><th scope="col">发现</th><th scope="col"><span className="visually-hidden">操作</span></th></tr></thead>
                <tbody>{selectedSite.feeds.map((feed) => {
                  const health = formatFeedHealth(feed)
                  return <tr key={feed.id}>
                    <td data-label="RSS"><strong>{feed.name}</strong><span className="table-subline">{feed.url}</span></td>
                    <td data-label="状态"><span className={`status-chip tone-${health.tone}`}>{health.label}</span><label className="table-toggle"><input type="checkbox" checked={feed.enabled} onChange={(event) => { void toggleFeed(feed.id, event.target.checked) }} disabled={updatingFeedId === feed.id} />启用</label>{feed.lastError && <span className="table-error">{feed.lastError}</span>}</td>
                    <td data-label="轮询">{formatPollInterval(feed.pollMinutes)}</td>
                    <td data-label="最近拉取">{feed.lastSuccess ? feed.lastSuccess.slice(0, 10) : '未运行'}{typeof feed.rateWaiting === 'number' && <small className="table-subline muted">限流等待 {feed.rateWaiting} 条 · 累计 {((feed.rateWaitMs ?? 0) / 1000).toFixed(1)} 秒</small>}</td>
                    <td data-label="发现">{feed.discoveredCount}{onLoadPreparations && <button type="button" className="ghost-button" onClick={() => setPreparationFeedId(feed.id)}>准备状态</button>}</td>
                    <td data-label="操作"><button type="button" className="ghost-button" onClick={() => { void testFeed(feed) }} disabled={testingFeedId === feed.id}>{testingFeedId === feed.id ? '测试中…' : '测试 RSS'}</button>{onArchiveFeed && <button type="button" className="ghost-button" onClick={() => { void archive(selectedSite, feed) }}>删除 RSS</button>}</td>
                  </tr>
                })}</tbody>
              </table>
            </div>
          </>}

          {onLoadPreparations && selectedSite?.feeds.filter(feed => feed.id === preparationFeedId).map(feed => <PreparationPanel key={feed.id} feedId={feed.id} name={feed.name} load={onLoadPreparations} onClose={() => setPreparationFeedId(null)} />)}
          {preview && <aside className="rss-preview" aria-live="polite" aria-labelledby="rss-preview-title">
            <div className="sites-rss-section-heading"><div><p className="eyebrow">READ ONLY</p><h3 id="rss-preview-title">测试结果</h3></div><span className="status-chip tone-ok">{preview.source === 'LOCAL_FIXTURE' ? '本地预览' : '已读取'}</span></div>
            <p className="muted">检查时间：{preview.checkedAt}</p>
            <ul>{preview.entries.map((entry) => <li key={`${entry.title}-${entry.publishedAt}`}><strong>{entry.title}</strong><span>{entry.publishedAt}</span></li>)}</ul>
          </aside>}
        </section>
      </div>
    </section>
  )
}
