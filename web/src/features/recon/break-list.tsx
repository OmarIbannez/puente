import { CircleCheck } from 'lucide-react'

import { Hash } from '@/components/hash'
import { Badge } from '@/components/ui/badge'
import { BREAK_KINDS, type ReconBreak } from '@/lib/api'
import { shortId } from '@/lib/format'
import { BREAK_KIND, breakKindLabel, humanize } from '@/lib/labels'

const ORDER: readonly string[] = BREAK_KINDS
import { routeHref } from '@/lib/route'

function Subject({ b }: { b: ReconBreak }) {
  if (b.subject_type === 'payout' && b.subject_id !== '') {
    return (
      <a href={routeHref({ name: 'payout', id: b.subject_id })} className="font-mono text-primary underline-offset-4 hover:underline">
        payout {shortId(b.subject_id)}
      </a>
    )
  }
  if (b.subject_type === '' || b.subject_id === '') return null
  return (
    <span className="font-mono">
      {b.subject_type.replace(/_/g, ' ')} {b.subject_id.length > 20 ? shortId(b.subject_id) : b.subject_id}
    </span>
  )
}

const HASH_KEYS = new Set(['tx_hash', 'to', 'cep_sello', 'clave', 'customer_id'])

function Evidence({ evidence }: { evidence: Record<string, string> }) {
  const explorer = evidence.explorer
  const rows = Object.entries(evidence).filter(([k]) => k !== 'explorer')
  if (rows.length === 0 && !explorer) return null
  return (
    <dl className="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-4 gap-y-1 text-xs">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{humanize(k)}</dt>
          <dd className="min-w-0">
            {k === 'tx_hash' ? (
              <Hash value={v} href={explorer} />
            ) : HASH_KEYS.has(k) ? (
              <Hash value={v} />
            ) : (
              <span className="tabular break-words">{v}</span>
            )}
          </dd>
        </div>
      ))}
      {explorer && !evidence.tx_hash && (
        <div className="contents">
          <dt className="text-muted-foreground">Explorer</dt>
          <dd>
            <a href={explorer} target="_blank" rel="noreferrer" className="text-primary underline-offset-4 hover:underline">
              Open transaction
            </a>
          </dd>
        </div>
      )}
    </dl>
  )
}

function BreakRow({ b }: { b: ReconBreak }) {
  return (
    <li className="space-y-2 px-4 py-3">
      <div className="flex flex-wrap items-start gap-x-3 gap-y-1">
        <p className="min-w-0 flex-1 text-sm">{b.summary}</p>
        <Badge variant="outline" className="capitalize">
          {b.source}
        </Badge>
      </div>
      <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs">
        <span className="text-muted-foreground">
          Subject: <Subject b={b} />
        </span>
        {(b.expected !== undefined || b.actual !== undefined) && (
          <span className="tabular">
            <span className="text-muted-foreground">Expected</span> <span className="font-medium">{b.expected ?? '—'}</span>
            <span className="mx-1.5 text-muted-foreground">vs actual</span>
            <span className="font-medium text-destructive">{b.actual ?? '—'}</span>
          </span>
        )}
      </div>
      {b.evidence && <Evidence evidence={b.evidence} />}
    </li>
  )
}

/** Breaks grouped by kind, each group with what the kind means. */
export function BreakList({ breaks }: { breaks: readonly ReconBreak[] }) {
  if (breaks.length === 0) {
    return (
      <p className="flex items-center gap-2 px-5 py-6 text-sm text-muted-foreground">
        <CircleCheck className="size-4 text-success" aria-hidden />
        No breaks: the ledger agrees with the chain and with the bank.
      </p>
    )
  }
  const groups = new Map<string, ReconBreak[]>()
  for (const b of breaks) groups.set(b.kind, [...(groups.get(b.kind) ?? []), b])
  const kinds = [...groups.keys()].sort((a, b) => ORDER.indexOf(a) - ORDER.indexOf(b))
  return (
    <div className="space-y-4">
      {kinds.map((kind) => {
        const list = groups.get(kind) ?? []
        const meaning = kind in BREAK_KIND ? BREAK_KIND[kind as keyof typeof BREAK_KIND].meaning : undefined
        return (
          <section key={kind} aria-label={breakKindLabel(kind)} className="overflow-hidden rounded-lg border border-destructive/25">
            <header className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 border-b border-destructive/20 bg-destructive/6 px-4 py-2">
              <h3 className="text-sm font-semibold">{breakKindLabel(kind)}</h3>
              <span className="tabular text-xs text-destructive">{list.length}</span>
              {meaning && <p className="w-full text-xs text-muted-foreground sm:w-auto">{meaning}</p>}
            </header>
            <ul className="divide-y divide-border">
              {list.map((b) => (
                <BreakRow key={b.id} b={b} />
              ))}
            </ul>
          </section>
        )
      })}
    </div>
  )
}
