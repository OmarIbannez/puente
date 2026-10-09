import { describe, expect, it } from 'vitest'

import { formatAgo, formatClabe, plural, shortHash, shortId } from '@/lib/format'
import { summarizeTicks } from '@/lib/ticks'
import type { TickReport } from '@/lib/api'

describe('format helpers', () => {
  it('shortens hashes and ids', () => {
    expect(shortHash('0x' + 'a'.repeat(64))).toBe('0xaaaaaaaa…aaaaaa')
    expect(shortHash('0x1234')).toBe('0x1234')
    expect(shortId('0b6f6a52-2f43-4d7c-9a8a-3f1b4c2d9e10')).toBe('0b6f6a52')
  })

  it('groups a CLABE and leaves anything else alone', () => {
    expect(formatClabe('012180015203300154')).toBe('012 180 01520330015 4')
    expect(formatClabe('123')).toBe('123')
  })

  it('says how long ago', () => {
    const now = Date.parse('2026-10-08T12:00:00Z')
    expect(formatAgo('2026-10-08T11:59:50Z', now)).toBe('10s ago')
    expect(formatAgo('2026-10-08T11:55:00Z', now)).toBe('5m ago')
    expect(formatAgo('2026-10-08T09:00:00Z', now)).toBe('3h ago')
    expect(formatAgo('not a date', now)).toBe('not a date')
  })

  it('pluralizes', () => {
    expect(plural(1, 'tick')).toBe('1 tick')
    expect(plural(5, 'tick')).toBe('5 ticks')
    expect(plural(2, 'entry', 'entries')).toBe('2 entries')
  })
})

describe('summarizeTicks', () => {
  const scan = { head: { number: 1, hash: '0x' }, from: 0, next: 1, fetched: 0, new: 0, revived: 0, confirmed: 0, orphaned: 0, windows: 1 }

  it('adds up what the ticks did', () => {
    const ticks: TickReport[] = [
      {
        deposits: { 'sim-usdc': { scan: { ...scan, new: 1 }, credit: { credited: 1, reversed: 0, unattributed: 0 } } },
        payouts: { processed: 2, changes: null },
      },
      {
        deposits: { 'sim-usdc': { scan, credit: { credited: 1, reversed: 1, unattributed: 0 } } },
        payouts: { processed: 0, changes: [{ payout_id: 'p', clave: 'c', from: 'submitted', to: 'liquidated' }] },
      },
    ]
    expect(summarizeTicks(ticks)).toBe(
      '2 deposits credited, 1 credit reversed, 2 payouts submitted to SPEI, 1 rail status change, 1 new chain log',
    )
  })

  it('says when nothing happened', () => {
    expect(summarizeTicks([{ deposits: {}, payouts: { processed: 0, changes: [] } }])).toBe('Nothing was waiting.')
  })
})
