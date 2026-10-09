import { ArrowRight, CircleCheck, TriangleAlert } from 'lucide-react'

import { Amount } from '@/components/amount'
import { ErrorNotice } from '@/components/feedback'
import { PageHeader } from '@/components/page-header'
import { StatTile } from '@/components/stat-tile'
import { PayoutStatusBadge } from '@/components/status-badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useApi } from '@/hooks/use-api'
import { getHealth, getOverview, PAYOUT_STATUSES, type AccountBalance } from '@/lib/api'
import { plural } from '@/lib/format'
import type { Asset, Money } from '@/lib/money'
import { routeHref } from '@/lib/route'
import { REPO_URL } from '@/features/shell/nav'
import { TrialBalance } from '@/features/ledger/trial-balance'
import { ReconSummary } from '@/features/recon/recon-summary'

function balanceOf(balances: readonly AccountBalance[], code: number, asset: Asset): Money {
  return balances.find((b) => b.code === code && b.balance.asset === asset)?.balance ?? { asset, minor: 0 }
}

const STEPS = [
  {
    title: 'Deposit',
    body: 'USDC or MXNB arrives on a chain. The scanner reads Transfer logs idempotently and, after enough confirmations, posts deposit:<transfer> (Dr 1010 hot wallet / Cr 2010 customer).',
  },
  {
    title: 'Payout',
    body: 'One transaction quotes the FX rate, reserves amount + fee (2010 → 2020) and queues an outbox job. A worker sends it over SPEI with a fixed clave de rastreo, retrying with the same clave.',
  },
  {
    title: 'Exactly once',
    body: 'Every entry has a unique ref such as payout:<id>:submit. Posting a ref twice is a no-op, so retries, replays and crashes can never book money twice. Each asset balances on its own.',
  },
  {
    title: 'Reconcile',
    body: 'A read-only job compares three independent sources: the chain, the bank\'s books, and the ledger. Any disagreement becomes a break with evidence. The fault lab makes them on demand.',
  },
]

function HowItWorks() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>How it works</CardTitle>
        <CardDescription>
          Stablecoins in, pesos out over SPEI, every movement in a double-entry ledger exactly once.{' '}
          <a href={REPO_URL} target="_blank" rel="noreferrer" className="text-primary underline-offset-4 hover:underline">
            Read the code and the design doc
          </a>
          .
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {STEPS.map((s, i) => (
            <li key={s.title} className="text-sm">
              <p className="flex items-center gap-2 font-medium">
                <span className="tabular grid size-5 place-items-center rounded-full bg-primary/10 text-[11px] font-semibold text-primary">
                  {i + 1}
                </span>
                {s.title}
              </p>
              <p className="mt-1 text-muted-foreground">{s.body}</p>
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  )
}

export function OverviewPage() {
  const overview = useApi(getOverview, 'overview')
  const health = useApi(getHealth, 'health')
  const o = overview.data
  const loading = o === undefined && !overview.error

  const ledger1100 = o ? balanceOf(o.balances, 1100, 'MXN') : undefined
  const bankMatch = o && ledger1100 ? ledger1100.minor === o.statement_balance.minor : false
  const totalPayouts = o ? Object.values(o.payouts_by_status).reduce((a, b) => a + b, 0) : 0

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Overview"
        title="Your sandbox at a glance"
        description="What Puente owes its customers, what the bank holds, and whether the books and the outside world agree."
      />
      {overview.error && <ErrorNotice error={overview.error} onRetry={overview.reload} />}

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {(['USDC', 'MXNB'] as const).map((asset) => {
          const owed = o ? balanceOf(o.balances, 2010, asset) : undefined
          const reserved = o ? balanceOf(o.balances, 2020, asset) : undefined
          return (
            <StatTile
              key={asset}
              label={`Customer balances · ${asset}`}
              loading={loading}
              value={owed ? <Amount value={owed} /> : '—'}
              hint={
                reserved && reserved.minor !== 0 ? (
                  <>
                    + <Amount value={reserved} /> reserved for payouts (2020)
                  </>
                ) : (
                  'Liability 2010, owed to customers'
                )
              }
            />
          )
        })}
        <StatTile
          label="SPEI settlement · ledger 1100"
          loading={loading}
          value={ledger1100 ? <Amount value={ledger1100} /> : '—'}
          hint={
            o && (
              <span className="inline-flex flex-wrap items-center gap-1">
                {bankMatch ? (
                  <CircleCheck className="size-3.5 text-success" aria-hidden />
                ) : (
                  <TriangleAlert className="size-3.5 text-destructive" aria-hidden />
                )}
                Bank statement <Amount value={o.statement_balance} />
                {!bankMatch && <span className="text-destructive">differs</span>}
              </span>
            )
          }
        />
        <StatTile
          label="Payouts"
          loading={loading}
          value={<span className="tabular">{totalPayouts}</span>}
          hint={
            o && (
              <span className="mt-1 flex flex-wrap gap-1">
                {PAYOUT_STATUSES.filter((s) => (o.payouts_by_status[s] ?? 0) > 0).map((s) => (
                  <a key={s} href={routeHref({ name: 'payouts' })} className="inline-flex items-center gap-1">
                    <PayoutStatusBadge status={s} />
                    <span className="tabular text-foreground">{o.payouts_by_status[s]}</span>
                  </a>
                ))}
              </span>
            )
          }
        />
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Trial balance</CardTitle>
            <CardDescription>
              The sum of every posting, per asset. Each asset balances on its own; value only crosses assets through FX
              clearing (3900).
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {o ? <TrialBalance sums={o.trial_balance} /> : <div className="h-11 animate-pulse rounded-md bg-surface-3" />}
            <a href="#/ledger" className="inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
              Open the ledger <ArrowRight className="size-3.5" aria-hidden />
            </a>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Latest reconciliation</CardTitle>
            <CardDescription>Ledger vs chain vs bank. Read-only: breaks are reported, never auto-fixed.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            {loading ? <div className="h-11 animate-pulse rounded-md bg-surface-3" /> : <ReconSummary run={o?.latest_recon} />}
            <a href="#/recon" className="inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline">
              Reconciliation and fault lab <ArrowRight className="size-3.5" aria-hidden />
            </a>
          </CardContent>
        </Card>
      </div>

      <HowItWorks />

      <p className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {o && <span>{plural(o.customers, 'customer')} in this sandbox</span>}
        {health.data && (
          <>
            <span>
              API <span className="font-mono">{health.data.version}</span> · database{' '}
              <span className={health.data.db === 'ok' ? 'text-success' : 'text-destructive'}>{health.data.db}</span>
            </span>
            <span>Live testnet {health.data.live ? 'on' : 'off'}</span>
          </>
        )}
      </p>
    </div>
  )
}
