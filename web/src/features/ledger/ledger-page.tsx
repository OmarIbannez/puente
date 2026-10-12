import { BookOpenText } from 'lucide-react'
import { useState } from 'react'

import { Amount } from '@/components/amount'
import { EmptyState, ErrorNotice } from '@/components/feedback'
import { PageHeader } from '@/components/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label, Select } from '@/components/ui/input'
import { SkeletonCard } from '@/components/ui/skeleton'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import { useAccounts } from '@/hooks/use-reference'
import { ENTRY_KINDS, getBalances, getEntries, type AccountBalance, type AccountType } from '@/lib/api'
import { ENTRY_KIND, humanize } from '@/lib/labels'
import { sumMinor, type Asset } from '@/lib/money'
import { navigate } from '@/lib/route'
import { JournalEntryCard } from '@/features/ledger/journal-entry'
import { TrialBalance } from '@/features/ledger/trial-balance'

const PAGE = 20

const TYPE_LABEL: Record<AccountType, string> = {
  asset: 'Assets',
  liability: 'Liabilities',
  equity: 'Equity',
  revenue: 'Revenue',
}

function ChartOfAccounts() {
  const { accounts } = useAccounts()
  const balances = useApi(getBalances, 'balances')
  const byKey = new Map<string, AccountBalance>((balances.data ?? []).map((b) => [`${String(b.code)}:${b.net.asset}`, b]))
  const trial: Partial<Record<Asset, number>> = {}
  for (const asset of ['USDC', 'MXNB', 'MXN'] as const) {
    trial[asset] = sumMinor((balances.data ?? []).filter((b) => b.net.asset === asset).map((b) => b.net))
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Chart of accounts</CardTitle>
        <CardDescription>
          Balances in each account's normal sign: debit-normal for assets, credit-normal for the rest. Customer balances (2010)
          are a sub-ledger with one party per customer.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 px-0 pb-0">
        {balances.error && <ErrorNotice error={balances.error} onRetry={balances.reload} className="mx-5" />}
        {!accounts || balances.data === undefined ? (
          <SkeletonCard lines={6} className="mx-5 mb-5 shadow-none" />
        ) : (
          <>
            <div className="px-5">
              <p className="mb-2 text-xs font-medium text-muted-foreground">Trial balance (sum of all postings)</p>
              <TrialBalance sums={trial} />
            </div>
            <Table>
              <THead>
                <tr>
                  <TH>Account</TH>
                  <TH>Type</TH>
                  <TH numeric>USDC</TH>
                  <TH numeric>MXNB</TH>
                  <TH numeric>MXN</TH>
                </tr>
              </THead>
              <TBody>
                {accounts.map((a) => (
                  <TR key={a.code}>
                    <TD>
                      <span className="font-mono text-xs text-muted-foreground">{a.code}</span> {a.name}
                      {a.per_party && (
                        <Badge variant="outline" className="ml-2">
                          per customer
                        </Badge>
                      )}
                    </TD>
                    <TD className="text-xs text-muted-foreground">{TYPE_LABEL[a.type]}</TD>
                    {(['USDC', 'MXNB', 'MXN'] as const).map((asset) => {
                      const b = byKey.get(`${String(a.code)}:${asset}`)
                      const holds = a.assets.includes(asset)
                      return (
                        <TD key={asset} numeric>
                          {b ? (
                            <Amount value={b.balance} bare signed />
                          ) : (
                            <span className="text-muted-foreground/50">{holds ? '0' : '·'}</span>
                          )}
                        </TD>
                      )
                    })}
                  </TR>
                ))}
              </TBody>
            </Table>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function Journal({ kind }: { kind: string | undefined }) {
  const [cursors, setCursors] = useState<number[]>([])
  const [forKind, setForKind] = useState(kind)
  // Reset paging when the filter changes (derived during render, not in an effect).
  if (forKind !== kind) {
    setForKind(kind)
    setCursors([])
  }
  const before = cursors.at(-1)
  const entries = useApi(() => getEntries({ kind, before, limit: PAGE }), `entries:${kind ?? ''}:${String(before)}`)
  const rows = entries.data ?? []

  return (
    <Card className="min-w-0">
      <CardHeader className="flex-row flex-wrap items-end justify-between gap-3">
        <div>
          <CardTitle>Journal</CardTitle>
          <CardDescription>Newest first. Every entry sums to zero per asset.</CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor="journal-kind" className="text-xs text-muted-foreground">
            Kind
          </Label>
          <Select
            id="journal-kind"
            className="h-8 w-auto text-xs"
            value={kind ?? ''}
            onChange={(e) => {
              navigate(e.target.value === '' ? { name: 'ledger' } : { name: 'ledger', kind: e.target.value })
            }}
          >
            <option value="">All kinds</option>
            {ENTRY_KINDS.map((k) => (
              <option key={k} value={k}>
                {ENTRY_KIND[k].label}
              </option>
            ))}
          </Select>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        {kind !== undefined && kind in ENTRY_KIND && (
          <p className="text-xs text-muted-foreground">{ENTRY_KIND[kind as keyof typeof ENTRY_KIND].hint}</p>
        )}
        {entries.error && <ErrorNotice error={entries.error} onRetry={entries.reload} />}
        {entries.data === undefined && !entries.error ? (
          <SkeletonCard lines={5} className="shadow-none" />
        ) : rows.length === 0 ? (
          <EmptyState icon={BookOpenText} title="No entries">
            {kind ? `No ${humanize(kind).toLowerCase()} entries on this page.` : 'Nothing has been posted yet.'}
          </EmptyState>
        ) : (
          rows.map((e) => <JournalEntryCard key={e.id} entry={e} />)
        )}
        <div className="flex items-center justify-between pt-1 text-xs text-muted-foreground">
          <span>{cursors.length === 0 ? 'Newest entries' : `Page ${String(cursors.length + 1)}`}</span>
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
                if (last) setCursors((c) => [...c, last.id])
              }}
            >
              Older
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

export function LedgerPage({ kind }: { kind: string | undefined }) {
  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Ledger"
        title="Double-entry, multi-asset"
        description="Every posting carries an asset and every entry balances per asset, checked in Go and again by a deferred trigger at commit. Entry refs are unique, so retries can post freely and nothing is booked twice."
      />
      <div className="grid gap-6 2xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="min-w-0">
          <ChartOfAccounts />
        </div>
        <Journal kind={kind} />
      </div>
    </div>
  )
}
