/**
 * Puente HTTP API: every request/response type and fetch function lives in
 * this one file so it can be checked against internal/api in one place.
 *
 * Conventions (internal/api):
 * - money is `{asset, minor}` with integer minor units (see lib/money);
 * - timestamps are RFC 3339;
 * - errors are `{"error": {"code", "message"}}`;
 * - the sandbox is chosen by the `puente_sandbox` cookie (same origin);
 * - money-moving POSTs (deposits/simulate, payouts, payouts/{id}/cancel)
 *   carry an `Idempotency-Key`; a replayed response has
 *   `Idempotent-Replay: true`.
 */

import type { Asset, Money, Stablecoin } from '@/lib/money'

/* ------------------------------------------------------------------------ */
/* Errors                                                                    */
/* ------------------------------------------------------------------------ */

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Turns an error response body into a typed ApiError. Never throws. */
export function parseApiError(status: number, body: unknown, statusText = ''): ApiError {
  if (isObject(body) && isObject(body.error)) {
    const { code, message } = body.error
    if (typeof code === 'string' && code !== '') {
      return new ApiError(status, code, typeof message === 'string' && message !== '' ? message : code)
    }
  }
  const fallback = statusText !== '' ? statusText : `Request failed with status ${String(status)}`
  return new ApiError(status, status >= 500 ? 'SERVER_ERROR' : 'HTTP_ERROR', fallback)
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(error: unknown): ApiError {
  if (error instanceof ApiError) return error
  const message = error instanceof Error ? error.message : String(error)
  return new ApiError(0, 'NETWORK_ERROR', message === '' ? 'Network error' : message)
}

/* ------------------------------------------------------------------------ */
/* Transport                                                                 */
/* ------------------------------------------------------------------------ */

export const IDEMPOTENCY_HEADER = 'Idempotency-Key'
export const REPLAY_HEADER = 'Idempotent-Replay'

type Method = 'GET' | 'POST' | 'PUT'

interface RequestOptions {
  method?: Method
  body?: unknown
  /** Sends an Idempotency-Key: `idempotencyKey`, or a fresh UUID. */
  idempotent?: boolean
  idempotencyKey?: string
}

export function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

/** A response with what the UI shows about the exchange itself. */
export interface RawResponse {
  status: number
  ok: boolean
  /** True when the server replayed a stored response for the key. */
  replay: boolean
  /** The key that was sent, if any. */
  key: string | undefined
  body: unknown
}

async function readJson(res: Response): Promise<unknown> {
  const text = await res.text()
  if (text === '') return null
  try {
    return JSON.parse(text) as unknown
  } catch {
    return text
  }
}

/** Sends a request and returns the response whatever its status. Throws only on network failure. */
export async function requestRaw(path: string, options: RequestOptions = {}): Promise<RawResponse> {
  const { method = 'GET', body, idempotent = false, idempotencyKey } = options
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const key = idempotent ? (idempotencyKey ?? newIdempotencyKey()) : undefined
  if (key !== undefined) headers[IDEMPOTENCY_HEADER] = key

  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return {
    status: res.status,
    ok: res.ok,
    replay: res.headers.get(REPLAY_HEADER)?.toLowerCase() === 'true',
    key,
    body: await readJson(res),
  }
}

/** Sends a request; resolves with the JSON body on 2xx, rejects with an ApiError otherwise. */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const res = await requestRaw(path, options)
  if (!res.ok) throw parseApiError(res.status, res.body)
  return res.body as T
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v))
  }
  const s = q.toString()
  return s === '' ? '' : `?${s}`
}

/* ------------------------------------------------------------------------ */
/* Health, networks, sandbox                                                 */
/* ------------------------------------------------------------------------ */

export interface Health {
  status: 'ok' | 'degraded'
  version: string
  db: string
  live: boolean
}

/** chain.Network */
export interface Network {
  key: string
  name: string
  chain_id: number
  asset: Stablecoin
  token: string
  /** fmt pattern with one %s for the tx hash, e.g. "https://sepolia.basescan.org/tx/%s". */
  explorer_tx_url?: string
  simulated: boolean
}

