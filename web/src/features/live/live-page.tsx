import { ExternalLink, Eye, Radio } from 'lucide-react'

import { EmptyState, ErrorNotice } from '@/components/feedback'
import { CopyButton, Hash } from '@/components/hash'
import { PageHeader } from '@/components/page-header'
import { Alert } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { SkeletonCard } from '@/components/ui/skeleton'
import { useApi } from '@/hooks/use-api'
import { getLive, type Network } from '@/lib/api'
import { explorerAddressUrl } from '@/lib/labels'
import { DepositsTable } from '@/features/deposits/deposits-table'
import { ReconSummary } from '@/features/recon/recon-summary'

const ENV = [
  { name: 'PUENTE_LIVE_USDC_RPC', what: 'A Base Sepolia RPC URL; follows Circle USDC (chain 84532).' },
  { name: 'PUENTE_LIVE_MXNB_RPC', what: 'An Arbitrum Sepolia RPC URL; follows Bitso MXNB (chain 421614).' },
  { name: 'PUENTE_LIVE_WATCH_ADDRESS', what: 'The deposit address to watch on every live network (0x…).' },
  { name: 'PUENTE_LIVE_CONFIRMATIONS', what: 'Optional. Blocks before a live deposit is credited (default 5).' },
]

function Disabled() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Live mode is off on this server</CardTitle>
        <CardDescription>
          When it is on, a shared, read-only sandbox follows real testnet deposits to one operator address and reconciles them
          like any other. It only reads public RPCs; the API never sends funds.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm">Set these environment variables and restart the server:</p>
        <dl className="divide-y divide-border rounded-md border border-border">
          {ENV.map((e) => (
            <div key={e.name} className="grid gap-1 px-3 py-2 sm:grid-cols-[16rem_1fr]">
              <dt className="font-mono text-xs">{e.name}</dt>
              <dd className="text-xs text-muted-foreground">{e.what}</dd>
            </div>
          ))}
        </dl>
        <p className="text-xs text-muted-foreground">
          Your own sandbox keeps working without it: its chain and bank are simulated.
        </p>
      </CardContent>
    </Card>
  )
}

function NetworkRow({ network, address }: { network: Network; address: string | undefined }) {
  const addressUrl = address ? explorerAddressUrl(network, address) : undefined
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5 text-sm">
      <span className="min-w-48 font-medium">{network.name}</span>
      <Badge variant="neutral">{network.asset}</Badge>
      <span className="text-xs text-muted-foreground">chain {network.chain_id}</span>
      <span className="text-xs text-muted-foreground">
        token <Hash value={network.token} copy={false} />
      </span>
      {addressUrl && (
        <a
          href={addressUrl}
          target="_blank"
          rel="noreferrer"
          className="ml-auto inline-flex items-center gap-1 text-xs text-primary underline-offset-4 hover:underline"
        >
          Address on explorer <ExternalLink className="size-3" aria-hidden />
        </a>
      )}
    </li>
  )
}

export function LivePage() {
  const live = useApi(getLive, 'live')
  const data = live.data
  const networks = data?.networks ?? []
  const deposits = data?.deposits ?? []

  return (
    <div className="space-y-6">
      <PageHeader
        eyebrow="Live testnet"
        title="Real deposits, read-only"
        description="The same scanner, ledger and reconciliation, pointed at public testnets instead of the simulated chain. Shared by every visitor and read-only."
        actions={
          <Badge variant="outline" size="lg">
            <Eye /> Read-only
          </Badge>
        }
      />
      {live.error && <ErrorNotice error={live.error} onRetry={live.reload} />}
      {!data ? (
        !live.error && <SkeletonCard lines={4} />
      ) : !data.enabled ? (
        <Disabled />
      ) : (
        <>
          <Card>
            <CardHeader>
              <CardTitle>Watched address</CardTitle>
              <CardDescription>
                Send testnet USDC (Base Sepolia) or MXNB (Arbitrum Sepolia) here, from a faucet or your wallet. It shows up
                below after the scanner sees it, and is credited after the confirmations.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              {data.address ? (
                <div className="flex items-center gap-2 rounded-md border border-border bg-surface-1 px-3 py-2">
                  <code className="min-w-0 flex-1 break-all font-mono text-sm">{data.address}</code>
                  <CopyButton value={data.address} label="Copy address" />
                </div>
              ) : (
                <Alert tone="warning">No address is configured.</Alert>
              )}
              <ul className="divide-y divide-border rounded-md border border-border" aria-label="Live networks">
                {networks.map((n) => (
                  <NetworkRow key={n.key} network={n} address={data.address} />
                ))}
              </ul>
            </CardContent>
          </Card>

          <Card className="min-w-0">
            <CardHeader className="pb-3">
              <CardTitle>Deposits</CardTitle>
              <CardDescription>The latest 50 transfers to the watched address, with explorer links.</CardDescription>
            </CardHeader>
            {deposits.length === 0 ? (
              <EmptyState icon={Radio} title="No live deposits yet" className="mx-5 mb-5">
                Send some testnet tokens to the address above.
              </EmptyState>
            ) : (
              <DepositsTable deposits={deposits} networks={networks} />
            )}
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Latest reconciliation</CardTitle>
              <CardDescription>Ledger credits re-checked against the canonical testnet.</CardDescription>
            </CardHeader>
            <CardContent>
              <ReconSummary run={data.latest_recon} link={false} />
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}
