import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

export function Skeleton({ className, ...props }: ComponentProps<'div'>) {
  return <div aria-hidden className={cn('animate-pulse rounded-md bg-surface-3', className)} {...props} />
}

/** A card-shaped placeholder with a few text lines. */
export function SkeletonCard({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div className={cn('rounded-xl border border-border bg-card p-5 shadow-card', className)} aria-busy>
      <Skeleton className="h-4 w-1/3" />
      <div className="mt-4 space-y-2.5">
        {Array.from({ length: lines }, (_, i) => (
          <Skeleton key={i} className={cn('h-3', i % 2 === 0 ? 'w-full' : 'w-4/5')} />
        ))}
      </div>
    </div>
  )
}
