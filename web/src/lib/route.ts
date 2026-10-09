export type Route =
  | { name: 'overview' }
  | { name: 'deposits' }
  | { name: 'payouts' }
  | { name: 'payout'; id: string }
  | { name: 'ledger'; kind?: string }
  | { name: 'bank' }
  | { name: 'recon'; runId?: number }
  | { name: 'live' }

export type RouteName = Route['name']

export const HOME: Route = { name: 'overview' }

/** Parses `#/payouts/<id>`, `#/ledger?kind=deposit`, `#/recon/12`, … Null when unknown. */
export function parseHash(hash: string): Route | null {
  const raw = hash.replace(/^#\/?/, '')
  const [path = '', query = ''] = raw.split('?')
  const params = new URLSearchParams(query)
  const segments = path.split('/').filter(Boolean).map(decodeURIComponent)
  const [head, id] = segments
  switch (head) {
    case 'overview':
      return { name: 'overview' }
    case 'deposits':
      return { name: 'deposits' }
    case 'payouts':
      return id ? { name: 'payout', id } : { name: 'payouts' }
    case 'ledger': {
      const kind = params.get('kind')
      return kind ? { name: 'ledger', kind } : { name: 'ledger' }
    }
    case 'bank':
      return { name: 'bank' }
    case 'recon': {
      if (id === undefined) return { name: 'recon' }
      const runId = /^\d+$/.test(id) ? Number(id) : Number.NaN
      return Number.isSafeInteger(runId) && runId > 0 ? { name: 'recon', runId } : { name: 'recon' }
    }
    case 'live':
      return { name: 'live' }
    default:
      return null
  }
}

export function routeHref(route: Route): string {
  switch (route.name) {
    case 'payout':
      return `#/payouts/${encodeURIComponent(route.id)}`
    case 'ledger':
      return route.kind ? `#/ledger?kind=${encodeURIComponent(route.kind)}` : '#/ledger'
    case 'recon':
      return route.runId === undefined ? '#/recon' : `#/recon/${String(route.runId)}`
    default:
      return `#/${route.name}`
  }
}

export function navigate(route: Route): void {
  window.location.hash = routeHref(route)
}
