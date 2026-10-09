/**
 * The payout quote, mirroring internal/payout Config.Quote and
 * internal/money Convert/ApplyBps exactly: integer math, rounding toward
 * zero, rates as integers scaled by 1e6.
 */

import { SCALE, type Asset, type Money } from '@/lib/money'

/** Fixed-point denominator of a rate (money.RateScale). */
export const RATE_SCALE = 1_000_000n
const RATE_DECIMALS = 6

/**
 * Parses a rate such as "18.452300" (pesos per whole unit) into its
 * 1e6-scaled integer, like money.ParseRate. Null when malformed or ≤ 0.
 */
export function parseRate(input: string): bigint | null {
  const m = /^(\d*)(?:\.(\d*))?$/.exec(input.trim())
  if (!m) return null
  const [, whole = '', frac = ''] = m
  if ((whole === '' && frac === '') || frac.length > RATE_DECIMALS) return null
  const v = BigInt(`${whole === '' ? '0' : whole}${frac.padEnd(RATE_DECIMALS, '0')}`)
  return v > 0n ? v : null
}

/** a × bps / 10,000, rounded toward zero (money.ApplyBps). */
export function applyBps(minor: bigint, bps: number): bigint {
  return (minor * BigInt(bps)) / 10_000n
}

export interface Conversion {
  /** The converted amount in target minor units, rounded toward zero. */
  out: bigint
  /** What rounding dropped, in millionths of one target minor unit. */
  dust: bigint
}

/**
 * Converts `minor` of `from` into `to` at `rate` (target units per whole
 * source unit, 1e6-scaled), rounding toward zero (money.Convert).
 */
export function convert(minor: bigint, from: Asset, rate: bigint, to: Asset): Conversion {
  const num = minor * rate * 10n ** BigInt(SCALE[to])
  const den = 10n ** BigInt(SCALE[from]) * RATE_SCALE
  const rem = num % den
  return { out: num / den, dust: ((rem < 0n ? -rem : rem) * 1_000_000n) / den }
}

export interface Quote {
  /** What the beneficiary receives, in the source asset. */
  amount: Money
  /** feeBps of amount, rounded down. */
  fee: Money
  /** amount + fee: what is reserved from the customer. */
  total: Money
  /** Pesos sent over SPEI: amount at the rate, rounded down to the centavo. */
  mxn: Money
  /** Sub-centavo remainder that stays in FX clearing (3900), in millionths of a centavo. */
  dust: number
  rate: bigint
  feeBps: number
}

export type QuoteResult = { ok: true; quote: Quote } | { ok: false; error: string }

/** Prices a payout of `minor` units of `asset` (Config.Quote). */
export function quotePayout(asset: Asset, minor: number, rateText: string | undefined, feeBps: number): QuoteResult {
  if (asset === 'MXN') return { ok: false, error: 'Payouts are funded in a stablecoin' }
  const rate = rateText === undefined ? null : parseRate(rateText)
  if (rate === null) return { ok: false, error: `No ${asset} rate configured` }
  if (minor <= 0) return { ok: false, error: 'Amount must be positive' }
  if (feeBps < 0 || feeBps > 10_000) return { ok: false, error: 'Fee out of range' }
  const amount = BigInt(minor)
  const fee = applyBps(amount, feeBps)
  const conv = convert(amount, asset, rate, 'MXN')
  if (conv.out <= 0n) return { ok: false, error: 'Converts to less than one centavo' }
  return {
    ok: true,
    quote: {
      amount: { asset, minor },
      fee: { asset, minor: Number(fee) },
      total: { asset, minor: Number(amount + fee) },
      mxn: { asset: 'MXN', minor: Number(conv.out) },
      dust: Number(conv.dust),
      rate,
      feeBps,
    },
  }
}

/** "18.452300" style rate text for display: trailing zeros trimmed to 4 decimals minimum. */
export function formatRate(rate: bigint): string {
  const whole = rate / RATE_SCALE
  const frac = (rate % RATE_SCALE).toString().padStart(RATE_DECIMALS, '0').replace(/0{1,2}$/, '')
  return `${whole.toString()}.${frac}`
}

/** Basis points as a percent: 50 → "0.50%". */
export function formatBps(bps: number): string {
  const v = BigInt(bps)
  return `${(v / 100n).toString()}.${(v % 100n).toString().padStart(2, '0')}%`
}
