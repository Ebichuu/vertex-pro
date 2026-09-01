import { describe, expect, it } from 'vitest'
import { fetchDashboard, fetchInstances } from '../api/client'
import { assertErrorEnvelope, assertPage } from '../mocks/fixtures'

describe('OpenAPI-backed MSW fixtures', () => {
  it('returns a dashboard fixture matching the JsonObject response', async () => {
    await expect(fetchDashboard()).resolves.toMatchObject({ instances: [{ id: 'qb-local' }], jobs_total: 3 })
  })

  it('returns a paginated instances fixture matching the Page schema', async () => {
    const page = await fetchInstances()
    expect(assertPage(page).next_cursor).toBeNull()
  })

  it('models stale data without mapping it to success', async () => {
    const response = await fetch('/api/v1/dashboard?scenario=stale')
    const body = await response.json()
    expect(body.snapshot_state).toBe('STALE')
  })

  it('models a stable error envelope', async () => {
    const response = await fetch('/api/v1/dashboard?scenario=error')
    const body = await response.json()
    expect(response.status).toBe(503)
    expect(assertErrorEnvelope(body).error.code).toBe('INSTANCE_UNAVAILABLE')
  })

  it('rejects a schema-incompatible page fixture', () => {
    expect(() => assertPage({ items: [] })).toThrow('Page fixture does not match OpenAPI Page schema')
  })
})