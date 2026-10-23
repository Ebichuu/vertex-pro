import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SettingsPage } from './SettingsPage'
import { ApiRequestError } from '../../generated/api-client'
import type { AppSettings } from '../../features/settings/settings'
import { settingsFixture } from '../../mocks/settings'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function apiError(status: number, code: string, message = 'internal secret details') {
  return new ApiRequestError(status, { code, message, request_id: 'settings-test', retryable: status === 409 })
}

const writable = () => vi.fn(async (settings: AppSettings) => ({ ...settings, revision: settings.revision + 1, restart_required: true }))

describe('UI-11 Settings', () => {
  it('等待表单加载，字段标签与提示分别关联，敏感信息仅显示遮罩', async () => {
    render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture({ timezone: 'Asia/Shanghai' }))} saveSettings={writable()} />)
    expect(screen.getByText('正在加载设置…')).toBeInTheDocument()
    const timezone = await screen.findByLabelText('时区')
    expect(timezone).toHaveValue('Asia/Shanghai')
    expect(timezone).toHaveAccessibleName('时区')
    expect(timezone).toHaveAccessibleDescription('用于历史和详情时间显示，统计仍按 UTC 分桶')
    expect(screen.getByLabelText('qui 凭据已遮罩')).toHaveTextContent('••••••••')
    expect(screen.queryByText('LOCAL ONLY')).not.toBeInTheDocument()
  })

  it('拒绝无效时区与保留期，焦点定位到首个错误', async () => {
    const saver = writable()
    render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} saveSettings={saver} />)
    fireEvent.change(await screen.findByLabelText('时区'), { target: { value: 'Mars/Olympus' } })
    fireEvent.change(screen.getByLabelText('小时统计保留（天）'), { target: { value: '10' } })
    fireEvent.submit(screen.getByRole('form', { name: '全局设置表单' }))
    expect(await screen.findByText('请输入有效的 IANA 时区，例如 Asia/Shanghai。')).toBeInTheDocument()
    expect(screen.getByLabelText('时区')).toHaveFocus()
    expect(screen.getByLabelText('小时统计保留（天）')).toHaveAccessibleDescription(expect.stringContaining('小时统计保留期不能短于原始统计保留期。'))
    expect(saver).not.toHaveBeenCalled()
  })

  it('保存后区分待重启值与当前运行值，下载只请求服务端报告', async () => {
    const saver = writable()
    const downloader = vi.fn().mockResolvedValue(undefined)
    render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} saveSettings={saver} downloadDiagnostics={downloader} />)
    fireEvent.change(await screen.findByLabelText('站点请求上限（次/分钟）'), { target: { value: '45' } })
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }))
    expect(await screen.findByText('设置已保存，重启服务后生效。')).toBeInTheDocument()
    expect(saver.mock.calls[0][0].values.rate_limit_per_minute).toBe(45)
    expect(screen.getByText('30 次/分钟')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '下载诊断包' }))
    expect(await screen.findByText('诊断包已下载，敏感信息已脱敏。')).toBeInTheDocument()
    expect(downloader).toHaveBeenCalledWith(expect.any(AbortSignal))
  })

  it('只读账号与缺失保存器均不能触发假保存，缺失下载器禁用下载', async () => {
    const saver = writable()
    const { rerender } = render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} saveSettings={saver} canEdit={false} />)
    expect(await screen.findByLabelText('时区')).toBeDisabled()
    expect(screen.getByRole('button', { name: '保存设置' })).toBeDisabled()
    fireEvent.submit(screen.getByRole('form', { name: '全局设置表单' }))
    expect(saver).not.toHaveBeenCalled()
    rerender(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} />)
    expect(await screen.findByText('当前设置只能查看，保存暂不可用。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存设置' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下载诊断包' })).toBeDisabled()
  })

  it('加载失败可以重试', async () => {
    const loader = vi.fn().mockRejectedValueOnce(new Error('private path')).mockResolvedValue(settingsFixture())
    render(<SettingsPage loadSettings={loader} saveSettings={writable()} />)
    fireEvent.click(await screen.findByRole('button', { name: '重试加载' }))
    expect(await screen.findByLabelText('时区')).toHaveValue('UTC')
    expect(screen.queryByText(/private path/)).not.toBeInTheDocument()
  })

  it('保存失败保留草稿，屏蔽服务端错误细节并允许重试', async () => {
    const saver = vi.fn().mockRejectedValueOnce(apiError(500, 'INTERNAL_ERROR', 'password=do-not-display')).mockImplementation(writable())
    render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} saveSettings={saver} />)
    fireEvent.change(await screen.findByLabelText('时区'), { target: { value: 'Europe/London' } })
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }))
    expect(await screen.findByText('设置保存失败，草稿已保留，请重试。')).toBeInTheDocument()
    expect(screen.getByLabelText('时区')).toHaveValue('Europe/London')
    expect(screen.queryByText(/do-not-display/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }))
    expect(await screen.findByText('设置已保存，重启服务后生效。')).toBeInTheDocument()
  })

  it('版本冲突保留草稿并禁止覆盖，主动载入后使用新版本', async () => {
    const latest = { ...settingsFixture({ timezone: 'Europe/London' }), revision: 2 }
    const loader = vi.fn().mockResolvedValueOnce(settingsFixture()).mockResolvedValue(latest)
    const saver = vi.fn().mockRejectedValue(apiError(409, 'REVISION_CONFLICT'))
    render(<SettingsPage loadSettings={loader} saveSettings={saver} />)
    fireEvent.change(await screen.findByLabelText('时区'), { target: { value: 'Asia/Shanghai' } })
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }))
    const reload = await screen.findByRole('button', { name: '放弃草稿并载入最新设置' })
    expect(screen.getByLabelText('时区')).toHaveValue('Asia/Shanghai')
    expect(screen.getByRole('button', { name: '保存设置' })).toBeDisabled()
    fireEvent.click(reload)
    await waitFor(() => expect(screen.getByLabelText('时区')).toHaveValue('Europe/London'))
    expect(screen.getByRole('button', { name: '保存设置' })).toBeEnabled()
  })

  it('断线期间可以编辑，重连检查版本且不覆盖未保存草稿', async () => {
    const loader = vi.fn().mockResolvedValue(settingsFixture())
    render(<SettingsPage loadSettings={loader} saveSettings={writable()} />)
    await screen.findByLabelText('时区')
    fireEvent(window, new Event('offline'))
    fireEvent.change(screen.getByLabelText('时区'), { target: { value: 'Europe/London' } })
    expect(screen.getByRole('button', { name: '保存设置' })).toBeDisabled()
    fireEvent(window, new Event('online'))
    await waitFor(() => expect(loader).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByRole('button', { name: '保存设置' })).toBeEnabled())
    expect(screen.getByLabelText('时区')).toHaveValue('Europe/London')
  })

  it('重连发现新版本时阻止旧草稿覆盖服务器', async () => {
    const loader = vi.fn().mockResolvedValueOnce(settingsFixture()).mockResolvedValue({ ...settingsFixture(), revision: 2 })
    render(<SettingsPage loadSettings={loader} saveSettings={writable()} />)
    fireEvent.change(await screen.findByLabelText('时区'), { target: { value: 'Europe/London' } })
    fireEvent(window, new Event('offline'))
    fireEvent(window, new Event('online'))
    await screen.findByRole('button', { name: '放弃草稿并载入最新设置' })
    expect(screen.getByLabelText('时区')).toHaveValue('Europe/London')
    expect(screen.getByRole('button', { name: '保存设置' })).toBeDisabled()
  })

  it('认证失效与诊断失败给出受控提示', async () => {
    const downloader = vi.fn().mockRejectedValue(new Error('key=do-not-display'))
    render(<SettingsPage loadSettings={vi.fn().mockResolvedValue(settingsFixture())} saveSettings={vi.fn().mockRejectedValue(apiError(401, 'UNAUTHENTICATED'))} downloadDiagnostics={downloader} />)
    await screen.findByLabelText('时区')
    fireEvent.click(screen.getByRole('button', { name: '保存设置' }))
    expect(await screen.findByText('登录已失效，请重新登录后重试。')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '下载诊断包' }))
    expect(await screen.findByText('诊断包下载失败，请稍后重试。')).toBeInTheDocument()
    expect(screen.queryByText(/do-not-display/)).not.toBeInTheDocument()
  })

  it('卸载页面会取消仍在进行的加载', () => {
    const loader = vi.fn(() => new Promise<AppSettings>(() => undefined))
    const { unmount } = render(<SettingsPage loadSettings={loader} />)
    unmount()
    expect((loader.mock.calls[0] as unknown as [AbortSignal])[0].aborted).toBe(true)
  })
})
