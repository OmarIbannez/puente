import type { ReactNode } from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

interface StatTileProps {
  label: string
  value: ReactNode
  hint?: ReactNode
  loading?: boolean
  className?: string
}

/** A single headline number. Values use text ink; color is reserved for marks. */
export function StatTile({ label, value, hint, loading = false, className }: StatTileProps) {
  return (
    <div className={cn('rounded-xl border border-border bg-card p-4 shadow-card', className)}>
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      {loading ? (
        <Skeleton className="mt-2 h-7 w-24" />
      ) : (
        <p className="mt-1 text-2xl font-semibold tracking-tight">{value}</p>
      )}
      {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
    </div>
  )
}
