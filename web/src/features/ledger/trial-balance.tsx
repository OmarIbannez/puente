import { CircleCheck, CircleX } from 'lucide-react'

import { Tooltip } from '@/components/ui/tooltip'
import { formatMoney, type Asset } from '@/lib/money'
import { cn } from '@/lib/utils'

const ORDER: readonly Asset[] = ['USDC', 'MXNB', 'MXN']

/**
 * The trial balance per asset: the sum of every posting. Each asset
 * balances on its own, so each must be exactly zero.
 */
export function TrialBalance({ sums, className }: { sums: Partial<Record<Asset, number>>; className?: string }) {
  return (
    <ul className={cn('grid gap-2 sm:grid-cols-3', className)} aria-label="Trial balance per asset">
      {ORDER.map((asset) => {
        const sum = sums[asset] ?? 0
        const ok = sum === 0
        return (
          <li
            key={asset}
            className={cn(
              'flex items-center justify-between gap-3 rounded-md border px-3 py-2',
              ok ? 'border-success/30 bg-success/6' : 'border-destructive/40 bg-destructive/8',
            )}
          >
            <span className="text-sm font-medium">{asset}</span>
            <Tooltip content={ok ? 'Debits equal credits for this asset.' : 'Debits and credits differ. This should never happen.'}>
              <span tabIndex={0} className="inline-flex items-center gap-1.5 text-sm">
                <span className="tabular font-mono text-xs">{ok ? '0' : formatMoney({ asset, minor: sum })}</span>
                {ok ? (
                  <CircleCheck className="size-4 text-success" aria-label="balanced" />
                ) : (
                  <CircleX className="size-4 text-destructive" aria-label="unbalanced" />
                )}
              </span>
            </Tooltip>
          </li>
        )
      })}
    </ul>
  )
}
