import { ArrowRight } from 'lucide-react'

import { Tooltip } from '@/components/ui/tooltip'
import type { PayoutEvent, PayoutStatus } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import { humanize, PAYOUT_STATUS } from '@/lib/labels'
import { cn } from '@/lib/utils'

const MAIN: readonly PayoutStatus[] = ['reserved', 'submitted', 'liquidated', 'returned']
const SIDE: readonly PayoutStatus[] = ['needs_review', 'rejected', 'released']

function Node({ status, visited, current }: { status: PayoutStatus; visited: boolean; current: boolean }) {
  const s = PAYOUT_STATUS[status]
  const danger = s.tone === 'danger'
  const warn = s.tone === 'warning'
  return (
    <Tooltip content={s.hint}>
      <span
        tabIndex={0}
        aria-current={current ? 'step' : undefined}
        className={cn(
          'inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-1 text-xs transition-colors',
          !visited && 'border-dashed border-border text-muted-foreground/70',
          visited && !current && 'border-border bg-surface-2 text-foreground',
          current && !danger && !warn && 'border-primary bg-primary/10 font-semibold text-primary ring-2 ring-primary/20',
          current && danger && 'border-destructive bg-destructive/10 font-semibold text-destructive ring-2 ring-destructive/20',
          current && warn && 'border-warning bg-warning/10 font-semibold text-warning ring-2 ring-warning/20',
        )}
      >
        <span
          aria-hidden
          className={cn('size-1.5 rounded-full', visited ? 'bg-current' : 'border border-current bg-transparent')}
        />
        {s.label}
      </span>
    </Tooltip>
  )
}

/** The payout state machine with the path this payout took highlighted. */
export function StateMachine({ events, status }: { events: readonly PayoutEvent[]; status: PayoutStatus }) {
  const visited = new Set<PayoutStatus>(events.map((e) => e.to))
  visited.add(status)
  return (
    <div className="space-y-2" aria-label="Payout state machine">
      <div className="flex flex-wrap items-center gap-1.5">
        {MAIN.map((s, i) => (
          <span key={s} className="inline-flex items-center gap-1.5">
            {i > 0 && <ArrowRight className="size-3.5 text-muted-foreground" aria-hidden />}
            <Node status={s} visited={visited.has(s)} current={s === status} />
          </span>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
        <span>Off the happy path:</span>
        {SIDE.map((s) => (
          <Node key={s} status={s} visited={visited.has(s)} current={s === status} />
        ))}
      </div>
    </div>
  )
}

function detailText(value: unknown): string {
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  return JSON.stringify(value)
}

/** Every transition, oldest first, as the payout_events table recorded it. */
export function EventTimeline({ events }: { events: readonly PayoutEvent[] }) {
  return (
    <ol className="relative space-y-4 border-l border-border pl-5">
      {events.map((e) => {
        const to = PAYOUT_STATUS[e.to]
        const details = Object.entries(e.detail ?? {})
        return (
          <li key={e.id} className="relative">
            <span
              aria-hidden
              className={cn(
                'absolute top-1 -left-[1.6rem] size-2.5 rounded-full border-2 border-card',
                to.tone === 'success' && 'bg-success',
                to.tone === 'danger' && 'bg-destructive',
                to.tone === 'warning' && 'bg-warning',
                (to.tone === 'brand' || to.tone === 'neutral' || to.tone === 'outline') && 'bg-primary',
              )}
            />
            <p className="text-sm">
              {e.from ? (
                <>
                  <span className="text-muted-foreground">{PAYOUT_STATUS[e.from].label}</span>
                  <ArrowRight className="mx-1 inline size-3.5 text-muted-foreground" aria-label="to" />
                  <span className="font-medium">{to.label}</span>
                </>
              ) : (
                <span className="font-medium">Created as {to.label.toLowerCase()}</span>
              )}
            </p>
            <p className="text-xs text-muted-foreground">{formatDateTime(e.created_at)}</p>
            {details.length > 0 && (
              <dl className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-xs">
                {details.map(([k, v]) => (
                  <div key={k} className="flex gap-1">
                    <dt className="text-muted-foreground">{humanize(k)}:</dt>
                    <dd className="tabular font-mono">{detailText(v)}</dd>
                  </div>
                ))}
              </dl>
            )}
          </li>
        )
      })}
    </ol>
  )
}
