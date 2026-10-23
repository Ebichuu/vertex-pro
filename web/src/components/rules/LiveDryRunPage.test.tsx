import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { rulesClient } from '../../features/rules/api'
import { LiveDryRunPage } from './LiveDryRunPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function setup() {
  vi.spyOn(rulesClient, 'listSites').mockResolvedValue({ items: [] })
  const request = vi.spyOn(rulesClient, 'dryRunRules').mockResolvedValue({ evaluated_at: '2026-10-23T00:00:00Z', rules: [] })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={client}><LiveDryRunPage /></QueryClientProvider>)
  return request
}

describe('Live Dry-run GB input', () => {
  it.each([['1.5', 1_500_000_000], ['1.073741824', 1_073_741_824], ['0', 0]])('sends %s GB as exact byte facts', async (input, bytes) => {
    const request = setup()
    fireEvent.change(screen.getByLabelText('预览大小（GB）'), { target: { value: input } })
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    await screen.findByText('没有适用的已启用规则。请先创建并启用规则。')
    expect(request.mock.calls[0][0]?.body).toMatchObject({ facts: { size: { present: true, value: { type: 'number', number: bytes } } } })
  })

  it.each(['', '   '])('keeps a blank size %j absent', async (input) => {
    const request = setup()
    fireEvent.change(screen.getByLabelText('预览大小（GB）'), { target: { value: input } })
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    await screen.findByText('没有适用的已启用规则。请先创建并启用规则。')
    expect(request.mock.calls[0][0]?.body).toMatchObject({ facts: {} })
    expect(request.mock.calls[0][0]?.body).not.toHaveProperty('facts.size')
  })

  it.each(['-1', 'abc', 'NaN', 'Infinity', '9007199.254740992', '0.0000000001'])('rejects invalid input %j without making a request', async (input) => {
    const request = setup()
    fireEvent.change(screen.getByLabelText('预览大小（GB）'), { target: { value: input } })
    fireEvent.click(screen.getByRole('button', { name: '运行 Dry-run' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('整数字节')
    expect(request).not.toHaveBeenCalled()
  })
})
