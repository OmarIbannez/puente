/**
 * Money, exactly as internal/money keeps it: an asset plus an integer count
 * of minor units. USDC and MXNB have 6 decimals, MXN (centavos) has 2.
 *
 * Every function here works on BigInt, so formatting, parsing and rounding
 * never touch a float.
 */

export type Asset = 'USDC' | 'MXNB' | 'MXN'
export type Stablecoin = Exclude<Asset, 'MXN'>

export const ASSETS: readonly Asset[] = ['USDC', 'MXNB', 'MXN']
export const STABLECOINS: readonly Stablecoin[] = ['USDC', 'MXNB']

/** An amount as the API sends it: `{"asset":"USDC","minor":1250000000}`. */
export interface Money {
  asset: Asset
  minor: number
}

export const SCALE: Readonly<Record<Asset, number>> = { USDC: 6, MXNB: 6, MXN: 2 }

/** The number of decimals shown in tables before the tooltip takes over. */
export const DISPLAY_DECIMALS = 2

export function isAsset(value: string): value is Asset {
  return value === 'USDC' || value === 'MXNB' || value === 'MXN'
}

function pow10(n: number): bigint {
  return 10n ** BigInt(n)
}

function groupThousands(digits: string): string {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

/** Splits |minor| into whole and fractional digit strings at `scale`. */
function split(abs: bigint, scale: number): [string, string] {
  if (scale === 0) return [abs.toString(), '']
  const unit = pow10(scale)
  return [(abs / unit).toString(), (abs % unit).toString().padStart(scale, '0')]
}

/**
 * Plain decimal with the asset's full scale and no grouping, as the API
 * accepts it: (USDC, 12500000) → "12.500000", (MXN, -5) → "-0.05".
 */
export function toDecimal(m: Money): string {
  const v = BigInt(m.minor)
  const [whole, frac] = split(v < 0n ? -v : v, SCALE[m.asset])
  return `${v < 0n ? '-' : ''}${whole}.${frac}`
}

/** Rounds |minor| at `scale` to `decimals`, half away from zero. */
function roundTo(abs: bigint, scale: number, decimals: number): bigint {
  if (decimals >= scale) return abs * pow10(decimals - scale)
  const div = pow10(scale - decimals)
  const q = abs / div
  return abs % div >= div / 2n ? q + 1n : q
}

export interface FormatOptions {
  /** Decimals to show; defaults to the asset's full scale. */
  decimals?: number
  /** Leave off the asset code. */
  bare?: boolean
}

/**
 * Formats an amount for people, grouped in thousands:
 * - MXN: "$1,234.56 MXN", "-$50.00 MXN" (as Go's money.Amount.String);
 * - stablecoins: "1,250.000000 USDC", or "1,250.00 USDC" with decimals: 2.
 * Fewer decimals than the scale rounds half away from zero.
 */
export function formatMoney(m: Money, options: FormatOptions = {}): string {
  const scale = SCALE[m.asset]
  const decimals = options.decimals ?? scale
  const v = BigInt(m.minor)
  const abs = roundTo(v < 0n ? -v : v, scale, decimals)
  const [whole, frac] = split(abs, decimals)
  const negative = v < 0n && abs !== 0n
  const number = `${groupThousands(whole)}${frac === '' ? '' : `.${frac}`}`
  const sign = negative ? '-' : ''
  if (m.asset === 'MXN') return `${sign}$${number}${options.bare ? '' : ' MXN'}`
  return `${sign}${number}${options.bare ? '' : ` ${m.asset}`}`
}

/** The short form shown in tables: two decimals for every asset. */
export function formatShort(m: Money, options: Omit<FormatOptions, 'decimals'> = {}): string {
  return formatMoney(m, { ...options, decimals: DISPLAY_DECIMALS })
}

/** True when formatShort drops nonzero digits, so the full value needs a tooltip. */
export function isTruncated(m: Money): boolean {
  const scale = SCALE[m.asset]
  if (scale <= DISPLAY_DECIMALS) return false
  return BigInt(m.minor) % pow10(scale - DISPLAY_DECIMALS) !== 0n
}

export type ParseResult = { ok: true; minor: number; decimal: string } | { ok: false; error: string }

/**
 * Reads a user-typed amount of `asset`, like Go's money.Parse: an optional
 * sign, digits, an optional dot and at most the asset's scale of decimals.
 * Thousands commas and spaces are ignored; more decimals than the scale is
 * an error, never a silent rounding.
 */
export function parseMoney(asset: Asset, input: string): ParseResult {
  const scale = SCALE[asset]
  let s = input.replace(/[,\s]/g, '')
  if (asset === 'MXN') s = s.replace(/^([+-]?)\$/, '$1')
  const m = /^([+-]?)(\d*)(?:\.(\d*))?$/.exec(s)
  if (!m || s === '' || ((m[2] ?? '') === '' && (m[3] ?? '') === '')) {
    return { ok: false, error: 'Enter a number' }
  }
  const [, sign = '', whole = '', frac] = m
  if (frac === '') return { ok: false, error: 'Enter digits after the decimal point' }
  const fracDigits = frac ?? ''
  if (fracDigits.length > scale) {
    return { ok: false, error: `${asset} has at most ${String(scale)} decimals` }
  }
  let minor = BigInt(`${whole === '' ? '0' : whole}${fracDigits.padEnd(scale, '0')}`)
  if (sign === '-') minor = -minor
  if (minor > BigInt(Number.MAX_SAFE_INTEGER) || minor < -BigInt(Number.MAX_SAFE_INTEGER)) {
    return { ok: false, error: 'Amount is too large' }
  }
  const value = Number(minor)
  return { ok: true, minor: value, decimal: toDecimal({ asset, minor: value }) }
}

/** Sums amounts of one asset. */
export function sumMinor(amounts: readonly Money[]): number {
  return Number(amounts.reduce((acc, a) => acc + BigInt(a.minor), 0n))
}
