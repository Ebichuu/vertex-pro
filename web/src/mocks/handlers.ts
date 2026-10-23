import { http, HttpResponse } from 'msw'
import { settingsHandlers } from './settings'
import { apiErrorFixture, authStatusFixture, dashboardFixture, instancesFixture, rulesFixture, sitesFixture, staleDashboardFixture } from './fixtures'


export const handlers = [
  ...settingsHandlers(),
  http.get('/api/v1/auth/status', () => HttpResponse.json(authStatusFixture)),
  http.get('/api/v1/dashboard', ({ request }) => {
    const scenario = new URL(request.url).searchParams.get('scenario')
    if (scenario === 'error') {
      return HttpResponse.json(apiErrorFixture, { status: 503 })
    }
    if (scenario === 'stale') {
      return HttpResponse.json(staleDashboardFixture)
    }
    return HttpResponse.json(dashboardFixture)
  }),
  http.get('/api/v1/instances', () => HttpResponse.json(instancesFixture)),
  http.get('/api/v1/sites', () => HttpResponse.json(sitesFixture)),
  http.post('/api/v1/sites', async ({ request }) => {
    const body = await request.json() as Record<string, unknown>
    return HttpResponse.json({ id: body.id, name: body.name, base_url: body.base_url, enabled: body.enabled, feeds: [] }, { status: 201, headers: { Location: `/api/v1/sites/${String(body.id)}`, ETag: '"created"' } })
  }),
  http.post('/api/v1/feeds', async ({ request }) => {
    const body = await request.json() as Record<string, unknown>
    return HttpResponse.json({ id: body.id, site_id: body.site_id, name: body.name, feed_url: body.feed_url, enabled: body.enabled, poll_minutes: body.poll_minutes, updated_at: '2026-08-29T08:00:00Z' }, { status: 201, headers: { Location: `/api/v1/feeds/${String(body.id)}`, ETag: '"created"' } })
  }),
  http.put('/api/v1/feeds/:id', async ({ params, request }) => {
    const site = sitesFixture.items.find((item) => item.id === 'site-hdsky')
    const feed = site && Array.isArray(site.feeds) ? site.feeds.find((item) => item.id === params.id) as Record<string, unknown> | undefined : undefined
    if (!feed || !request.headers.get('If-Match')) {
      return HttpResponse.json({ error: { code: 'REVISION_CONFLICT', message: 'feed revision changed' } }, { status: 409 })
    }
    const patch = await request.json() as Record<string, unknown>
    return HttpResponse.json({ ...feed, ...patch, updated_at: '2026-08-29T08:01:00Z' }, { headers: { ETag: '"2026-08-29T08:01:00Z"' } })
  }),
  http.post('/api/v1/feeds/:id/test', ({ params, request }) => {
    if (!request.headers.get('Idempotency-Key')) {
      return HttpResponse.json({ error: { code: 'INVALID_REQUEST', message: 'Idempotency-Key is required' } }, { status: 400 })
    }
    return HttpResponse.json({
      command_id: `cmd-test-${String(params.id)}`,
      state: 'SUCCEEDED',
      revision: 2,
      result: {
        feed_id: String(params.id),
        source: 'READ_ONLY_FETCH',
        checked_at: '2026-08-29T08:02:00Z',
        feed_url: 'https://hdsky.me/rss',
        format: 'rss',
        entries: [{ id: 'entry-1', title: 'Remote RSS item', link: 'https://hdsky.me/item-1', published_at: '2026-08-29T08:00:00Z' }],
      },
      error: null,
    }, { status: 202, headers: { Location: `/api/v1/commands/cmd-test-${String(params.id)}` } })
  }),
  http.get('/api/v1/rules', () => HttpResponse.json(rulesFixture)),
  http.get('/api/v1/rules/:id', ({ params }) => {
    const rule = rulesFixture.items.find((item) => item.id === params.id)
    return rule ? HttpResponse.json(rule, { headers: { ETag: `"${String(Number(rule.revision))}"` } }) : HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'rule not found' } }, { status: 404 })
  }),
  http.put('/api/v1/rules/:id', async ({ params, request }) => {
    const rule = rulesFixture.items.find((item) => item.id === params.id)
    if (!rule || !request.headers.get('If-Match')) return HttpResponse.json({ error: { code: 'REVISION_CONFLICT', message: 'stale rule' } }, { status: 409 })
    const patch = await request.json() as Record<string, unknown>
    return HttpResponse.json({ ...rule, ...patch, revision: Number(rule.revision) + 1, updated_at: '2026-08-25T10:00:00Z' }, { headers: { ETag: `"${String(Number(rule.revision) + 1)}"` } })
  }),
]
