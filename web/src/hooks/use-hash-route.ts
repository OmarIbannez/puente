import { useMemo, useSyncExternalStore } from 'react'

import { parseHash, type Route } from '@/lib/route'

function subscribe(onChange: () => void): () => void {
  window.addEventListener('hashchange', onChange)
  return () => {
    window.removeEventListener('hashchange', onChange)
  }
}

function snapshot(): string {
  return window.location.hash
}

/** The current hash route (null when the hash is empty or unknown). */
export function useHashRoute(): Route | null {
  const hash = useSyncExternalStore(subscribe, snapshot)
  return useMemo(() => parseHash(hash), [hash])
}
