import { Button } from '../ui/button'
import { useEffect, useRef, useState } from 'react'
import type { PreparationLoader, PreparationPage } from '../../features/sites/sites'
import { formatCapacityGB } from '../../features/shared/units'

const labels: Record<string, string> = {
  QUEUED: '等待准备', RATE_WAIT: '等待站点限流许可', PREPARING: '正在获取种子',
  RETRY_WAIT: '等待重试', READY: '已进入投递流程', FILTERED: '未进入投递流程',
  BASELINE: '历史条目，已跳过', FAILED: '准备失败',
}
const reasons: Record<string, string> = {
  PROCESSING_DEADLINE: '超过准备期限', PROCESSING_ATTEMPTS_EXHAUSTED: '重试次数已用完',
  TORRENT_FETCH_FAILED: '种子获取失败', RSS_ENTRY_INVALID: 'RSS 条目无效',
}

export function PreparationPanel({ feedId, name, load, onClose }: { feedId: string; name: string; load: PreparationLoader; onClose: () => void }) {
  const [cursors, setCursors] = useState([''])
  const [page, setPage] = useState<PreparationPage | null>(null)
  const [failed, setFailed] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const loader = useRef(load)
  loader.current = load
  const cursor = cursors[cursors.length - 1]
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    setPage(null); setFailed(false)
    const update = async () => {
      try {
        const result = await loader.current(feedId, cursor, controller.signal)
        if (!controller.signal.aborted) { setPage(result); setFailed(false) }
      } catch { if (!controller.signal.aborted) setFailed(true) }
      finally { if (!controller.signal.aborted) timer = setTimeout(() => { void update() }, 5000) }
    }
    void update()
    return () => { controller.abort(); clearTimeout(timer) }
  }, [feedId, cursor, refresh])
  return <aside className="rss-preview" aria-label={`${name} 准备状态`}>
    <div className="sites-rss-section-heading"><h3>{name} · 逐条准备状态</h3><Button type="button" onClick={onClose}>关闭准备状态</Button></div>
    <p className="muted">每 5 秒刷新。已进入投递流程的任务可在历史页面查看发送和确认结果。</p>
    {failed && <p role="alert">准备状态读取失败，当前数据可能已过期。<Button type="button" onClick={() => setRefresh(value => value + 1)}>重新读取</Button></p>}
    {!page && !failed && <p role="status">正在加载准备状态…</p>}
    {page && <>
      {page.items.length === 0 ? <p>暂无准备记录。</p> : <div className="sites-rss-table-wrap"><table className="sites-rss-table" aria-label="种子准备记录">
        <thead><tr><th>种子</th><th>大小</th><th>准备状态</th><th>限流等待</th><th>尝试次数</th></tr></thead>
        <tbody>{page.items.map(item => <tr key={item.id}>
          <td>{item.title || '未命名条目'}</td><td>{formatCapacityGB(item.size_bytes)}</td>
          <td>{labels[item.state] ?? '状态未知'}{item.last_error_code && <small className="table-subline">{reasons[item.last_error_code] ?? item.last_error_code}</small>}{item.state === 'RETRY_WAIT' && <small className="table-subline">下次重试：{new Date(item.next_attempt_at).toLocaleString()}</small>}</td>
          <td>{(item.rate_wait_ms / 1000).toFixed(1)} 秒</td><td>{item.attempts} / {item.max_attempts}</td>
        </tr>)}</tbody>
      </table></div>}
      <div className="sites-rss-section-heading"><Button type="button" disabled={cursors.length === 1} onClick={() => setCursors(value => value.slice(0, -1))}>上一页</Button><span>第 {cursors.length} 页</span><Button type="button" disabled={!page.next_cursor || failed} onClick={() => setCursors(value => [...value, page.next_cursor])}>下一页</Button></div>
    </>}
  </aside>
}
