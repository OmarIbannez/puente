import { describe, expect, it } from 'vitest'

import { formatMoney, formatShort, isTruncated, parseMoney, sumMinor, toDecimal } from '@/lib/money'

describe('toDecimal', () => {
  it('writes the full scale of each asset', () => {
    expect(toDecimal({ asset: 'USDC', minor: 12_500_000 })).toBe('12.500000')
    expect(toDecimal({ asset: 'MXNB', minor: 1 })).toBe('0.000001')
    expect(toDecimal({ asset: 'MXN', minor: 123_456 })).toBe('1234.56')
  })

  it('keeps the sign on small negatives', () => {
    expect(toDecimal({ asset: 'MXN', minor: -5 })).toBe('-0.05')
    expect(toDecimal({ asset: 'USDC', minor: -1 })).toBe('-0.000001')
    expect(toDecimal({ asset: 'MXN', minor: 0 })).toBe('0.00')
  })
})

describe('formatMoney', () => {
  it('formats pesos like Go money.Amount.String', () => {
    expect(formatMoney({ asset: 'MXN', minor: 123_456 })).toBe('$1,234.56 MXN')
    expect(formatMoney({ asset: 'MXN', minor: -5_000 })).toBe('-$50.00 MXN')
    expect(formatMoney({ asset: 'MXN', minor: 0 })).toBe('$0.00 MXN')
    expect(formatMoney({ asset: 'MXN', minor: 100_000_000_00 })).toBe('$100,000,000.00 MXN')
  })

  it('groups stablecoins and shows the full scale by default', () => {
    expect(formatMoney({ asset: 'USDC', minor: 1_250_000_000 })).toBe('1,250.000000 USDC')
    expect(formatMoney({ asset: 'MXNB', minor: 1_234_567_890_123 })).toBe('1,234,567.890123 MXNB')
    expect(formatMoney({ asset: 'USDC', minor: -999_999 })).toBe('-0.999999 USDC')
  })

  it('rounds half away from zero when showing fewer decimals', () => {
    expect(formatShort({ asset: 'USDC', minor: 1_250_000_000 })).toBe('1,250.00 USDC')
    expect(formatShort({ asset: 'USDC', minor: 1_234_994_999 })).toBe('1,234.99 USDC')
    expect(formatShort({ asset: 'USDC', minor: 1_234_995_000 })).toBe('1,235.00 USDC')
    expect(formatShort({ asset: 'USDC', minor: -1_005_000 })).toBe('-1.01 USDC')
    expect(formatShort({ asset: 'MXN', minor: 123_456 })).toBe('$1,234.56 MXN')
  })

  it('drops the sign when a negative rounds to zero', () => {
    expect(formatShort({ asset: 'USDC', minor: -4_000 })).toBe('0.00 USDC')
  })

  it('can leave off the asset code', () => {
    expect(formatMoney({ asset: 'MXN', minor: 99 }, { bare: true })).toBe('$0.99')
    expect(formatShort({ asset: 'MXNB', minor: 2_000_000 }, { bare: true })).toBe('2.00')
  })

  it('stays exact beyond float precision of the decimal value', () => {
    // 9,007,199.254740991 USDC: the decimal has more digits than a double can hold.
    expect(formatMoney({ asset: 'USDC', minor: Number.MAX_SAFE_INTEGER })).toBe('9,007,199,254.740991 USDC')
  })
})

describe('isTruncated', () => {
  it('flags stablecoin amounts with digits past the cents', () => {
    expect(isTruncated({ asset: 'USDC', minor: 1_250_000_000 })).toBe(false)
    expect(isTruncated({ asset: 'USDC', minor: 1_250_000_001 })).toBe(true)
    expect(isTruncated({ asset: 'MXN', minor: 1 })).toBe(false)
  })
})

describe('parseMoney', () => {
  it('reads plain and grouped decimals at each scale', () => {
    expect(parseMoney('USDC', '25')).toEqual({ ok: true, minor: 25_000_000, decimal: '25.000000' })
    expect(parseMoney('USDC', '1,250.5')).toEqual({ ok: true, minor: 1_250_500_000, decimal: '1250.500000' })
    expect(parseMoney('MXNB', '.000001')).toEqual({ ok: true, minor: 1, decimal: '0.000001' })
    expect(parseMoney('MXN', '$1,234.56')).toEqual({ ok: true, minor: 123_456, decimal: '1234.56' })
  })

  it('reads signs', () => {
    expect(parseMoney('MXN', '-0.05')).toMatchObject({ ok: true, minor: -5 })
    expect(parseMoney('USDC', '+3')).toMatchObject({ ok: true, minor: 3_000_000 })
  })

  it('rejects more decimals than the asset holds instead of rounding', () => {
    expect(parseMoney('MXN', '1.005')).toEqual({ ok: false, error: 'MXN has at most 2 decimals' })
    expect(parseMoney('USDC', '0.0000001')).toMatchObject({ ok: false })
  })

  it('rejects malformed input', () => {
    for (const bad of ['', '.', '-', '5.', 'abc', '1.2.3', '1e6', '--1']) {
      expect(parseMoney('USDC', bad).ok, bad).toBe(false)
    }
  })

  it('rejects amounts outside the safe integer range', () => {
    expect(parseMoney('USDC', '99999999999')).toEqual({ ok: false, error: 'Amount is too large' })
  })
})

describe('sumMinor', () => {
  it('adds exactly', () => {
    expect(
      sumMinor([
        { asset: 'USDC', minor: 1 },
        { asset: 'USDC', minor: -3 },
        { asset: 'USDC', minor: 10 },
      ]),
    ).toBe(8)
  })
})
