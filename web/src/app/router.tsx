import { useMemo, useState } from 'react'
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate } from '@tanstack/react-router'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { AppShell } from '../components/shell/AppShell'
import { DashboardCards } from '../components/dashboard/DashboardCards'
import { DashboardSummary } from '../components/dashboard/DashboardSummary'
import { Button } from '../components/ui/button'
import { ArrowUpRight, RefreshCw } from 'lucide-react'
import { HistoryTable } from '../components/history/HistoryTable'
import { ApiClient } from '../generated/api-client'
import type { HistoryPageData } from '../features/history/history'
import type { DashboardData } from '../features/dashboard/dashboard'
import { SitesRssPage } from '../components/sites/SitesRssPage'
import type { Feed, FeedPatch, FeedPreview, PreparationPage, Site, SiteFormValues } from '../features/sites/sites'
import { siteIdForName } from '../features/sites/sites'
import { RequestState, RulesPage, SavedRuleEditor } from './rules-pages'
import { LiveDryRunPage } from '../components/rules/LiveDryRunPage'
import { ExceptionsPage } from '../components/exceptions/ExceptionsPage'
import { HistoryDetailPage } from '../components/history/HistoryDetailPage'
import { loadHistoryDetail } from '../features/history/api'
import { DownloaderDetailPage } from '../components/downloaders/DownloaderDetailPage'
import { loadDownloaderDetail } from '../features/downloaders/api'
import { csrfHeaders } from '../features/auth/auth'
import { SettingsPage } from '../components/settings/SettingsPage'
import { downloadDiagnostics, loadSettings, saveSettings } from '../features/settings/settings'
import { DisplayTimezoneContext } from '../features/settings/timezone'

const apiClient = new ApiClient(window.location.origin)

type SitesResponse = {
  items?: unknown
}

type CreateSiteForm = SiteFormValues

type CreateSiteResult = {
  site: Site
  warning?: string
}

function parseSites(payload: unknown): Site[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray((payload as SitesResponse).items)) {
    throw new Error('站点 API 响应格式无效')
  }
  return ((payload as SitesResponse).items as unknown[]).map(parseSite)
}

function parseSite(value: unknown): Site {
  if (!value || typeof value !== 'object') throw new Error('站点数据无效')
  const raw = value as Record<string, unknown>
  if (typeof raw.id !== 'string' || typeof raw.name !== 'string' || typeof raw.base_url !== 'string' || typeof raw.enabled !== 'boolean') {
    throw new Error('站点数据字段无效')
  }
  const siteId = raw.id
  const rawFeeds = Array.isArray(raw.feeds) ? raw.feeds : []
  return {
    id: siteId,
    name: raw.name,
    baseUrl: raw.base_url,
    credentialRef: typeof raw.credential_ref === 'string' ? raw.credential_ref : '',
    updatedAt: typeof raw.updated_at === 'string' ? raw.updated_at : '',
    enabled: raw.enabled,
    feeds: rawFeeds.map((feed) => parseFeed(feed, siteId)),
  }
}

function parseFeed(value: unknown, fallbackSiteId: string): Feed {
  if (!value || typeof value !== 'object') throw new Error('RSS 数据无效')
  const raw = value as Record<string, unknown>
  if (typeof raw.id !== 'string' || typeof raw.name !== 'string' || typeof raw.feed_url !== 'string' || typeof raw.enabled !== 'boolean') {
    throw new Error('RSS 数据字段无效')
  }
  return {
    id: raw.id,
    siteId: typeof raw.site_id === 'string' ? raw.site_id : fallbackSiteId,
    name: raw.name,
    url: raw.feed_url,
    enabled: raw.enabled,
    pollMinutes: typeof raw.poll_minutes === 'number' ? raw.poll_minutes : 15,
    updatedAt: typeof raw.updated_at === 'string' ? raw.updated_at : '',
    lastSuccess: typeof raw.last_success_at === 'string' ? raw.last_success_at : null,
    lastError: typeof raw.last_error_code === 'string' && raw.last_error_code ? raw.last_error_code : null,
    rateWaiting: typeof raw.rate_waiting === 'number' ? raw.rate_waiting : undefined,
    rateWaitMs: typeof raw.rate_wait_ms === 'number' ? raw.rate_wait_ms : undefined,
    discoveredCount: typeof raw.last_entries === 'number' ? raw.last_entries : 0,
  }
}

