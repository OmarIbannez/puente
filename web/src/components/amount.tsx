import { Tooltip } from '@/components/ui/tooltip'
import { formatMoney, formatShort, isTruncated, type Money } from '@/lib/money'
import { cn } from '@/lib/utils'

interface AmountProps {
  value: Money
  /** Show every decimal of the asset instead of two. */
  full?: boolean
  /** Leave off the asset code (when a column header carries it). */
  bare?: boolean
  /** Tint negatives with the danger color. */
  signed?: boolean
  className?: string
}

/**
 * An exact amount. Stablecoins show two decimals; when that hides digits,
 * the full six-decimal value is one hover or focus away.
 */
export function Amount({ value, full = false, bare = false, signed = false, className }: AmountProps) {
  const exact = formatMoney(value, { bare })
  const text = full ? exact : formatShort(value, { bare })
  const cls = cn('tabular whitespace-nowrap', signed && value.minor < 0 && 'text-destructive', className)
  if (full || !isTruncated(value)) {
    return (
      <span className={cls} title={full ? undefined : formatMoney(value)}>
        {text}
      </span>
    )
  }
  return (
    <Tooltip content={<span className="font-mono">{formatMoney(value)}</span>}>
      <span tabIndex={0} className={cn(cls, 'cursor-help underline decoration-dotted decoration-muted-foreground/50 underline-offset-4')}>
        {text}
      </span>
    </Tooltip>
  )
}
