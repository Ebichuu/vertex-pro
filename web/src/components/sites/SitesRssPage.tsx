import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { useEffect, useRef, useState } from 'react'
import { ApiRequestError } from '../../generated/api-client'
import { PreparationPanel } from './PreparationPanel'
import { FeedEditor } from './FeedEditor'
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '../ui/dialog'
import { Badge } from '../ui/badge'
import { Plus, RefreshCw, Rss, Clock3, Pencil, Play, Pause, Trash2 } from 'lucide-react'
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
  onRefresh?: () => Promise<unknown>
  onCreateSite?: (form: SiteFormValues, existingSite?: Site) => Promise<SiteCreationResult>
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

function CredentialRefInput({ site, onSave }: { site: Site; onSave: (site: Site, value: string) => Promise<boolean> }) {
  const [value, setValue] = useState(site.credentialRef)
  const [focused, setFocused] = useState(false)
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)
  useEffect(() => { if (!focused && !saving && !dirty) setValue(site.credentialRef) }, [site.credentialRef, focused, saving, dirty])
  return <label>凭据引用<Input value={value} placeholder="env:VERTEX_SITE_ZMPT" disabled={saving} onFocus={() => setFocused(true)} onChange={event => { setValue(event.target.value); setDirty(true) }} onBlur={() => {
    setFocused(false)
    if (value === site.credentialRef) { setDirty(false); return }
    setSaving(true)
    void onSave(site, value).then(saved => { if (saved) setDirty(false) }).finally(() => setSaving(false))
  }} /></label>
}

