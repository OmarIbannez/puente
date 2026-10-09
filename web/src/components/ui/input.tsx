import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

const fieldBase =
  'w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-xs transition-colors placeholder:text-muted-foreground/70 hover:border-primary/40 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-offset-0 aria-invalid:border-destructive aria-invalid:ring-destructive/30 disabled:cursor-not-allowed disabled:opacity-50'

export function Input({ className, ...props }: ComponentProps<'input'>) {
  return <input className={cn(fieldBase, 'h-9', className)} {...props} />
}

export function Select({ className, ...props }: ComponentProps<'select'>) {
  return <select className={cn(fieldBase, 'h-9 cursor-pointer pr-8', className)} {...props} />
}

export function Label({ className, ...props }: ComponentProps<'label'>) {
  return <label className={cn('text-sm font-medium text-foreground', className)} {...props} />
}

interface MoneyInputProps extends Omit<ComponentProps<'input'>, 'type'> {
  large?: boolean
}

/** A peso amount field with a leading "$" and an "MXN" suffix. */
export function MoneyInput({ className, large = false, ...props }: MoneyInputProps) {
  return (
    <div className="relative">
      <span
        className={cn(
          'pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground',
          large ? 'text-xl' : 'text-sm',
        )}
      >
        $
      </span>
      <input
        inputMode="decimal"
        autoComplete="off"
        className={cn(
          fieldBase,
          'tabular pl-7 pr-14',
          large ? 'h-14 pl-8 text-2xl font-semibold tracking-tight' : 'h-9',
          className,
        )}
        {...props}
      />
      <span className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-xs font-medium text-muted-foreground">
        MXN
      </span>
    </div>
  )
}
