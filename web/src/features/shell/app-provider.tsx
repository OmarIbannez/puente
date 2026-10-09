import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'

import { AppContext, type AppContextValue, type ToastInput } from '@/hooks/app-context'
import { getSandbox, toApiError, type ApiError, type SandboxInfo } from '@/lib/api'
import { Toaster, type ToastItem } from '@/features/shell/toaster'

/** Retry delays while the server wakes up (Render's free tier sleeps). */
const WAKE_DELAYS_MS = [1_500, 3_000, 5_000, 8_000]
/** Give up waiting after this long and show the error with a retry button. */
const WAKE_BUDGET_MS = 120_000
/** How often pages refresh while visible: the server's loop moves sandboxes on its own. */
const REFRESH_MS = 20_000

/** Network failures and gateway errors are what a sleeping server looks like. */
function isWaking(err: ApiError): boolean {
  return err.status === 0 || err.status === 502 || err.status === 503 || err.status === 504
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [sandbox, setSandbox] = useState<SandboxInfo | undefined>(undefined)
  const [sandboxError, setSandboxError] = useState<ApiError | undefined>(undefined)
  const [epoch, setEpoch] = useState(0)
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const [attempt, setAttempt] = useState(0)
  const [wakeStartedAt, setWakeStartedAt] = useState(() => Date.now())
  const ready = sandbox !== undefined

  // The first /api/sandbox call creates and seeds the visitor's sandbox and
  // sets its cookie. Nothing else is requested until it returns, so pages
  // don't race to create several sandboxes. A cold server is retried.
  useEffect(() => {
    if (ready) return
    let live = true
    let timer: number | undefined
    getSandbox().then(
      (s) => {
        if (!live) return
        setSandbox(s)
        setSandboxError(undefined)
      },
      (err: unknown) => {
        if (!live) return
        const apiError = toApiError(err)
        setSandboxError(apiError)
        const elapsed = Date.now() - wakeStartedAt
        if (isWaking(apiError) && elapsed < WAKE_BUDGET_MS) {
          const delay = WAKE_DELAYS_MS[Math.min(attempt, WAKE_DELAYS_MS.length - 1)] ?? 5_000
          timer = window.setTimeout(() => {
            setAttempt((a) => a + 1)
          }, delay)
        }
      },
    )
    return () => {
      live = false
      window.clearTimeout(timer)
    }
  }, [ready, attempt, wakeStartedAt])

  // After the first load, refresh the sandbox (armed faults get consumed)
  // whenever anything changes.
  useEffect(() => {
    if (!ready || epoch === 0) return
    let live = true
    getSandbox().then(
      (s) => {
        if (live) setSandbox(s)
      },
      () => undefined,
    )
    return () => {
      live = false
    }
  }, [ready, epoch])

  const bump = useCallback(() => {
    setEpoch((e) => e + 1)
  }, [])

  // The background worker ticks active sandboxes and runs scheduled recon,
  // so data moves without the visitor doing anything.
  useEffect(() => {
    if (!ready) return
    const id = window.setInterval(() => {
      if (document.visibilityState === 'visible') bump()
    }, REFRESH_MS)
    return () => {
      window.clearInterval(id)
    }
  }, [ready, bump])

  const retry = useCallback(() => {
    setWakeStartedAt(Date.now())
    setSandboxError(undefined)
    setAttempt((a) => a + 1)
  }, [])

  const toast = useCallback((t: ToastInput) => {
    setToasts((list) => [...list.slice(-3), { ...t, id: crypto.randomUUID() }])
  }, [])

  const dismiss = useCallback((id: string) => {
    setToasts((list) => list.filter((t) => t.id !== id))
  }, [])

  const value = useMemo<AppContextValue>(
    () => ({ sandbox, sandboxError, setSandbox, epoch, bump, toast, retry, wakeStartedAt }),
    [sandbox, sandboxError, epoch, bump, toast, retry, wakeStartedAt],
  )

  return (
    <AppContext value={value}>
      {children}
      <Toaster toasts={toasts} onDismiss={dismiss} />
    </AppContext>
  )
}