function parseCreatedSite(form: CreateSiteForm, payload: unknown): Site {
  const parsed = parseSite(payload)
  return { ...parsed, credentialRef: form.credentialRef.trim(), enabled: form.enabled }
}

async function updateFeedOnAPI(feed: Feed, patch: FeedPatch): Promise<Feed> {
  if (!feed.updatedAt) {
    throw new Error('RSS 版本不可用，请刷新页面后重试。')
  }
  const payload = await apiClient.updateFeed<unknown>(feed.id, {
    body: {
      enabled: patch.enabled,
      name: patch.name,
      feed_url: patch.url,
      poll_minutes: patch.pollMinutes,
    },
    headers: { 'If-Match': `"${feed.updatedAt}"`, ...csrfHeaders() },
  })
  return parseFeed(payload, feed.siteId)
}

function parseFeedPreview(value: unknown, fallbackFeedId: string): FeedPreview {
  if (!value || typeof value !== 'object') throw new Error('RSS 测试结果无效')
  const raw = value as Record<string, unknown>
  if (typeof raw.feed_id !== 'string' || typeof raw.checked_at !== 'string' || (raw.source !== 'READ_ONLY_FETCH' && raw.source !== 'LOCAL_FIXTURE') || !Array.isArray(raw.entries)) {
    throw new Error('RSS 测试结果字段无效')
  }
  return {
    feedId: raw.feed_id || fallbackFeedId,
    source: raw.source,
    checkedAt: raw.checked_at,
    entries: raw.entries.map((entry) => {
      if (!entry || typeof entry !== 'object') throw new Error('RSS 测试条目无效')
      const item = entry as Record<string, unknown>
      if (typeof item.title !== 'string' || typeof item.published_at !== 'string') throw new Error('RSS 测试条目字段无效')
      return { title: item.title, publishedAt: item.published_at }
    }),
  }
}

async function testFeedOnAPI(feed: Feed): Promise<FeedPreview> {
  const suffix = typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  const payload = await apiClient.testFeed<unknown>(feed.id, {
    body: {},
    headers: { 'Idempotency-Key': `feed-test-${feed.id}-${suffix}`, ...csrfHeaders() },
  })
  const raw = payload as Record<string, unknown>
  if (raw.state === 'FAILED') {
    const failure = raw.error as Record<string, unknown> | null
    throw new Error(typeof failure?.code === 'string' ? failure.code : 'RSS 测试命令失败')
  }
  return parseFeedPreview(raw.result, feed.id)
}

async function createSiteOnAPI(form: CreateSiteForm, existingSiteCount: number): Promise<CreateSiteResult> {
  const siteId = siteIdForName(form.siteName, existingSiteCount)
  const sitePayload = await apiClient.createSite<unknown>({
    body: { id: siteId, name: form.siteName.trim(), base_url: form.siteUrl.trim(), enabled: form.enabled, credential_ref: form.credentialRef.trim() },
    headers: csrfHeaders(),
  })
  const site = parseCreatedSite(form, sitePayload)
  let feedPayload: unknown
  try {
    feedPayload = await apiClient.createFeed<unknown>({
      body: { id: `${site.id}-feed-1`, site_id: site.id, name: form.feedName.trim(), feed_url: form.feedUrl.trim(), enabled: form.enabled, poll_minutes: Number(form.pollMinutes) },
      headers: csrfHeaders(),
    })
  } catch (error) {
    return { site, warning: `站点已保存，但 RSS 保存失败：${String(error)}` }
  }
  return { site: { ...site, feeds: [{ ...parseFeed(feedPayload, site.id), pollMinutes: Number(form.pollMinutes) }] } }
}