export function SitesRssPage({ initialSites, onChange, onRefresh, onCreateSite, onUpdateFeed, onTestFeed, onUpdateSite, onArchiveSite, onArchiveFeed, onLoadPreparations }: SitesRssPageProps) {
  const [sites, setSites] = useState(() => cloneSites(initialSites))
  const sitesRef = useRef(sites)
  useEffect(() => { sitesRef.current = cloneSites(initialSites); setSites(sitesRef.current) }, [initialSites])
  const [selectedSiteId, setSelectedSiteId] = useState('')
  const [search, setSearch] = useState('')
  const [createOpen, setCreateOpen] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [editingFeedId, setEditingFeedId] = useState<string | null>(null)
  const createTriggerRef = useRef<HTMLButtonElement>(null)
  const editTriggerRef = useRef<HTMLButtonElement | null>(null)
  const [form, setForm] = useState<SiteFormValues>(EMPTY_SITE_FORM)
  const [errors, setErrors] = useState<ReturnType<typeof validateSiteForm>>({})
  const [notice, setNotice] = useState<string | null>(null)
  const [preview, setPreview] = useState<FeedPreview | null>(null)
  const [preparationFeedId, setPreparationFeedId] = useState<string | null>(null)
  const [testingFeedId, setTestingFeedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [pendingSite, setPendingSite] = useState<Site | null>(null)
  const [updatingFeedId, setUpdatingFeedId] = useState<string | null>(null)

  const editingFeed = sites.flatMap(site => site.feeds).find(feed => feed.id === editingFeedId)
  const visibleSites = sites.filter(site => !selectedSiteId || site.id === selectedSiteId).map(site => ({ ...site, feeds: site.feeds.filter(feed => `${site.name} ${feed.name}`.toLocaleLowerCase().includes(search.toLocaleLowerCase())) })).filter(site => site.feeds.length > 0 || site.name.toLocaleLowerCase().includes(search.toLocaleLowerCase()))
  const commit = (nextSites: Site[]) => {
    sitesRef.current = nextSites
    setSites(nextSites)
    onChange?.(nextSites)
  }

  const updateForm = <K extends keyof SiteFormValues>(key: K, value: SiteFormValues[K]) => {
    setForm((current) => ({ ...current, [key]: value }))
    setErrors((current) => ({ ...current, [key]: undefined }))
    setNotice(null)
    setCreateError(null)
  }

  const createSite = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (creating) return
    const existingSite = pendingSite ? sitesRef.current.find(site => site.id === pendingSite.id) : undefined
    if (pendingSite && !existingSite) {
      setPendingSite(null)
      setCreateError('已创建的站点已被删除，草稿已保留，请重新添加。')
      return
    }
    const nextErrors = validateSiteForm(form)
    if (Object.keys(nextErrors).length > 0) {
      setErrors(nextErrors)
      setNotice(null)
      return
    }
    setCreating(true)
    setCreateError(null)
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
      const result = onCreateSite ? await onCreateSite(form, existingSite) : { site: {
        id: siteId,
        name: form.siteName.trim(),
        baseUrl: form.siteUrl.trim(),
        credentialRef: form.credentialRef.trim(),
        enabled: form.enabled,
        feeds: [feed],
      } }
      commit([...sitesRef.current.filter(site => site.id !== result.site.id), result.site])
      setSelectedSiteId(result.site.id)
      if (result.warning) {
        setPendingSite(result.site)
        setCreateError(result.warning)
        return
      }
      setPendingSite(null)
      setForm(EMPTY_SITE_FORM)
      setErrors({})
      setPreview(null)
      setCreateOpen(false)
      setNotice(result.warning ?? (onCreateSite ? '站点和 RSS 已保存。' : '站点和 RSS 已加入列表。'))
    } catch (error) {
      setCreateError(configurationError(error, '站点保存失败，请检查配置后重试。'))
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
    if (!onUpdateSite) return false
    try { const saved = await onUpdateSite(site, site.enabled, credentialRef); commit(sitesRef.current.map(item => item.id === site.id ? saved : item)); setNotice('凭据引用已保存。'); return true }
    catch (error) { setNotice(configurationError(error, '凭据引用保存失败，请检查格式或刷新后重试。')); return false }
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
      <header className="sites-rss-heading"><div><h1 id="sites-rss-title">站点与 RSS</h1><p className="muted">管理订阅来源、轮询时间和运行状态。</p></div><div className="rules-page-actions">
        <Button ref={createTriggerRef} variant="default" onClick={() => setCreateOpen(true)}><Plus aria-hidden="true" />添加站点 RSS</Button>
        {onRefresh && <Button disabled={refreshing} onClick={async () => { setRefreshing(true); try { await onRefresh() } catch { setNotice('刷新失败，请稍后重试。') } finally { setRefreshing(false) } }}><RefreshCw aria-hidden="true" className={refreshing ? 'animate-spin' : undefined} />刷新</Button>}
      </div></header>
      {notice && <p className="sites-rss-notice" role="status">{notice}</p>}
      <section className="panel rss-queue" aria-label="RSS 订阅列表">
        <div className="rss-queue-toolbar"><h2>RSS 订阅 <span className="muted">{sites.reduce((n,site) => n + site.feeds.length, 0)}</span></h2><div className="rss-queue-filters"><Input aria-label="搜索站点或 RSS" placeholder="搜索站点或 RSS…" value={search} onChange={event => setSearch(event.target.value)} /><select aria-label="选择站点" value={selectedSiteId} onChange={event => { setSelectedSiteId(event.target.value); setPreview(null) }}><option value="">全部站点</option>{sites.map(site => <option key={site.id} value={site.id}>{site.name}</option>)}</select></div></div>
        {visibleSites.length === 0 && <p className="empty-state">{sites.length ? '没有匹配的 RSS 来源。' : '暂无站点，先添加一个 RSS 来源。'}</p>}
        <div className="rss-site-list">{visibleSites.map(site => <section className="rss-site-group" key={site.id} aria-label={site.name}>
          <div className="rss-site-heading"><h3>{site.name}</h3><Badge variant="outline">{site.enabled ? '站点已启用' : '站点已停用'}</Badge><details className="site-config"><summary>站点设置</summary><div className="site-config-fields"><p className="muted">{site.baseUrl}</p><CredentialRefInput site={site} onSave={saveCredential} /><label className="checkbox-field"><Input type="checkbox" checked={site.enabled} onChange={event => { void toggleSite(site.id, event.target.checked) }} />站点启用</label>{onArchiveSite && <Button size="sm" className="danger" onClick={() => { void archive(site) }}>删除站点</Button>}</div></details></div>
          <ul className="rss-queue-items" aria-label={site.name + ' RSS'}>{site.feeds.map(feed => {
            const health = formatFeedHealth(feed)
            return <li key={feed.id} className="rss-queue-item"><span className="rss-queue-icon"><Rss aria-hidden="true" /></span><div className="rss-queue-copy"><div className="rss-queue-title"><strong>{feed.name}</strong><Badge variant="ghost" className={'status-chip tone-' + (site.enabled ? health.tone : 'neutral')}>{site.enabled ? health.label : '站点已停用'}</Badge></div><div className="rss-queue-meta"><span><Clock3 aria-hidden="true" />{formatPollInterval(feed.pollMinutes)}</span><span>最近成功：{feed.lastSuccess ? feed.lastSuccess.replace('T', ' ').replace(/\.\d+Z$/, 'Z').replace('Z', ' UTC') : '未运行'}</span><span>最近发现 {feed.discoveredCount} 条</span></div>{typeof feed.rateWaiting === 'number' && <small className="muted">限流等待 {feed.rateWaiting} 条 · 累计 {((feed.rateWaitMs ?? 0) / 1000).toFixed(1)} 秒</small>}{feed.lastError && <p className="rss-queue-error">{feed.lastError}</p>}</div>
              <div className="rss-queue-actions"><Button size="sm" variant="ghost" onClick={event => { editTriggerRef.current = event.currentTarget; setEditingFeedId(feed.id) }}><Pencil aria-hidden="true" />编辑</Button><Button size="sm" variant="ghost" disabled={updatingFeedId === feed.id} onClick={() => { void toggleFeed(feed.id, !feed.enabled) }}>{feed.enabled ? <Pause aria-hidden="true" /> : <Play aria-hidden="true" />}{feed.enabled ? '暂停 RSS' : '启用 RSS'}</Button><Button size="sm" variant="ghost" onClick={() => { void testFeed(feed) }} disabled={testingFeedId === feed.id}>{testingFeedId === feed.id ? '测试中…' : '测试 RSS'}</Button>{onLoadPreparations && <Button size="sm" variant="ghost" onClick={() => setPreparationFeedId(feed.id)}>准备状态</Button>}{onArchiveFeed && <Button size="icon" variant="ghost" aria-label="删除 RSS" title="删除 RSS" onClick={() => { void archive(site, feed) }}><Trash2 aria-hidden="true" /></Button>}</div>
            </li>
          })}</ul>{site.feeds.length === 0 && <p className="muted">此站点暂无 RSS。</p>}
        </section>)}</div>
      </section>
      <Dialog open={createOpen} onOpenChange={open => { if (!creating) setCreateOpen(open) }}><DialogContent busy={creating} onCloseAutoFocus={event => { event.preventDefault(); createTriggerRef.current?.focus() }} className="rss-create-dialog"><DialogTitle id="sites-rss-form-title">添加站点 RSS</DialogTitle><DialogDescription>填写站点信息和订阅地址。启用后会按轮询周期获取新种。</DialogDescription>
        <form className="sites-rss-form" onSubmit={createSite} aria-labelledby="sites-rss-form-title">
          {createError && <p className="field-error" role="alert">{createError}</p>}
          <div className="sites-rss-form-grid">
            <label>站点名称<Input disabled={creating || Boolean(pendingSite)} value={form.siteName} onChange={(event) => updateForm('siteName', event.target.value)} aria-invalid={Boolean(errors.siteName)} aria-describedby={errors.siteName ? 'site-name-error' : undefined} /></label>
            {errors.siteName && <p id="site-name-error" className="field-error" role="alert">{errors.siteName}</p>}
            <label>站点地址<Input disabled={creating || Boolean(pendingSite)} type="url" value={form.siteUrl} onChange={(event) => updateForm('siteUrl', event.target.value)} placeholder="https://example.test" aria-invalid={Boolean(errors.siteUrl)} aria-describedby={errors.siteUrl ? 'site-url-error' : undefined} /></label>
            {errors.siteUrl && <p id="site-url-error" className="field-error" role="alert">{errors.siteUrl}</p>}
            <label>RSS 名称<Input disabled={creating} value={form.feedName} onChange={(event) => updateForm('feedName', event.target.value)} aria-invalid={Boolean(errors.feedName)} /></label>
            {errors.feedName && <p className="field-error" role="alert">{errors.feedName}</p>}
            <label>RSS 地址<Input disabled={creating} type="url" value={form.feedUrl} onChange={(event) => updateForm('feedUrl', event.target.value)} placeholder="https://example.test/rss.xml" aria-invalid={Boolean(errors.feedUrl)} /></label>
            {errors.feedUrl && <p className="field-error" role="alert">{errors.feedUrl}</p>}
            <label>凭据引用<span className="field-hint">引用专用环境变量；内容为 Cookie / Authorization 的 JSON 对象</span><Input disabled={creating || Boolean(pendingSite)} value={form.credentialRef} onChange={(event) => updateForm('credentialRef', event.target.value)} placeholder="env:VERTEX_SITE_ZMPT" /></label>
            <label>轮询周期（分钟）<Input disabled={creating} type="number" min="1" max="1440" value={form.pollMinutes} onChange={(event) => updateForm('pollMinutes', event.target.value)} aria-invalid={Boolean(errors.pollMinutes)} /></label>
            {errors.pollMinutes && <p className="field-error" role="alert">{errors.pollMinutes}</p>}
          </div>
          <label className="checkbox-field"><Input disabled={creating || Boolean(pendingSite)} type="checkbox" checked={form.enabled} onChange={(event) => updateForm('enabled', event.target.checked)} />创建后启用</label>
          <Button type="submit" variant="default" className="primary-button" disabled={creating}>{creating ? '保存中…' : pendingSite ? '重试添加 RSS' : '添加 RSS'}</Button>
        </form>
      </DialogContent></Dialog>
      {editingFeed && <FeedEditor key={editingFeed.id} feed={editingFeed} close={() => setEditingFeedId(null)} restoreFocus={() => editTriggerRef.current?.focus()} save={async patch => { const current = sitesRef.current.flatMap(site => site.feeds).find(feed => feed.id === editingFeed.id); if (!current) throw new Error('RSS 已被删除'); const updated = onUpdateFeed ? await onUpdateFeed(current, patch) : { ...current, ...patch }; commit(sitesRef.current.map(site => ({...site, feeds: site.feeds.map(feed => feed.id === current.id ? updated : feed)}))); setNotice('RSS 已更新。') }} />}
      {onLoadPreparations && sites.flatMap(site => site.feeds).filter(feed => feed.id === preparationFeedId).map(feed => <PreparationPanel key={feed.id} feedId={feed.id} name={feed.name} load={onLoadPreparations} onClose={() => setPreparationFeedId(null)} />)}
      {preview && <aside className="panel rss-preview" aria-live="polite" aria-labelledby="rss-preview-title"><div className="sites-rss-section-heading"><h3 id="rss-preview-title">测试结果</h3><Badge variant="outline">{preview.source === 'LOCAL_FIXTURE' ? '本地预览' : '已读取'}</Badge></div><p className="muted">检查时间：{preview.checkedAt}</p><ul>{preview.entries.map(entry => <li key={entry.title + entry.publishedAt}><strong>{entry.title}</strong><span>{entry.publishedAt}</span></li>)}</ul></aside>}
    </section>
  )
}
