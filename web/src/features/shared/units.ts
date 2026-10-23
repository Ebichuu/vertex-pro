export const BYTES_PER_GB = 1_000_000_000
export const BYTES_PER_MB = 1_000_000
export const BYTES_PER_TB = 1_000_000_000_000

function formatDecimalUnit(value: number | null | undefined, divisor: number, unit: string, unknownLabel: string): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value < 0) return unknownLabel
  const amount = value / divisor
  if (amount > 0 && amount < 0.01) return `<0.01 ${unit}`
  return `${amount.toFixed(2)} ${unit}`
}

export function formatCapacityGB(bytes: number | null | undefined, unknownLabel = '未知'): string {
  return formatDecimalUnit(bytes, BYTES_PER_GB, 'GB', unknownLabel)
}

export function formatTerabytes(bytes: number | null | undefined, unknownLabel = '未知'): string {
  return formatDecimalUnit(bytes, BYTES_PER_TB, 'TB', unknownLabel)
}

export function formatRateMBps(bytesPerSecond: number | null | undefined, unknownLabel = '未知'): string {
  return formatDecimalUnit(bytesPerSecond, BYTES_PER_MB, 'MB/s', unknownLabel)
}
