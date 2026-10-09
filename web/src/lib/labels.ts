/** Human labels and tones for the API's enums. Pure data, no React. */

import type {
  BreakKind,
  ChainStatus,
  CreditState,
  EntryKind,
  FaultName,
  Network,
  PayoutStatus,
  RailStatus,
  StatementKind,
} from '@/lib/api'

export type Tone = 'neutral' | 'brand' | 'success' | 'warning' | 'danger' | 'outline'

/** SNAKE_or_snake → "Snake or snake". */
export function humanize(code: string): string {
  const text = code.toLowerCase().split(/[_\s-]+/).filter(Boolean).join(' ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

export const PAYOUT_STATUS: Record<PayoutStatus, { label: string; tone: Tone; hint: string }> = {
  reserved: { label: 'Reserved', tone: 'brand', hint: 'Funds earmarked; waiting for the outbox worker.' },
  submitted: { label: 'Submitted', tone: 'brand', hint: 'Order sent to SPEI; pesos in flight.' },
  liquidated: { label: 'Liquidated', tone: 'success', hint: 'The rail settled the order (liquidado).' },
  rejected: { label: 'Rejected', tone: 'danger', hint: 'The rail refused the order; funds are being released.' },
  released: { label: 'Released', tone: 'neutral', hint: 'Funds went back to the customer (cancel or reject).' },
  returned: { label: 'Returned', tone: 'warning', hint: 'Devuelto after settlement; amount credited back, fee kept.' },
  needs_review: { label: 'Needs review', tone: 'warning', hint: 'Every retry timed out; a later rail sync resolves it.' },
}

export const RAIL_STATUS: Record<RailStatus, { label: string; tone: Tone }> = {
  en_proceso: { label: 'En proceso', tone: 'brand' },
  liquidado: { label: 'Liquidado', tone: 'success' },
  rechazado: { label: 'Rechazado', tone: 'danger' },
  devuelto: { label: 'Devuelto', tone: 'warning' },
}

export const CHAIN_STATUS: Record<ChainStatus, { label: string; tone: Tone }> = {
  seen: { label: 'Seen', tone: 'neutral' },
  confirmed: { label: 'Confirmed', tone: 'success' },
  orphaned: { label: 'Orphaned', tone: 'danger' },
}

export const CREDIT_STATE: Record<CreditState, { label: string; tone: Tone; hint: string }> = {
  pending: { label: 'Pending', tone: 'neutral', hint: 'Waiting for confirmations (3 blocks on the simulated chain).' },
  credited: { label: 'Credited', tone: 'success', hint: 'Posted to the ledger as deposit:<transfer>.' },
  reversed: { label: 'Reversed', tone: 'danger', hint: 'A reorg orphaned the block; the credit was reversed.' },
  unattributed: { label: 'Unattributed', tone: 'outline', hint: 'Sent to an address no customer owns; never credited.' },
}

export const ENTRY_KIND: Record<EntryKind, { label: string; hint: string }> = {
  funding: { label: 'Funding', hint: 'Treasury prefunds the SPEI account: Dr 1100 / Cr 3000.' },
  deposit: { label: 'Deposit', hint: 'Confirmed on-chain deposit: Dr 1010 / Cr 2010.' },
  deposit_reversal: { label: 'Deposit reversal', hint: 'Undoes a credited deposit orphaned by a reorg.' },
  payout_reserve: { label: 'Payout reserve', hint: 'Earmarks amount + fee: Dr 2010 / Cr 2020.' },
  payout_submit: { label: 'Payout submit', hint: 'Books the fee and the FX conversion; pesos in flight (2100).' },
  payout_settle: { label: 'Payout settle', hint: 'Order liquidated: Dr 2100 / Cr 1100.' },
  payout_release: { label: 'Payout release', hint: 'Reverses the reserve (and the submit after a rejection).' },
  payout_return: { label: 'Payout return', hint: 'Devolución after settlement; the fee is kept.' },
}

export function entryKindLabel(kind: string): string {
  return kind in ENTRY_KIND ? ENTRY_KIND[kind as EntryKind].label : humanize(kind)
}

export const STATEMENT_KIND: Record<StatementKind, string> = {
  funding: 'Funding',
  liquidation_debit: 'Liquidation',
  return_credit: 'Return',
  bank_fee: 'Bank fee',
}

export const BREAK_KIND: Record<BreakKind, { label: string; meaning: string }> = {
  duplicate: { label: 'Duplicate', meaning: 'The same transfer was reported or credited more than once.' },
  amount_mismatch: { label: 'Amount mismatch', meaning: 'Both sides know the item, but the amounts differ.' },
  missing_on_ledger: { label: 'Missing on ledger', meaning: 'The chain or the bank has it; the ledger does not.' },
  missing_on_chain: { label: 'Missing on chain', meaning: 'The ledger credited a transfer the canonical chain no longer has.' },
  missing_on_rail: { label: 'Missing on rail', meaning: 'The ledger booked a payout the bank has no matching order for.' },
  balance_mismatch: { label: 'Balance mismatch', meaning: 'Ledger account 1100 differs from the bank statement balance.' },
}

export function breakKindLabel(kind: string): string {
  return kind in BREAK_KIND ? BREAK_KIND[kind as BreakKind].label : humanize(kind)
}

export interface FaultInfo {
  name: FaultName
  label: string
  layer: 'chain' | 'rail'
  /** What arming it does, consumed by one event. */
  effect: string
  /** The break or state it should produce. */
  shows: string
}

export const FAULTS: readonly FaultInfo[] = [
  {
    name: 'duplicate_log',
    label: 'Duplicate log',
    layer: 'chain',
    effect: 'The indexer reports the next deposit twice (log 0 and a phantom log 1); the chain has one.',
    shows: 'Both logs get credited; recon re-reads the chain and raises a duplicate break.',
  },
  {
    name: 'short_deposit',
    label: 'Short deposit',
    layer: 'chain',
    effect: 'The next deposit lands on the canonical chain 1% short of what the feed said.',
    shows: 'Recon finds an amount mismatch between the ledger credit and the canonical chain.',
  },
  {
    name: 'reorg',
    label: 'Reorg',
    layer: 'chain',
    effect: 'The next mined block replaces the block of the most recent confirmed deposit.',
    shows: 'The scanner sees the new block hash and posts a deposit_reversal; the balance may go negative.',
  },
  {
    name: 'rail_timeout',
    label: 'Rail timeout',
    layer: 'rail',
    effect: 'The bank accepts the next order but the acknowledgement is lost.',
    shows: 'No break: retries reuse the same clave, the bank dedupes (submit count > 1), the ledger posts once.',
  },
  {
    name: 'rail_reject',
    label: 'Rail reject',
    layer: 'rail',
    effect: 'The bank rejects the next order ("Cuenta inexistente").',
    shows: 'No break: the payout goes rejected → released and the release entry reverses reserve and submit.',
  },
  {
    name: 'rail_return',
    label: 'Rail return',
    layer: 'rail',
    effect: 'The next liquidated order is returned (devuelto) a few ticks later.',
    shows: 'No break: the payout goes liquidated → returned with a compensating payout_return entry.',
  },
]

/** Network display name from its key, falling back to the key. */
export function networkName(networks: readonly Network[], key: string): string {
  return networks.find((n) => n.key === key)?.name ?? key
}

/** Explorer link for an address, derived from the network's tx URL pattern (".../tx/%s"). */
export function explorerAddressUrl(network: Network, address: string): string | undefined {
  const pattern = network.explorer_tx_url
  if (!pattern?.includes('/tx/%s')) return undefined
  return pattern.replace('/tx/%s', `/address/${address}`)
}

/** The explorer home for a network, from the same pattern. */
export function explorerTxUrl(network: Network, hash: string): string | undefined {
  return network.explorer_tx_url?.replace('%s', hash)
}
