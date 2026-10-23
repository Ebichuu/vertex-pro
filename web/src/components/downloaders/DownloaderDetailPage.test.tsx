import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DownloaderDetailPage } from './DownloaderDetailPage'
import { loadLocalDownloaderDetail } from '../../features/downloaders/downloader-detail'
import { loadDownloaderDetail } from '../../features/downloaders/api'
import { http, HttpResponse } from 'msw'
import { server } from '../../mocks/server'

afterEach(cleanup)

describe('UI-09 Downloader Detail', () => {
  it('真实空快照和全缺失统计保持未知，并说明数据陈旧', async () => {
    server.use(
      http.get('/api/v1/dashboard', () => HttpResponse.json({ instances: [{ id: 'real-qb', name: '真实下载器', sync_state: 'READY', freshness: 'FRESH', realtime_freshness: 'NOT_FRESH', download_rate: 0, upload_rate: 0, realtime_source: '', physical_free_bytes: -1, effective_free_bytes: -1 }] })),
      http.get('/api/v1/stats/downloaders/summary', ({ request }) => HttpResponse.json({
        instance_id: 'real-qb', range: new URL(request.url).searchParams.get('range'), download_bytes: 0, upload_bytes: 0,
        series: [{ bucket_start: '2026-10-22T00:00:00Z', download_bytes: 0, upload_bytes: 0, missing: true }], next_cursor: '',
      })),
    )
    render(<DownloaderDetailPage downloaderId="real-qb" loadDetail={loadDownloaderDetail} onBack={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: '真实下载器' })).toBeInTheDocument()
    expect(screen.getByRole('note')).toHaveTextContent('以下最近观测值不代表下载器当前状态')
    expect(screen.getByRole('img', { name: '本日（UTC）下载与上传流量图' })).toBeInTheDocument()
    expect(screen.queryByText('0 B')).not.toBeInTheDocument()
    expect(screen.queryByText('0 KiB/s')).not.toBeInTheDocument()
    expect(screen.queryByText('LOCAL ONLY')).not.toBeInTheDocument()
    expect(screen.getAllByText('暂无数据').length).toBeGreaterThan(5)
  })
  it('展示实时状态、容量和日统计图', async () => {
    render(<DownloaderDetailPage downloaderId="qb-a" loadDetail={loadLocalDownloaderDetail} onBack={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: 'qB-A · NVMe' })).toBeInTheDocument()
    expect(screen.getByText('实时状态')).toBeInTheDocument()
    expect(screen.getByText('2.4 MiB/s')).toBeInTheDocument()
    expect(screen.getByText('1.21 TiB')).toBeInTheDocument()
    expect(screen.getByRole('img', { name: '最近 24 小时下载与上传速度图' })).toBeInTheDocument()
    expect(screen.getByText(/1 个小时缺失/)).toBeInTheDocument()
  })

  it('切换周周期并提示计数器重置', async () => {
    render(<DownloaderDetailPage downloaderId="qb-a" loadDetail={loadLocalDownloaderDetail} onBack={vi.fn()} />)
    await screen.findByRole('heading', { name: 'qB-A · NVMe' })
    fireEvent.click(screen.getByRole('tab', { name: '周' }))
    expect(screen.getByText('最近 7 天上传')).toBeInTheDocument()
    expect(screen.getByText(/检测到 1 次计数器重置/)).toBeInTheDocument()
  })

  it('未知下载器显示失败并支持重试', async () => {
    const loader = vi.fn().mockRejectedValue(new Error('not found'))
    render(<DownloaderDetailPage downloaderId="missing" loadDetail={loader} onBack={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: '下载器详情加载失败' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect(loader).toHaveBeenCalledTimes(2)
  })

  it('降级下载器不把未知容量显示为零', async () => {
    render(<DownloaderDetailPage downloaderId="qb-b" loadDetail={loadLocalDownloaderDetail} onBack={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: 'qB-B · Bulk' })).toBeInTheDocument()
    expect(screen.getByText('降级')).toBeInTheDocument()
    expect(screen.getAllByText('暂无数据').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText(/2 个小时缺失/)).toBeInTheDocument()
  })
})
