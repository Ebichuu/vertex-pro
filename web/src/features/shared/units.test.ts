import { describe, expect, it } from 'vitest'
import { BYTES_PER_GB, BYTES_PER_MB, BYTES_PER_TB, formatCapacityGB, formatRateMBps, formatTerabytes } from './units'

describe('decimal display units', () => {
  it('uses decimal GB for torrent sizes', () => {
    expect(formatCapacityGB(BYTES_PER_GB)).toBe('1.00 GB')
    expect(formatCapacityGB(1024 ** 3)).toBe('1.07 GB')
    expect(formatCapacityGB(0)).toBe('0.00 GB')
    expect(formatCapacityGB(1)).toBe('<0.01 GB')
    expect(formatCapacityGB(9_999_999)).toBe('<0.01 GB')
  })

  it('uses decimal TB for disk capacity and cumulative traffic', () => {
    expect(formatTerabytes(BYTES_PER_TB)).toBe('1.00 TB')
    expect(formatTerabytes(1024 ** 4)).toBe('1.10 TB')
    expect(formatTerabytes(0)).toBe('0.00 TB')
    expect(formatTerabytes(1)).toBe('<0.01 TB')
    expect(formatTerabytes(9_999_999_999)).toBe('<0.01 TB')
  })

  it('uses decimal MB/s for upload and download rates', () => {
    expect(formatRateMBps(100 * BYTES_PER_MB)).toBe('100.00 MB/s')
    expect(formatRateMBps(1024 ** 2)).toBe('1.05 MB/s')
    expect(formatRateMBps(0)).toBe('0.00 MB/s')
    expect(formatRateMBps(1)).toBe('<0.01 MB/s')
  })

  it('keeps missing, invalid and negative measurements unknown', () => {
    for (const value of [undefined, null, -1, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(formatCapacityGB(value)).toBe('未知')
      expect(formatTerabytes(value)).toBe('未知')
      expect(formatRateMBps(value, '暂无数据')).toBe('暂无数据')
    }
  })
})
