import { LoaderCircle, ScanSearch } from 'lucide-react'

import { EmptyState, ErrorNotice } from '@/components/feedback'
import { PageHeader } from '@/components/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { SkeletonCard } from '@/components/ui/skeleton'
import { useAction } from '@/hooks/use-action'
import { useApi } from '@/hooks/use-api'
import { useApp } from '@/hooks/app-context'
import { getReconRun, getReconRuns, runRecon } from '@/lib/api'
import { formatAgo, formatDateTime, plural } from '@/lib/format'
import { navigate, routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'
import { BreakList } from '@/features/recon/break-list'
import { FaultLab } from '@/features/recon/fault-lab'
import { ReconSummary } from '@/features/recon/recon-summary'

interface Recipe {
  title: string
  steps: string[]
  result: string
}

const RECIPES: readonly Recipe[] = [
  {
    title: 'A duplicated chain log',
    steps: ['Arm Duplicate log', 'Simulate a deposit', 'Advance 5', 'Run reconciliation'],
    result: 'A duplicate break: one transfer credited twice.',
  },
  {
    title: 'An indexer that lies about the amount',
    steps: ['Arm Short deposit', 'Simulate a deposit', 'Advance 5', 'Run reconciliation'],
    result: 'An amount mismatch: the chain moved 1% less than the ledger credited.',
  },
  {
    title: 'A bank charge nobody booked',
    steps: ['Inject bank fee', 'Run reconciliation'],
    result: 'A balance mismatch: ledger 1100 ≠ the bank statement.',
  },
  {
    title: 'An order keyed in at the bank portal',
    steps: ['Inject manual order', 'Advance 1 tick', 'Run reconciliation'],
    result: 'Missing on ledger (rail), and the balances drift once it settles.',
  },
  {
    title: 'Failures the design absorbs',
    steps: ['Arm Rail timeout (or Reject, or Return)', 'Create a payout', 'Advance 5', 'Run reconciliation'],
    result: 'No break. Open the payout: same clave on every retry, entries posted once.',
  },
]

function TryThis() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Try this</CardTitle>
        <CardDescription>Short recipes that make a break appear, or prove one can't.</CardDescription>
      </CardHeader>
      <CardContent>
        <ol className="space-y-3">
          {RECIPES.map((r, i) => (
            <li key={r.title} className="text-sm">
              <p className="font-medium">
                <span className="tabular mr-1.5 text-muted-foreground">{i + 1}.</span>
                {r.title}
              </p>
              <p className="mt-1 flex flex-wrap items-center gap-1 text-xs">
                {r.steps.map((s, j) => (
                  <span key={s} className="inline-flex items-center gap-1">
                    {j > 0 && <span className="text-muted-foreground">→</span>}
                    <span className="rounded border border-border bg-surface-1 px-1.5 py-0.5">{s}</span>
                  </span>
                ))}
              </p>
              <p className="mt-1 text-xs text-muted-foreground">{r.result}</p>
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  )
}

export function ReconPage({ runId }: { runId: number | undefined }) {
  const { bump, toast } = useApp()
  const runs = useApi(getReconRuns, 'recon:runs')
  const selectedId = runId ?? runs.data?.[0]?.id
  const run = useApi(() => (selectedId === undefined ? Promise.resolve(null) : getReconRun(selectedId)), `recon:run:${String(selectedId)}`)
  const start = useAction(runRecon)

  async function reconcile() {
    const result = await start.run()
    if (!result.ok) {
      toast({ title: 'Reconciliation failed', description: result.error.message, tone: 'danger' })
      return
    }
    bump()
    navigate({ name: 'recon', runId: result.data.id })
    toast({
      title: result.data.break_count === 0 ? 'All sources agree' : `${plural(result.data.break_count, 'break')} found`,
      description: `Run #${String(result.data.id)} compared the ledger with the chain and the bank.`,
      tone: result.data.break_count === 0 ? 'success' : 'warning',
    })
  }

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Reconciliation"
        title="Ledger vs chain vs bank"
        description="A run matches chain transfers to deposit entries, bank orders to payout entries and states, and ledger account 1100 to the bank statement. Runs are read-only: breaks are reported with their evidence, never auto-fixed."
        actions={
          <Button variant="pay" onClick={() => void reconcile()} disabled={start.pending}>
            {start.pending ? <LoaderCircle className="animate-spin" /> : <ScanSearch />}
            Run reconciliation
          </Button>
        }
      />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_26rem]">
        <div className="min-w-0 space-y-6">
          <Card>
            <CardHeader>
              <CardTitle>{selectedId === undefined ? 'Breaks' : `Run #${String(selectedId)}`}</CardTitle>
              {run.data && <ReconSummary run={run.data} link={false} />}
            </CardHeader>
            <CardContent>
              {run.error && <ErrorNotice error={run.error} onRetry={run.reload} />}
              {runs.data?.length === 0 ? (
                <EmptyState icon={ScanSearch} title="No runs yet">
                  Run a reconciliation to compare the three sources. The worker also runs one every few minutes.
                </EmptyState>
              ) : run.data === undefined && !run.error ? (
                <SkeletonCard lines={4} className="shadow-none" />
              ) : (
                run.data && <BreakList breaks={run.data.breaks ?? []} />
              )}
            </CardContent>
          </Card>

          <Card className="min-w-0">
            <CardHeader className="pb-3">
              <CardTitle>Runs</CardTitle>
              <CardDescription>The latest 20, manual and scheduled.</CardDescription>
            </CardHeader>
            {runs.error && <ErrorNotice error={runs.error} onRetry={runs.reload} className="mx-5 mb-4" />}
            <ul className="divide-y divide-border border-t border-border">
              {(runs.data ?? []).map((r) => {
                const active = r.id === selectedId
                return (
                  <li key={r.id}>
                    <a
                      href={routeHref({ name: 'recon', runId: r.id })}
                      aria-current={active ? 'true' : undefined}
                      className={cn(
                        'flex flex-wrap items-center gap-x-3 gap-y-1 px-5 py-2.5 text-sm transition-colors hover:bg-surface-1',
                        active && 'bg-primary/6',
                      )}
                    >
                      <span className="font-mono text-xs text-muted-foreground">#{r.id}</span>
                      {r.break_count === 0 ? (
                        <Badge variant="success">clean</Badge>
                      ) : (
                        <Badge variant="danger">{plural(r.break_count, 'break')}</Badge>
                      )}
                      <span className="text-xs text-muted-foreground">{r.trigger}</span>
                      <span className="ml-auto text-xs text-muted-foreground" title={formatDateTime(r.started_at)}>
                        {formatAgo(r.started_at)}
                      </span>
                    </a>
                  </li>
                )
              })}
            </ul>
          </Card>
        </div>

        <div className="space-y-6">
          <FaultLab />
          <TryThis />
        </div>
      </div>
    </div>
  )
}
