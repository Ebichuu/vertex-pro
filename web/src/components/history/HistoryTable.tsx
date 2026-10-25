import { mergePages, statusTone, USER_STATUS_FILTERS, type HistoryEntry, type HistoryPageData } from '../../features/history/history'

type HistoryTableProps = {
  pages: ReadonlyArray<HistoryPageData>
  statusFilter: string
  onStatusFilterChange: (status: string) => void
  hasMore: boolean
  loading: boolean
  onLoadMore: () => void
  onOpen: (jobId: string) => void
}

// HistoryTable 展示种子、来源、目标下载器、用户状态（UI-07）。
// 已删除条目保留在列表中并显示删除时间与证据来源（HIS-02 合同）。
export function HistoryTable({ pages, statusFilter, onStatusFilterChange, hasMore, loading, onLoadMore, onOpen }: HistoryTableProps) {
  const entries = mergePages(pages)
  return (
    <section aria-label="历史记录">
      <div className="history-toolbar">
        <label htmlFor="history-status-filter">状态筛选</label>
        <select id="history-status-filter" value={statusFilter} onChange={(event) => onStatusFilterChange(event.target.value)}>
          {USER_STATUS_FILTERS.map((option) => (
            <option key={option.value} value={option.value}>{option.label}</option>
          ))}
        </select>
        <span className="muted" role="note">共 {entries.length} 条</span>
      </div>
      <div className="history-table-scroll" role="region" aria-label="投递历史表格，可横向滚动" tabIndex={0}>
      <table className="history-table" aria-label="投递历史">
        <thead>
          <tr>
            <th scope="col">种子</th>
            <th scope="col">来源</th>
            <th scope="col">目标下载器</th>
            <th scope="col">状态</th>
            <th scope="col">发现时间</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => (
            <HistoryRow key={entry.id} entry={entry} onOpen={onOpen} />
          ))}
          {entries.length === 0 && (
            <tr><td colSpan={5} className="muted">没有匹配的投递记录。</td></tr>
          )}
        </tbody>
      </table>
      </div>
      {hasMore && (
        <button type="button" className="ghost-button" onClick={onLoadMore} disabled={loading}>
          {loading ? '加载中…' : '加载更多'}
        </button>
      )}
    </section>
  )
}

function HistoryRow({ entry, onOpen }: { entry: HistoryEntry; onOpen: (jobId: string) => void }) {
  const deleted = Boolean(entry.deleted_at)
  const title = entry.title || entry.id
  return (
    <tr className={deleted ? 'history-row-deleted' : undefined}>
      <td><button type="button" className="history-title-link" aria-label={`查看 ${title} 详情`} onClick={() => onOpen(entry.id)}>{title}</button></td>
      <td>{entry.site || '—'}</td>
      <td>{entry.downloader || '—'}</td>
      <td>
        <span className={`status-chip tone-${statusTone(entry.user_status)}`}>{entry.user_status_label}</span>
        {deleted && (
          <span className="muted deletion-note">
            {' '}删除于 {entry.deleted_at?.slice(0, 10)}（{entry.deletion_source === 'QB_USER_REMOVED' ? '手动删除' : entry.deletion_source === 'QUI_LIFECYCLE' ? '生命周期策略' : '来源待确认'}）
          </span>
        )}
      </td>
      <td className="muted">{entry.discovered_at.slice(0, 10)}</td>
    </tr>
  )
}
