import { useMemo, type ReactNode } from 'react'

import { useApi } from '@/hooks/use-api'
import { ReferenceContext, type Reference } from '@/hooks/use-reference'
import { getAccounts, getCustomers } from '@/lib/api'

/** Loads customers and the chart of accounts once for every page. */
export function ReferenceProvider({ children }: { children: ReactNode }) {
  const customers = useApi(getCustomers, 'customers').data
  const accounts = useApi(getAccounts, 'accounts').data

  const value = useMemo<Reference>(() => {
    const names = new Map((customers ?? []).map((c) => [c.id, c.name]))
    const byCode = new Map((accounts ?? []).map((a) => [a.code, a]))
    return {
      customers,
      accounts,
      nameOf: (id) => (id === undefined ? undefined : names.get(id)),
      accountOf: (code) => byCode.get(code),
    }
  }, [customers, accounts])

  return <ReferenceContext value={value}>{children}</ReferenceContext>
}
