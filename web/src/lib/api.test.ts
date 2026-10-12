import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  ApiError,
  cancelPayout,
  createPayout,
  createPayoutRaw,
  getLatestRecon,
  getOverview,
  IDEMPOTENCY_HEADER,
  parseApiError,
  REPLAY_HEADER,
  runRecon,
  setFaults,
  simulateDeposit,
  toApiError,
  NO_FAULTS,
  type CreatePayoutInput,
  isFinalClientError,
} from '@/lib/api'

function mockFetch(status: number, body: unknown, headers: Record<string, string> = {}) {
  const fn = vi.fn<(url: string, init: RequestInit) => Promise<Response>>(() =>
    Promise.resolve(
      new Response(body === null ? null : JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json', ...headers },
      }),
    ),
  )
  vi.stubGlobal('fetch', fn)
  return fn
}

function callOf(fn: ReturnType<typeof mockFetch>, call = 0): [string, RequestInit] {
  const c = fn.mock.calls[call]
  if (!c) throw new Error(`no fetch call ${String(call)}`)
  return c
}

function headersOf(fn: ReturnType<typeof mockFetch>, call = 0): Record<string, string> {
  return callOf(fn, call)[1].headers as Record<string, string>
}

const PAYOUT_INPUT: CreatePayoutInput = {
  customer_id: 'c-1',
  asset: 'USDC',
  amount: '100',
  beneficiary_clabe: '002010077777777771',
  beneficiary_name: 'Ana López',
  concepto: 'Renta',
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('parseApiError', () => {
  it('reads the error envelope', () => {
    const err = parseApiError(422, { error: { code: 'INSUFFICIENT_FUNDS', message: 'available 1.00 USDC' } })
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(422)
    expect(err.code).toBe('INSUFFICIENT_FUNDS')
    expect(err.message).toBe('available 1.00 USDC')
  })

  it('falls back to the code when the message is empty', () => {
    expect(parseApiError(409, { error: { code: 'CONFLICT', message: '' } }).message).toBe('CONFLICT')
  })

  it('handles bodies without an envelope', () => {
    expect(parseApiError(502, '<html>Bad gateway</html>', 'Bad Gateway')).toMatchObject({
      status: 502,
      code: 'SERVER_ERROR',
      message: 'Bad Gateway',
    })
    expect(parseApiError(404, null)).toMatchObject({ code: 'HTTP_ERROR', message: 'Request failed with status 404' })
    expect(parseApiError(400, { error: 'nope' }).code).toBe('HTTP_ERROR')
  })
})

describe('toApiError', () => {
  it('wraps network failures', () => {
    expect(toApiError(new TypeError('Failed to fetch'))).toMatchObject({ status: 0, code: 'NETWORK_ERROR' })
    const original = new ApiError(404, 'NOT_FOUND', 'x')
    expect(toApiError(original)).toBe(original)
  })
})

describe('request', () => {
  it('rejects with the server error on non-2xx', async () => {
    mockFetch(422, { error: { code: 'VALIDATION', message: 'amount must be positive' } })
    await expect(getOverview()).rejects.toMatchObject({ status: 422, code: 'VALIDATION', message: 'amount must be positive' })
  })

  it('sends no Idempotency-Key on reads and non-money writes', async () => {
    const fn = mockFetch(200, {})
    await getOverview()
    await setFaults(NO_FAULTS)
    await runRecon()
    for (const call of [0, 1, 2]) expect(headersOf(fn, call)[IDEMPOTENCY_HEADER]).toBeUndefined()
    expect(callOf(fn, 1)[1].method).toBe('PUT')
    expect(callOf(fn, 1)[1].body).toBe(JSON.stringify({ faults: NO_FAULTS }))
  })

  it('sends the caller key on every money-moving POST', async () => {
    const fn = mockFetch(201, {})
    await createPayout(PAYOUT_INPUT, 'key-payout')
    await simulateDeposit({ customer_id: 'c-1', asset: 'MXNB', amount: '5' }, 'key-deposit')
    await cancelPayout('p/1', 'key-cancel')
    expect(headersOf(fn, 0)[IDEMPOTENCY_HEADER]).toBe('key-payout')
    expect(headersOf(fn, 1)[IDEMPOTENCY_HEADER]).toBe('key-deposit')
    expect(headersOf(fn, 2)[IDEMPOTENCY_HEADER]).toBe('key-cancel')
    expect(callOf(fn, 2)[0]).toBe('/api/payouts/p%2F1/cancel')
  })
})

describe('createPayoutRaw', () => {
  it('reports the replay header and the key it sent', async () => {
    mockFetch(201, { id: 'p-1' }, { [REPLAY_HEADER]: 'true' })
    const res = await createPayoutRaw(PAYOUT_INPUT, 'same-key')
    expect(res).toEqual({ status: 201, ok: true, replay: true, key: 'same-key', body: { id: 'p-1' } })
  })

  it('returns error responses instead of throwing', async () => {
    mockFetch(409, { error: { code: 'IDEMPOTENCY_IN_PROGRESS', message: 'still processing' } })
    const res = await createPayoutRaw(PAYOUT_INPUT, 'k')
    expect(res.ok).toBe(false)
    expect(res.replay).toBe(false)
    expect(parseApiError(res.status, res.body).code).toBe('IDEMPOTENCY_IN_PROGRESS')
  })
})

describe('getLatestRecon', () => {
  it('maps the 404 of a never-reconciled sandbox to null', async () => {
    mockFetch(404, { error: { code: 'NOT_FOUND', message: 'recon: no runs' } })
    await expect(getLatestRecon()).resolves.toBeNull()
  })

  it('still rejects other errors', async () => {
    mockFetch(500, { error: { code: 'INTERNAL', message: 'internal server error' } })
    await expect(getLatestRecon()).rejects.toMatchObject({ code: 'INTERNAL' })
  })
})

describe('isFinalClientError', () => {
  it('is true for stored 4xx outcomes and false for 409, 5xx and network errors', () => {
    expect(isFinalClientError(new ApiError(422, 'INSUFFICIENT_FUNDS', 'x'))).toBe(true)
    expect(isFinalClientError(new ApiError(404, 'NOT_FOUND', 'x'))).toBe(true)
    expect(isFinalClientError(new ApiError(409, 'IDEMPOTENCY_IN_PROGRESS', 'x'))).toBe(false)
    expect(isFinalClientError(new ApiError(503, 'UNAVAILABLE', 'x'))).toBe(false)
    expect(isFinalClientError(new ApiError(0, 'NETWORK', 'x'))).toBe(false)
  })
})
