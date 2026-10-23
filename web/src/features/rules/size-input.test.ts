import { describe, expect, it } from 'vitest'
import { bytesToGBInput, parseGBInput, parseSizeValueInput, validateSizeConditions } from './size-input'

describe('GB size inputs', () => {
  it.each([
    ['1.5', 1_500_000_000], ['1.073741824', 1_073_741_824], ['0.000000001', 1],
    ['1.0000000010', 1_000_000_001], ['.5', 500_000_000], ['0', 0],
    ['9007199.254740991', Number.MAX_SAFE_INTEGER],
  ])('converts %s GB to exact integer bytes', (input, bytes) => {
    expect(parseGBInput(input)).toEqual({ bytes })
    expect(parseGBInput(bytesToGBInput(bytes))).toEqual({ bytes })
  })

  it.each(['', ' ', '-1', 'NaN', 'Infinity', 'abc', '0x10', '0.0000000001', '9007199.254740992', '1.0000000001'])('rejects invalid GB input %j without rounding', (input) => {
    expect(parseGBInput(input).error).toContain('整数字节')
  })

  it('formats the original byte count without floating-point loss or display rounding', () => {
    expect(bytesToGBInput(1_073_741_824)).toBe('1.073741824')
    expect(bytesToGBInput(Number.MAX_SAFE_INTEGER)).toBe('9007199.254740991')
  })

  it('parses exact ranges and sets and rejects missing or inverted endpoints', () => {
    expect(parseSizeValueInput('1, 10', 'number_range').value).toEqual({ type: 'number_range', numbers: [1_000_000_000, 10_000_000_000] })
    expect(parseSizeValueInput('1, 1.5, 10', 'number_set').value).toEqual({ type: 'number_set', numbers: [1_000_000_000, 1_500_000_000, 10_000_000_000] })
    for (const input of ['1', '1,', ',10', '10,1', '1,2,3', '1,-10']) expect(parseSizeValueInput(input, 'number_range').error).toBeDefined()
    expect(parseSizeValueInput('1,,10', 'number_set').error).toBeDefined()
  })

  it('validates nested size conditions but permits absent values for existence operators', () => {
    expect(validateSizeConditions({ kind: 'group', operator: 'or', children: [{ kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number' } }] })).toContain('整数字节')
    for (const number of [-1, Number.MAX_SAFE_INTEGER + 1, Infinity, NaN, 0.5]) expect(validateSizeConditions({ kind: 'leaf', field: 'size', operator: 'equals', value: { type: 'number', number } })).toBeDefined()
    expect(validateSizeConditions({ kind: 'leaf', field: 'size', operator: 'exists' })).toBeUndefined()
  })
})
