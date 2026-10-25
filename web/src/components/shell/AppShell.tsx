import { Link, useRouterState } from '@tanstack/react-router'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Activity, ChevronRight, CircleAlert, History, LayoutDashboard, Menu, Moon, Rss, Settings2, SlidersHorizontal, Sun, X, type LucideIcon } from 'lucide-react'
import { Button } from '../ui/button'
import { ErrorBoundary } from '../ui/ErrorBoundary'
import { useLanguagePreference, useTheme } from '../../app/preferences'
import { useConnectionStatus, type ConnectionState } from '../../app/event-stream'

const NAV_ENTRIES: ReadonlyArray<{ to: string; key: string; label: string; labelEn: string; icon: LucideIcon }> = [
  { to: '/', key: 'dashboard', label: '总览', labelEn: 'Dashboard', icon: LayoutDashboard },
  { to: '/sites', key: 'sites', label: '站点与 RSS', labelEn: 'Sites & RSS', icon: Rss },
  { to: '/rules', key: 'rules', label: '规则', labelEn: 'Rules', icon: SlidersHorizontal },
  { to: '/history', key: 'history', label: '历史', labelEn: 'History', icon: History },
  { to: '/exceptions', key: 'exceptions', label: '异常', labelEn: 'Exceptions', icon: CircleAlert },
  { to: '/settings', key: 'settings', label: '设置', labelEn: 'Settings', icon: Settings2 },
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
  const pathname = useRouterState({ select: state => state.location.pathname })
  const current = NAV_ENTRIES.find(entry => entry.to !== '/' && pathname.startsWith(entry.to))
  const pageLabel = current ? (language === 'zh-CN' ? current.label : current.labelEn) : pathname.startsWith('/downloaders') ? '下载器详情' : pathname === '/aggregation' ? '规则预览' : language === 'zh-CN' ? '总览' : 'Dashboard'
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
      <Button ref={toggleRef} type="button" className="sidebar-toggle hidden max-[768px]:inline-flex" size="icon" variant="ghost" aria-expanded={!compact || navOpen} aria-controls="primary-nav" onClick={() => setNavOpen((open) => !open)}>
        {navOpen ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
        <span className="visually-hidden">切换导航</span>
      </Button>
      <aside className="sidebar">
        <Link to="/" className="brand" onClick={navigate}><span className="brand-mark"><Activity aria-hidden="true" /></span><span>Vertex <strong>Pro</strong></span></Link>
        <nav id="primary-nav" hidden={compact && !navOpen} aria-label={language === 'zh-CN' ? '主导航' : 'Primary'}>
          <p className="nav-caption">{language === 'zh-CN' ? '工作空间' : 'Workspace'}</p>
          <ul className="nav-list">
            {NAV_ENTRIES.map((entry) => (
              <li key={entry.key}>
                <Link to={entry.to} onClick={navigate} activeOptions={{ exact: entry.to === '/' }} activeProps={{ className: 'nav-link active', 'aria-current': 'page' }} className="nav-link">
                  <entry.icon aria-hidden="true" />
                  {language === 'zh-CN' ? entry.label : entry.labelEn}
                </Link>
              </li>
            ))}
          </ul>
        </nav>
        <div className="sidebar-footer"><span className="sidebar-footer-icon"><Activity aria-hidden="true" /></span><div><strong>Vertex Pro</strong><span>{language === 'zh-CN' ? '自动化下载控制台' : 'Download automation'}</span></div></div>
      </aside>
      <div className="main-column">
        <header className="topbar">
          <div className="breadcrumb"><span>{language === 'zh-CN' ? '工作空间' : 'Workspace'}</span><ChevronRight aria-hidden="true" /><strong>{pageLabel}</strong></div>
          <span className={`connection-status connection-${connection}`} role="status">
            <span className={`connection-dot tone-${CONNECTION_LABELS[connection].tone}`} aria-hidden="true" />
            {connectionLabel}
          </span>
          <div className="topbar-actions">
            <Button type="button" variant="ghost" size="icon" onClick={toggleTheme} aria-pressed={theme === 'dark'} aria-label={theme === 'dark' ? '切换浅色主题 Light' : '切换深色主题 Dark'} title={theme === 'dark' ? '切换浅色主题' : '切换深色主题'}>
              {theme === 'dark' ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}
            </Button>
            <Button type="button" variant="ghost" size="sm" onClick={toggleLanguage} aria-label={language === 'zh-CN' ? '切换英文导航' : '切换中文导航'}>
              {language === 'zh-CN' ? 'EN' : '中文'}
            </Button>
          </div>
        </header>
        <main ref={mainRef} className="page-content" id="main-content" tabIndex={-1}>
          <ErrorBoundary>{children}</ErrorBoundary>
        </main>
      </div>
    </div>
  )
}
