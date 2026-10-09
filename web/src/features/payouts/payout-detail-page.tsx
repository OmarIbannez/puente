import { ArrowLeft, ArrowRight, Ban, Landmark, LoaderCircle } from 'lucide-react'
import type { ReactNode } from 'react'

import { Amount } from '@/components/amount'
import { ErrorNotice } from '@/components/feedback'
import { Hash } from '@/components/hash'
import { PayoutStatusBadge, RailStatusBadge, SimulatedBadge } from '@/components/status-badge'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { SkeletonCard } from '@/components/ui/skeleton'
import { useAction } from '@/hooks/use-action'
import { useApi } from '@/hooks/use-api'
import { useApp } from '@/hooks/app-context'
import { useIdempotencyKey } from '@/hooks/use-idempotency-key'
import { useCustomers } from '@/hooks/use-reference'
import { cancelPayout, getPayout, isFinalClientError, type PayoutDetail, type RailOrder } from '@/lib/api'
import { formatClabe, formatDateTime, plural, shortId } from '@/lib/format'
import { formatRate } from '@/lib/quote'
import { CepReceipt } from '@/features/payouts/cep-receipt'
import { EventTimeline, StateMachine } from '@/features/payouts/payout-timeline'
import { JournalEntryCard } from '@/features/ledger/journal-entry'

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm">{children}</dd>
    </div>
  )
}

function CancelButton({ payout }: { payout: PayoutDetail }) {
  const { bump, toast } = useApp()
  const [key, rotate] = useIdempotencyKey(payout.id)
  const cancel = useAction(cancelPayout)
  async function run() {
    const result = await cancel.run(payout.id, key)
    if (!result.ok) {
      if (isFinalClientError(result.error)) rotate()
      toast({ title: 'Cancel failed', description: result.error.message, tone: 'danger' })
      return
    }
    rotate()
    bump()
    toast({ title: 'Payout released', description: 'The reserve was reversed; the funds are available again.', tone: 'success' })
  }
  return (
    <Button variant="outline" size="sm" onClick={() => void run()} disabled={cancel.pending}>
      {cancel.pending ? <LoaderCircle className="animate-spin" /> : <Ban />}
      Cancel payout
    </Button>
  )
}

function RailCard({ rail, attempts }: { rail: RailOrder | undefined; attempts: number }) {
  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-2">
          <CardTitle className="flex items-center gap-2">
            <Landmark className="size-4 text-muted-foreground" aria-hidden />
            At the bank (SPEI)
          </CardTitle>
          <SimulatedBadge />
        </div>
        <CardDescription>The bank's own record of this clave, kept apart from Puente's ledger.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {!rail ? (
          <p className="text-sm text-muted-foreground">Not submitted yet. The outbox worker sends it on the next tick.</p>
        ) : (
          <>
            <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
              <Fact label="Status">
                <RailStatusBadge status={rail.status} />
                {rail.return_pending && <span className="ml-1.5 text-xs text-warning">return pending</span>}
              </Fact>
              <Fact label="Amount">
                <Amount value={rail.amount} />
              </Fact>
              <Fact label="Submissions of this clave">
                <span className="tabular">{rail.submit_count}</span>
                {rail.submit_count > 1 && (
                  <span className="block text-xs text-muted-foreground">
                    {plural(attempts, 'attempt')} by Puente, one order at the bank: deduped on the clave.
                  </span>
                )}
              </Fact>
              <Fact label="Ordering account">
                <span className="font-mono text-xs">{formatClabe(rail.ordering_clabe)}</span>
              </Fact>
              {rail.reason && <Fact label="Reason">{rail.reason}</Fact>}
              <Fact label="Accepted">{formatDateTime(rail.created_at)}</Fact>
              {rail.liquidated_at && <Fact label="Liquidated">{formatDateTime(rail.liquidated_at)}</Fact>}
              {rail.returned_at && <Fact label="Returned">{formatDateTime(rail.returned_at)}</Fact>}
            </dl>
            {rail.cep ? (
              <CepReceipt cep={rail.cep} />
            ) : (
              <p className="text-xs text-muted-foreground">The CEP receipt appears once the order is liquidado.</p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function PayoutDetailPage({ id }: { id: string }) {
  const detail = useApi(() => getPayout(id), `payout:${id}`)
  const { nameOf } = useCustomers()
  const p = detail.data

  return (
    <div className="space-y-6">
      <a href="#/payouts" className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="size-4" aria-hidden />
        All payouts
      </a>
      {detail.error && <ErrorNotice error={detail.error} onRetry={detail.reload} title="Couldn't load this payout" />}
      {!p ? (
        !detail.error && (
          <div className="grid gap-4 lg:grid-cols-2">
            <SkeletonCard lines={4} />
            <SkeletonCard lines={6} />
          </div>
        )
      ) : (
        <>
          <div className="flex animate-fade-up flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
            <div>
              <p className="text-xs font-semibold uppercase tracking-[0.14em] text-accent">Payout {shortId(p.id)}</p>
              <h1 className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xl font-semibold sm:text-3xl">
                <Amount value={p.amount} />
                <ArrowRight className="size-5 text-muted-foreground" aria-label="becomes" />
                <Amount value={p.mxn} />
              </h1>
              <p className="mt-1.5 text-sm text-muted-foreground">
                To {p.beneficiary_name} · {formatClabe(p.beneficiary_clabe)} · from {nameOf(p.customer_id) ?? shortId(p.customer_id)}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <PayoutStatusBadge status={p.status} />
              {p.status === 'reserved' && <CancelButton payout={p} />}
            </div>
          </div>

          {p.last_error && (
            <Alert tone="warning" role="status">
              Last rail error: {p.last_error}
            </Alert>
          )}

          <Card>
            <CardContent className="pt-5">
              <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-3 lg:grid-cols-6">
                <Fact label="Fee">
                  <Amount value={p.fee} />
                </Fact>
                <Fact label="Rate (fixed at creation)">
                  <span className="tabular">{formatRate(BigInt(p.rate))}</span>
                </Fact>
                <Fact label="Clave de rastreo">
                  <Hash value={p.clave} full />
                </Fact>
                <Fact label="Concepto">{p.concepto === '' ? '—' : p.concepto}</Fact>
                <Fact label="Attempts">{p.attempts}</Fact>
                <Fact label="Created">{formatDateTime(p.created_at)}</Fact>
              </dl>
            </CardContent>
          </Card>

          <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
            <Card>
              <CardHeader>
                <CardTitle>State machine</CardTitle>
                <CardDescription>
                  Each transition locks the payout row, checks the edge is legal, writes an event and posts its entry in one
                  transaction.
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-5">
                <StateMachine events={p.events ?? []} status={p.status} />
                <EventTimeline events={p.events ?? []} />
              </CardContent>
            </Card>
            <RailCard rail={p.rail} attempts={p.attempts} />
          </div>

          <Card>
            <CardHeader>
              <CardTitle>Ledger entries</CardTitle>
              <CardDescription>
                Refs are <code className="font-mono">payout:{shortId(p.id)}…:reserve|submit|settle|release|return</code>. Each is
                posted at most once, whatever the retries.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-3">
              {(p.entries ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No entries yet.</p>
              ) : (
                [...(p.entries ?? [])].sort((a, b) => a.id - b.id).map((e) => <JournalEntryCard key={e.id} entry={e} />)
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}
