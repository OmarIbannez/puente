import {
  ArrowDownToLine,
  BookOpenText,
  Landmark,
  LayoutDashboard,
  Radio,
  ScanSearch,
  Send,
  type LucideIcon,
} from 'lucide-react'

import type { Route, RouteName } from '@/lib/route'

export type ViewName = 'overview' | 'deposits' | 'payouts' | 'ledger' | 'bank' | 'recon' | 'live'

export interface View {
  name: ViewName
  label: string
  icon: LucideIcon
  /** One line under the label in the nav. */
  blurb: string
  route: Route
}

export const VIEWS: readonly View[] = [
  { name: 'overview', label: 'Overview', icon: LayoutDashboard, blurb: 'Balances and invariants', route: { name: 'overview' } },
  { name: 'deposits', label: 'Deposits', icon: ArrowDownToLine, blurb: 'Chain transfers in', route: { name: 'deposits' } },
  { name: 'payouts', label: 'Payouts', icon: Send, blurb: 'SPEI transfers out', route: { name: 'payouts' } },
  { name: 'ledger', label: 'Ledger', icon: BookOpenText, blurb: 'Double-entry books', route: { name: 'ledger' } },
  { name: 'bank', label: 'Bank (SPEI)', icon: Landmark, blurb: "The bank's books", route: { name: 'bank' } },
  { name: 'recon', label: 'Reconciliation', icon: ScanSearch, blurb: 'Breaks and fault lab', route: { name: 'recon' } },
  { name: 'live', label: 'Live testnet', icon: Radio, blurb: 'Real Sepolia deposits', route: { name: 'live' } },
]

const VIEW_OF: Record<RouteName, ViewName> = {
  overview: 'overview',
  deposits: 'deposits',
  payouts: 'payouts',
  payout: 'payouts',
  ledger: 'ledger',
  bank: 'bank',
  recon: 'recon',
  live: 'live',
}

export function viewOf(route: Route): ViewName {
  return VIEW_OF[route.name]
}
