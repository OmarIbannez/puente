import { Badge } from '@/components/ui/badge'
import { Tooltip } from '@/components/ui/tooltip'
import type { ChainStatus, CreditState, PayoutStatus, RailStatus } from '@/lib/api'
import { CHAIN_STATUS, CREDIT_STATE, PAYOUT_STATUS, RAIL_STATUS, type Tone } from '@/lib/labels'

const VARIANT: Record<Tone, 'neutral' | 'brand' | 'success' | 'warning' | 'danger' | 'outline'> = {
  neutral: 'neutral',
  brand: 'brand',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
  outline: 'outline',
}

function Dot() {
  return <span aria-hidden className="size-1.5 rounded-full bg-current" />
}

export function PayoutStatusBadge({ status }: { status: PayoutStatus }) {
  const s = PAYOUT_STATUS[status]
  return (
    <Tooltip content={s.hint}>
      <Badge variant={VARIANT[s.tone]} tabIndex={0}>
        <Dot />
        {s.label}
      </Badge>
    </Tooltip>
  )
}

export function RailStatusBadge({ status }: { status: RailStatus }) {
  const s = RAIL_STATUS[status]
  return (
    <Badge variant={VARIANT[s.tone]}>
      <Dot />
      {s.label}
    </Badge>
  )
}

export function ChainStatusBadge({ status }: { status: ChainStatus }) {
  const s = CHAIN_STATUS[status]
  return <Badge variant={VARIANT[s.tone]}>{s.label}</Badge>
}

export function CreditStateBadge({ state }: { state: CreditState }) {
  const s = CREDIT_STATE[state]
  return (
    <Tooltip content={s.hint}>
      <Badge variant={VARIANT[s.tone]} tabIndex={0}>
        {s.label}
      </Badge>
    </Tooltip>
  )
}

/** "Simulado" marker for anything that comes from the simulated chain or rail. */
export function SimulatedBadge({ label = 'simulado' }: { label?: string }) {
  return (
    <Badge variant="outline" className="border-dashed font-mono uppercase tracking-wider text-muted-foreground">
      {label}
    </Badge>
  )
}
