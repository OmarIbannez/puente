import { LoaderCircle, RefreshCw, WifiOff } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { useApp } from '@/hooks/app-context'
import { Logo } from '@/features/shell/nav'

/** After this long, the wait is explained as a cold start. */
const COLD_AFTER_MS = 4_000
/** Matches the provider's budget: after it, retries stop. */
const GIVE_UP_AFTER_MS = 120_000

const STEPS = [
  'Waking up the demo server…',
  'Connecting to Postgres…',
  'Creating your sandbox: a simulated chain and a simulated SPEI bank…',
  'Seeding customers, deposits and payouts…',
]

/**
 * Shown until the first /api/sandbox call returns. The free hosting tier
 * sleeps when idle, so the first visit can take up to a minute.
 */
export function WakeScreen() {
  const { sandboxError, wakeStartedAt, retry } = useApp()
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    const id = window.setInterval(() => {
      setNow(Date.now())
    }, 1_000)
    return () => {
      window.clearInterval(id)
    }
  }, [])

  const elapsed = Math.max(0, now - wakeStartedAt)
  const seconds = Math.floor(elapsed / 1000)
  const waking = sandboxError === undefined || sandboxError.status === 0 || sandboxError.status >= 502
  const gaveUp = sandboxError !== undefined && (!waking || elapsed >= GIVE_UP_AFTER_MS)
  const cold = elapsed >= COLD_AFTER_MS
  const step = STEPS[Math.min(STEPS.length - 1, Math.floor(seconds / 12))] ?? STEPS[0]

  return (
    <div className="grid min-h-dvh place-items-center bg-dots px-4">
      <div className="w-full max-w-md rounded-xl border border-border bg-card p-6 shadow-lift" aria-live="polite">
        <Logo />
        {gaveUp ? (
          <div role="alert" className="mt-6">
            <p className="flex items-center gap-2 font-medium">
              <WifiOff className="size-4 text-destructive" aria-hidden />
              Can't reach the Puente API
            </p>
            <p className="mt-1.5 text-sm text-muted-foreground">{sandboxError.message}</p>
            <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">
              {sandboxError.code}
              {sandboxError.status > 0 && ` · HTTP ${String(sandboxError.status)}`}
            </p>
            <p className="mt-3 text-sm text-muted-foreground">
              Running locally? Start the server with <code className="font-mono text-foreground">puente serve</code> on :8000.
            </p>
            <Button className="mt-4" onClick={retry}>
              <RefreshCw />
              Try again
            </Button>
          </div>
        ) : (
          <div className="mt-6" aria-busy="true">
            <p className="flex items-center gap-2 font-medium">
              <LoaderCircle className="size-4 animate-spin text-primary" aria-hidden />
              {cold ? step : 'Opening your sandbox…'}
            </p>
            {cold && (
              <p className="mt-2 text-sm text-muted-foreground">
                The demo runs on a free tier that sleeps when nobody is using it. The first visit can take up to a minute;
                after that it is fast. <span className="tabular">({String(seconds)}s)</span>
              </p>
            )}
            <div className="mt-5 h-1 overflow-hidden rounded-full bg-surface-3">
              <div className="h-full w-1/3 animate-pulse rounded-full bg-primary" />
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
