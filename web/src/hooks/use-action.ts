import { useCallback, useState } from 'react'

import { toApiError, type ApiError } from '@/lib/api'

export type ActionResult<T> = { ok: true; data: T } | { ok: false; error: ApiError }

export interface ActionState<A extends unknown[], T> {
  run: (...args: A) => Promise<ActionResult<T>>
  pending: boolean
  /** The last failure, kept for inline display until the next run. */
  error: ApiError | undefined
  clearError: () => void
}

/** Wraps a mutation: tracks pending state and the last ApiError. Never rejects. */
export function useAction<A extends unknown[], T>(fn: (...args: A) => Promise<T>): ActionState<A, T> {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<ApiError | undefined>(undefined)

  const run = useCallback(
    async (...args: A): Promise<ActionResult<T>> => {
      setPending(true)
      setError(undefined)
      try {
        return { ok: true, data: await fn(...args) }
      } catch (err) {
        const apiError = toApiError(err)
        setError(apiError)
        return { ok: false, error: apiError }
      } finally {
        setPending(false)
      }
    },
    [fn],
  )

  const clearError = useCallback(() => {
    setError(undefined)
  }, [])

  return { run, pending, error, clearError }
}
