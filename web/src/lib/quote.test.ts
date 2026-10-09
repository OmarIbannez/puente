import { describe, expect, it } from 'vitest'

import { applyBps, convert, formatBps, formatRate, parseRate, quotePayout } from '@/lib/quote'

describe('parseRate', () => {
  it('scales by 1e6 like money.ParseRate', () => {
    expect(parseRate('18.4523')).toBe(18_452_300n)
    expect(parseRate('18.452300')).toBe(18_452_300n)
    expect(parseRate('1')).toBe(1_000_000n)
  })

  it('rejects zero, negatives, junk and extra decimals', () => {
    for (const bad of ['', '0', '0.000000', '-1', 'x', '1.0000001']) {
      expect(parseRate(bad), bad).toBeNull()
    }
  })
})

describe('applyBps', () => {
  it('rounds toward zero', () => {
    expect(applyBps(12_345_678n, 50)).toBe(61_728n)
    expect(applyBps(199n, 50)).toBe(0n)
    expect(applyBps(-12_345_678n, 50)).toBe(-61_728n)
  })
})

describe('convert', () => {
  it('matches Go money.Convert: 12.345678 USDC at 18.4523 is 227.80 MXN', () => {
    expect(convert(12_345_678n, 'USDC', 18_452_300n, 'MXN')).toEqual({ out: 22_780n, dust: 615_415n })
  })

  it('leaves sub-centavo dust on MXNB at 1:1', () => {
    expect(convert(1_234_567n, 'MXNB', 1_000_000n, 'MXN')).toEqual({ out: 123n, dust: 456_700n })
  })

  it('rounds negatives toward zero too', () => {
    expect(convert(-12_345_678n, 'USDC', 18_452_300n, 'MXN').out).toBe(-22_780n)
  })
})

describe('quotePayout', () => {
  it('prices fee, total and pesos', () => {
    const r = quotePayout('USDC', 12_345_678, '18.452300', 50)
    expect(r).toEqual({
      ok: true,
      quote: {
        amount: { asset: 'USDC', minor: 12_345_678 },
        fee: { asset: 'USDC', minor: 61_728 },
        total: { asset: 'USDC', minor: 12_407_406 },
        mxn: { asset: 'MXN', minor: 22_780 },
        dust: 615_415,
        rate: 18_452_300n,
        feeBps: 50,
      },
    })
  })

  it('refuses what the server refuses', () => {
    expect(quotePayout('USDC', 0, '18.4523', 50)).toEqual({ ok: false, error: 'Amount must be positive' })
    expect(quotePayout('MXN', 100, '1', 50).ok).toBe(false)
    expect(quotePayout('MXNB', 1_000_000, undefined, 50)).toEqual({ ok: false, error: 'No MXNB rate configured' })
    expect(quotePayout('MXNB', 9_999, '1.0', 50)).toEqual({ ok: false, error: 'Converts to less than one centavo' })
  })
})

describe('display helpers', () => {
  it('formats rates and basis points', () => {
    expect(formatRate(18_452_300n)).toBe('18.4523')
    expect(formatRate(1_000_000n)).toBe('1.0000')
    expect(formatRate(17_123_456n)).toBe('17.123456')
    expect(formatBps(50)).toBe('0.50%')
    expect(formatBps(1_250)).toBe('12.50%')
  })
})
