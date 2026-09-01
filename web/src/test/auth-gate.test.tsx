import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, afterEach, vi } from 'vitest'
import { ApiRequestError } from '../generated/api-client'
import { AuthGate } from '../components/auth/AuthGate'

const authGateClient = vi.hoisted(() => ({
  getAuthStatus: vi.fn(),
  initializeAuth: vi.fn(),
  loginAuth: vi.fn(),
}))

vi.mock('../features/auth/auth', async () => {
  const actual = await vi.importActual<typeof import('../features/auth/auth')>('../features/auth/auth')
  return { ...actual, authClient: authGateClient }
})

afterEach(() => {
  cleanup()
  vi.resetAllMocks()
})

function renderAuthGate() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <AuthGate><p>控制台内容</p></AuthGate>
    </QueryClientProvider>,
  )
}

describe('AuthGate', () => {
  it('initializes the administrator, then logs in before releasing the application', async () => {
    const state = { initialized: false, authenticated: false }
    let initializeCalls = 0
    let loginCalls = 0
    authGateClient.getAuthStatus.mockImplementation(() => Promise.resolve({ ...state }))
    authGateClient.initializeAuth.mockImplementation(async ({ body }: { body: { password: string } }) => {
      expect(body.password).toBe('correct-horse-battery-staple')
      initializeCalls += 1
      state.initialized = true
      return { initialized: true, authenticated: false }
    })
    authGateClient.loginAuth.mockImplementation(async ({ body }: { body: { password: string } }) => {
      expect(body.password).toBe('correct-horse-battery-staple')
      loginCalls += 1
      state.authenticated = true
      return { initialized: true, authenticated: true }
    })

    renderAuthGate()
    expect(await screen.findByRole('heading', { name: '初始化管理员' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'correct-horse-battery-staple' } })
    fireEvent.click(screen.getByRole('button', { name: '完成初始化' }))

    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'correct-horse-battery-staple' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByText('控制台内容')).toBeInTheDocument()
    expect(initializeCalls).toBe(1)
    expect(loginCalls).toBe(1)
  })

  it('keeps the application blocked and shows the contract error for invalid credentials', async () => {
    authGateClient.getAuthStatus.mockResolvedValue({ initialized: true, authenticated: false })
    authGateClient.loginAuth.mockRejectedValue(new ApiRequestError(401, {
      code: 'UNAUTHENTICATED',
      message: 'invalid credentials',
      request_id: 'req-auth-test',
      retryable: false,
    }))

    renderAuthGate()
    expect(await screen.findByRole('heading', { name: '管理员登录' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'wrong-password' } })
    fireEvent.click(screen.getByRole('button', { name: '登录' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('口令不正确。')
    expect(screen.getByRole('heading', { name: '管理员登录' })).toBeInTheDocument()
    expect(screen.queryByText('控制台内容')).not.toBeInTheDocument()
  })
})