function Dashboard() {
  const navigate = useNavigate()
  const query = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => apiClient.getDashboard<DashboardData>(),
    refetchInterval: 15_000,
  })
  const recent = useQuery({ queryKey: ['history', 'recent'], queryFn: () => apiClient.listJobs<HistoryPageData>({ query: { limit: '5' } }), refetchInterval: 15_000 })
  if (query.isPending) {
    return (
      <section aria-labelledby="dashboard-title" className="panel">
        <h1 id="dashboard-title">下载器总览</h1>
        <p className="muted">正在加载实例卡片…</p>
        <div className="dashboard-summary" aria-hidden="true">{[1, 2, 3, 4].map(key => <div className="loading-skeleton" key={key} />)}</div>
      </section>
    )
  }
  if (query.isError && !query.data) {
    return (
      <section aria-labelledby="dashboard-title" className="panel">
        <h1 id="dashboard-title">下载器总览</h1>
        <p className="muted" role="alert">仪表盘加载失败：{String(query.error)}</p>
        <Button onClick={() => { void query.refetch() }} disabled={query.isFetching}>重新加载</Button>
      </section>
    )
  }
  return (
    <section aria-labelledby="dashboard-title" className="dashboard-page">
      <div className="page-heading"><div><h1 id="dashboard-title">下载器总览</h1>
      <p className="muted">下载速度、容量与任务动态，每 15 秒自动更新。</p></div>
      <Button onClick={() => { void query.refetch(); void recent.refetch() }} disabled={query.isFetching || recent.isFetching}><RefreshCw aria-hidden="true" className={query.isFetching || recent.isFetching ? 'animate-spin' : undefined} />{query.isFetching ? '刷新中…' : '刷新'}</Button></div>
      {query.isError && <p role="alert" className="inline-warning">刷新失败，当前展示上次成功获取的数据。请稍后重试。</p>}
      <DashboardSummary data={query.data!} />
      <DashboardCards data={query.data!} onOpen={(downloaderId) => { void navigate({ to: '/downloaders/$downloaderId', params: { downloaderId } }) }} />
      <section className="recent-history" aria-labelledby="recent-history-title">
        <div className="section-heading"><h2 id="recent-history-title">最近投递</h2><Button asChild variant="ghost" size="sm"><Link to="/history">查看全部<ArrowUpRight aria-hidden="true" /></Link></Button></div>
        {recent.isPending ? <p className="muted">正在加载最近记录…</p> : recent.isError ? <p role="alert" className="inline-warning">最近记录加载失败。<Button variant="link" size="sm" onClick={() => { void recent.refetch() }}>重试</Button></p> : <HistoryTable compact pages={[recent.data]} statusFilter="" onStatusFilterChange={() => {}} hasMore={false} loading={false} onLoadMore={() => {}} onOpen={jobId => { void navigate({ to: '/history/$jobId', params: { jobId } }) }} />}
      </section>
    </section>
  )
}

function Rules() {
  const navigate = useNavigate()
  return <RulesPage onEdit={(ruleId) => { void navigate({ to: '/rules/$ruleId/edit', params: { ruleId } }) }} onCreate={() => { void navigate({ to: '/rules/new' }) }} />
}
function RuleEditorRoutePage() {
  const { ruleId } = ruleEditorRoute.useParams()
  const navigate = useNavigate()
  return <SavedRuleEditor ruleId={ruleId} onBack={() => { void navigate({ to: '/rules' }) }} />
}
function NewRuleRoutePage() {
  const navigate = useNavigate()
  return <SavedRuleEditor onBack={() => { void navigate({ to: '/rules' }) }} />
}
function Sites() {
  const query = useQuery({
    queryKey: ['sites'],
    queryFn: () => apiClient.listSites<SitesResponse>(),
    refetchInterval: 15000,
  })
  const siteRows = useMemo(() => query.data ? parseSites(query.data) : [], [query.data])
  const refreshAfter = async <T,>(operation: () => Promise<T>): Promise<T> => {
    try { return await operation() } finally { void query.refetch() }
  }
  if (query.isPending) {
    return (
      <section aria-labelledby="sites-title" className="panel">
        <h1 id="sites-title">站点与 RSS</h1>
        <p className="muted">正在加载站点来源…</p>
      </section>
    )
  }
  if (query.isError) {
    return (
      <section aria-labelledby="sites-title" className="panel">
        <h1 id="sites-title">站点与 RSS</h1>
        <p className="muted">站点来源加载失败：{String(query.error)}</p>
      </section>
    )
  }
  return <SitesRssPage
    initialSites={siteRows}
    onCreateSite={(form) => refreshAfter(() => createSiteOnAPI(form, siteRows.length))}
    onUpdateSite={(site, enabled, credentialRef) => refreshAfter(async () => {
      const saved = await apiClient.updateSite<{updated_at: string}>(site.id, { body: { enabled, credential_ref: credentialRef }, headers: { ...csrfHeaders(), 'If-Match': `"${site.updatedAt}"` } })
      return { ...site, enabled, credentialRef: credentialRef ?? site.credentialRef, updatedAt: saved.updated_at }
    })}
    onArchiveSite={site => refreshAfter(async () => { await apiClient.archiveSite(site.id, { headers: { ...csrfHeaders(), 'If-Match': `"${site.updatedAt}"` } }) })}
    onArchiveFeed={feed => refreshAfter(async () => { await apiClient.archiveFeed(feed.id, { headers: { ...csrfHeaders(), 'If-Match': `"${feed.updatedAt}"` } }) })}
    onUpdateFeed={(feed, patch) => refreshAfter(() => updateFeedOnAPI(feed, patch))}
    onTestFeed={testFeedOnAPI}
    onLoadPreparations={(id, cursor, signal) => apiClient.listFeedPreparations<PreparationPage>(id, { query: { cursor: cursor || undefined, limit: '50' }, signal })}
  />
}

