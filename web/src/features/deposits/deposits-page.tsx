import { ArrowDownToLine, LoaderCircle } from 'lucide-react'
import { useState, type SubmitEvent } from 'react'

import { Amount } from '@/components/amount'
import { EmptyState, ErrorNotice } from '@/components/feedback'
import { PageHeader } from '@/components/page-header'
import { SimulatedBadge } from '@/components/status-badge'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input, Label, MoneyInput, Select } from '@/components/ui/input'
import { SkeletonCard } from '@/components/ui/skeleton'
import { useAction } from '@/hooks/use-action'
import { useApi } from '@/hooks/use-api'
import { useApp, useSandbox } from '@/hooks/app-context'
import { useIdempotencyKey } from '@/hooks/use-idempotency-key'
import { useCustomers } from '@/hooks/use-reference'
import { getDeposits, simulateDeposit, type Deposit } from '@/lib/api'
import { shortHash } from '@/lib/format'
import { parseMoney, STABLECOINS, type Stablecoin } from '@/lib/money'
import { DepositsTable } from '@/features/deposits/deposits-table'

const PAGE = 25

function SimulateDepositForm() {
  const { customers } = useCustomers()
  const { bump, toast } = useApp()
  const [customerId, setCustomerId] = useState('')
  const [asset, setAsset] = useState<Stablecoin>('USDC')
  const [amount, setAmount] = useState('250')
  const [key, rotate] = useIdempotencyKey()
  const action = useAction(simulateDeposit)

  const selected = customerId === '' ? customers?.[0] : customers?.find((c) => c.id === customerId)
  const parsed = parseMoney(asset, amount)
  const amountError = amount === '' ? undefined : parsed.ok ? (parsed.minor <= 0 ? 'Must be positive' : undefined) : parsed.error
  const canSubmit = selected !== undefined && parsed.ok && parsed.minor > 0 && !action.pending

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    if (!selected || !parsed.ok) return
    const result = await action.run({ customer_id: selected.id, asset, amount: parsed.decimal }, key)
    if (!result.ok) return
    rotate()
    bump()
    toast({
      title: 'Transfer on the simulated chain',
      description: `${shortHash(result.data.tx_hash)} in block ${String(result.data.block_number)}. Advance 3 ticks to confirm it; then it is credited exactly once.`,
      tone: 'success',
    })
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-2">
          <CardTitle>Simulate a deposit</CardTitle>
          <SimulatedBadge label="sim chain" />
        </div>
        <CardDescription>
          Sends stablecoins to the customer's deposit address on your simulated chain. The scanner picks it up and credits it
          after 3 confirmations.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="space-y-3" onSubmit={(e) => void submit(e)}>
          <div className="space-y-1.5">
            <Label htmlFor="dep-customer">Customer</Label>
            <Select
              id="dep-customer"
              value={selected?.id ?? ''}
              onChange={(e) => {
                setCustomerId(e.target.value)
              }}
            >
              {(customers ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
            {selected && (
              <p className="flex flex-wrap gap-x-3 text-xs text-muted-foreground">
                {STABLECOINS.map((a) => {
                  const avail = selected.available[a]
                  return avail ? (
                    <span key={a}>
                      Available <Amount value={avail} />
                    </span>
                  ) : null
                })}
              </p>
            )}
          </div>
          <div className="grid grid-cols-[7rem_1fr] gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="dep-asset">Asset</Label>
              <Select
                id="dep-asset"
                value={asset}
                onChange={(e) => {
                  setAsset(e.target.value === 'MXNB' ? 'MXNB' : 'USDC')
                }}
              >
                {STABLECOINS.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="dep-amount">Amount</Label>
              <MoneyInput
                id="dep-amount"
                asset={asset}
                value={amount}
                aria-invalid={amountError !== undefined}
                aria-describedby={amountError ? 'dep-amount-error' : undefined}
                onChange={(e) => {
                  setAmount(e.target.value)
                }}
              />
            </div>
          </div>
          {amountError && (
            <p id="dep-amount-error" className="text-xs text-destructive">
              {amountError}
            </p>
          )}
          <div className="space-y-1">
            <Label htmlFor="dep-key" className="text-xs text-muted-foreground">
              Idempotency-Key
            </Label>
            <Input id="dep-key" readOnly value={key} className="h-8 font-mono text-xs text-muted-foreground" />
          </div>
          {action.error && (
            <Alert tone="danger" role="alert">
              {action.error.message}
            </Alert>
          )}
          <Button type="submit" className="w-full" disabled={!canSubmit}>
            {action.pending ? <LoaderCircle className="animate-spin" /> : <ArrowDownToLine />}
            Send to deposit address
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}

export function DepositsPage() {
  const sandbox = useSandbox()
  const [network, setNetwork] = useState('')
  const [cursors, setCursors] = useState<number[]>([])
  const before = cursors.at(-1)
  const deposits = useApi(() => getDeposits({ network, before, limit: PAGE }), `deposits:${network}:${String(before)}`)
  const rows: Deposit[] = deposits.data ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Deposits"
        title="Chain transfers in"
        description="Every Transfer log the scanner read, newest first. Ingestion is idempotent on (chain, tx hash, log index); a transfer is credited once it has enough confirmations, and reversed if a reorg orphans it."
      />
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <Card className="min-w-0 lg:order-1">
          <CardHeader className="flex-row flex-wrap items-center justify-between gap-3 pb-1">
            <CardTitle>Transfers</CardTitle>
            <div className="flex items-center gap-2">
              <Label htmlFor="dep-network" className="text-xs text-muted-foreground">
                Network
              </Label>
              <Select
                id="dep-network"
                className="h-8 w-auto text-xs"
                value={network}
                onChange={(e) => {
                  setNetwork(e.target.value)
                  setCursors([])
                }}
              >
                <option value="">All networks</option>
                {sandbox.networks.map((n) => (
                  <option key={n.key} value={n.key}>
                    {n.name}
                  </option>
                ))}
              </Select>
            </div>
          </CardHeader>
          <div className="pt-3">
            {deposits.error && <ErrorNotice error={deposits.error} onRetry={deposits.reload} className="mx-5 mb-4" />}
            {deposits.data === undefined && !deposits.error ? (
              <SkeletonCard lines={6} className="mx-5 mb-5 shadow-none" />
            ) : rows.length === 0 ? (
              <EmptyState icon={ArrowDownToLine} title="No transfers yet" className="mx-5 mb-5">
                Simulate a deposit to see the scanner pick it up.
              </EmptyState>
            ) : (
              <DepositsTable deposits={rows} networks={sandbox.networks} />
            )}
          </div>
          <div className="flex items-center justify-between border-t border-border px-5 py-2.5 text-xs text-muted-foreground">
            <span>{cursors.length === 0 ? 'Newest first' : `Page ${String(cursors.length + 1)}`}</span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={cursors.length === 0}
                onClick={() => {
                  setCursors((c) => c.slice(0, -1))
                }}
              >
                Newer
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={rows.length < PAGE}
                onClick={() => {
                  const last = rows.at(-1)
                  if (last) setCursors((c) => [...c, last.transfer_id])
                }}
              >
                Older
              </Button>
            </div>
          </div>
        </Card>
        <div className="lg:order-2">
          <SimulateDepositForm />
        </div>
      </div>
    </div>
  )
}
