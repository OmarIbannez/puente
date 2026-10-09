import type { TickReport } from '@/lib/api'
import { plural } from '@/lib/format'

/** One line about what a batch of ticks did. */
export function summarizeTicks(ticks: readonly TickReport[]): string {
  let mined = 0
  let credited = 0
  let reversed = 0
  let changes = 0
  let processed = 0
  for (const t of ticks) {
    for (const d of Object.values(t.deposits)) {
      mined += d.scan.new
      credited += d.credit.credited
      reversed += d.credit.reversed
    }
    changes += t.payouts.changes?.length ?? 0
    processed += t.payouts.processed
  }
  const parts = [
    credited > 0 && `${plural(credited, 'deposit')} credited`,
    reversed > 0 && `${plural(reversed, 'credit')} reversed`,
    processed > 0 && `${plural(processed, 'payout')} submitted to SPEI`,
    changes > 0 && plural(changes, 'rail status change'),
    mined > 0 && plural(mined, 'new chain log'),
  ].filter((p): p is string => typeof p === 'string')
  return parts.length > 0 ? parts.join(', ') : 'Nothing was waiting.'
}
