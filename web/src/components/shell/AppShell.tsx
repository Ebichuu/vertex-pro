import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { ErrorBoundary } from '../ui/ErrorBoundary'
import { useConnectionStatus, useLanguagePreference, useTheme, type ConnectionState } from '../../app/preferences'

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
  return (
    <div className="app-shell" data-connection={connection}>
      <button type="button" className="sidebar-toggle" aria-expanded="true" aria-controls="primary-nav">
        <span aria-hidden="true">☰</span>
        <span className="visually-hidden">切换导航</span>
      </button>
      <aside className="sidebar">
        <Link to="/" className="brand">Vertex Pro</Link>
        <nav id="primary-nav" aria-label={language === 'zh-CN' ? '主导航' : 'Primary'}>
          <ul className="nav-list">
            {NAV_ENTRIES.map((entry) => (
              <li key={entry.key}>
                <Link to={entry.to} activeProps={{ className: 'nav-link active', 'aria-current': 'page' }} className="nav-link">
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
            <span className={`connection-dot tone-${connectionLabel === '已连接' || connectionLabel === 'Connected' ? 'ok' : connectionLabel === '离线' || connectionLabel === 'Offline' ? 'bad' : 'warn'}`} aria-hidden="true" />
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
        <main className="page-content" id="main-content">
          <ErrorBoundary>{children}</ErrorBoundary>
        </main>
      </div>
    </div>
  )
}
