import { Amount } from '@/components/amount'
import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import { useAccounts, useCustomers } from '@/hooks/use-reference'
import type { JournalEntry as Entry } from '@/lib/api'
import { formatDateTime, shortId } from '@/lib/format'
import { ENTRY_KIND, entryKindLabel } from '@/lib/labels'
import { routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'

function SubjectLink({ subject }: { subject: NonNullable<Entry['subject']> }) {
  if (subject.type === 'payout') {
    return (
      <a href={routeHref({ name: 'payout', id: subject.id })} className="font-mono text-primary underline-offset-4 hover:underline">
        payout {shortId(subject.id)}
      </a>
    )
  }
  return (
    <span className="font-mono">
      {subject.type.replace(/_/g, ' ')} {shortId(subject.id)}
    </span>
  )
}

/**
 * One journal entry as an accountant reads it: debits and credits in their
 * own columns, one row per posting. The sum per asset is always zero; the
 * API (and a database trigger) refuse anything else.
 */
export function JournalEntryCard({ entry, className }: { entry: Entry; className?: string }) {
  const { accountOf } = useAccounts()
  const { nameOf } = useCustomers()
  const kindInfo = entry.kind in ENTRY_KIND ? ENTRY_KIND[entry.kind] : undefined

  return (
    <article className={cn('overflow-hidden rounded-lg border border-border bg-card', className)}>
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border bg-surface-1 px-4 py-2">
        <span className="font-mono text-xs text-muted-foreground">#{entry.id}</span>
        {kindInfo ? (
          <Tooltip content={kindInfo.hint}>
            <Badge variant="brand" tabIndex={0}>
              {kindInfo.label}
            </Badge>
          </Tooltip>
        ) : (
          <Badge variant="brand">{entryKindLabel(entry.kind)}</Badge>
        )}
        <Tooltip content="The ref is unique per sandbox. Posting the same ref twice is a no-op: this is the exactly-once guarantee.">
          <code tabIndex={0} className="cursor-help truncate font-mono text-xs text-foreground">
            {entry.ref}
          </code>
        </Tooltip>
        <span className="ml-auto text-xs text-muted-foreground">{formatDateTime(entry.created_at)}</span>
      </header>
      {(entry.memo !== '' || entry.subject) && (
        <p className="flex flex-wrap gap-x-3 px-4 pt-2 text-xs text-muted-foreground">
          {entry.memo !== '' && <span>{entry.memo}</span>}
          {entry.subject && <SubjectLink subject={entry.subject} />}
        </p>
      )}
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
              <th scope="col" className="px-4 py-1.5 text-left font-medium">
                Account
              </th>
              <th scope="col" className="px-3 py-1.5 text-left font-medium">
                Party
              </th>
              <th scope="col" className="px-3 py-1.5 text-right font-medium">
                Debit
              </th>
              <th scope="col" className="px-4 py-1.5 text-right font-medium">
                Credit
              </th>
            </tr>
          </thead>
          <tbody>
            {entry.postings.map((p, i) => {
              const account = accountOf(p.account)
              const debit = p.amount.minor > 0
              const abs = { asset: p.amount.asset, minor: Math.abs(p.amount.minor) }
              return (
                <tr key={i} className="border-t border-border/60">
                  <td className={cn('px-4 py-1.5', !debit && 'pl-8')}>
                    <span className="font-mono text-xs text-muted-foreground">{p.account}</span>{' '}
                    <span>{account?.name ?? ''}</span>
                  </td>
                  <td className="px-3 py-1.5 text-xs text-muted-foreground">
                    {p.party ? (nameOf(p.party) ?? <span className="font-mono">{shortId(p.party)}</span>) : ''}
                  </td>
                  <td className="px-3 py-1.5 text-right">{debit && <Amount value={abs} />}</td>
                  <td className="px-4 py-1.5 text-right">{!debit && <Amount value={abs} />}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </article>
  )
}
