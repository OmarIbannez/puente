import { useEffect } from 'react'

import { TooltipProvider } from '@/components/ui/tooltip'
import { useApp } from '@/hooks/app-context'
import { useHashRoute } from '@/hooks/use-hash-route'
import { HOME, routeHref, type Route } from '@/lib/route'
import { BankPage } from '@/features/bank/bank-page'
import { DepositsPage } from '@/features/deposits/deposits-page'
import { LedgerPage } from '@/features/ledger/ledger-page'
import { LivePage } from '@/features/live/live-page'
import { OverviewPage } from '@/features/overview/overview-page'
import { PayoutDetailPage } from '@/features/payouts/payout-detail-page'
import { PayoutsPage } from '@/features/payouts/payouts-page'
import { ReconPage } from '@/features/recon/recon-page'
import { AppProvider } from '@/features/shell/app-provider'
import { REPO_URL, SideNav } from '@/features/shell/nav'
import { ReferenceProvider } from '@/features/shell/reference-provider'
import { SandboxBar } from '@/features/shell/sandbox-bar'
import { viewOf } from '@/features/shell/views'
import { WakeScreen } from '@/features/shell/wake-screen'

function Page({ route }: { route: Route }) {
  switch (route.name) {
    case 'overview':
      return <OverviewPage />
    case 'deposits':
      return <DepositsPage />
    case 'payouts':
      return <PayoutsPage />
    case 'payout':
      return <PayoutDetailPage id={route.id} />
    case 'ledger':
      return <LedgerPage kind={route.kind} />
    case 'bank':
      return <BankPage />
    case 'recon':
      return <ReconPage runId={route.runId} />
    case 'live':
      return <LivePage />
  }
}

function Shell() {
  const route = useHashRoute()
  const { sandbox } = useApp()
  const effective = route ?? HOME

  useEffect(() => {
    if (route === null) window.history.replaceState(null, '', routeHref(HOME))
  }, [route])

  useEffect(() => {
    window.scrollTo({ top: 0 })
  }, [effective.name])

  // Nothing else is requested until the first /api/sandbox call has set the
  // sandbox cookie; parallel first requests would each create a sandbox.
  if (!sandbox) return <WakeScreen />

  return (
    <ReferenceProvider>
      <div className="min-h-dvh lg:grid lg:grid-cols-[15.5rem_minmax(0,1fr)]">
        <SideNav current={viewOf(effective)} />
        <div className="min-w-0">
          <SandboxBar />
          <main className="mx-auto max-w-7xl px-4 py-6 sm:px-6 sm:py-8">
            <Page route={effective} />
          </main>
          <footer className="mx-auto max-w-7xl px-4 pb-10 text-xs text-muted-foreground sm:px-6">
            Puente is a demo. The chain and the SPEI bank in your sandbox are simulated, and receipts are marked{' '}
            <span className="font-mono">simulado</span>; no real funds move. The live view only reads public testnets.{' '}
            <a href={REPO_URL} target="_blank" rel="noreferrer" className="text-primary underline-offset-4 hover:underline">
              Source on GitHub
            </a>
            .
          </footer>
        </div>
      </div>
    </ReferenceProvider>
  )
}

export default function App() {
  return (
    <AppProvider>
      <TooltipProvider delayDuration={200}>
        <Shell />
      </TooltipProvider>
    </AppProvider>
  )
}
