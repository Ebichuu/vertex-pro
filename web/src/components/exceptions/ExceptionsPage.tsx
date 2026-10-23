import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { csrfHeaders } from '../../features/auth/auth'
import { items, record, ruleError, rulesClient } from '../../features/rules/api'
import type { ExceptionItem } from '../../features/exceptions/exceptions'
import { ExceptionsList } from './ExceptionsList'

function exceptionItem(value: unknown): ExceptionItem {
  const raw = record(value)
  if (typeof raw.id !== 'string' || typeof raw.title !== 'string') throw new Error('异常列表格式无效。')
  return { id: raw.id, title: raw.title, downloader: typeof raw.downloader === 'string' ? raw.downloader : '', user_status: String(raw.attempt_state || raw.job_state || ''), user_status_label: typeof raw.user_status_label === 'string' ? raw.user_status_label : '需确认', allowed_actions: Array.isArray(raw.allowed_actions) ? raw.allowed_actions.filter((action): action is string => typeof action === 'string') : [], action_unavailable_reason: typeof raw.action_unavailable_reason === 'string' ? raw.action_unavailable_reason : undefined }
}

export function ExceptionsPage() {
  const jobs = useInfiniteQuery({ queryKey: ['exceptions'], initialPageParam: '', queryFn: async ({ pageParam }) => {
    const raw = await rulesClient.listJobs({ query: { user_status: 'NEEDS_CONFIRMATION', limit: '50', cursor: pageParam || undefined } })
    return { items: items(raw).map(exceptionItem), nextCursor: typeof raw.next_cursor === 'string' ? raw.next_cursor : '' }
  }, getNextPageParam: (page) => page.nextCursor || undefined })
  const instances = useQuery({ queryKey: ['exception-instances'], queryFn: async () => items(await rulesClient.listInstances()) })
  const failures = jobs.error || instances.error
  if (jobs.isPending || instances.isPending) return <p role="status">正在加载异常…</p>
  const uncertain = instances.data?.filter((row) => {
    const quality = record(row.data_quality ?? {})
    return quality.capacity_state === 'CAPACITY_UNCERTAIN' || quality.capacity_state === 'RECONCILING'
  }).map((row) => String(row.name || row.id)) ?? []
  const unique = new Map(jobs.data?.pages.flatMap((page) => page.items).map((item) => [item.id, item]))
  return <section className="exceptions-page"><h1>异常</h1><p className="muted">操作使用服务端当前快照核对证据；没有新鲜证据时不会执行人工确认。</p>
    {failures && <p role="alert">异常信息加载失败：{ruleError(failures)}<button type="button" className="ghost-button" onClick={() => { void jobs.refetch(); void instances.refetch() }}>重试加载</button></p>}
    {!jobs.isError && !instances.isError && <ExceptionsList items={[...unique.values()]} uncertainInstances={uncertain} onAction={async (jobID, action, commandID) => {
      const response = await rulesClient.acknowledgeUnknownJob(jobID, { body: { action, command_id: commandID }, headers: { ...csrfHeaders(), 'Idempotency-Key': commandID } })
      if (response.state === 'FAILED') throw new Error('命令失败。')
      if (response.state === 'PENDING' || response.state === 'RUNNING') throw new Error('命令尚未完成，请刷新后检查。')
      await Promise.all([jobs.refetch(), instances.refetch()])
    }} />}
    {jobs.hasNextPage && <button type="button" className="ghost-button" disabled={jobs.isFetching} onClick={() => { void jobs.fetchNextPage() }}>加载更多异常</button>}
  </section>
}
