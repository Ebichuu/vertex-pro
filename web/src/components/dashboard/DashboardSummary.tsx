import { ArrowDownToLine, ArrowUpFromLine, HardDrive, Server } from 'lucide-react'
import { Card } from '../ui/card'
import { formatBytes, formatRate, summarizeInstances, type DashboardData } from '../../features/dashboard/dashboard'

export function DashboardSummary({ data }: { data: DashboardData }) {
  const summary = summarizeInstances(data.instances)
  const coverage = (count: number) => count === 0 ? '暂无有效数据' : count === summary.total ? '全部实例的有效数据' : `仅 ${count}/${summary.total} 个实例有有效数据`
  return <section className="dashboard-summary" aria-label="当前运行概况">
    <Card className="summary-card gap-3 p-5 shadow-none">
      <div className="summary-label"><span>在线下载器</span><Server aria-hidden="true" /></div>
      <div className="summary-value">{summary.online} <small>/ {summary.total} 台</small></div>
      <p className="summary-footnote">{summary.total ? '连接正常且快照新鲜' : '尚未配置下载器'}</p>
    </Card>
    {([
      { label: '总下载速度', icon: ArrowDownToLine, text: formatRate(summary.download.value), count: summary.download.count },
      { label: '总上传速度', icon: ArrowUpFromLine, text: formatRate(summary.upload.value), count: summary.upload.count },
      { label: '可分配容量', icon: HardDrive, text: formatBytes(summary.capacity.value), count: summary.capacity.count },
    ] as const).map(item => <Card className="summary-card gap-3 p-5 shadow-none" key={item.label}>
      <div className="summary-label"><span>{item.label}</span><item.icon aria-hidden="true" /></div>
      <div className="summary-value">{item.text}</div>
      <p className="summary-footnote">{coverage(item.count)}</p>
    </Card>)}
  </section>
}
