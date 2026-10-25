import { Input } from '../ui/input'
import { Button } from '../ui/button'
import { useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { authClient, authErrorMessage, type AuthStatus } from '../../features/auth/auth'
import { Activity } from 'lucide-react'
import { Card } from '../ui/card'

export function AuthGate({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient()
  const status = useQuery({
    queryKey: ['auth-status'],
    queryFn: ({ signal }) => authClient.getAuthStatus<AuthStatus>({ signal }),
    retry: 1,
  })
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  if (status.isPending) {
    return <AuthFrame><p className="muted" role="status">正在检查登录状态…</p></AuthFrame>
  }
  if (status.isError) {
    return <AuthFrame><div role="alert" className="auth-error">{authErrorMessage(status.error)}</div><Button type="button" variant="default" className="primary-button" onClick={() => { void status.refetch() }}>重新连接</Button></AuthFrame>
  }

  const initialized = status.data.initialized
  if (status.data.authenticated) {
    return <>{children}</>
  }

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setBusy(true)
    setActionError(null)
    try {
      if (initialized) {
        await authClient.loginAuth({ body: { password } })
      } else {
        await authClient.initializeAuth({ body: { password } })
      }
      setPassword('')
      await queryClient.invalidateQueries({ queryKey: ['auth-status'] })
    } catch (error) {
      setActionError(authErrorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthFrame>
      <div className="brand auth-brand"><span className="brand-mark"><Activity aria-hidden="true" /></span><span>Vertex <strong>Pro</strong></span></div>
      <h1>{initialized ? '管理员登录' : '初始化管理员'}</h1>
      <p className="muted">{initialized ? '请输入管理员口令以继续。' : '设置首个管理员口令后即可进入控制台。'}</p>
      <form className="auth-form" onSubmit={submit}>
        <label htmlFor="auth-password">管理员口令</label>
        <Input
          id="auth-password"
          type="password"
          autoComplete={initialized ? 'current-password' : 'new-password'}
          value={password}
          onChange={(event) => setPassword(event.target.value)}
          minLength={initialized ? undefined : 12}
          required
          autoFocus
        />
        {actionError && <p className="auth-error" role="alert">{actionError}</p>}
        <Button type="submit" variant="default" className="primary-button" disabled={busy}>
          {busy ? '处理中…' : initialized ? '登录' : '完成初始化'}
        </Button>
      </form>
    </AuthFrame>
  )
}

function AuthFrame({ children }: { children: ReactNode }) {
  return <main className="auth-frame"><Card className="auth-panel shadow-none">{children}</Card></main>
}