/** store.Faults: each is consumed by the one event it affects. */
export interface Faults {
  duplicate_log: boolean
  short_deposit: boolean
  reorg: boolean
  rail_timeout: boolean
  rail_reject: boolean
  rail_return: boolean
}

export type FaultName = keyof Faults

export const NO_FAULTS: Faults = {
  duplicate_log: false,
  short_deposit: false,
  reorg: false,
  rail_timeout: false,
  rail_reject: false,
  rail_return: false,
}

export interface SandboxInfo {
  id: string
  faults: Faults
  networks: Network[]
  /** MXN per whole unit, 6-decimal strings, e.g. {"USDC": "18.452300"}. */
  rates: Partial<Record<Asset, string>>
  fee_bps: number
}

export const getHealth = () => request<Health>('/api/health')
export const getNetworks = () => request<{ networks: Network[] }>('/api/networks').then((r) => r.networks)
export const getSandbox = () => request<SandboxInfo>('/api/sandbox')
export const resetSandbox = () => request<SandboxInfo>('/api/sandbox/reset', { method: 'POST' })
export const setFaults = (faults: Faults) => request<SandboxInfo>('/api/sandbox/faults', { method: 'PUT', body: { faults } })

/** payout.Change */
export interface PayoutChange {
  payout_id: string
  clave: string
  from: PayoutStatus
  to: PayoutStatus
}

/** chain.ScanResult */
export interface ScanResult {
  head: { number: number; hash: string }
  from: number
  next: number
  fetched: number
  new: number
  revived: number
  confirmed: number
  orphaned: number
  windows: number
}

/** engine.TickReport */
export interface TickReport {
  deposits: Record<string, { scan: ScanResult; credit: { credited: number; reversed: number; unattributed: number } }>
  payouts: { processed: number; changes: PayoutChange[] | null }
}

export const advance = (ticks: number) =>
  request<{ ticks: TickReport[] }>('/api/sandbox/advance', { method: 'POST', body: { ticks } }).then((r) => r.ticks)

export type InjectKind = 'bank_fee' | 'manual_order'

/** Books something only the bank knows about. `amount` is MXN, e.g. "125.00" (the server's default). */
export const inject = (kind: InjectKind, amount?: string) =>
  request<unknown>('/api/sandbox/inject', { method: 'POST', body: amount === undefined ? { kind } : { kind, amount } })

/* ------------------------------------------------------------------------ */
/* Ledger                                                                    */
/* ------------------------------------------------------------------------ */

export type AccountType = 'asset' | 'liability' | 'equity' | 'revenue'

/** ledger.AccountInfo */
export interface AccountInfo {
  code: number
  name: string
  type: AccountType
  assets: Asset[]
  /** A customer sub-ledger: every posting names the customer (party). */
  per_party: boolean
}

/** ledger.AccountBalance */
export interface AccountBalance extends AccountInfo {
  /** Raw signed sum (debits positive). */
  net: Money
  /** In the account's normal-balance sign: positive is a normal balance. */
  balance: Money
}

/** ledger.Posting: debits positive, credits negative. */
export interface Posting {
  account: number
  amount: Money
  /** Customer id on sub-ledger postings. */
  party?: string
}

export type EntryKind =
  | 'funding'
  | 'deposit'
  | 'deposit_reversal'
  | 'payout_reserve'
  | 'payout_submit'
  | 'payout_settle'
  | 'payout_release'
  | 'payout_return'

export const ENTRY_KINDS: readonly EntryKind[] = [
  'funding',
  'deposit',
  'deposit_reversal',
  'payout_reserve',
  'payout_submit',
  'payout_settle',
  'payout_release',
  'payout_return',
]

/** ledger.PostedEntry */
export interface JournalEntry {
  id: number
  ref: string
  kind: EntryKind
  memo: string
  subject?: { type: string; id: string }
  created_at: string
  postings: Posting[]
}

export const getAccounts = () => request<{ accounts: AccountInfo[] }>('/api/ledger/accounts').then((r) => r.accounts)
export const getBalances = () => request<{ balances: AccountBalance[] }>('/api/ledger/balances').then((r) => r.balances)
export const getEntries = (f: { kind?: string; before?: number; limit?: number } = {}) =>
  request<{ entries: JournalEntry[] | null }>(`/api/ledger/entries${query(f)}`).then((r) => r.entries ?? [])

