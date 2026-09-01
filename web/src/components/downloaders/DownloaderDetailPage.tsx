import { useEffect, useMemo, useState } from 'react'
import {
  formatDownloaderBytes,
  formatDownloaderRate,
  formatDownloaderTime,
  freshnessLabel,
  healthLabel,
  healthTone,
  type DownloaderDetail,
  type DownloaderDetailLoader,
  type DownloaderPeriodKey,
  type DownloaderPeriodSummary,
} from '../../features/downloaders/downloader-detail'

type DownloaderDetailPageProps = {
  downloaderId: string
  loadDetail: DownloaderDetailLoader
  onBack: () => void
}

type LoadState =
  | { status: 'loading' }
  | { status: 'ready'; downloaderId: string; detail: DownloaderDetail }
  | { status: 'error'; downloaderId: string }

const PERIODS: ReadonlyArray<{ key: DownloaderPeriodKey; label: string }> = [
  { key: 'day', label: '日' },
  { key: 'week', label: '周' },
  { key: 'month', label: '月' },
]

export function DownloaderDetailPage({ downloaderId, loadDetail, onBack }: DownloaderDetailPageProps) {
  const [attempt, setAttempt] = useState(0)
  const [periodKey, setPeriodKey] = useState<DownloaderPeriodKey>('day')
  const [state, setState] = useState<LoadState>({ status: 'loading' })

  useEffect(() => {
    let active = true
    setState({ status: 'loading' })
    setPeriodKey('day')
    void loadDetail(downloaderId).then(
      (detail) => {
        if (active) setState({ status: 'ready', downloaderId, detail })
      },
      () => {
        if (active) setState({ status: 'error', downloaderId })
      },
    )
    return () => { active = false }
  }, [attempt, downloaderId, loadDetail])

  if (state.status === 'loading' || state.downloaderId !== downloaderId) {
    return (
      <section className="downloader-detail-page" aria-labelledby="downloader-detail-loading-title">
        <button type="button" className="history-back-button" onClick={onBack}>← 返回总览</button>
        <div className="panel">
          <h1 id="downloader-detail-loading-title">下载器详情</h1>
          <p className="muted" role="status" aria-live="polite">正在加载下载器数据…</p>
        </div>
      </section>
    )
  }

  if (state.status === 'error') {
    return (
      <section className="downloader-detail-page" aria-labelledby="downloader-detail-error-title">
        <button type="button" className="history-back-button" onClick={onBack}>← 返回总览</button>
        <div className="panel" role="alert">
          <h1 id="downloader-detail-error-title">下载器详情加载失败</h1>
          <p className="muted">未找到或暂时无法读取这个下载器。请返回总览或重新加载。</p>
          <button type="button" className="ghost-button" onClick={() => setAttempt((value) => value + 1)}>重新加载</button>
        </div>
      </section>
    )
  }

  const { detail } = state
  const tone = healthTone(detail)
  const period = detail.periods[periodKey]
  return (
    <article className="downloader-detail-page" aria-labelledby="downloader-detail-title">
      <div className="downloader-detail-heading">
        <div>
          <button type="button" className="history-back-button" onClick={onBack}>← 返回总览</button>
          <p className="eyebrow">DOWNLOADER DETAIL</p>
          <h1 id="downloader-detail-title">{detail.name}</h1>
          <p className="muted">实时传输、容量与流量统计来自同一份下载器快照。</p>
        </div>
        <div className="downloader-detail-status">
          <span className={`status-chip tone-${tone}`}>{healthLabel(detail.health)}</span>
          <span className={`status-chip tone-${detail.freshness === 'FRESH' ? 'ok' : 'wait'}`}>{freshnessLabel(detail.freshness)}</span>
          <span className="page-badge">LOCAL ONLY</span>
        </div>
      </div>

      <section className="downloader-detail-section" aria-labelledby="downloader-realtime-title">
        <div className="downloader-detail-section-heading">
          <div><p className="eyebrow">REALTIME SNAPSHOT</p><h2 id="downloader-realtime-title">实时状态</h2></div>
          <span>观测于 {formatDownloaderTime(detail.realtime.observedAt)}</span>
        </div>
        <dl className="downloader-metric-grid">
          <Metric label="下载速度" value={formatDownloaderRate(detail.realtime.downloadRate)} />
          <Metric label="上传速度" value={formatDownloaderRate(detail.realtime.uploadRate)} />
          <Metric label="活动任务" value={formatCount(detail.realtime.activeCount)} />
          <Metric label="下载中" value={formatCount(detail.realtime.downloadingCount)} />
          <Metric label="做种中" value={formatCount(detail.realtime.seedingCount)} />
          <Metric label="已暂停" value={formatCount(detail.realtime.pausedCount)} />
        </dl>
        <p className="downloader-source-note">数据来源：{detail.realtime.source}</p>
      </section>

      <section className="downloader-detail-section" aria-labelledby="downloader-capacity-title">
        <div className="downloader-detail-section-heading">
          <div><p className="eyebrow">CAPACITY</p><h2 id="downloader-capacity-title">容量</h2></div>
          <span>容量状态：{detail.capacity.state}</span>
        </div>
        <dl className="downloader-capacity-grid">
          <Metric label="总容量" value={formatDownloaderBytes(detail.capacity.totalBytes)} />
          <Metric label="物理可用" value={formatDownloaderBytes(detail.capacity.freeBytes)} />
          <Metric label="Vertex 可用" value={formatDownloaderBytes(detail.capacity.effectiveFreeBytes)} />
        </dl>
        <CapacityBar capacity={detail.capacity} />
        <p className="downloader-source-note">容量观测于 {formatDownloaderTime(detail.capacity.observedAt)}；未知值保持未知，不按 0 计算。</p>
      </section>

      <section className="downloader-detail-section" aria-labelledby="downloader-stats-title">
        <div className="downloader-detail-section-heading">
          <div><p className="eyebrow">TRAFFIC HISTORY</p><h2 id="downloader-stats-title">流量统计</h2></div>
          <div className="downloader-period-tabs" role="tablist" aria-label="统计周期">
            {PERIODS.map((item) => (
              <button key={item.key} type="button" role="tab" aria-selected={periodKey === item.key} className={periodKey === item.key ? 'active' : ''} onClick={() => setPeriodKey(item.key)}>{item.label}</button>
            ))}
          </div>
        </div>
        <PeriodSummary period={period} />
        <TrafficChart period={period} />
      </section>

      <section className="downloader-quality" aria-labelledby="downloader-quality-title">
        <div><p className="eyebrow">DATA QUALITY</p><h2 id="downloader-quality-title">数据质量</h2></div>
        <p>{freshnessLabel(detail.freshness)}，最近观测于 {formatDownloaderTime(detail.observedAt)}。</p>
        {detail.reasonCodes.length > 0 && <ul className="reason-list" aria-label="数据质量原因">{detail.reasonCodes.map((reason) => <li key={reason} className="reason-chip">{reason}</li>)}</ul>}
        {period.missingPoints > 0 && <p>当前周期有 {period.missingPoints} 个小时缺失，图表已保留空白，不会插值。</p>}
        {period.counterResets > 0 && <p>当前周期检测到 {period.counterResets} 次计数器重置，汇总按有效增量计算。</p>}
      </section>
    </article>
  )
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function formatCount(value?: number): string {
  return value === undefined || value < 0 ? '暂无数据' : String(value)
}

function CapacityBar({ capacity }: { capacity: DownloaderDetail['capacity'] }) {
  const total = capacity.totalBytes
  const effective = capacity.effectiveFreeBytes
  const ratio = total && effective !== undefined && total > 0 ? Math.max(0, Math.min(100, effective / total * 100)) : undefined
  return (
    <div className="downloader-capacity-bar" aria-label={ratio === undefined ? '容量比例暂无数据' : `Vertex 可用容量 ${ratio.toFixed(0)}%`}>
      <div className="downloader-capacity-track"><span style={{ width: `${ratio ?? 0}%` }} /></div>
      <span>{ratio === undefined ? '容量比例暂无数据' : `${ratio.toFixed(0)}% 可用`}</span>
    </div>
  )
}

function PeriodSummary({ period }: { period: DownloaderPeriodSummary }) {
  return (
    <dl className="downloader-period-summary">
      <Metric label={`${period.label}上传`} value={formatDownloaderBytes(period.uploadedBytes)} />
      <Metric label={`${period.label}下载`} value={formatDownloaderBytes(period.downloadedBytes)} />
      <Metric label="分享率" value={period.ratio === undefined ? '暂无数据' : period.ratio.toFixed(2)} />
    </dl>
  )
}

function TrafficChart({ period }: { period: DownloaderPeriodSummary }) {
  const max = useMemo(() => Math.max(1, ...period.points.flatMap((point) => [point.downloadRate ?? 0, point.uploadRate ?? 0])), [period.points])
  return (
    <div className="downloader-chart-wrap">
      <div className="downloader-chart-legend"><span><i className="legend-swatch download" />下载</span><span><i className="legend-swatch upload" />上传</span><span><i className="legend-swatch missing" />缺失小时</span></div>
      <div className="downloader-chart" role="img" aria-label={`${period.label}下载与上传速度图`}>
        {period.points.map((point) => {
          const downloadHeight = point.downloadRate === undefined ? 0 : Math.max(4, point.downloadRate / max * 100)
          const uploadHeight = point.uploadRate === undefined ? 0 : Math.max(4, point.uploadRate / max * 100)
          return (
            <div key={point.timestamp} className={`downloader-chart-point${point.missing ? ' is-missing' : ''}`}>
              <div className="downloader-bars" aria-hidden="true"><span className="bar-download" style={{ height: `${downloadHeight}%` }} /><span className="bar-upload" style={{ height: `${uploadHeight}%` }} /></div>
              <span className="downloader-chart-label">{point.label}</span>
              <span className="visually-hidden">{point.label}：{point.missing ? '数据缺失' : `下载 ${formatDownloaderRate(point.downloadRate)}，上传 ${formatDownloaderRate(point.uploadRate)}`}{point.counterReset ? '，计数器重置' : ''}</span>
            </div>
          )
        })}
      </div>
      <p className="downloader-chart-note">柱状图展示采样速度；缺失小时保持空白，计数器重置会在数据质量中说明。</p>
    </div>
  )
}
