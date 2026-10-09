import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

/** A dense data table that scrolls sideways on narrow screens. */
export function Table({ className, ...props }: ComponentProps<'table'>) {
  return (
    <div className="-mx-px overflow-x-auto">
      <table className={cn('w-full border-collapse text-sm', className)} {...props} />
    </div>
  )
}

export function THead({ className, ...props }: ComponentProps<'thead'>) {
  return <thead className={cn('border-b border-border bg-surface-1', className)} {...props} />
}

export function TBody({ className, ...props }: ComponentProps<'tbody'>) {
  return <tbody className={cn('divide-y divide-border', className)} {...props} />
}

export function TR({ className, ...props }: ComponentProps<'tr'>) {
  return <tr className={cn('transition-colors hover:bg-surface-1', className)} {...props} />
}

interface CellProps {
  /** Right-aligned with tabular figures, for amounts and counts. */
  numeric?: boolean
}

export function TH({ className, numeric = false, ...props }: ComponentProps<'th'> & CellProps) {
  return (
    <th
      scope="col"
      className={cn(
        'whitespace-nowrap px-3 py-2 text-left text-[11px] font-semibold uppercase tracking-wide text-muted-foreground first:pl-5 last:pr-5',
        numeric && 'text-right',
        className,
      )}
      {...props}
    />
  )
}

export function TD({ className, numeric = false, ...props }: ComponentProps<'td'> & CellProps) {
  return (
    <td
      className={cn('px-3 py-2 align-middle first:pl-5 last:pr-5', numeric && 'tabular text-right whitespace-nowrap', className)}
      {...props}
    />
  )
}
