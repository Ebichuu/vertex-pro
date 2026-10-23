import { BYTES_PER_GB } from '../shared/units'
import { fieldSpec, operatorValueType, type VertexCondition, type VertexValue, type VertexValueType } from './model'

const SIZE_ERROR = '大小需为非负十进制 GB 数值，须对应整数字节，且不超过 9007199.254740991 GB。'
const BYTE_SCALE = BigInt(BYTES_PER_GB)

export type SizeInputResult = { bytes: number; error?: never } | { error: string; bytes?: never }

// Convert the decimal text directly: floating-point multiplication can round away a byte.
export function parseGBInput(input: string): SizeInputResult {
  const text = input.trim()
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)$/.test(text)) return { error: SIZE_ERROR }
  const [integer = '', decimal = ''] = text.split('.')
  const whole = integer.replace(/^0+/, '') || '0'
  const fraction = decimal.replace(/0+$/, '')
  if (whole.length > 7 || fraction.length > 9) return { error: SIZE_ERROR }
  const bytes = BigInt(whole) * BYTE_SCALE + BigInt(fraction.padEnd(9, '0'))
  if (bytes > BigInt(Number.MAX_SAFE_INTEGER)) return { error: SIZE_ERROR }
  return { bytes: Number(bytes) }
}

export function bytesToGBInput(bytes: number | undefined): string {
  if (bytes === undefined || !Number.isSafeInteger(bytes) || bytes < 0) return ''
  const integer = BigInt(bytes)
  const fraction = String(integer % BYTE_SCALE).padStart(9, '0').replace(/0+$/, '')
  return `${integer / BYTE_SCALE}${fraction ? `.${fraction}` : ''}`
}

export function sizeValueInput(value: VertexValue | undefined): string {
  if (value?.type === 'number') return bytesToGBInput(value.number)
  return (value?.numbers ?? []).map(bytesToGBInput).join(', ')
}

export function parseSizeValueInput(input: string, type: VertexValueType): { value?: VertexValue; error?: string } {
  const parts = type === 'number' ? [input] : input.split(',')
  if (type === 'number_range' && parts.length !== 2) return { error: '大小区间需填写两个以逗号分隔的 GB 数值。' }
  const numbers: number[] = []
  for (const part of parts) {
    const parsed = parseGBInput(part)
    if (parsed.error !== undefined) return { error: parsed.error }
    numbers.push(parsed.bytes)
  }
  if (type === 'number_range' && numbers[0] > numbers[1]) return { error: '大小区间下限不能大于上限。' }
  return { value: type === 'number' ? { type, number: numbers[0] } : { type, numbers } }
}

export function validateSizeConditions(condition: VertexCondition): string | undefined {
  if (condition.kind === 'group') {
    for (const child of condition.children) {
      const error = validateSizeConditions(child)
      if (error) return error
    }
    return undefined
  }
  if (condition.field !== 'size' || condition.operator === 'exists' || condition.operator === 'not_exists') return undefined
  const expectedType = operatorValueType(fieldSpec('size'), condition.operator)
  const value = condition.value
  if (!value || value.type !== expectedType) return SIZE_ERROR
  const numbers = expectedType === 'number' ? [value.number] : value.numbers
  if (!numbers?.length || !numbers.every((number) => typeof number === 'number' && Number.isSafeInteger(number) && number >= 0)) return SIZE_ERROR
  if (expectedType === 'number_range' && (numbers.length !== 2 || numbers[0]! > numbers[1]!)) return '大小区间需填写两个从小到大排列的 GB 数值。'
  return undefined
}
