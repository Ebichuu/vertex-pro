import { useState } from 'react'
import { createRootRoute, createRoute, createRouter, Link, Outlet, useNavigate } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AppShell } from '../components/shell/AppShell'
import { DashboardCards } from '../components/dashboard/DashboardCards'
import { HistoryTable } from '../components/history/HistoryTable'
import { ApiClient, ApiRequestError } from '../generated/api-client'
import type { HistoryPageData } from '../features/history/history'
import type { DashboardData } from '../features/dashboard/dashboard'
import { RuleList } from '../components/rules/RuleList'
import type { RuleSummary } from '../features/rules/rule-list'
import { SitesRssPage } from '../components/sites/SitesRssPage'
import type { Feed, FeedPatch, FeedPreview, Site, SiteFormValues } from '../features/sites/sites'
import { siteIdForName } from '../features/sites/sites'
import { RuleEditorPage } from '../components/rules/RuleEditorPage'
import type { RuleEditorDraft } from '../features/rules/rule-editor'
import type { VertexConditionTree } from '../features/rules/model'
import { RuleDryRunPage } from '../components/rules/RuleDryRunPage'
import { recentRssItems, runLocalDryRun } from '../features/rules/dry-run'
import { HistoryDetailPage } from '../components/history/HistoryDetailPage'
import { loadLocalHistoryDetail } from '../features/history/history-detail'
import { DownloaderDetailPage } from '../components/downloaders/DownloaderDetailPage'
import { loadLocalDownloaderDetail } from '../features/downloaders/downloader-detail'
import { csrfHeaders } from '../features/auth/auth'

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
    credentialRef: '',
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
    lastSuccess: null,
    lastError: null,
    discoveredCount: 0,
  }
}

type RulesResponse = {
  items?: unknown
}

function parseRules(payload: unknown): RuleSummary[] {
  if (!payload || typeof payload !== 'object' || !Array.isArray((payload as RulesResponse).items)) {
    throw new Error('规则 API 响应格式无效')
  }
  return ((payload as RulesResponse).items as unknown[]).map(parseRule)
}

