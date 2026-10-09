import { createContext, useContext } from 'react'

import type { ApiError, SandboxInfo } from '@/lib/api'

export type ToastTone = 'info' | 'success' | 'warning' | 'danger'

export interface ToastInput {
  title: string
  description?: string
  tone?: ToastTone
}

export interface AppContextValue {
  /** The visitor's sandbox; undefined until the first /api/sandbox call returns. */
  sandbox: SandboxInfo | undefined
  /** Set when /api/sandbox could not be reached (yet). */
  sandboxError: ApiError | undefined
  /** Replaces the sandbox after a call that returns it (faults, reset). */
  setSandbox: (sandbox: SandboxInfo) => void
  /** Bumped after anything that changes sandbox state; every useApi reloads. */
  epoch: number
  bump: () => void
  toast: (toast: ToastInput) => void
  /** Starts waiting for the server again after giving up. */
  retry: () => void
  /** When the current wait for the server began (ms since epoch). */
  wakeStartedAt: number
}

export const AppContext = createContext<AppContextValue | null>(null)

export function useApp(): AppContextValue {
  const ctx = useContext(AppContext)
  if (!ctx) throw new Error('useApp must be used inside <AppProvider>')
  return ctx
}

/** The sandbox, for components rendered only after it loaded. */
export function useSandbox(): SandboxInfo {
  const { sandbox } = useApp()
  if (!sandbox) throw new Error('useSandbox used before the sandbox loaded')
  return sandbox
}
