import type { Page } from '../mocks/fixtures'
import { assertDashboard, assertPage } from '../mocks/fixtures'

const apiBase = '/api/v1'

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(`${apiBase}${path}`)
  const body: unknown = await response.json()
  if (!response.ok) {
    throw new Error(`API request failed: ${response.status}`)
  }
  return body as T
}

export async function fetchDashboard(): Promise<Record<string, unknown>> {
  const body = await getJSON<unknown>('/dashboard')
  return assertDashboard(body)
}

export async function fetchInstances(): Promise<Page> {
  const body = await getJSON<unknown>('/instances')
  return assertPage(body)
}