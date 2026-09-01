import { ApiClient, ApiRequestError } from '../../generated/api-client'

export type AuthStatus = {
  initialized: boolean
  authenticated: boolean
}

export const authClient = new ApiClient(window.location.origin)

export function csrfHeaders(): Record<string, string> {
  const cookie = document.cookie.split('; ').find((value) => value.startsWith('vertex_csrf='))
  if (!cookie) return {}
  const token = decodeURIComponent(cookie.slice('vertex_csrf='.length))
  return token ? { 'X-CSRF-Token': token } : {}
}

export function authErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    if (error.body.code === 'RATE_LIMITED') return '登录暂时锁定，请稍后再试。'
    if (error.body.code === 'UNAUTHENTICATED') return '口令不正确。'
    if (error.body.code === 'JOB_STATE_CONFLICT') return error.body.message || '当前认证状态不允许此操作。'
    if (error.body.code === 'VALIDATION_FAILED') return error.body.message || '口令不符合要求。'
  }
  return '无法连接认证服务，请稍后重试。'
}
