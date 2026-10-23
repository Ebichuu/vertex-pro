import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, afterEach, vi } from 'vitest'
import { queryClient } from '../app/query-client'
import { App } from '../main'
import { router } from '../app/router'
import { http, HttpResponse } from 'msw'
import { server } from '../mocks/server'

const authGateClient = vi.hoisted(() => ({
  getAuthStatus: vi.fn().mockResolvedValue({ initialized: true, authenticated: true }),
  initializeAuth: vi.fn(),
  loginAuth: vi.fn(),
}))

vi.mock('../features/auth/auth', async () => {
  const actual = await vi.importActual<typeof import('../features/auth/auth')>('../features/auth/auth')
  return { ...actual, authClient: authGateClient }
})

afterEach(async () => {
  await router.navigate({ to: '/' })
  cleanup()
  queryClient.clear()
})

describe('Vertex Pro application shell', () => {
  it('runs the UI-06 dry-run workflow on the aggregation route', async () => {
    server.use(http.post('/api/v1/rules/dry-run', () => HttpResponse.json({ evaluated_at: '2026-10-22T01:00:00Z', rules: [{ id: 'rule-live', name: '真实规则', priority: 10, revision: 1, state: 'TRUE', winner: true, reason: { code: 'MATCHED' } }], aggregation: { winner: { rule_id: 'rule-live', target_instance_id: 'qb-a' }, tags: [] }, blockers: [] })))
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
    await router.navigate({ to: '/aggregation' })
    expect(await screen.findByRole('heading', { name: 'Dry-run 与聚合预览' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    expect(await screen.findByRole('heading', { name: '竞速投递' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '规则判定' })).toBeInTheDocument()
    expect(screen.getByText('目标 qB')).toBeInTheDocument()
    expect(screen.getAllByText(/不下载、不创建 Reservation、不发送 Add/)).toHaveLength(2)
    expect(screen.queryByText('竞速总览')).not.toBeInTheDocument()
    expect(screen.queryByText('RSS 投递')).not.toBeInTheDocument()
    expect(screen.queryByText('历史记录')).not.toBeInTheDocument()
  })

  it('opens history detail from its real API route', async () => {
    server.use(http.get('/api/v1/jobs/:id', ({ params }) => HttpResponse.json({
      id: params.id, title: '服务端历史任务', user_status: 'NEEDS_CONFIRMATION', job_state: 'UNKNOWN',
      timeline: [{ event_id: 1, stage: '待确认', to_state: 'UNKNOWN', occurred_at: '2026-10-22T01:00:00Z' }],
    })))
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
    await router.navigate({ to: '/history/$jobId', params: { jobId: 'live-job' } })
    expect(await screen.findByRole('heading', { name: '服务端历史任务' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '投递时间线' })).toBeInTheDocument()
    expect(screen.getByText('高级诊断')).toBeInTheDocument()
    expect(screen.queryByText('LOCAL ONLY')).not.toBeInTheDocument()
  })

  it('opens downloader detail from real snapshot and traffic APIs', async () => {
    server.use(
      http.get('/api/v1/dashboard', () => HttpResponse.json({ instances: [{ id: 'qb-live', name: '服务端下载器', sync_state: 'READY', freshness: 'FRESH', realtime_freshness: 'FRESH', realtime_source: 'SYNC_SERVER_STATE', download_rate: 100 }] })),
      http.get('/api/v1/stats/downloaders/summary', ({ request }) => HttpResponse.json({
        instance_id: 'qb-live', range: new URL(request.url).searchParams.get('range'),
        download_bytes: 1024, upload_bytes: 2048, next_cursor: '',
        series: [{ bucket_start: '2026-10-22T00:00:00Z', download_bytes: 1024, upload_bytes: 2048, missing: false }],
      })),
    )
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
    await router.navigate({ to: '/downloaders/$downloaderId', params: { downloaderId: 'qb-live' } })
    expect(await screen.findByRole('heading', { name: '服务端下载器' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '实时状态' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: '本日（UTC）下载与上传流量图' })).toBeInTheDocument()
    expect(screen.queryByText('LOCAL ONLY')).not.toBeInTheDocument()
  })
})
