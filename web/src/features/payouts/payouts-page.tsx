import { Send } from 'lucide-react'
import { useState } from 'react'

import { Amount } from '@/components/amount'
import { EmptyState, ErrorNotice } from '@/components/feedback'
import { PageHeader } from '@/components/page-header'
import { PayoutStatusBadge } from '@/components/status-badge'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { SkeletonCard } from '@/components/ui/skeleton'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { useCustomers } from '@/hooks/use-reference'
import { getPayouts, PAYOUT_STATUSES, type PayoutStatus } from '@/lib/api'
import { formatAgo, formatDateTime, shortId } from '@/lib/format'
import { PAYOUT_STATUS } from '@/lib/labels'
import { navigate, routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'
import { PayoutForm } from '@/features/payouts/payout-form'

function StatusFilter({ value, onChange }: { value: PayoutStatus | ''; onChange: (s: PayoutStatus | '') => void }) {
  const options: (PayoutStatus | '')[] = ['', ...PAYOUT_STATUSES]
  return (
    <div role="radiogroup" aria-label="Filter by status" className="flex flex-wrap gap-1">
      {options.map((s) => {
        const active = s === value
        return (
          <button
            key={s === '' ? 'all' : s}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => {
              onChange(s)
            }}
            className={cn(
              'cursor-pointer rounded-full border px-2.5 py-0.5 text-xs transition-colors',
              active
                ? 'border-primary/40 bg-primary/10 font-medium text-primary'
                : 'border-border text-muted-foreground hover:bg-surface-2 hover:text-foreground',
            )}
          >
            {s === '' ? 'All' : PAYOUT_STATUS[s].label}
          </button>
        )
      })}
    </div>
  )
}

export function PayoutsPage() {
  const [status, setStatus] = useState<PayoutStatus | ''>('')
  const payouts = useApi(() => getPayouts({ status }), `payouts:${status}`)
  const { nameOf } = useCustomers()
  const rows = payouts.data ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Payouts"
        title="SPEI transfers out"
        description="A payout reserves the stablecoin, queues an outbox job in the same transaction, and goes to the bank with a fixed clave de rastreo. Every state change locks the row, checks the edge and posts its entry once."
      />
      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_26rem]">
        <Card className="min-w-0">
          <CardHeader className="gap-3 pb-1">
            <CardTitle>Payouts</CardTitle>
            <StatusFilter value={status} onChange={setStatus} />
          </CardHeader>
          <div className="pt-3">
            {payouts.error && <ErrorNotice error={payouts.error} onRetry={payouts.reload} className="mx-5 mb-4" />}
            {payouts.data === undefined && !payouts.error ? (
              <SkeletonCard lines={6} className="mx-5 mb-5 shadow-none" />
            ) : rows.length === 0 ? (
              <EmptyState icon={Send} title={status === '' ? 'No payouts yet' : 'No payouts in this state'} className="mx-5 mb-5">
                Create one with the form.
              </EmptyState>
            ) : (
              <Table>
                <THead>
                  <tr>
                    <TH>Payout</TH>
                    <TH>Customer</TH>
                    <TH numeric>Amount</TH>
                    <TH numeric>MXN out</TH>
                    <TH>Beneficiary</TH>
                    <TH>Status</TH>
                    <TH>Created</TH>
                  </tr>
                </THead>
                <TBody>
                  {rows.map((p) => (
                    <TR
                      key={p.id}
                      className="cursor-pointer"
                      onClick={() => {
                        navigate({ name: 'payout', id: p.id })
                      }}
                    >
                      <TD>
                        <a
                          href={routeHref({ name: 'payout', id: p.id })}
                          className="font-mono text-xs text-primary underline-offset-4 hover:underline"
                          onClick={(e) => {
                            e.stopPropagation()
                          }}
                        >
                          {shortId(p.id)}
                        </a>
                        <span className="block font-mono text-[11px] text-muted-foreground">{p.clave}</span>
                      </TD>
                      <TD className="whitespace-nowrap">{nameOf(p.customer_id) ?? shortId(p.customer_id)}</TD>
                      <TD numeric>
                        <Amount value={p.amount} />
                      </TD>
                      <TD numeric className="font-medium">
                        <Amount value={p.mxn} />
                      </TD>
                      <TD className="max-w-48 truncate text-xs" title={p.beneficiary_name}>
                        {p.beneficiary_name}
                      </TD>
                      <TD>
                        <PayoutStatusBadge status={p.status} />
                        {p.attempts > 1 && (
                          <span className="ml-1.5 text-[11px] text-muted-foreground" title="Submissions to the rail">
                            ×{p.attempts}
                          </span>
                        )}
                      </TD>
                      <TD className="whitespace-nowrap text-xs text-muted-foreground" title={formatDateTime(p.created_at)}>
                        {formatAgo(p.created_at)}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            )}
          </div>
        </Card>
        <div>
          <PayoutForm />
        </div>
      </div>
    </div>
  )
}
