import { CircleAlert, RefreshCw, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'

import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import type { ApiError } from '@/lib/api'
import { cn } from '@/lib/utils'

interface ErrorNoticeProps {
  error: ApiError
  title?: string
  onRetry?: () => void
  className?: string
}

/** An API error in plain words, with the server's code for debugging. */
export function ErrorNotice({ error, title, onRetry, className }: ErrorNoticeProps) {
  return (
    <Alert tone="danger" role="alert" className={className}>
      <CircleAlert />
      <div className="min-w-0 flex-1">
        {title && <p className="font-medium">{title}</p>}
        <p className={cn(title && 'text-muted-foreground')}>{error.message}</p>
        <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">
          {error.code}
          {error.status > 0 && ` · HTTP ${error.status}`}
        </p>
      </div>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw />
          Retry
        </Button>
      )}
    </Alert>
  )
}

interface EmptyStateProps {
  icon: LucideIcon
  title: string
  children?: ReactNode
  action?: ReactNode
  className?: string
}

export function EmptyState({ icon: Icon, title, children, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center rounded-xl border border-dashed border-border bg-surface-1 px-6 py-10 text-center',
        className,
      )}
    >
      <span className="grid size-11 place-items-center rounded-full bg-primary/10 text-primary">
        <Icon className="size-5" aria-hidden />
      </span>
      <p className="mt-3 font-medium">{title}</p>
      {children && <div className="mt-1 max-w-sm text-sm text-muted-foreground">{children}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
