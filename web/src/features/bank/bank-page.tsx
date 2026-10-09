import { CircleCheck, Landmark, TriangleAlert } from 'lucide-react'

import { Amount } from '@/components/amount'
import { EmptyState, ErrorNotice } from '@/components/feedback'
import { Hash } from '@/components/hash'
import { PageHeader } from '@/components/page-header'
import { StatTile } from '@/components/stat-tile'
import { RailStatusBadge, SimulatedBadge } from '@/components/status-badge'
import { Alert } from '@/components/ui/alert'
import { Card, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { SkeletonCard } from '@/components/ui/skeleton'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { getBalances, getPayouts, getRailOrders, getRailStatement, type StatementLine } from '@/lib/api'
import { formatAgo, formatClabe, formatDateTime } from '@/lib/format'
import { STATEMENT_KIND } from '@/lib/labels'
import type { Money } from '@/lib/money'
import { routeHref } from '@/lib/route'

/** Running balance after each line, given lines newest first and the closing balance. */
function withRunning(lines: readonly StatementLine[], closing: Money): { line: StatementLine; after: Money }[] {
  let after = BigInt(closing.minor)
  return lines.map((line) => {
    const row = { line, after: { asset: closing.asset, minor: Number(after) } }
    after -= BigInt(line.amount.minor)
    return row
  })
}

export function BankPage() {
  const orders = useApi(getRailOrders, 'rail:orders')
  const statement = useApi(getRailStatement, 'rail:statement')
  const balances = useApi(getBalances, 'balances')
  const payouts = useApi(() => getPayouts(), 'payouts:')

  const ledger1100 = balances.data?.find((b) => b.code === 1100 && b.net.asset === 'MXN')?.balance
  const bank = statement.data?.balance
  const agree = bank !== undefined && ledger1100?.minor === bank.minor
  const payoutByClave = new Map((payouts.data ?? []).map((p) => [p.clave, p.id]))
  // The payouts list is one page (50); only claim "no payout" when it is complete.
  const allPayoutsKnown = (payouts.data?.length ?? 50) < 50

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Bank (SPEI)"
        title="The bank's books"
        description="The simulated SPEI participant keeps its own orders and settlement statement in separate tables. Puente's ledger never writes here: that independence is what makes reconciliation meaningful."
        actions={<SimulatedBadge label="simulated bank" />}
      />

      <div className="grid gap-3 sm:grid-cols-3">
        <StatTile
          label="Bank statement balance"
          value={bank ? <Amount value={bank} /> : '—'}
          loading={statement.data === undefined && !statement.error}
          hint="What the bank says the settlement account holds"
        />
        <StatTile
          label="Ledger account 1100"
          value={ledger1100 ? <Amount value={ledger1100} /> : '—'}
          loading={balances.data === undefined && !balances.error}
          hint="What Puente's books say it holds"
        />
        <StatTile
          label="Agreement"
          loading={ledger1100 === undefined || bank === undefined}
          value={
            agree ? (
              <span className="inline-flex items-center gap-2 text-success">
                <CircleCheck className="size-6" aria-hidden />
                Match
              </span>
            ) : (
              <span className="inline-flex items-center gap-2 text-destructive">
                <TriangleAlert className="size-6" aria-hidden />
                Differs
              </span>
            )
          }
          hint={
            agree || ledger1100 === undefined || bank === undefined ? (
              'Invariant: 1100 equals the statement balance'
            ) : (
              <>
                Difference <Amount value={{ asset: 'MXN', minor: ledger1100.minor - bank.minor }} />: run reconciliation
              </>
            )
          }
        />
      </div>

      <Card className="min-w-0">
        <CardHeader className="pb-3">
          <CardTitle>Payment orders</CardTitle>
          <CardDescription>
            Keyed by clave de rastreo. Submitting the same clave again returns the existing order (the submit count goes up),
            so a retried payout can never pay twice.
          </CardDescription>
        </CardHeader>
        {orders.error && <ErrorNotice error={orders.error} onRetry={orders.reload} className="mx-5 mb-4" />}
        {orders.data === undefined && !orders.error ? (
          <SkeletonCard lines={5} className="mx-5 mb-5 shadow-none" />
        ) : (orders.data ?? []).length === 0 ? (
          <EmptyState icon={Landmark} title="No orders yet" className="mx-5 mb-5">
            Payouts reach the bank when the outbox worker runs on the next tick.
          </EmptyState>
        ) : (
          <Table>
            <THead>
              <tr>
                <TH>Clave de rastreo</TH>
                <TH>Beneficiary</TH>
                <TH numeric>Amount</TH>
                <TH>Status</TH>
                <TH numeric>Submits</TH>
                <TH>Accepted</TH>
              </tr>
            </THead>
            <TBody>
              {(orders.data ?? []).map((o) => {
                const payoutId = payoutByClave.get(o.clave)
                return (
                  <TR key={o.id}>
                    <TD>
                      {payoutId ? (
                        <a
                          href={routeHref({ name: 'payout', id: payoutId })}
                          className="font-mono text-xs text-primary underline-offset-4 hover:underline"
                        >
                          {o.clave}
                        </a>
                      ) : (
                        <span className="font-mono text-xs">
                          {o.clave}
                          {allPayoutsKnown && <span className="ml-1.5 font-sans text-[11px] text-warning">no payout</span>}
                        </span>
                      )}
                    </TD>
                    <TD>
                      <span className="block max-w-56 truncate text-sm">{o.beneficiary_name}</span>
                      <span className="font-mono text-[11px] text-muted-foreground">{formatClabe(o.beneficiary_clabe)}</span>
                    </TD>
                    <TD numeric>
                      <Amount value={o.amount} />
                    </TD>
                    <TD>
                      <RailStatusBadge status={o.status} />
                      {o.reason && <span className="block text-[11px] text-muted-foreground">{o.reason}</span>}
                    </TD>
                    <TD numeric className={o.submit_count > 1 ? 'font-semibold text-primary' : 'text-muted-foreground'}>
                      {o.submit_count}
                    </TD>
                    <TD className="whitespace-nowrap text-xs text-muted-foreground" title={formatDateTime(o.created_at)}>
                      {formatAgo(o.created_at)}
                    </TD>
                  </TR>
                )
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <Card className="min-w-0">
        <CardHeader className="pb-3">
          <CardTitle>Settlement account statement</CardTitle>
          <CardDescription>Credits positive, debits negative. Newest first, with the balance after each line.</CardDescription>
        </CardHeader>
        {statement.error && <ErrorNotice error={statement.error} onRetry={statement.reload} className="mx-5 mb-4" />}
        {statement.data && statement.data.lines.length >= 200 && (
          <Alert tone="info" className="mx-5 mb-3">
            Showing the latest 200 lines.
          </Alert>
        )}
        {!statement.data ? (
          !statement.error && <SkeletonCard lines={5} className="mx-5 mb-5 shadow-none" />
        ) : (
          <Table>
            <THead>
              <tr>
                <TH>Line</TH>
                <TH>Kind</TH>
                <TH>Clave</TH>
                <TH numeric>Amount</TH>
                <TH numeric>Balance after</TH>
                <TH>Booked</TH>
              </tr>
            </THead>
            <TBody>
              {withRunning(statement.data.lines, statement.data.balance).map(({ line, after }) => (
                <TR key={line.id}>
                  <TD className="font-mono text-xs text-muted-foreground">{line.id}</TD>
                  <TD className={line.kind === 'bank_fee' ? 'font-medium text-warning' : undefined}>{STATEMENT_KIND[line.kind]}</TD>
                  <TD>{line.clave ? <Hash value={line.clave} full copy={false} /> : <span className="text-muted-foreground">—</span>}</TD>
                  <TD numeric>
                    <Amount value={line.amount} signed />
                  </TD>
                  <TD numeric className="text-muted-foreground">
                    <Amount value={after} />
                  </TD>
                  <TD className="whitespace-nowrap text-xs text-muted-foreground" title={formatDateTime(line.created_at)}>
                    {formatAgo(line.created_at)}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
    </div>
  )
}
