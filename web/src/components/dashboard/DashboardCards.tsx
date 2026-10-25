import { cardTone, formatBytes, formatRate, freshnessLabel, type DashboardData } from '../../features/dashboard/dashboard'
import { ArrowUpRight, Server } from 'lucide-react'
import { Card } from '../ui/card'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { useState } from 'react'

type DashboardCardsProps = { data: DashboardData; onOpen?: (downloaderId: string) => void }
const syncLabels: Record<string, string> = { OFFLINE: '离线', STALE: '数据过期', SYNCING: '同步中', BOOTSTRAP: '初始化中' }

// DashboardCards 一屏呈现全部 qB 实例卡片（UI-02）：在线状态、实时速率、
// 任务计数、容量与数据新鲜度；异常实例带原因码，不美化为健康。
export function DashboardCards({ data, onOpen }: DashboardCardsProps) {
  const [filter, setFilter] = useState('all')
  const instances = [...data.instances].filter(card => filter === 'all' || (filter === 'online' ? card.sync_state === 'READY' && card.freshness === 'FRESH' : cardTone(card) !== 'ok'))
    .sort((a, b) => Number(b.sync_state === 'READY') - Number(a.sync_state === 'READY') || a.id.localeCompare(b.id, undefined, { numeric: true }))
  return (
    <section aria-labelledby="dashboard-cards-title">
      <div className="section-heading"><h2 id="dashboard-cards-title">下载器</h2>
      <div className="dashboard-filters"><p className="muted" role="note">实例 {data.instances.length} 个 · 任务 {data.jobs_total} 个</p><select aria-label="筛选下载器" value={filter} onChange={event => setFilter(event.target.value)}><option value="all">全部下载器</option><option value="online">在线</option><option value="attention">需关注</option></select></div></div>
      <ul className="dashboard-grid" aria-label="实例卡片">
        {instances.map((card) => (
          <li key={card.id} aria-label={`实例 ${card.id}`}>
            <Card className="dashboard-card gap-5 p-5 shadow-none">
            <header className="dashboard-card-header">
              <span className="downloader-symbol"><Server aria-hidden="true" /></span>
              <div className="dashboard-card-identity"><span className="dashboard-card-name">实例 {card.id}</span><small>qBittorrent · qui</small></div>
              <Badge variant="ghost" className={`status-chip tone-${cardTone(card)}`} title={card.sync_state}>{card.sync_state === 'READY' ? cardTone(card) === 'ok' ? '在线' : '需关注' : syncLabels[card.sync_state] ?? card.sync_state}</Badge>
            </header>
            <dl className="dashboard-card-stats">
              <div><dt>下载</dt><dd>{formatRate(card.download_rate)}</dd></div>
              <div><dt>上传</dt><dd>{formatRate(card.upload_rate)}</dd></div>
              <div><dt>下载中</dt><dd>{Number.isFinite(card.downloading_count) && card.downloading_count >= 0 ? card.downloading_count : '未知'}</dd></div>
              <div><dt>做种</dt><dd>{Number.isFinite(card.seeding_count) && card.seeding_count >= 0 ? card.seeding_count : '未知'}</dd></div>
              <div><dt>物理可用</dt><dd>{formatBytes(card.physical_free_bytes)}</dd></div>
              <div><dt>Vertex 可用</dt><dd>{formatBytes(card.effective_free_bytes)}</dd></div>
            </dl>
            <footer className="dashboard-card-footer">
              <span className="muted">{freshnessLabel(card)}{card.realtime_freshness !== 'FRESH' && ' · 速率已过期'}</span>
              {onOpen && <Button type="button" variant="ghost" size="sm" onClick={() => onOpen(card.id)} aria-label={`查看实例 ${card.id} 详情`}>查看详情<ArrowUpRight aria-hidden="true" /></Button>}
              {card.reason_codes.length > 0 && (
                <details><summary className="muted">查看 {card.reason_codes.length} 项状态原因</summary>
                <ul className="reason-list" aria-label="原因">
                  {card.reason_codes.map((reason) => (
                    <li key={reason} className="reason-chip">{reason}</li>
                  ))}
                </ul>
                </details>
              )}
            </footer>
            </Card>
          </li>
        ))}
      </ul>
      {data.instances.length === 0 && <p className="empty-state">还没有下载器。配置实例后，可在这里查看连接状态、速度和容量。</p>}
      {data.instances.length > 0 && instances.length === 0 && <p className="empty-state">没有符合当前筛选条件的下载器。</p>}
    </section>
  )
}