/* ------------------------------------------------------------------------ */
/* Overview, customers, deposits                                             */
/* ------------------------------------------------------------------------ */

export type PayoutStatus = 'reserved' | 'submitted' | 'liquidated' | 'rejected' | 'released' | 'returned' | 'needs_review'

export const PAYOUT_STATUSES: readonly PayoutStatus[] = [
  'reserved',
  'submitted',
  'liquidated',
  'needs_review',
  'rejected',
  'released',
  'returned',
]

export interface Overview {
  /** Per-asset sum of every posting; zero when the books balance. */
  trial_balance: Partial<Record<Asset, number>>
  balances: AccountBalance[]
  /** The bank's settlement statement balance (MXN). */
  statement_balance: Money
  payouts_by_status: Partial<Record<PayoutStatus, number>>
  customers: number
  latest_recon?: ReconRun
}

export interface CustomerAddress {
  customer_id: string
  network: string
  address: string
}

export interface Customer {
  id: string
  name: string
  created_at: string
  available: Partial<Record<Stablecoin, Money>>
  addresses: CustomerAddress[]
}

export type ChainStatus = 'seen' | 'confirmed' | 'orphaned'
export type CreditState = 'pending' | 'credited' | 'reversed' | 'unattributed'

/** deposit.View */
export interface Deposit {
  transfer_id: number
  network: string
  tx_hash: string
  log_index: number
  from: string
  to: string
  amount: Money
  block_number: number
  status: ChainStatus
  explorer_url?: string
  customer_id?: string
  customer_name?: string
  state: CreditState
  generation: number
  created_at: string
}

/** chain.Transfer */
export interface ChainTransfer {
  chain_id: number
  token: string
  tx_hash: string
  log_index: number
  block_number: number
  block_hash: string
  from: string
  to: string
  amount: Money
}

export interface SimulateDepositInput {
  customer_id: string
  asset: Stablecoin
  /** Decimal string, e.g. "250.5". */
  amount: string
}

export const getOverview = () => request<Overview>('/api/overview')
export const getCustomers = () => request<{ customers: Customer[] | null }>('/api/customers').then((r) => r.customers ?? [])
export const getDeposits = (f: { network?: string; before?: number; limit?: number } = {}) =>
  request<{ deposits: Deposit[] | null }>(`/api/deposits${query(f)}`).then((r) => r.deposits ?? [])
export const simulateDeposit = (input: SimulateDepositInput, idempotencyKey: string) =>
  request<ChainTransfer>('/api/deposits/simulate', { method: 'POST', body: input, idempotent: true, idempotencyKey })

/* ------------------------------------------------------------------------ */
/* Payouts and the rail                                                      */
/* ------------------------------------------------------------------------ */

/** payout.Payout */
export interface Payout {
  id: string
  customer_id: string
  amount: Money
  fee: Money
  /** 1e6-scaled integer: 18452300 is 18.4523 MXN per unit. */
  rate: number
  mxn: Money
  beneficiary_clabe: string
  beneficiary_name: string
  concepto: string
  clave: string
  status: PayoutStatus
  attempts: number
  last_error?: string
  created_at: string
  updated_at: string
}

/** payout.Event; `from` is absent on the creation event. */
export interface PayoutEvent {
  id: number
  payout_id: string
  from?: PayoutStatus
  to: PayoutStatus
  detail: Record<string, unknown> | null
  created_at: string
}

export type RailStatus = 'en_proceso' | 'liquidado' | 'rechazado' | 'devuelto'

/** spei.CEP: a simulated receipt shaped like Banxico's. */
export interface Cep {
  simulado: boolean
  fecha_operacion: string
  hora_operacion: string
  clave_rastreo: string
  emisor: string
  receptor: string
  cuenta_beneficiario: string
  nombre_beneficiario: string
  concepto: string
  monto: string
  sello: string
}

/** spei.Record */
export interface RailOrder {
  id: number
  clave: string
  beneficiary_clabe: string
  beneficiary_name: string
  concepto: string
  amount: Money
  ordering_clabe: string
  status: RailStatus
  reason?: string
  return_pending: boolean
  submit_count: number
  cep?: Cep
  created_at: string
  liquidated_at?: string
  returned_at?: string
  updated_at: string
}

