export type AggregationPreviewState = 'matched' | 'blocked' | 'empty'

export type RuleMatchPreview = {
  id: string
  name: string
  priority: number
  revision: number
  reason: string
  winner: boolean
}

export type AggregationPreviewData = {
  state: AggregationPreviewState
  matches: RuleMatchPreview[]
  target: string | null
  category: string | null
  tags: string[]
  savePathPolicy: string | null
  sequentialDownload: boolean | null
  participationWindowMinutes: number | null
  constraints: string[]
  blockers: string[]
}

export const sampleAggregationPreview: AggregationPreviewData = {
  state: 'matched',
  matches: [
    { id: 'rule-4k', name: '4K 电影', priority: 100, revision: 3, reason: '优先级最高', winner: true },
    { id: 'rule-free', name: 'Freeleech', priority: 60, revision: 2, reason: '促销状态匹配', winner: false },
  ],
  target: 'qB-A · Profile 5.2.2',
  category: '电影 / 4K',
  tags: ['vertex-pro', '4k', 'freeleech'],
  savePathPolicy: '按分类使用子目录',
  sequentialDownload: false,
  participationWindowMinutes: 20,
  constraints: ['Profile 5.2.2', 'Storage: media-fast'],
  blockers: [],
}
