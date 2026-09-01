import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { recentRssItems, runLocalDryRun, type DryRunExecutor } from '../../features/rules/dry-run'
import { RuleDryRunPage } from './RuleDryRunPage'

afterEach(cleanup)

describe('UI-06 Dry-run and aggregation preview', () => {
  it('shows recent RSS input and waits for an explicit dry-run', () => {
    render(<RuleDryRunPage items={recentRssItems} runDryRun={runLocalDryRun} />)
    expect(screen.getByLabelText('近期 RSS 项')).toHaveValue('rss-dune-4k')
    expect(screen.getByText('35.8 GiB')).toBeInTheDocument()
    expect(screen.getByText('等待运行')).toBeInTheDocument()
  })

  it('shows every rule, condition reason, winner and final action without side effects', async () => {
    const executor = vi.fn(runLocalDryRun)
    render(<RuleDryRunPage items={recentRssItems} runDryRun={executor} />)
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    expect(await screen.findByRole('heading', { name: '规则判定' })).toBeInTheDocument()
    expect(executor).toHaveBeenCalledWith('rss-dune-4k')
    expect(screen.getAllByText('4K 电影')).toHaveLength(2)
    expect(screen.getByText('标题命中 2160p。')).toBeInTheDocument()
    expect(screen.getByText('动画')).toBeInTheDocument()
    expect(screen.getAllByText('Winner')).toHaveLength(3)
    expect(screen.getByText(/qB-A.*Profile 5\.2\.2/)).toBeInTheDocument()
    expect(screen.getByText('按分类使用子目录')).toBeInTheDocument()
    expect(screen.getByText('启用')).toBeInTheDocument()
    expect(screen.getAllByText(/不下载、不创建 Reservation、不发送 Add/)).toHaveLength(2)
  })

  it('surfaces UNKNOWN conditions and missing fields as a fail-closed blocker', async () => {
    render(<RuleDryRunPage items={recentRssItems} runDryRun={runLocalDryRun} />)
    fireEvent.change(screen.getByLabelText('近期 RSS 项'), { target: { value: 'rss-missing-size' } })
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    expect(await screen.findByText('字段缺失')).toBeInTheDocument()
    expect(screen.getByText('size_bytes')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('无法安全判定高优先级规则')
  })

  it('renders executor failures and does not retain a stale result', async () => {
    const failingExecutor: DryRunExecutor = async () => { throw new Error('fixture unavailable') }
    render(<RuleDryRunPage items={recentRssItems} runDryRun={failingExecutor} />)
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('fixture unavailable')
    expect(screen.queryByRole('heading', { name: '规则判定' })).not.toBeInTheDocument()
  })
})
