import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, afterEach, beforeEach, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { queryClient } from '../app/query-client'
import { router } from '../app/router'
import { server } from '../mocks/server'
import { ErrorBoundary } from '../components/ui/ErrorBoundary'
import { TestEventSource } from './event-source'

beforeEach(() => {
  TestEventSource.instances = []
  vi.stubGlobal('EventSource', TestEventSource)
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderApp() {
  return render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('UI-01 App Shell', () => {
  it('总览读取 UTC 今日上传和站点统计，移除重复的最近历史表', async () => {
    server.use(http.get('/api/v1/stats/downloaders/summary', ({ request }) => {
      expect(new URL(request.url).searchParams.get('range')).toBe('DAY')
      return HttpResponse.json({ instance_id: '', range: 'DAY', upload_bytes: 2_000_000_000_000, download_bytes: 100, next_cursor: '', series: [{ bucket_start: '2026-08-24T00:00:00Z', missing: false, upload_bytes: 2_000_000_000_000, download_bytes: 100 }] })
    }))
    void router.navigate({ to: '/' })
    renderApp()
    expect(await screen.findByText('2.00 TB')).toBeInTheDocument()
    const table = await screen.findByRole('table', { name: '今日站点数据' })
    expect(within(table).getByText('HDSky')).toBeInTheDocument()
    expect(within(table).getByText('12')).toBeInTheDocument()
    expect(screen.queryByText('最近投递')).not.toBeInTheDocument()
  })
  it('左侧导航包含六个主入口且可键盘聚焦', async () => {
    renderApp()
    const nav = await screen.findByRole('navigation', { name: '主导航' })
    const links = nav.querySelectorAll('a')
    expect(links).toHaveLength(6)
    for (const link of links) {
      expect(link.getAttribute('href')).toBeTruthy()
    }
    expect(screen.getByRole('link', { name: '总览' })).toHaveAttribute('href', '/')
    expect(screen.getByRole('link', { name: '异常' })).toHaveAttribute('href', '/exceptions')
  })

  it('主题切换持久化到 documentElement 与 localStorage', async () => {
    renderApp()
    const toggle = await screen.findByRole('button', { name: /Dark|Light/ })
    fireEvent.click(toggle)
    expect(['dark', 'light']).toContain(document.documentElement.dataset.theme)
    expect(window.localStorage.getItem('vertex.theme')).toBe(document.documentElement.dataset.theme)
  })

  it('连接状态等待 SSE 确认；仅浏览器在线不足以恢复连接', async () => {
    renderApp()
    const status = await screen.findByRole('status')
    expect(['已连接', '离线', '重连中'].some((label) => status.textContent?.includes(label))).toBe(true)
    act(() => TestEventSource.instances.at(-1)!.emit('stream.ready'))
    expect(await screen.findByText('已连接')).toBeInTheDocument()
    act(() => window.dispatchEvent(new Event('offline')))
    expect(await screen.findByText('离线')).toBeInTheDocument()
    act(() => window.dispatchEvent(new Event('online')))
    expect(await screen.findByText('重连中')).toBeInTheDocument()
    act(() => TestEventSource.instances.at(-1)!.emit('stream.ready'))
    expect(await screen.findByText('已连接')).toBeInTheDocument()
  })

  it('loads Sites & RSS from the resource API', async () => {
    void router.navigate({ to: '/sites' })
    renderApp()
    expect(await screen.findByRole('heading', { name: '站点与 RSS' })).toBeInTheDocument()
    expect(await screen.findByText('HDSky RSS')).toBeInTheDocument()
    expect(screen.queryByText('secret/hdsky')).not.toBeInTheDocument()
  })

  it('persists Feed toggle and runs a read-only test through the API', async () => {
    void router.navigate({ to: '/sites' })
    renderApp()
    await screen.findByRole('heading', { name: '站点与 RSS' })
    const toggle = screen.getByRole('button', { name: '暂停 RSS' })
    fireEvent.click(toggle)
    expect(await screen.findByText('RSS 已更新。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启用 RSS' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '测试 RSS' }))
    expect(await screen.findByText('Remote RSS item')).toBeInTheDocument()
    expect(screen.getByText('已读取')).toBeInTheDocument()
  })

  it('keeps Feed state unchanged when the API reports an ETag conflict', async () => {
    server.use(http.put('/api/v1/feeds/:id', () => HttpResponse.json({ error: { code: 'REVISION_CONFLICT', message: 'feed revision changed' } }, { status: 409 })))
    void router.navigate({ to: '/sites' })
    renderApp()
    await screen.findByRole('heading', { name: '站点与 RSS' })
    const toggle = screen.getByRole('button', { name: '暂停 RSS' })
    fireEvent.click(toggle)
    expect(await screen.findByText(/RSS 更新失败/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '暂停 RSS' })).toBeInTheDocument()
  })

  it('persists a site and then its first feed through the route adapter', async () => {
    void router.navigate({ to: '/sites' })
    renderApp()
    await screen.findByRole('heading', { name: '站点与 RSS' })
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    fireEvent.change(screen.getByLabelText('站点名称'), { target: { value: 'New Site' } })
    fireEvent.change(screen.getByLabelText('站点地址'), { target: { value: 'https://new.example' } })
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'New RSS' } })
    fireEvent.change(screen.getByLabelText('RSS 地址'), { target: { value: 'https://new.example/rss' } })
    fireEvent.click(screen.getByRole('button', { name: '添加 RSS' }))
    expect(await screen.findByText('站点和 RSS 已保存。')).toBeInTheDocument()
    expect(screen.getByText('New RSS')).toBeInTheDocument()
  })

  it('keeps the created site visible when the feed request fails', async () => {
    server.use(http.post('/api/v1/feeds', () => HttpResponse.json({ error: { code: 'QUI_UNREACHABLE', message: 'feed unavailable' } }, { status: 502 })))
    void router.navigate({ to: '/sites' })
    renderApp()
    await screen.findByRole('heading', { name: '站点与 RSS' })
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    fireEvent.change(screen.getByLabelText('站点名称'), { target: { value: 'Partial Site' } })
    fireEvent.change(screen.getByLabelText('站点地址'), { target: { value: 'https://partial.example' } })
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'Partial RSS' } })
    fireEvent.change(screen.getByLabelText('RSS 地址'), { target: { value: 'https://partial.example/rss' } })
    fireEvent.click(screen.getByRole('button', { name: '添加 RSS' }))
    expect(await screen.findByText(/站点已保存，但 RSS 保存失败/)).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'Partial Site' })).toBeInTheDocument()
  })

  it('loads Rules from the resource API', async () => {
    void router.navigate({ to: '/rules' })
    renderApp()
    expect(await screen.findByRole('heading', { name: '规则配置' })).toBeInTheDocument()
    expect(await screen.findByText('4K 电影')).toBeInTheDocument()
    expect(screen.getByText('148')).toBeInTheDocument()
  })
  it('persists RuleList toggles through the revision API', async () => {
    void router.navigate({ to: '/rules' })
    renderApp()
    const toggle = await screen.findByRole('checkbox', { name: '4K 电影 启用' })
    fireEvent.click(toggle)
    await waitFor(() => expect(toggle).not.toBeChecked())
    expect(await screen.findByText('v4 · 更新于 2026-08-25')).toBeInTheDocument()
  })
  it('移动端键盘展开与关闭导航，选择页面后收起并移动焦点', async () => {
    const media = new EventTarget() as MediaQueryList
    Object.defineProperty(media, 'matches', { value: true, writable: true })
    vi.stubGlobal('matchMedia', () => media)
    const user = userEvent.setup()
    renderApp()
    const toggle = await screen.findByRole('button', { name: /切换导航/ })
    expect(toggle).toHaveAttribute('aria-controls', 'primary-nav')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('navigation')).not.toBeInTheDocument()
    toggle.focus()
    await user.keyboard('{Enter}')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await user.tab()
    await user.tab()
    expect(screen.getByRole('link', { name: '总览' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(toggle).toHaveFocus()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await user.click(toggle)
    await user.click(screen.getByRole('link', { name: '总览' }))
    expect(await screen.findByRole('heading', { name: '下载器总览' })).toBeInTheDocument()
    expect(screen.getByRole('main')).toHaveFocus()
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    act(() => {
      Object.defineProperty(media, 'matches', { value: false })
      media.dispatchEvent(new Event('change'))
    })
    expect(screen.getByRole('navigation')).toBeVisible()
  })

  it('全局错误边界捕获渲染异常', () => {
    function Boom(): never {
      throw new Error('boom')
    }
    const consoleError = console.error
    console.error = () => {}
    try {
      render(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      )
      expect(screen.getByText('页面暂时不可用')).toBeTruthy()
    } finally {
      console.error = consoleError
    }
  })
})
