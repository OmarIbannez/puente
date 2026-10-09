import { useCallback, useState } from 'react'

import { newIdempotencyKey } from '@/lib/api'

/**
 * One Idempotency-Key per user intent: retries after an error reuse it, and
 * `rotate()` after a success makes the next submission a new request.
 */
export function useIdempotencyKey(): [string, () => void] {
  const [key, setKey] = useState(newIdempotencyKey)
  const rotate = useCallback(() => {
    setKey(newIdempotencyKey())
  }, [])
  return [key, rotate]
}
