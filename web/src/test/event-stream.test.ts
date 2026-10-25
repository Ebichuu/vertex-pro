import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { subscribeServerEvents } from '../app/event-stream'
import { TestEventSource } from './event-source'

let client: QueryClient
let stop: (() => void) | undefined
const source = () => TestEventSource.instances.at(-1)!
const factory = (url: string) => new TestEventSource(url) as unknown as EventSource
beforeEach(() => {
  vi.useFakeTimers()
  TestEventSource.instances = []
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
})
afterEach(() => { stop?.(); client.clear(); vi.useRealTimers(); vi.restoreAllMocks() })

describe('服务端事件连接', () => {
  it('只在 stream.ready 后显示连接；原连接重试保持浏览器游标', () => {
    const state = vi.fn()
    stop = subscribeServerEvents(client, state, factory)
    expect(source().url).toBe('/api/v1/events/stream')
    expect(state).toHaveBeenLastCalledWith('reconnecting')
    source().emit('open')
    expect(state).toHaveBeenLastCalledWith('reconnecting')
    source().emit('stream.ready')
    expect(state).toHaveBeenLastCalledWith('online')
    source().emit('error')
    expect(state).toHaveBeenLastCalledWith('reconnecting')
    expect(TestEventSource.instances).toHaveLength(1)
    source().emit('stream.ready')
    expect(state).toHaveBeenLastCalledWith('online')
  })

  it('浏览器重新联网仍需服务器确认，旧连接事件不能恢复状态', () => {
    const state = vi.fn()
    stop = subscribeServerEvents(client, state, factory)
    const old = source()
    window.dispatchEvent(new Event('offline'))
    expect(state).toHaveBeenLastCalledWith('offline')
    expect(old.readyState).toBe(2)
    old.emit('stream.ready')
    expect(state).toHaveBeenLastCalledWith('offline')
    window.dispatchEvent(new Event('online'))
    expect(state).toHaveBeenLastCalledWith('reconnecting')
    old.emit('stream.ready')
    expect(state).toHaveBeenLastCalledWith('reconnecting')
    source().emit('stream.ready')
    expect(state).toHaveBeenLastCalledWith('online')
  })

  it('初始离线时不创建请求', () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false)
    const state = vi.fn()
    stop = subscribeServerEvents(client, state, factory)
    expect(state).toHaveBeenLastCalledWith('offline')
    expect(TestEventSource.instances).toHaveLength(0)
  })

  it('过期游标重建连接，重置只刷新读取数据而保留表单草稿', async () => {
    for (const key of ['history', 'dashboard', 'rule-editor', 'settings']) client.setQueryData([key], { draft: '未保存' })
    stop = subscribeServerEvents(client, vi.fn(), factory)
    const old = source()
    old.emit('stream.reset_required')
    expect(old.readyState).toBe(2)
    await vi.advanceTimersByTimeAsync(300)
    expect(client.getQueryState(['history'])?.isInvalidated).toBe(true)
    expect(client.getQueryState(['dashboard'])?.isInvalidated).toBe(true)
    expect(client.getQueryState(['rule-editor'])?.isInvalidated).toBe(false)
    expect(client.getQueryState(['settings'])?.isInvalidated).toBe(false)
    await vi.advanceTimersByTimeAsync(700)
    expect(TestEventSource.instances).toHaveLength(2)
  })

  it('不可重试的 HTTP 错误检查会话，并以有上限的退避重建连接', async () => {
    client.setQueryData(['auth-status'], { authenticated: true })
    stop = subscribeServerEvents(client, vi.fn(), factory)
    source().readyState = 2
    source().emit('error')
    await vi.advanceTimersByTimeAsync(300)
    expect(client.getQueryState(['auth-status'])?.isInvalidated).toBe(true)
    await vi.advanceTimersByTimeAsync(700)
    expect(TestEventSource.instances).toHaveLength(2)
    source().readyState = 2
    source().emit('error')
    await vi.advanceTimersByTimeAsync(1_000)
    expect(TestEventSource.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(TestEventSource.instances).toHaveLength(3)
  })

  it('批量事件合并；慢请求期间不会反复取消，完成后处理新事件', async () => {
    const resolvers: Array<(value: number) => void> = []
    const fetchData = vi.fn(() => new Promise<number>((resolve) => resolvers.push(resolve)))
    client.setQueryData(['dashboard'], 0)
    const observer = new QueryObserver(client, { queryKey: ['dashboard'], queryFn: fetchData, staleTime: Infinity })
    const unsubscribe = observer.subscribe(() => {})
    try {
      stop = subscribeServerEvents(client, vi.fn(), factory)
      for (let i = 0; i < 200; i++) source().emit('stats.realtime')
      await vi.advanceTimersByTimeAsync(300)
      expect(fetchData).toHaveBeenCalledTimes(1)
      for (let i = 0; i < 200; i++) source().emit('stats.realtime')
      await vi.advanceTimersByTimeAsync(5_000)
      expect(fetchData).toHaveBeenCalledTimes(1)
      resolvers[0](1)
      await vi.advanceTimersByTimeAsync(300)
      expect(fetchData).toHaveBeenCalledTimes(2)
      resolvers[1](2)
      await vi.advanceTimersByTimeAsync(300)
      expect(client.getQueryData(['dashboard'])).toBe(2)
      expect(fetchData).toHaveBeenCalledTimes(2)
    } finally { unsubscribe() }
  })

  it('清理连接、待刷新和重试；StrictMode 重新挂载只有一个活连接', async () => {
    const state = vi.fn()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    stop = subscribeServerEvents(client, state, factory)
    source().emit('stream.reset_required')
    const old = source()
    stop()
    state.mockClear()
    old.emit('stream.ready')
    window.dispatchEvent(new Event('online'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(state).not.toHaveBeenCalled()
    expect(invalidate).not.toHaveBeenCalled()
    stop = subscribeServerEvents(client, state, factory)
    expect(TestEventSource.instances.filter((stream) => stream.readyState !== 2)).toHaveLength(1)
  })

  it('慢历史刷新不阻止独立总览刷新', async () => {
    let finishHistory!: (value: number) => void
    client.setQueryData(['history'], 0)
    client.setQueryData(['dashboard'], 0)
    const history = new QueryObserver(client, { queryKey: ['history'], queryFn: () => new Promise<number>((resolve) => { finishHistory = resolve }), staleTime: Infinity })
    const fetchDashboard = vi.fn(async () => 1)
    const dashboard = new QueryObserver(client, { queryKey: ['dashboard'], queryFn: fetchDashboard, staleTime: Infinity })
    const offHistory = history.subscribe(() => {})
    const offDashboard = dashboard.subscribe(() => {})
    try {
      stop = subscribeServerEvents(client, vi.fn(), factory)
      source().emit('job.state.changed')
      await vi.advanceTimersByTimeAsync(300)
      expect(client.getQueryState(['history'])?.fetchStatus).toBe('fetching')
      expect(fetchDashboard).toHaveBeenCalledTimes(1)
      source().emit('stats.realtime')
      await vi.advanceTimersByTimeAsync(300)
      expect(fetchDashboard).toHaveBeenCalledTimes(2)
      expect(client.getQueryState(['history'])?.fetchStatus).toBe('fetching')
      finishHistory(1)
      await vi.advanceTimersByTimeAsync(300)
    } finally { offHistory(); offDashboard() }
  })
})
