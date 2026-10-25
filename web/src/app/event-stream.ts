import { useEffect, useState } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'

export type ConnectionState = 'online' | 'offline' | 'reconnecting'

// Only read models are refreshed. In particular, rule-editor and settings drafts
// must not be replaced by background events while the user is editing them.
const READ_MODELS = ['dashboard', 'sites', 'rules', 'history', 'exceptions', 'exception-instances']
const EVENT_QUERIES: Record<string, readonly string[]> = {
  'job.preparation.changed': ['sites', 'history', 'exceptions'],
  'job.state.changed': ['dashboard', 'history', 'exceptions'],
  'attempt.state.changed': ['dashboard', 'history', 'exceptions'],
  'command.state.changed': READ_MODELS,
  'capacity.state.changed': ['dashboard', 'exception-instances'],
  'instance.health.changed': ['dashboard', 'exception-instances'],
  'stats.realtime': ['dashboard'],
  'system.alert': ['dashboard', 'exceptions'],
}

export function subscribeServerEvents(
  client: QueryClient,
  onState: (state: ConnectionState) => void,
  createSource: (url: string) => EventSource = (url) => new EventSource(url),
): () => void {
  let stopped = false
  let offline = !navigator.onLine
  let source: EventSource | undefined
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined
  let refreshTimer: ReturnType<typeof setTimeout> | undefined
  let retryDelay = 1_000
  const refreshing = new Set<string>()
  const pending = new Set<string>()

  function scheduleRefresh() {
    if (stopped || offline || refreshTimer !== undefined || ![...pending].some((root) => !refreshing.has(root))) return
    refreshTimer = setTimeout(() => {
      refreshTimer = undefined
      if (stopped || offline) return
      // Serialize each read model without making a slow view hold up the other
      // views. Event bursts cannot repeatedly cancel the same in-flight fetch.
      for (const root of pending) {
        if (refreshing.has(root)) continue
        pending.delete(root)
        refreshing.add(root)
        void client.invalidateQueries({ queryKey: [root] })
          .finally(() => { refreshing.delete(root); scheduleRefresh() })
      }
    }, 300)
  }

  function refresh(roots: readonly string[]) {
    for (const root of roots) pending.add(root)
    scheduleRefresh()
  }

  function disconnect() {
    source?.close()
    source = undefined
    clearTimeout(reconnectTimer)
    reconnectTimer = undefined
  }

  function reconnect() {
    disconnect()
    if (stopped || offline) return
    onState('reconnecting')
    reconnectTimer = setTimeout(connect, retryDelay)
    retryDelay = Math.min(retryDelay * 2, 30_000)
  }

  function connect() {
    reconnectTimer = undefined
    if (stopped || offline) return
    onState('reconnecting')
    let current: EventSource
    try { current = createSource('/api/v1/events/stream') } catch { reconnect(); return }
    source = current
    const active = () => !stopped && !offline && source === current
    current.addEventListener('stream.ready', () => {
      if (!active()) return
      retryDelay = 1_000
      onState('online')
      refresh(READ_MODELS)
    })
    current.addEventListener('stream.reset_required', () => {
      if (!active()) return
      // A fresh EventSource drops an expired Last-Event-ID. Repair the read
      // models instead of reconnecting forever with the same invalid cursor.
      refresh(READ_MODELS)
      reconnect()
    })
    for (const [event, roots] of Object.entries(EVENT_QUERIES)) {
      current.addEventListener(event, () => { if (active()) refresh(roots) })
    }
    current.addEventListener('error', () => {
      if (!active()) return
      onState('reconnecting')
      if (current.readyState === 2) {
        // Authentication failures close EventSource permanently. Re-check the
        // session so AuthGate can return to login; bounded retry also recovers
        // from a temporary non-SSE HTTP response without reloading the page.
        refresh(['auth-status'])
        reconnect()
      }
      // CONNECTING is retried by the browser, retaining its Last-Event-ID.
    })
  }

  const goOffline = () => {
    offline = true
    disconnect()
    clearTimeout(refreshTimer)
    refreshTimer = undefined
    onState('offline')
  }
  const goOnline = () => {
    offline = false
    disconnect()
    connect()
  }
  window.addEventListener('online', goOnline)
  window.addEventListener('offline', goOffline)
  if (offline) onState('offline')
  else connect()
  return () => {
    stopped = true
    disconnect()
    clearTimeout(refreshTimer)
    pending.clear()
    window.removeEventListener('online', goOnline)
    window.removeEventListener('offline', goOffline)
  }
}

export function useConnectionStatus(): ConnectionState {
  const client = useQueryClient()
  const [state, setState] = useState<ConnectionState>(() => navigator.onLine ? 'reconnecting' : 'offline')
  useEffect(() => subscribeServerEvents(client, setState), [client])
  return state
}
