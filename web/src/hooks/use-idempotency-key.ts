import { useCallback, useState } from 'react'

import { newIdempotencyKey } from '@/lib/api'

/**
 * One Idempotency-Key per user intent. `fingerprint` describes the request
 * (its inputs): when it changes, the key changes, so an edited request is
 * never sent under a key the server has already stored a different answer
 * for. Retrying the same request reuses the key; call `rotate()` after a
 * success, or after a final 4xx (see `isFinalClientError`), to start over.
 */
export function useIdempotencyKey(fingerprint = ''): [string, () => void] {
  const [state, setState] = useState(() => ({ fingerprint, key: newIdempotencyKey() }))
  const rotate = useCallback(() => {
    setState((s) => ({ fingerprint: s.fingerprint, key: newIdempotencyKey() }))
  }, [])
  if (state.fingerprint !== fingerprint) {
    // Derived state: a new request gets a new key, during this render.
    const next = { fingerprint, key: newIdempotencyKey() }
    setState(next)
    return [next.key, rotate]
  }
  return [state.key, rotate]
}