export type StatementKind = 'funding' | 'liquidation_debit' | 'return_credit' | 'bank_fee'

/** spei.StatementLine: signed MXN, credits to the account positive. */
export interface StatementLine {
  id: number
  clave?: string
  kind: StatementKind
  amount: Money
  created_at: string
}

export interface PayoutDetail extends Payout {
  events: PayoutEvent[] | null
  entries: JournalEntry[] | null
  rail?: RailOrder
}

export interface CreatePayoutInput {
  customer_id: string
  asset: Stablecoin
  amount: string
  beneficiary_clabe: string
  beneficiary_name: string
  concepto: string
}

export const getPayouts = (f: { status?: string; customer_id?: string } = {}) =>
  request<{ payouts: Payout[] | null }>(`/api/payouts${query(f)}`).then((r) => r.payouts ?? [])
export const getPayout = (id: string) => request<PayoutDetail>(`/api/payouts/${encodeURIComponent(id)}`)
export const createPayout = (input: CreatePayoutInput, idempotencyKey: string) =>
  request<Payout>('/api/payouts', { method: 'POST', body: input, idempotent: true, idempotencyKey })
/** Like createPayout, but returns the raw exchange (status, replay header) for the "send twice" demo. */
export const createPayoutRaw = (input: CreatePayoutInput, idempotencyKey: string) =>
  requestRaw('/api/payouts', { method: 'POST', body: input, idempotent: true, idempotencyKey })
export const cancelPayout = (id: string, idempotencyKey: string) =>
  request<Payout>(`/api/payouts/${encodeURIComponent(id)}/cancel`, { method: 'POST', idempotent: true, idempotencyKey })

export const getRailOrders = () => request<{ orders: RailOrder[] | null }>('/api/rail/orders').then((r) => r.orders ?? [])
export const getRailStatement = () =>
  request<{ balance: Money; lines: StatementLine[] | null }>('/api/rail/statement').then((r) => ({
    balance: r.balance,
    lines: r.lines ?? [],
  }))

/* ------------------------------------------------------------------------ */
/* Reconciliation                                                            */
/* ------------------------------------------------------------------------ */

export type BreakKind =
  | 'missing_on_ledger'
  | 'missing_on_chain'
  | 'missing_on_rail'
  | 'duplicate'
  | 'amount_mismatch'
  | 'balance_mismatch'

export const BREAK_KINDS: readonly BreakKind[] = [
  'duplicate',
  'amount_mismatch',
  'missing_on_ledger',
  'missing_on_chain',
  'missing_on_rail',
  'balance_mismatch',
]

/** recon.Break */
export interface ReconBreak {
  id: number
  kind: BreakKind
  source: 'chain' | 'rail' | 'balance'
  subject_type: string
  subject_id: string
  summary: string
  expected?: string
  actual?: string
  /** e.g. tx_hash, explorer, clave, rail_status, cep_sello, difference. */
  evidence?: Record<string, string>
}

/** recon.Run; `breaks` is only filled on single-run reads. */
export interface ReconRun {
  id: number
  trigger: 'manual' | 'scheduled' | 'cli'
  started_at: string
  finished_at?: string
  /** Items checked per matcher name. */
  checked: Record<string, number> | null
  break_count: number
  breaks?: ReconBreak[] | null
}

export const runRecon = () => request<ReconRun>('/api/recon/runs', { method: 'POST' })
export const getReconRuns = () => request<{ runs: ReconRun[] | null }>('/api/recon/runs').then((r) => r.runs ?? [])
export const getReconRun = (id: number) => request<ReconRun>(`/api/recon/runs/${String(id)}`)

/** The newest run, or null when the sandbox was never reconciled (the API answers 404). */
export async function getLatestRecon(): Promise<ReconRun | null> {
  try {
    return await request<ReconRun>('/api/recon/runs/latest')
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null
    throw err
  }
}

/* ------------------------------------------------------------------------ */
/* Live testnet                                                              */
/* ------------------------------------------------------------------------ */

export interface Live {
  enabled: boolean
  networks: Network[] | null
  address?: string
  deposits: Deposit[] | null
  latest_recon?: ReconRun
}

export const getLive = () => request<Live>('/api/live')
