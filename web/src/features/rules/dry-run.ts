import type { AggregationPreviewData } from './aggregation-preview'
import { formatCapacityGB } from '../shared/units'

export type DryRunEvaluationState = 'TRUE' | 'FALSE' | 'UNKNOWN'

export type DryRunConditionPreview = {
  id: string
  label: string
  state: DryRunEvaluationState
  detail: string
  missingField?: string
}

export type DryRunRulePreview = {
  id: string
  name: string
  priority: number
  revision: number
  state: DryRunEvaluationState
  reason: string
  winner: boolean
  conditions: DryRunConditionPreview[]
}

export type RecentRssItem = {
  id: string
  title: string
  site: string
  feed: string
  sizeBytes: number | null
  publishedAt: string
}

export type DryRunPreviewResult = {
  itemId: string
  evaluatedAt: string
  rules: DryRunRulePreview[]
  aggregation: AggregationPreviewData
}

export type DryRunExecutor = (itemId: string) => Promise<DryRunPreviewResult>

export const recentRssItems: RecentRssItem[] = [
  { id: 'rss-dune-4k', title: 'Dune.Part.Two.2024.2160p.UHD.BluRay', site: 'HDSky', feed: 'HDSky RSS', sizeBytes: 38_439_342_080, publishedAt: '2026-08-30T09:20:00Z' },
  { id: 'rss-silo-free', title: 'Silo.S02E04.1080p.WEB-DL', site: 'M-Team', feed: 'M-Team Freeleech', sizeBytes: 7_516_192_768, publishedAt: '2026-08-30T08:55:00Z' },
  { id: 'rss-missing-size', title: 'Archive.Documentary.Remux', site: 'HDSky', feed: 'HDSky RSS', sizeBytes: null, publishedAt: '2026-08-30T08:35:00Z' },
]

const localResults: Record<string, DryRunPreviewResult> = {
  'rss-dune-4k': {
    itemId: 'rss-dune-4k',
    evaluatedAt: '2026-08-30T09:21:00Z',
    rules: [
      {
        id: 'rule-4k', name: '4K 电影', priority: 100, revision: 3, state: 'TRUE', reason: '全部条件满足，优先级最高。', winner: true,
        conditions: [
          { id: 'site', label: '站点等于 HDSky', state: 'TRUE', detail: 'RSS 项站点为 HDSky。' },
          { id: 'size', label: '大小不少于 20 GB', state: 'TRUE', detail: `当前大小 ${formatCapacityGB(38_439_342_080)}。` },
          { id: 'title', label: '标题包含 2160p', state: 'TRUE', detail: '标题命中 2160p。' },
        ],
      },
      {
        id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, state: 'TRUE', reason: '促销与站点条件满足。', winner: false,
        conditions: [
          { id: 'promotion', label: '促销状态为 Freeleech', state: 'TRUE', detail: 'RSS 元数据标记为 Freeleech。' },
          { id: 'feed', label: 'RSS 源已启用', state: 'TRUE', detail: 'HDSky RSS 当前启用。' },
        ],
      },
      {
        id: 'rule-anime', name: '动画', priority: 40, revision: 1, state: 'FALSE', reason: '分类条件未满足。', winner: false,
        conditions: [{ id: 'category', label: '分类包含 Anime', state: 'FALSE', detail: 'RSS 项分类为 Movie。' }],
      },
    ],
    aggregation: {
      state: 'matched',
      matches: [
        { id: 'rule-4k', name: '4K 电影', priority: 100, revision: 3, reason: '优先级最高', winner: true },
        { id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, reason: '促销状态匹配', winner: false },
      ],
      target: 'qB-A · Profile 5.2.2',
      category: '电影 / 4K',
      tags: ['vertex-pro', '4k', 'freeleech'],
      savePathPolicy: '按分类使用子目录',
      sequentialDownload: true,
      participationWindowMinutes: 20,
      constraints: ['Profile 5.2.2', 'Storage: media-fast'],
      blockers: [],
    },
  },
  'rss-silo-free': {
    itemId: 'rss-silo-free',
    evaluatedAt: '2026-08-30T09:00:00Z',
    rules: [
      {
        id: 'rule-4k', name: '4K 电影', priority: 100, revision: 3, state: 'FALSE', reason: '标题与大小条件未满足。', winner: false,
        conditions: [
          { id: 'size', label: '大小不少于 20 GB', state: 'FALSE', detail: `当前大小 ${formatCapacityGB(7_516_192_768)}。` },
          { id: 'title', label: '标题包含 2160p', state: 'FALSE', detail: '标题只包含 1080p。' },
        ],
      },
      {
        id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, state: 'TRUE', reason: '促销状态满足，成为唯一命中规则。', winner: true,
        conditions: [{ id: 'promotion', label: '促销状态为 Freeleech', state: 'TRUE', detail: 'RSS 元数据标记为 Freeleech。' }],
      },
      {
        id: 'rule-anime', name: '动画', priority: 40, revision: 1, state: 'FALSE', reason: '分类条件未满足。', winner: false,
        conditions: [{ id: 'category', label: '分类包含 Anime', state: 'FALSE', detail: 'RSS 项分类为 TV。' }],
      },
    ],
    aggregation: {
      state: 'matched',
      matches: [{ id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, reason: '唯一命中规则', winner: true }],
      target: '高速组 → qB-B · Bulk',
      category: 'TV / Freeleech',
      tags: ['vertex-pro', 'freeleech'],
      savePathPolicy: '使用 qB 默认路径',
      sequentialDownload: false,
      participationWindowMinutes: 15,
      constraints: ['Profile 5.2.2', 'Storage: media-fast'],
      blockers: [],
    },
  },
  'rss-missing-size': {
    itemId: 'rss-missing-size',
    evaluatedAt: '2026-08-30T08:40:00Z',
    rules: [
      {
        id: 'rule-4k', name: '4K 电影', priority: 100, revision: 3, state: 'UNKNOWN', reason: '大小字段缺失，无法安全判定。', winner: false,
        conditions: [
          { id: 'size', label: '大小不少于 20 GB', state: 'UNKNOWN', detail: 'RSS 项没有提供大小。', missingField: 'size_bytes' },
          { id: 'title', label: '标题包含 2160p', state: 'FALSE', detail: '标题未命中 2160p。' },
        ],
      },
      {
        id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, state: 'FALSE', reason: '促销状态未满足。', winner: false,
        conditions: [{ id: 'promotion', label: '促销状态为 Freeleech', state: 'FALSE', detail: 'RSS 元数据未标记促销。' }],
      },
      {
        id: 'rule-anime', name: '动画', priority: 40, revision: 1, state: 'FALSE', reason: '分类条件未满足。', winner: false,
        conditions: [{ id: 'category', label: '分类包含 Anime', state: 'FALSE', detail: 'RSS 项分类为 Documentary。' }],
      },
    ],
    aggregation: {
      state: 'blocked',
      matches: [],
      target: null,
      category: null,
      tags: [],
      savePathPolicy: null,
      sequentialDownload: null,
      participationWindowMinutes: null,
      constraints: [],
      blockers: ['大小字段缺失，无法安全判定高优先级规则。'],
    },
  },
}

export const runLocalDryRun: DryRunExecutor = async (itemId) => {
  const result = localResults[itemId]
  if (!result) throw new Error('找不到所选 RSS 项。')
  return result
}
