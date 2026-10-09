import { createContext, useContext } from 'react'

import type { AccountInfo, Customer } from '@/lib/api'

/** Data many components label things with, loaded once by <ReferenceProvider>. */
export interface Reference {
  customers: Customer[] | undefined
  accounts: AccountInfo[] | undefined
  nameOf: (customerId: string | undefined) => string | undefined
  accountOf: (code: number) => AccountInfo | undefined
}

export const ReferenceContext = createContext<Reference | null>(null)

function useReference(): Reference {
  const ctx = useContext(ReferenceContext)
  if (!ctx) throw new Error('useReference must be used inside <ReferenceProvider>')
  return ctx
}

/** The sandbox's customers, plus an id → name lookup for party columns. */
export function useCustomers(): Pick<Reference, 'customers' | 'nameOf'> {
  return useReference()
}

/** The chart of accounts, plus a code → account lookup. */
export function useAccounts(): Pick<Reference, 'accounts' | 'accountOf'> {
  return useReference()
}
