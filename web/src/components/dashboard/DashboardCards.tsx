import { cardTone, formatBytes, formatRate, freshnessLabel, type DashboardData } from '../../features/dashboard/dashboard'

type DashboardCardsProps = { data: DashboardData; onOpen?: (downloaderId: string) => void }

// DashboardCards 一屏呈现全部 qB 实例卡片（UI-02）：在线状态、实时速率、
// 任务计数、容量与数据新鲜度；异常实例带原因码，不美化为健康。
export function DashboardCards({ data, onOpen }: DashboardCardsProps) {
  return (
    <section aria-labelledby="dashboard-cards-title">
      <h2 id="dashboard-cards-title" className="visually-hidden">下载器卡片</h2>
      <p className="muted" role="note">实例 {data.instances.length} 个 · 任务 {data.jobs_total} 个</p>
      <ul className="dashboard-grid" aria-label="实例卡片">
        {data.instances.map((card) => (
          <li key={card.id} className={`dashboard-card tone-${cardTone(card)}`} aria-label={`实例 ${card.id}`}>
            <header className="dashboard-card-header">
              <span className="dashboard-card-name">{card.id}</span>
              <span className={`connection-status tone-${cardTone(card)}`}>{card.sync_state}</span>
            </header>
            <dl className="dashboard-card-stats">
              <div><dt>下载</dt><dd>{formatRate(card.download_rate)}</dd></div>
              <div><dt>上传</dt><dd>{formatRate(card.upload_rate)}</dd></div>
              <div><dt>下载中</dt><dd>{card.downloading_count}</dd></div>
              <div><dt>做种</dt><dd>{card.seeding_count}</dd></div>
              <div><dt>物理可用</dt><dd>{formatBytes(card.physical_free_bytes)}</dd></div>
              <div><dt>Vertex 可用</dt><dd>{formatBytes(card.effective_free_bytes)}</dd></div>
            </dl>
            <footer className="dashboard-card-footer">
              <span className="muted">{freshnessLabel(card)}</span>
              {onOpen && <button type="button" className="text-link dashboard-detail-link dashboard-detail-button" onClick={() => onOpen(card.id)}>查看详情 →</button>}
              {card.reason_codes.length > 0 && (
                <ul className="reason-list" aria-label="原因">
                  {card.reason_codes.map((reason) => (
                    <li key={reason} className="reason-chip">{reason}</li>
                  ))}
                </ul>
              )}
            </footer>
          </li>
        ))}
      </ul>
    </section>
  )
}
