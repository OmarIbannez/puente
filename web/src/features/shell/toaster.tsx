import { CircleCheck, CircleX, Info, TriangleAlert, X, type LucideIcon } from 'lucide-react'
import { useEffect } from 'react'

import type { ToastInput, ToastTone } from '@/hooks/app-context'
import { cn } from '@/lib/utils'

export interface ToastItem extends ToastInput {
  id: string
}

const toneIcon: Record<ToastTone, { icon: LucideIcon; className: string }> = {
  info: { icon: Info, className: 'text-primary' },
  success: { icon: CircleCheck, className: 'text-success' },
  warning: { icon: TriangleAlert, className: 'text-warning' },
  danger: { icon: CircleX, className: 'text-destructive' },
}

function ToastCard({ toast, onDismiss }: { toast: ToastItem; onDismiss: (id: string) => void }) {
  const { icon: Icon, className } = toneIcon[toast.tone ?? 'info']

  useEffect(() => {
    const timer = window.setTimeout(() => {
      onDismiss(toast.id)
    }, 6_000)
    return () => {
      window.clearTimeout(timer)
    }
  }, [toast.id, onDismiss])

  return (
    <div className="pointer-events-auto animate-toast-in rounded-lg border border-border bg-popover p-3.5 text-popover-foreground shadow-lift">
      <div className="flex items-start gap-3">
        <Icon className={cn('mt-0.5 size-4.5 shrink-0', className)} aria-hidden />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold">{toast.title}</p>
          {toast.description && <p className="mt-0.5 text-sm text-muted-foreground">{toast.description}</p>}
        </div>
        <button
          type="button"
          onClick={() => {
            onDismiss(toast.id)
          }}
          className="-m-1 cursor-pointer rounded-md p-1 text-muted-foreground transition-colors hover:bg-surface-2 hover:text-foreground"
          aria-label="Dismiss notification"
          title="Dismiss"
        >
          <X className="size-4" />
        </button>
      </div>
    </div>
  )
}

export function Toaster({ toasts, onDismiss }: { toasts: ToastItem[]; onDismiss: (id: string) => void }) {
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed inset-x-3 bottom-3 z-[60] flex flex-col gap-2 sm:left-auto sm:right-4 sm:bottom-4 sm:w-96"
    >
      {toasts.map((t) => (
        <ToastCard key={t.id} toast={t} onDismiss={onDismiss} />
      ))}
    </div>
  )
}
