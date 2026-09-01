import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DownloaderDetailPage } from './DownloaderDetailPage'
import { loadLocalDownloaderDetail } from '../../features/downloaders/downloader-detail'

afterEach(cleanup)

describe('UI-09 Downloader Detail', () => {
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
