import { useCallback, useEffect, useRef, useState } from 'react'

import { toApiError, type ApiError } from '@/lib/api'
import { useApp } from '@/hooks/app-context'

export interface ApiState<T> {
  data: T | undefined
  error: ApiError | undefined
  /** True while the current key has not resolved yet (stale data stays visible). */
  loading: boolean
  reload: () => void
}

interface Settled<T> {
  key: string
  data?: T
  error?: ApiError
}

/**
 * Loads `load()` whenever `key` changes, the user reloads, or the sandbox
 * changes (advance, reset, faults, any write). Pass `null` as key to skip loading.
 */
export function useApi<T>(load: () => Promise<T>, key: string | null): ApiState<T> {
  const { epoch } = useApp()
  const [version, setVersion] = useState(0)
  const [settled, setSettled] = useState<Settled<T>>({ key: '' })
  const loadRef = useRef(load)

  useEffect(() => {
    loadRef.current = load
  })

  const requestKey = key === null ? null : `${key}#${epoch}#${version}`

  useEffect(() => {
    if (requestKey === null) return
    let live = true
    loadRef.current().then(
      (data) => {
        if (live) setSettled({ key: requestKey, data })
      },
      (err: unknown) => {
        if (live) setSettled((prev) => ({ key: requestKey, data: prev.data, error: toApiError(err) }))
      },
    )
    return () => {
      live = false
    }
  }, [requestKey])

  const reload = useCallback(() => {
    setVersion((v) => v + 1)
  }, [])

  const current = settled.key === requestKey
  return {
    data: settled.data,
    error: current ? settled.error : undefined,
    loading: requestKey !== null && !current,
    reload,
  }
}