function parseRule(value: unknown): RuleSummary {
  if (!value || typeof value !== 'object') throw new Error('规则数据无效')
  const raw = value as Record<string, unknown>
  if (typeof raw.id !== 'string' || typeof raw.name !== 'string' || typeof raw.priority !== 'number' || !Number.isInteger(raw.priority) || typeof raw.enabled !== 'boolean' || typeof raw.revision !== 'number' || !Number.isInteger(raw.revision) || raw.revision < 1 || typeof raw.updated_at !== 'string') {
    throw new Error('规则数据字段无效')
  }
  return {
    id: raw.id,
    name: raw.name,
    priority: raw.priority,
    enabled: raw.enabled,
    revision: raw.revision,
    matchCount: typeof raw.match_count === 'number' ? raw.match_count : 0,
    target: typeof raw.target === 'string' ? raw.target : '未设置',
    updatedAt: raw.updated_at,
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
    body: { id: siteId, name: form.siteName.trim(), base_url: form.siteUrl.trim(), enabled: form.enabled },
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
  if (query.isPending) {
    return (
      <section aria-labelledby="dashboard-title" className="panel">
        <h1 id="dashboard-title">下载器总览</h1>
        <p className="muted">正在加载实例卡片…</p>
      </section>
    )
  }
  if (query.isError) {
    return (
      <section aria-labelledby="dashboard-title" className="panel">
        <h1 id="dashboard-title">下载器总览</h1>
        <p className="muted">仪表盘加载失败：{String(query.error)}</p>
      </section>
    )
  }
  return (
    <section aria-labelledby="dashboard-title" className="dashboard-page">
      <p className="eyebrow">DASHBOARD</p>
      <h1 id="dashboard-title">下载器总览</h1>
      <p className="muted">实时速率来自 API-10 快照，容量与新鲜度来自数据质量投影；SSE 瞬时事件驱动 15 秒自动刷新。</p>
      <DashboardCards data={query.data} onOpen={(downloaderId) => { void navigate({ to: '/downloaders/$downloaderId', params: { downloaderId } }) }} />
    </section>
  )
}

const AggregationPreviewPage = () => <RuleDryRunPage items={recentRssItems} runDryRun={runLocalDryRun} />

const sampleRules: RuleSummary[] = [
  { id: 'rule-4k', name: '4K 电影', priority: 100, enabled: true, revision: 3, matchCount: 148, target: 'qB-A · NVMe', updatedAt: '2026-08-25T09:00:00Z' },
  { id: 'rule-free', name: 'Freeleech', priority: 60, enabled: true, revision: 2, matchCount: 83, target: '高速组', updatedAt: '2026-08-24T12:00:00Z' },
  { id: 'rule-anime', name: '动画', priority: 40, enabled: false, revision: 1, matchCount: 27, target: 'qB-C · Archive', updatedAt: '2026-08-23T16:00:00Z' },
]

function Rules() {
  const navigate = useNavigate()
  const query = useQuery({
    queryKey: ['rules'],
    queryFn: () => apiClient.listRules<RulesResponse>(),
  })
  if (query.isPending) {
    return (
      <section aria-labelledby="rules-title" className="panel">
        <h1 id="rules-title">规则配置</h1>
        <p className="muted">正在加载投递规则…</p>
      </section>
    )
  }
  if (query.isError) {
    return (
      <section aria-labelledby="rules-title" className="panel">
        <h1 id="rules-title">规则配置</h1>
        <p className="muted">规则加载失败：{String(query.error)}</p>
      </section>
    )
  }
  const updateRule = async (rule: RuleSummary, expectedRevision: number, patch: Partial<Pick<RuleSummary, 'name' | 'priority' | 'enabled'>>) => {
    try {
      const payload = await apiClient.updateRule<unknown>(rule.id, { body: patch, headers: { 'If-Match': `"${expectedRevision}"`, ...csrfHeaders() } })
      return { ok: true as const, rule: parseRule(payload) }
    } catch (error) {
      if (error instanceof ApiRequestError && error.status === 409 && error.body.code === 'REVISION_CONFLICT') {
        const current = await apiClient.getRule<unknown>(rule.id)
        return { ok: false as const, status: 409 as const, code: 'REVISION_CONFLICT' as const, current: parseRule(current) }
      }
      throw error
    }
  }
  return (
    <section className="rules-page" aria-labelledby="rules-title">
      <div className="rules-page-heading">
        <div>
          <p className="eyebrow">AUTOMATION RULES</p>
          <h1 id="rules-title">规则配置</h1>
          <p className="muted">调整规则优先级和启用状态，再回到聚合预览检查最终决策。</p>
        </div>
        <div className="rules-page-actions"><Link to="/aggregation" className="ghost-button">Dry-run 预览</Link><span className="page-badge">LOCAL ONLY</span></div>
      </div>
      <RuleList rules={parseRules(query.data)} onUpdateRule={updateRule} onEdit={(rule) => { void navigate({ to: '/rules/$ruleId/edit', params: { ruleId: rule.id } }) }} />
      <Link to="/" className="text-link">返回总览</Link>
    </section>
  )
}

const sampleInstances = [
  { id: 'qb-a', label: 'qB-A · NVMe' },
  { id: 'qb-b', label: 'qB-B · Bulk' },
  { id: 'qb-c', label: 'qB-C · Archive' },
]

const sampleGroups = [
  { id: 'fast', label: '高速组' },
  { id: 'archive', label: '归档组' },
]

function ruleDraft(rule: RuleSummary): RuleEditorDraft {
  const conditionTree: VertexConditionTree = {
    version: 1,
    root: {
      kind: 'group',
      operator: 'and',
      children: [
        { kind: 'leaf', field: 'site', operator: 'equals', value: { type: 'string', string: 'HDSky' } },
        { kind: 'leaf', field: 'size', operator: 'greater_or_equal', value: { type: 'number', number: 21_474_836_480 } },
        { kind: 'leaf', field: 'title', operator: 'contains', value: { type: 'string', string: '2160p' } },
      ],
    },
  }
  const groupTarget = rule.target === '高速组'
  return {
    id: rule.id,
    name: rule.name,
    enabled: rule.enabled,
    priority: rule.priority,
    revision: rule.revision,
    conditionTree,
    actions: {
      targetMode: groupTarget ? 'INSTANCE_GROUP' : 'FIXED_INSTANCE',
      targetInstanceId: groupTarget ? '' : rule.id === 'rule-anime' ? 'qb-c' : 'qb-a',
      targetGroupId: groupTarget ? 'fast' : '',
      category: rule.id === 'rule-anime' ? 'anime' : 'movie/4k',
      tags: ['vertex-pro', rule.id === 'rule-free' ? 'freeleech' : '4k'],
      savePathPolicy: 'CATEGORY_SUBDIRECTORY',
      sequentialDownload: false,
      participationWindowMinutes: 20,
    },
  }
}

function RuleEditorRoutePage() {
  const { ruleId } = ruleEditorRoute.useParams()
  const navigate = useNavigate()
  const rule = sampleRules.find((item) => item.id === ruleId) ?? { ...sampleRules[0], id: ruleId, name: '未命名规则' }
  return <RuleEditorPage initialDraft={ruleDraft(rule)} instances={sampleInstances} groups={sampleGroups} onCancel={() => { void navigate({ to: '/rules' }) }} />
}

type PlaceholderPageProps = { id: string; eyebrow: string; title: string; note: string }

function PlaceholderPage({ id, eyebrow, title, note }: PlaceholderPageProps) {
  const titleId = `${id}-title`
  return (
    <section className="panel" aria-labelledby={titleId}>
      <p className="eyebrow">{eyebrow}</p>
      <h1 id={titleId}>{title}</h1>
      <p className="muted">{note}</p>
    </section>
  )
}

function Sites() {
  const query = useQuery({
    queryKey: ['sites'],
    queryFn: () => apiClient.listSites<SitesResponse>(),
  })
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
    initialSites={parseSites(query.data)}
    onCreateSite={(form) => createSiteOnAPI(form, parseSites(query.data).length)}
    onUpdateFeed={updateFeedOnAPI}
    onTestFeed={testFeedOnAPI}
  />
}

function History() {
  const navigate = useNavigate()
  const [statusFilter, setStatusFilter] = useState('')
  const [pages, setPages] = useState<HistoryPageData[]>([])
  const query = useQuery({
    queryKey: ['history', statusFilter],
    queryFn: () => apiClient.listJobs<HistoryPageData>({ query: { user_status: statusFilter || undefined, limit: '50' } }),
  })
  if (query.isPending) {
    return (
      <section aria-labelledby="history-title" className="panel">
        <h1 id="history-title">历史</h1>
        <p className="muted">正在加载投递历史…</p>
      </section>
    )
  }
  const current: HistoryPageData = query.data ?? { items: [], next_cursor: '' }
  const visible = pages.length === 0 || pages[pages.length - 1].next_cursor !== current.next_cursor ? [...pages, current] : pages
  return (
    <section aria-labelledby="history-title" className="history-page">
      <p className="eyebrow">HISTORY</p>
      <h1 id="history-title">历史</h1>
      <p className="muted">投递历史在种子被删除后仍然保留；状态使用用户语言，删除行带删除时间与证据来源。</p>
      <HistoryTable
        pages={visible}
        statusFilter={statusFilter}
        onStatusFilterChange={(next) => { setStatusFilter(next); setPages([]) }}
        hasMore={Boolean(current.next_cursor)}
        loading={query.isFetching}
        onLoadMore={() => {
          void query.refetch()
        }}
        onOpen={(jobId) => { void navigate({ to: '/history/$jobId', params: { jobId } }) }}
      />
    </section>
  )
}

function HistoryDetailRoutePage() {
  const { jobId } = historyDetailRoute.useParams()
  const navigate = useNavigate()
  return <HistoryDetailPage jobId={jobId} loadDetail={loadLocalHistoryDetail} onBack={() => { void navigate({ to: '/history' }) }} />
}
function DownloaderDetailRoutePage() {
  const { downloaderId } = downloaderDetailRoute.useParams()
  const navigate = useNavigate()
  return <DownloaderDetailPage downloaderId={downloaderId} loadDetail={loadLocalDownloaderDetail} onBack={() => { void navigate({ to: '/' }) }} />
}
const Exceptions = () => (
  <PlaceholderPage id="exceptions" eyebrow="EXCEPTIONS" title="异常" note="UNKNOWN、容量不可信等需人工处理的异常将在命令层接线后开放。" />
)
const Settings = () => (
  <PlaceholderPage id="settings" eyebrow="SYSTEM" title="系统设置" note="时区、保留期、限流与安全设置将在设置 API 接线后开放。" />
)

const rootRoute = createRootRoute({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
})
const dashboardRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: Dashboard })
const aggregationRoute = createRoute({ getParentRoute: () => rootRoute, path: '/aggregation', component: AggregationPreviewPage })
const sitesRoute = createRoute({ getParentRoute: () => rootRoute, path: '/sites', component: Sites })
const rulesRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rules', component: Rules })
const ruleEditorRoute = createRoute({ getParentRoute: () => rootRoute, path: '/rules/$ruleId/edit', component: RuleEditorRoutePage })
const historyRoute = createRoute({ getParentRoute: () => rootRoute, path: '/history', component: History })
const historyDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/history/$jobId', component: HistoryDetailRoutePage })
const downloaderDetailRoute = createRoute({ getParentRoute: () => rootRoute, path: '/downloaders/$downloaderId', component: DownloaderDetailRoutePage })
const exceptionsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/exceptions', component: Exceptions })
const settingsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/settings', component: Settings })
const routeTree = rootRoute.addChildren([dashboardRoute, aggregationRoute, sitesRoute, rulesRoute, ruleEditorRoute, historyRoute, historyDetailRoute, downloaderDetailRoute, exceptionsRoute, settingsRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