function History() {
  const navigate = useNavigate()
  const [statusFilter, setStatusFilter] = useState('')
  const query = useInfiniteQuery({
    queryKey: ['history', statusFilter],
    initialPageParam: '',
    queryFn: ({ pageParam }) => apiClient.listJobs<HistoryPageData>({ query: { user_status: statusFilter || undefined, limit: '50', cursor: pageParam || undefined } }),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    refetchInterval: (activeQuery) => 15_000 * Math.max(1, activeQuery.state.data?.pages.length ?? 1),
  })
  if (query.isPending) {
    return (
      <section aria-labelledby="history-title" className="panel">
        <h1 id="history-title">历史</h1>
        <p className="muted">正在加载投递历史…</p>
      </section>
    )
  }
  return (
    <section aria-labelledby="history-title" className="history-page">
      <p className="eyebrow">HISTORY</p>
      <h1 id="history-title">历史</h1>
      <p className="muted">投递历史在种子被删除后仍然保留；状态使用用户语言，删除行带删除时间与证据来源。</p>
      <HistoryTable
        pages={query.data?.pages ?? []}
        statusFilter={statusFilter}
        onStatusFilterChange={setStatusFilter}
        hasMore={query.hasNextPage}
        loading={query.isFetching}
        onLoadMore={() => {
          void query.fetchNextPage()
        }}
        onOpen={(jobId) => { void navigate({ to: '/history/$jobId', params: { jobId } }) }}
      />
      {query.isError && <RequestState error={query.error} retry={() => { void query.refetch() }} />}
    </section>
  )
}

function HistoryDetailRoutePage() {
  const { jobId } = historyDetailRoute.useParams()
  const navigate = useNavigate()
  return <HistoryDetailPage jobId={jobId} loadDetail={loadHistoryDetail} onBack={() => { void navigate({ to: '/history' }) }} />
}
function DownloaderDetailRoutePage() {
  const { downloaderId } = downloaderDetailRoute.useParams()
  const navigate = useNavigate()
  return <DownloaderDetailPage downloaderId={downloaderId} loadDetail={loadDownloaderDetail} onBack={() => { void navigate({ to: '/' }) }} />
}
const Settings = () => (
  <SettingsPage loadSettings={loadSettings} saveSettings={saveSettings} downloadDiagnostics={downloadDiagnostics} />
)

function AppLayout() {
  const query = useQuery({ queryKey: ['settings', 'active'], queryFn: ({ signal }) => loadSettings(signal), staleTime: 60_000, retry: 1 })
  return <DisplayTimezoneContext.Provider value={query.data?.active.timezone ?? 'UTC'}>
    <AppShell>
      <Outlet />
    </AppShell>
  </DisplayTimezoneContext.Provider>
}

const rootRoute = createRootRoute({ component: AppLayout })
const dashboardRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: Dashboard })
const aggregationRoute = createRoute({ getParentRoute: () => rootRoute, path: '/aggregation', component: LiveDryRunPage })
const sitesRoute = createRoute({ getParentRoute: () => rootRoute, path: '/sites', component: Sites })
const rulesRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rules', component: Rules })
const ruleEditorRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rules/$ruleId/edit', component: RuleEditorRoutePage })
const newRuleRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rules/new', component: NewRuleRoutePage })
const historyRoute = createRoute({ getParentRoute: () => rootRoute, path: '/history', component: History })
const historyDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/history/$jobId', component: HistoryDetailRoutePage })
const downloaderDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/downloaders/$downloaderId', component: DownloaderDetailRoutePage })
const exceptionsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/exceptions', component: ExceptionsPage })
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: Settings })
const routeTree = rootRoute.addChildren([dashboardRoute, aggregationRoute, sitesRoute, rulesRoute, newRuleRoute, ruleEditorRoute, historyRoute, historyDetailRoute, downloaderDetailRoute, exceptionsRoute, settingsRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
