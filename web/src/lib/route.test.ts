import { describe, expect, it } from 'vitest'

import { parseHash, routeHref, type Route } from '@/lib/route'

describe('parseHash', () => {
  it('reads every top-level page', () => {
    for (const name of ['overview', 'deposits', 'payouts', 'ledger', 'bank', 'recon', 'live'] as const) {
      expect(parseHash(`#/${name}`)).toEqual({ name })
    }
  })

  it('reads a payout detail', () => {
    expect(parseHash('#/payouts/0b6f6a52-2f43-4d7c-9a8a-3f1b4c2d9e10')).toEqual({
      name: 'payout',
      id: '0b6f6a52-2f43-4d7c-9a8a-3f1b4c2d9e10',
    })
  })

  it('reads the journal filter and a recon run', () => {
    expect(parseHash('#/ledger?kind=payout_submit')).toEqual({ name: 'ledger', kind: 'payout_submit' })
    expect(parseHash('#/recon/42')).toEqual({ name: 'recon', runId: 42 })
  })

  it('ignores a malformed run id', () => {
    expect(parseHash('#/recon/abc')).toEqual({ name: 'recon' })
    expect(parseHash('#/recon/0')).toEqual({ name: 'recon' })
  })

  it('tolerates a missing slash and returns null for unknown or empty hashes', () => {
    expect(parseHash('#deposits')).toEqual({ name: 'deposits' })
    expect(parseHash('')).toBeNull()
    expect(parseHash('#/')).toBeNull()
    expect(parseHash('#/nope')).toBeNull()
  })
})

describe('routeHref', () => {
  it('round-trips through parseHash', () => {
    const routes: Route[] = [
      { name: 'overview' },
      { name: 'payout', id: 'a/b c' },
      { name: 'ledger', kind: 'deposit' },
      { name: 'ledger' },
      { name: 'recon', runId: 7 },
      { name: 'recon' },
      { name: 'live' },
    ]
    for (const r of routes) expect(parseHash(routeHref(r))).toEqual(r)
  })
})
