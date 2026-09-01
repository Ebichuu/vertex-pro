import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { RuleAggregationPreview } from './RuleAggregationPreview'
import { sampleAggregationPreview } from '../../features/rules/aggregation-preview'

afterEach(cleanup)

describe('RuleAggregationPreview', () => {
  it('shows race status and compact aggregation summary', () => {
    render(<RuleAggregationPreview data={sampleAggregationPreview} />)
    expect(screen.getByRole('heading', { name: '竞速投递' })).toBeInTheDocument()
    expect(screen.getAllByText('命中规则')).toHaveLength(2)
    expect(screen.getByText(/qB-A.*Profile 5\.2\.2/)).toBeInTheDocument()
    expect(screen.getByText('vertex-pro')).toBeInTheDocument()
    expect(screen.getByText('按分类使用子目录')).toBeInTheDocument()
    expect(screen.getByText('20 分钟')).toBeInTheDocument()
    expect(screen.getByText('4K 电影')).toBeInTheDocument()
    expect(screen.getByText('Freeleech')).toBeInTheDocument()
    expect(screen.getAllByText('Winner')).toHaveLength(2)
  })

  it('shows fail-closed blockers instead of pretending delivery is available', () => {
    render(<RuleAggregationPreview data={{ ...sampleAggregationPreview, state: 'blocked', blockers: ['固定 qB-A 当前不可用', 'Storage 约束没有交集'] }} />)
    expect(screen.getByRole('alert')).toHaveTextContent('固定 qB-A 当前不可用')
    expect(screen.getByRole('alert')).toHaveTextContent('Storage 约束没有交集')
    expect(screen.getByText('已阻止')).toBeInTheDocument()
  })
})
