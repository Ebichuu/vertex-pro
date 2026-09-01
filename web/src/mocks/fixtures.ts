export type JsonObject = Record<string, unknown>

export type Page = {
  items: JsonObject[]
  next_cursor: string | null
}

export type ErrorEnvelope = {
  error: {
    code: string
    message: string
    request_id?: string
    retryable?: boolean
  }
}

export const authStatusFixture = {
  initialized: true,
  authenticated: true,
}

export const dashboardFixture: JsonObject = {
  generated_at: '2026-08-24T00:00:00Z',
  jobs_total: 3,
  user_status_counts: { DOWNLOADING: 2, WAITING: 1 },
  instances: [{
    id: 'qb-local',
    sync_state: 'READY',
    snapshot_revision: 5,
    freshness: 'FRESH',
    capacity_state: 'TRUSTED',
    physical_free_bytes: 1099511627776,
    effective_free_bytes: 1099511627776,
    storage_state: 'VERIFIED',
    reason_codes: [],
    download_rate: 0,
    upload_rate: 0,
    downloading_count: 2,
    seeding_count: 0,
    realtime_freshness: 'NOT_FRESH',
    realtime_source: '',
  }],
}

export const staleDashboardFixture: JsonObject = {
  ...dashboardFixture,
  generated_at: '2026-08-23T23:00:00Z',
  snapshot_state: 'STALE',
}

export const instancesFixture: Page = {
  items: [{ id: 'qb-local', name: '本地 qB', health: 'AVAILABLE', profile: '5.2.2' }],
  next_cursor: null,
}

export const sitesFixture: Page = {
  items: [{
    id: 'site-hdsky',
    name: 'HDSky',
    base_url: 'https://hdsky.me',
    enabled: true,
    feeds: [{ id: 'feed-hdsky', site_id: 'site-hdsky', name: 'HDSky RSS', feed_url: 'https://hdsky.me/rss', enabled: true, poll_minutes: 15, etag: null, last_modified: null, updated_at: '2026-08-29T08:00:00Z' }],
  }],
  next_cursor: null,
}

export const rulesFixture: Page = {
  items: [
    { id: 'rule-4k', name: '4K 电影', priority: 100, enabled: true, revision: 3, match_count: 148, target: 'qB-A · NVMe', state: 'PENDING', updated_at: '2026-08-25T09:00:00Z' },
    { id: 'rule-free', name: 'Freeleech', priority: 60, enabled: true, revision: 2, match_count: 83, target: '高速组', state: 'PENDING', updated_at: '2026-08-24T12:00:00Z' },
    { id: 'rule-anime', name: '动画', priority: 40, enabled: false, revision: 1, match_count: 27, target: 'qB-C · Archive', state: 'PENDING', updated_at: '2026-08-23T16:00:00Z' },
  ],
  next_cursor: null,
}

export const apiErrorFixture: ErrorEnvelope = {
  error: {
    code: 'INSTANCE_UNAVAILABLE',
    message: 'Downloader instance is unavailable',
    request_id: 'req_mock_001',
    retryable: true,
  },
}

export function assertDashboard(value: unknown): JsonObject {
  if (!isObject(value) || typeof value.generated_at !== 'string') {
    throw new Error('Dashboard fixture does not match OpenAPI JsonObject response')
  }
  return value
}

export function assertPage(value: unknown): Page {
  if (!isObject(value) || !Array.isArray(value.items) || !(typeof value.next_cursor === 'string' || value.next_cursor === null)) {
    throw new Error('Page fixture does not match OpenAPI Page schema')
  }
  if (!value.items.every(isObject)) {
    throw new Error('Page fixture items must be JsonObject values')
  }
  return value as Page
}

export function assertErrorEnvelope(value: unknown): ErrorEnvelope {
  if (!isObject(value) || !isObject(value.error) || typeof value.error.code !== 'string' || typeof value.error.message !== 'string') {
    throw new Error('Error fixture does not match OpenAPI ErrorEnvelope schema')
  }
  return value as ErrorEnvelope
}

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}