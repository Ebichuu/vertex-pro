import { Link } from '@tanstack/react-router'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ErrorBoundary } from '../ui/ErrorBoundary'
import { useLanguagePreference, useTheme } from '../../app/preferences'
import { useConnectionStatus, type ConnectionState } from '../../app/event-stream'

const NAV_ENTRIES: ReadonlyArray<{ to: string; key: string; label: string; labelEn: string }> = [
  { to: '/', key: 'dashboard', label: '总览', labelEn: 'Dashboard' },
  { to: '/sites', key: 'sites', label: '站点与 RSS', labelEn: 'Sites & RSS' },
  { to: '/rules', key: 'rules', label: '规则', labelEn: 'Rules' },
  { to: '/history', key: 'history', label: '历史', labelEn: 'History' },
  { to: '/exceptions', key: 'exceptions', label: '异常', labelEn: 'Exceptions' },
  { to: '/settings', key: 'settings', label: '设置', labelEn: 'Settings' },
]

const CONNECTION_LABELS: Record<ConnectionState, { zh: string; en: string; tone: string }> = {
  online: { zh: '已连接', en: 'Connected', tone: 'ok' },
  reconnecting: { zh: '重连中', en: 'Reconnecting', tone: 'warn' },
  offline: { zh: '离线', en: 'Offline', tone: 'bad' },
}

type AppShellProps = { children: ReactNode }

// AppShell 提供左侧导航、主题/语言切换、连接状态与全局错误边界（UI-01）。
// 移动端通过 sidebar-toggle 折叠导航（CSS 断点 768px）。
export function AppShell({ children }: AppShellProps) {
  const { theme, toggleTheme } = useTheme()
  const { language, toggleLanguage } = useLanguagePreference()
  const connection = useConnectionStatus()
  const connectionLabel = CONNECTION_LABELS[connection][language === 'zh-CN' ? 'zh' : 'en']
  const [compact, setCompact] = useState(() => window.matchMedia?.('(max-width: 768px)').matches ?? false)
  const [navOpen, setNavOpen] = useState(false)
  const toggleRef = useRef<HTMLButtonElement>(null)
  const mainRef = useRef<HTMLElement>(null)
  useEffect(() => {
    const media = window.matchMedia?.('(max-width: 768px)')
    if (!media) return
    const update = () => { setCompact(media.matches); setNavOpen(false) }
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  const navigate = () => {
    if (!compact) return
    setNavOpen(false)
    mainRef.current?.focus()
  }
  return (
    <div className="app-shell" data-connection={connection} onKeyDown={(event) => {
      if (event.key === 'Escape' && compact && navOpen) {
        setNavOpen(false)
        toggleRef.current?.focus()
      }
    }}>
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <button ref={toggleRef} type="button" className="sidebar-toggle" aria-expanded={!compact || navOpen} aria-controls="primary-nav" onClick={() => setNavOpen((open) => !open)}>
        <span aria-hidden="true">☰</span>
        <span className="visually-hidden">切换导航</span>
      </button>
      <aside className="sidebar">
        <Link to="/" className="brand" onClick={navigate}>Vertex Pro</Link>
        <nav id="primary-nav" hidden={compact && !navOpen} aria-label={language === 'zh-CN' ? '主导航' : 'Primary'}>
          <ul className="nav-list">
            {NAV_ENTRIES.map((entry) => (
              <li key={entry.key}>
                <Link to={entry.to} onClick={navigate} activeProps={{ className: 'nav-link active', 'aria-current': 'page' }} className="nav-link">
                  {language === 'zh-CN' ? entry.label : entry.labelEn}
                </Link>
              </li>
            ))}
          </ul>
        </nav>
      </aside>
      <div className="main-column">
        <header className="topbar">
          <span className={`connection-status connection-${connection}`} role="status">
            <span className={`connection-dot tone-${CONNECTION_LABELS[connection].tone}`} aria-hidden="true" />
            {connectionLabel}
          </span>
          <div className="topbar-actions">
            <button type="button" className="ghost-button" onClick={toggleTheme} aria-pressed={theme === 'dark'}>
              {theme === 'dark' ? '☀ Light' : '🌙 Dark'}
            </button>
            <button type="button" className="ghost-button" onClick={toggleLanguage}>
              {language === 'zh-CN' ? 'EN' : '中文'}
            </button>
          </div>
        </header>
        <main ref={mainRef} className="page-content" id="main-content" tabIndex={-1}>
          <ErrorBoundary>{children}</ErrorBoundary>
        </main>
      </div>
    </div>
  )
}
