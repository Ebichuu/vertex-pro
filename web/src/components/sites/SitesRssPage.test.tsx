import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SitesRssPage } from './SitesRssPage'
import type { Site } from '../../features/sites/sites'
import { ApiRequestError } from '../../generated/api-client'

afterEach(cleanup)

const sites: Site[] = [
  {
    id: 'site-hdsky',
    name: 'HDSky',
    baseUrl: 'https://hdsky.me',
    credentialRef: 'secret/hdsky',
    enabled: true,
    feeds: [{ id: 'feed-hdsky', siteId: 'site-hdsky', name: 'HDSky RSS', url: 'https://hdsky.me/rss', enabled: true, pollMinutes: 15, lastSuccess: '2026-08-29T08:00:00Z', lastError: null, discoveredCount: 12 }],
  },
]

describe('UI-03 Sites & RSS', () => {
  it('显示本地限流等待数量与累计时间', () => {
    const data = structuredClone(sites)
    data[0].feeds[0].rateWaiting = 2
    data[0].feeds[0].rateWaitMs = 12345
    render(<SitesRssPage initialSites={data} />)
    expect(screen.getByText('限流等待 2 条 · 累计 12.3 秒')).toBeInTheDocument()
  })
  it('renders source metadata and feed health without exposing secrets', () => {
    render(<SitesRssPage initialSites={sites} />)
    expect(screen.getByRole('heading', { name: '站点与 RSS' })).toBeInTheDocument()
    expect(screen.getByText('HDSky RSS')).toBeInTheDocument()
    expect(screen.getByText('运行正常')).toBeInTheDocument()
    expect(screen.getByDisplayValue('secret/hdsky')).toBeInTheDocument()
    expect(screen.queryByText('password')).not.toBeInTheDocument()
  })

  it('validates URLs before adding a local site and feed', () => {
    render(<SitesRssPage initialSites={[]} />)
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    fireEvent.change(screen.getByLabelText('站点名称'), { target: { value: '新站点' } })
    fireEvent.change(screen.getByLabelText('站点地址'), { target: { value: 'javascript:alert(1)' } })
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: '主 RSS' } })
    fireEvent.change(screen.getByLabelText('RSS 地址'), { target: { value: 'https://example.test/rss.xml' } })
    fireEvent.click(screen.getByRole('button', { name: '添加 RSS' }))
    expect(screen.getByText('请输入有效的 HTTP(S) 站点地址。')).toBeInTheDocument()
    expect(screen.queryByText('新站点')).not.toBeInTheDocument()
  })

  it('adds a site, toggles feed state, and reports the local change', () => {
    const onChange = vi.fn()
    render(<SitesRssPage initialSites={[]} onChange={onChange} />)
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    fireEvent.change(screen.getByLabelText('站点名称'), { target: { value: '新站点' } })
    fireEvent.change(screen.getByLabelText('站点地址'), { target: { value: 'https://example.test' } })
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: '主 RSS' } })
    fireEvent.change(screen.getByLabelText('RSS 地址'), { target: { value: 'https://example.test/rss.xml' } })
    fireEvent.click(screen.getByRole('button', { name: '添加 RSS' }))
    expect(screen.getByText('站点和 RSS 已加入列表。')).toBeInTheDocument()
    expect(screen.getByText('主 RSS')).toBeInTheDocument()
    expect(onChange).toHaveBeenCalledTimes(1)
    const toggle = screen.getByRole('button', { name: '暂停 RSS' })
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: '启用 RSS' })).toBeInTheDocument()
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('runs a read-only preview and does not create another feed', async () => {
    render(<SitesRssPage initialSites={sites} />)
    const table = screen.getByRole('region', { name: 'RSS 订阅列表' })
    fireEvent.click(within(table).getByRole('button', { name: '测试 RSS' }))
    expect(await screen.findByRole('heading', { name: '测试结果' })).toBeInTheDocument()
    expect(screen.getByText('本地预览')).toBeInTheDocument()
    expect(screen.getAllByText(/示例条目/)).toHaveLength(2)
    expect(table.querySelectorAll('.rss-queue-item')).toHaveLength(1)
  })

  it('persists site enablement and archives a feed after confirmation', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const onUpdateSite = vi.fn(async (site: Site, enabled: boolean) => ({ ...site, enabled }))
    const onArchiveFeed = vi.fn().mockResolvedValue(undefined)
    render(<SitesRssPage initialSites={sites} onUpdateSite={onUpdateSite} onArchiveFeed={onArchiveFeed} />)
    fireEvent.click(screen.getByText('站点设置'))
    fireEvent.click(screen.getByRole('checkbox', { name: '站点启用' }))
    await waitFor(() => expect(onUpdateSite).toHaveBeenCalledWith(sites[0], false))
    await waitFor(() => expect(screen.getByRole('checkbox', { name: '站点启用' })).not.toBeChecked())
    fireEvent.click(screen.getByRole('button', { name: '删除 RSS' }))
    await waitFor(() => expect(onArchiveFeed).toHaveBeenCalledWith(sites[0].feeds[0]))
    expect(screen.queryByText('HDSky RSS')).not.toBeInTheDocument()
    expect(confirm).toHaveBeenCalledTimes(1)
    confirm.mockRestore()
  })

  it('keeps a credential draft through a polling refresh and saves against the latest site version', async () => {
    const onUpdateSite = vi.fn(async (site: Site, enabled: boolean, credentialRef?: string) => ({ ...site, enabled, credentialRef: credentialRef ?? site.credentialRef }))
    const { rerender } = render(<SitesRssPage initialSites={sites} onUpdateSite={onUpdateSite} />)
    const input = screen.getByDisplayValue('secret/hdsky')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'env:VERTEX_SITE_NEW' } })
    const refreshed = [{ ...sites[0], updatedAt: '2026-10-23T01:00:00Z', credentialRef: 'env:VERTEX_SITE_OTHER' }]
    rerender(<SitesRssPage initialSites={refreshed} onUpdateSite={onUpdateSite} />)
    expect(screen.getByDisplayValue('env:VERTEX_SITE_NEW')).toBeInTheDocument()
    fireEvent.blur(screen.getByDisplayValue('env:VERTEX_SITE_NEW'))
    await waitFor(() => expect(onUpdateSite).toHaveBeenCalledWith(refreshed[0], true, 'env:VERTEX_SITE_NEW'))
  })

  it('retains a rejected credential draft through refresh and retries it', async () => {
    const onUpdateSite = vi.fn().mockRejectedValueOnce(new Error('offline')).mockImplementation(async (site: Site, enabled: boolean, credentialRef: string) => ({ ...site, enabled, credentialRef }))
    const { rerender } = render(<SitesRssPage initialSites={sites} onUpdateSite={onUpdateSite} />)
    const input = screen.getByDisplayValue('secret/hdsky')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'env:VERTEX_SITE_NEW' } })
    fireEvent.blur(input)
    expect(await screen.findByRole('status')).toHaveTextContent('凭据引用保存失败')
    await waitFor(() => expect(input).toBeEnabled())
    const refreshed = [{ ...sites[0], updatedAt: 'new-revision' }]
    rerender(<SitesRssPage initialSites={refreshed} onUpdateSite={onUpdateSite} />)
    expect(input).toHaveValue('env:VERTEX_SITE_NEW')
    fireEvent.focus(input)
    fireEvent.blur(input)
    await waitFor(() => expect(onUpdateSite).toHaveBeenLastCalledWith(refreshed[0], true, 'env:VERTEX_SITE_NEW'))
    expect(await screen.findByText('凭据引用已保存。')).toBeInTheDocument()
    expect(onUpdateSite).toHaveBeenCalledTimes(2)
  })

  it('selects the next site after archiving the current one', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const next = { ...sites[0], id: 'site-next', name: 'Next', feeds: [] }
    const onArchiveSite = vi.fn().mockResolvedValue(undefined)
    render(<SitesRssPage initialSites={[sites[0], next]} onArchiveSite={onArchiveSite} />)
    fireEvent.change(screen.getByRole('combobox', { name: '选择站点' }), { target: { value: 'site-hdsky' } })
    fireEvent.click(screen.getByText('站点设置'))
    fireEvent.click(screen.getByRole('button', { name: '删除站点' }))
    await waitFor(() => expect(onArchiveSite).toHaveBeenCalledWith(sites[0]))
    await waitFor(() => expect(screen.getByRole('combobox', { name: '选择站点' })).toHaveValue('site-next'))
    confirm.mockRestore()
  })

  it('shows server detail when qui policy disable is not confirmed', async () => {
    const failure = new ApiRequestError(502, { code: 'QUI_UNREACHABLE', message: '本地已停用，但 qui 删除策略尚未确认', request_id: 'test', retryable: true })
    const onUpdateSite = vi.fn().mockRejectedValue(failure)
    render(<SitesRssPage initialSites={sites} onUpdateSite={onUpdateSite} />)
    fireEvent.click(screen.getByText('站点设置'))
    fireEvent.click(screen.getByRole('checkbox', { name: '站点启用' }))
    expect(await screen.findByRole('status')).toHaveTextContent('本地已停用，但 qui 删除策略尚未确认')
  })

  it('keeps creation errors inside the dialog and preserves the form for retry', async () => {
    const onCreateSite = vi.fn().mockRejectedValue(new Error('fixture failure'))
    render(<SitesRssPage initialSites={[]} onCreateSite={onCreateSite} />)
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('站点名称'), { target: { value: 'New' } })
    fireEvent.change(within(dialog).getByLabelText('站点地址'), { target: { value: 'https://new.test' } })
    fireEvent.change(within(dialog).getByLabelText('RSS 名称'), { target: { value: 'New RSS' } })
    fireEvent.change(within(dialog).getByLabelText('RSS 地址'), { target: { value: 'https://new.test/rss' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '添加 RSS' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('站点保存失败')
    expect(within(dialog).getByLabelText('RSS 名称')).toHaveValue('New RSS')
    expect(within(dialog).getByRole('button', { name: '添加 RSS' })).toBeEnabled()
  })

  it('preserves an RSS edit through refresh and saves with the latest revision', async () => {
    const onUpdateFeed = vi.fn(async (feed, patch) => ({ ...feed, ...patch }))
    const { rerender } = render(<SitesRssPage initialSites={sites} onUpdateFeed={onUpdateFeed} />)
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'Edited RSS' } })
    const refreshed = structuredClone(sites)
    refreshed[0].feeds[0].updatedAt = '2026-08-29T09:00:00Z'
    refreshed[0].feeds[0].enabled = false
    refreshed[0].feeds[0].url = 'https://hdsky.me/new-rss'
    refreshed[0].feeds[0].pollMinutes = 30
    rerender(<SitesRssPage initialSites={refreshed} onUpdateFeed={onUpdateFeed} />)
    expect(screen.getByLabelText('RSS 名称')).toHaveValue('Edited RSS')
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    await waitFor(() => expect(onUpdateFeed).toHaveBeenCalledWith(refreshed[0].feeds[0], { name: 'Edited RSS' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: '启用 RSS' })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: '编辑' })).toHaveFocus())
  })

  it('retries partial creation against the refreshed site and detects its removal', async () => {
    const partial = { ...sites[0], feeds: [] }
    const onCreateSite = vi.fn().mockResolvedValue({ site: partial, warning: '站点已保存，但 RSS 保存失败' })
    const { rerender } = render(<SitesRssPage initialSites={[]} onCreateSite={onCreateSite} />)
    fireEvent.click(screen.getByRole('button', { name: '添加站点 RSS' }))
    fireEvent.change(screen.getByLabelText('站点名称'), { target: { value: 'HDSky' } })
    fireEvent.change(screen.getByLabelText('站点地址'), { target: { value: 'https://hdsky.me' } })
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'Draft RSS' } })
    fireEvent.change(screen.getByLabelText('RSS 地址'), { target: { value: 'https://hdsky.me/rss' } })
    fireEvent.click(screen.getByRole('button', { name: '添加 RSS' }))
    await screen.findByText('站点已保存，但 RSS 保存失败')
    const refreshed = { ...sites[0], updatedAt: 'new-revision' }
    rerender(<SitesRssPage initialSites={[refreshed]} onCreateSite={onCreateSite} />)
    onCreateSite.mockResolvedValue({ site: refreshed, warning: '站点已保存，但 RSS 保存失败' })
    fireEvent.click(screen.getByRole('button', { name: '重试添加 RSS' }))
    await waitFor(() => expect(onCreateSite).toHaveBeenLastCalledWith(expect.anything(), refreshed))
    await waitFor(() => expect(screen.getByRole('button', { name: '重试添加 RSS' })).toBeEnabled())
    rerender(<SitesRssPage initialSites={[]} onCreateSite={onCreateSite} />)
    fireEvent.click(screen.getByRole('button', { name: '重试添加 RSS' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('已创建的站点已被删除')
    expect(screen.getByLabelText('站点名称')).toBeEnabled()
    expect(screen.getByLabelText('RSS 名称')).toHaveValue('Draft RSS')
    expect(onCreateSite).toHaveBeenCalledTimes(2)
    expect(screen.getByRole('button', { name: '添加 RSS' })).toBeEnabled()
  })

  it('keeps the RSS draft and blocks a refreshed conflict on the edited field', async () => {
    const onUpdateFeed = vi.fn()
    const { rerender } = render(<SitesRssPage initialSites={sites} onUpdateFeed={onUpdateFeed} />)
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'My draft' } })
    const refreshed = structuredClone(sites)
    refreshed[0].feeds[0].name = 'Other editor'
    refreshed[0].feeds[0].updatedAt = 'new-revision'
    rerender(<SitesRssPage initialSites={refreshed} onUpdateFeed={onUpdateFeed} />)
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('字段已被其他操作更新')
    expect(screen.getByLabelText('RSS 名称')).toHaveValue('My draft')
    expect(onUpdateFeed).not.toHaveBeenCalled()
  })

  it('keeps the RSS draft when the save reports a revision conflict', async () => {
    const onUpdateFeed = vi.fn().mockRejectedValue(new ApiRequestError(409, { code: 'REVISION_CONFLICT', message: 'changed', request_id: 'test', retryable: false }))
    render(<SitesRssPage initialSites={sites} onUpdateFeed={onUpdateFeed} />)
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    fireEvent.change(screen.getByLabelText('RSS 名称'), { target: { value: 'My draft' } })
    fireEvent.click(screen.getByRole('button', { name: '保存修改' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('草稿已保留')
    expect(screen.getByLabelText('RSS 名称')).toHaveValue('My draft')
  })
})
