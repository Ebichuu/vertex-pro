export type Site = {
  updatedAt?: string
  id: string
  name: string
  baseUrl: string
  credentialRef: string
  enabled: boolean
  feeds: Feed[]
}

export type Feed = {
  id: string
  siteId: string
  name: string
  url: string
  enabled: boolean
  pollMinutes: number
  lastSuccess: string | null
  lastError: string | null
  discoveredCount: number
  rateWaiting?: number
  rateWaitMs?: number
  updatedAt?: string
}

export type FeedPatch = {
  enabled?: boolean
  name?: string
  url?: string
  pollMinutes?: number
}

export type Preparation = {
  id: string
  title: string
  size_bytes: number | null
  state: 'QUEUED' | 'RATE_WAIT' | 'PREPARING' | 'RETRY_WAIT' | 'READY' | 'FILTERED' | 'BASELINE' | 'FAILED'
  attempts: number
  max_attempts: number
  rate_wait_ms: number
  discovered_at: string
  next_attempt_at: string
  deadline_at: string
  last_error_code: string
  job_id: string
}
export type PreparationPage = { items: Preparation[]; next_cursor: string }
export type PreparationLoader = (feedId: string, cursor: string, signal: AbortSignal) => Promise<PreparationPage>

export type SiteFormValues = {
  siteName: string
  siteUrl: string
  feedName: string
  feedUrl: string
  credentialRef: string
  pollMinutes: string
  enabled: boolean
}

export type SiteFormErrors = Partial<Record<keyof SiteFormValues, string>>

export type FeedPreview = {
  feedId: string
  source: 'LOCAL_FIXTURE' | 'READ_ONLY_FETCH'
  checkedAt: string
  entries: ReadonlyArray<{ title: string; publishedAt: string }>
}

export function siteIdForName(value: string, existingCount: number): string {
  const normalized = value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-')
  return `${normalized.replace(/^-|-$/g, '') || 'site'}-${existingCount + 1}`
}

export const EMPTY_SITE_FORM: SiteFormValues = {
  siteName: '',
  siteUrl: '',
  feedName: '',
  feedUrl: '',
  credentialRef: '',
  pollMinutes: '15',
  enabled: true,
}

export function validateSiteForm(values: SiteFormValues): SiteFormErrors {
  const errors: SiteFormErrors = {}
  if (!values.siteName.trim()) errors.siteName = '请输入站点名称。'
  if (!isHttpUrl(values.siteUrl)) errors.siteUrl = '请输入有效的 HTTP(S) 站点地址。'
  if (!values.feedName.trim()) errors.feedName = '请输入 RSS 名称。'
  if (!isHttpUrl(values.feedUrl)) errors.feedUrl = '请输入有效的 HTTP(S) RSS 地址。'
  const pollMinutes = Number(values.pollMinutes)
  if (!Number.isInteger(pollMinutes) || pollMinutes < 1 || pollMinutes > 1440) {
    errors.pollMinutes = '轮询周期需为 1 到 1440 分钟。'
  }
  return errors
}

export function isHttpUrl(value: string): boolean {
  try {
    const url = new URL(value.trim())
    return (url.protocol === 'http:' || url.protocol === 'https:') && Boolean(url.hostname) && !url.username && !url.password
  } catch {
    return false
  }
}

export function formatPollInterval(minutes: number): string {
  return minutes === 1 ? '每 1 分钟' : `每 ${minutes} 分钟`
}

export function formatFeedHealth(feed: Feed): { label: string; tone: 'ok' | 'warn' | 'bad' } {
  if (!feed.enabled) return { label: '已停用', tone: 'warn' }
  if (feed.lastError === 'RSS_ENTRY_INVALID') return { label: '部分条目无效', tone: 'warn' }
  if (feed.lastError) return { label: '最近失败', tone: 'bad' }
  if (feed.lastSuccess) return { label: '运行正常', tone: 'ok' }
  return { label: '等待首次拉取', tone: 'warn' }
}

export function localPreview(feed: Feed, checkedAt = '本地预览'): FeedPreview {
  return {
    feedId: feed.id,
    source: 'LOCAL_FIXTURE',
    checkedAt,
    entries: [
      { title: `${feed.name} 示例条目 01`, publishedAt: '等待真实拉取' },
      { title: `${feed.name} 示例条目 02`, publishedAt: '等待真实拉取' },
    ],
  }
}
