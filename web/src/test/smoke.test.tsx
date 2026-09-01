import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, afterEach, vi } from 'vitest'
import { queryClient } from '../app/query-client'
import { App } from '../main'
import { router } from '../app/router'

const authGateClient = vi.hoisted(() => ({
  getAuthStatus: vi.fn().mockResolvedValue({ initialized: true, authenticated: true }),
  initializeAuth: vi.fn(),
  loginAuth: vi.fn(),
}))

vi.mock('../features/auth/auth', async () => {
  const actual = await vi.importActual<typeof import('../features/auth/auth')>('../features/auth/auth')
  return { ...actual, authClient: authGateClient }
})

afterEach(cleanup)

describe('Vertex Pro application shell', () => {
  it('runs the UI-06 dry-run workflow on the aggregation route', async () => {
    router.navigate({ to: '/aggregation' })
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
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

  it('opens the local-only UI-08 history detail route', async () => {
    void router.navigate({ to: '/history/$jobId', params: { jobId: 'history-job-1' } })
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
    expect(await screen.findByRole('heading', { name: 'The.Last.Horizon.2026.2160p.UHD.BluRay' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '投递时间线' })).toBeInTheDocument()
    expect(screen.getByText('高级诊断')).toBeInTheDocument()
  })

  it('opens the local-only UI-09 downloader detail route', async () => {
    void router.navigate({ to: '/downloaders/$downloaderId', params: { downloaderId: 'qb-a' } })
    render(<QueryClientProvider client={queryClient}><App /></QueryClientProvider>)
    expect(await screen.findByRole('heading', { name: 'qB-A · NVMe' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '实时状态' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: '最近 24 小时下载与上传速度图' })).toBeInTheDocument()
  })
})
