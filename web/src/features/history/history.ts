// History 列表的展示模型（UI-07）：种子、来源、目标下载器、用户状态，
// 以及删除后仍保留的删除时间与证据来源（HIS-02 合同）。

export interface HistoryEntry {
  id: string
  title: string
  site: string
  downloader: string
  user_status: string
  user_status_label: string
  discovered_at: string
  confirmed_at?: string
  finished_at?: string
  deleted_at?: string
  deletion_source?: string
}

export interface HistoryPageData {
  items: ReadonlyArray<HistoryEntry>
  next_cursor: string
}

export const USER_STATUS_FILTERS: ReadonlyArray<{ value: string; label: string }> = [
  { value: '', label: '全部状态' },
  { value: 'WAITING_RULE', label: '等待规则' },
  { value: 'PREPARING_SEED', label: '准备种子' },
  { value: 'WAITING_DOWNLOADER', label: '等待下载器' },
  { value: 'ADDING', label: '正在添加' },
  { value: 'DOWNLOADING', label: '下载中' },
  { value: 'SEEDING', label: '做种中' },
  { value: 'COMPLETED', label: '已完成' },
  { value: 'DELETED', label: '已删除' },
  { value: 'FAILED', label: '失败' },
  { value: 'NEEDS_CONFIRMATION', label: '需确认' },
]

// statusTone 决定状态徽章配色：成功/进行/需关注三类。
export function statusTone(status: string): 'ok' | 'run' | 'wait' | 'bad' {
  switch (status) {
    case 'COMPLETED':
    case 'SEEDING':
      return 'ok'
    case 'DOWNLOADING':
    case 'ADDING':
    case 'PREPARING_SEED':
      return 'run'
    case 'NEEDS_CONFIRMATION':
    case 'DELETED':
      return 'wait'
    default:
      return 'bad'
  }
}

// mergePages 合并翻页结果并按 id 去重（删除期间的稳定分页由服务端保证）。
export function mergePages(pages: ReadonlyArray<HistoryPageData>): HistoryEntry[] {
  const seen = new Set<string>()
  const merged: HistoryEntry[] = []
  for (const page of pages) {
    for (const entry of page.items) {
      if (!seen.has(entry.id)) {
        seen.add(entry.id)
        merged.push(entry)
      }
    }
  }
  return merged
}
