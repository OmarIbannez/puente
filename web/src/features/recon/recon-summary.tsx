import { CircleCheck, ScanSearch, TriangleAlert } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import type { ReconRun } from '@/lib/api'
import { formatAgo, formatDateTime, plural } from '@/lib/format'
import { humanize } from '@/lib/labels'
import { routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'

/** "chain:sim-usdc" → "Chain · sim-usdc", "rail" → "Rail". */
function matcherLabel(name: string): string {
  const [head = name, rest] = name.split(':')
  return rest ? `${humanize(head)} · ${rest}` : humanize(head)
}

/** A run's verdict and what it checked, compact enough for a card. */
export function ReconSummary({ run, link = true, className }: { run: ReconRun | null | undefined; link?: boolean; className?: string }) {
  if (!run) {
    return (
      <div className={cn('flex items-start gap-3 text-sm', className)}>
        <ScanSearch className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
        <p className="text-muted-foreground">
          No reconciliation has run yet. The worker runs one every few minutes, or start one on the{' '}
          <a href="#/recon" className="text-primary underline-offset-4 hover:underline">
            Reconciliation
          </a>{' '}
          page.
        </p>
      </div>
    )
  }
  const clean = run.break_count === 0
  const checked = Object.entries(run.checked ?? {})
  const title = (
    <span className="inline-flex items-center gap-2 font-medium">
      {clean ? (
        <CircleCheck className="size-4 text-success" aria-hidden />
      ) : (
        <TriangleAlert className="size-4 text-destructive" aria-hidden />
      )}
      {clean ? 'All sources agree' : `${plural(run.break_count, 'break')} found`}
    </span>
  )
  return (
    <div className={cn('space-y-2 text-sm', className)}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {link ? (
          <a href={routeHref({ name: 'recon', runId: run.id })} className="hover:underline">
            {title}
          </a>
        ) : (
          title
        )}
        <span className="text-xs text-muted-foreground" title={formatDateTime(run.started_at)}>
          Run #{run.id} · {run.trigger} · {formatAgo(run.started_at)}
        </span>
      </div>
      {checked.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label="Items checked per source">
          {checked.map(([name, n]) => (
            <li key={name}>
              <Badge variant="neutral">
                {matcherLabel(name)} <span className="tabular font-semibold text-foreground">{n}</span>
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
