import { cva, type VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

export const alertVariants = cva(
  'flex items-start gap-3 rounded-lg border p-3 text-sm [&>svg]:mt-0.5 [&>svg]:size-4 [&>svg]:shrink-0',
  {
    variants: {
      tone: {
        neutral: 'border-border bg-surface-1 text-foreground [&>svg]:text-muted-foreground',
        info: 'border-primary/30 bg-primary/8 text-foreground [&>svg]:text-primary',
        success: 'border-success/30 bg-success/8 text-foreground [&>svg]:text-success',
        warning: 'border-warning/35 bg-warning/8 text-foreground [&>svg]:text-warning',
        danger: 'border-destructive/30 bg-destructive/8 text-foreground [&>svg]:text-destructive',
      },
    },
    defaultVariants: { tone: 'neutral' },
  },
)

export interface AlertProps extends ComponentProps<'div'>, VariantProps<typeof alertVariants> {}

/** An inline banner: icon first, then text. Pass `role="alert"` or `role="status"` as appropriate. */
export function Alert({ className, tone, ...props }: AlertProps) {
  return <div className={cn(alertVariants({ tone }), className)} {...props} />
}
