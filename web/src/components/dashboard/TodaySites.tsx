import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { Button } from '../ui/button'
import { Badge } from '../ui/badge'
import type { DashboardData } from '../../features/dashboard/dashboard'

export function TodaySites({ data }: { data: DashboardData }) {
  return <section className="today-sites" aria-labelledby="today-sites-title">
    <div className="section-heading"><div><h2 id="today-sites-title">今日站点数据</h2><p className="muted">{data.today_date ?? '今日'} · UTC 零点起统计，发现数量按来源去重</p></div><Button asChild variant="ghost" size="sm"><Link to="/sites">管理站点<ArrowUpRight aria-hidden="true" /></Link></Button></div>
    {data.today_sites === undefined ? <p className="empty-state">今日站点统计暂不可用。</p> : <div className="history-list"><div className="history-table-scroll" role="region" aria-label="今日站点统计表，可横向滚动" tabIndex={0}>
      <table className="history-table" aria-label="今日站点数据"><thead><tr><th>站点</th><th>今日发现</th><th>生成任务</th><th>已确认进种</th><th>待处理异常</th></tr></thead>
        <tbody>{data.today_sites.map(site => <tr key={site.id}><td><strong>{site.name}</strong><span className="today-site-state"><Badge variant="ghost" className={`status-chip tone-${site.enabled ? 'ok' : 'neutral'}`}>{site.enabled ? '已启用' : '已停用'}</Badge><small className="muted">{site.feeds} 个 RSS</small></span></td><td>{site.discovered}</td><td>{site.jobs}</td><td>{site.confirmed}</td><td>{site.attention}</td></tr>)}{data.today_sites.length === 0 && <tr><td colSpan={5} className="empty-cell muted">还没有配置站点。</td></tr>}</tbody>
      </table>
    </div><p className="today-sites-note muted">已确认进种包含今日确认的较早任务；待处理异常仅统计今日生成且仍需处理的任务。</p></div>}
  </section>
}
