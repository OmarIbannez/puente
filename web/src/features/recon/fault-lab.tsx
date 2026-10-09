import { FlaskConical, LoaderCircle, Receipt, UserPen } from 'lucide-react'
import { useState } from 'react'

import { Alert } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label, MoneyInput } from '@/components/ui/input'
import { useAction } from '@/hooks/use-action'
import { useApp, useSandbox } from '@/hooks/app-context'
import { inject, setFaults, type FaultName, type InjectKind } from '@/lib/api'
import { FAULTS, type FaultInfo } from '@/lib/labels'
import { parseMoney } from '@/lib/money'
import { cn } from '@/lib/utils'

function Switch({ checked, disabled, onChange, label }: { checked: boolean; disabled: boolean; onChange: () => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={onChange}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'border-warning bg-warning' : 'border-input bg-surface-3',
      )}
    >
      <span
        aria-hidden
        className={cn(
          'inline-block size-4 rounded-full bg-card shadow transition-transform',
          checked ? 'translate-x-4' : 'translate-x-0.5',
        )}
      />
    </button>
  )
}

function FaultRow({ fault, armed, pending, onToggle }: { fault: FaultInfo; armed: boolean; pending: boolean; onToggle: () => void }) {
  return (
    <li className={cn('flex gap-3 rounded-md border px-3 py-2.5', armed ? 'border-warning/50 bg-warning/6' : 'border-border')}>
      <Switch checked={armed} disabled={pending} onChange={onToggle} label={`Arm ${fault.label}`} />
      <div className="min-w-0 text-sm">
        <p className="flex flex-wrap items-center gap-2 font-medium">
          {fault.label}
          {armed && <Badge variant="warning">armed · next event</Badge>}
        </p>
        <p className="text-xs text-muted-foreground">{fault.effect}</p>
        <p className="mt-0.5 text-xs">
          <span className="text-muted-foreground">Expect: </span>
          {fault.shows}
        </p>
      </div>
    </li>
  )
}

function InjectPanel() {
  const { bump, toast } = useApp()
  const [amount, setAmount] = useState('125.00')
  const action = useAction(inject)
  const [which, setWhich] = useState<InjectKind | null>(null)
  const parsed = parseMoney('MXN', amount)
  const valid = parsed.ok && parsed.minor > 0

  async function run(kind: InjectKind) {
    if (!parsed.ok) return
    setWhich(kind)
    const result = await action.run(kind, parsed.decimal)
    setWhich(null)
    if (!result.ok) return
    bump()
    toast({
      title: kind === 'bank_fee' ? 'Bank fee booked at the bank' : 'Manual order keyed in at the bank',
      description:
        kind === 'bank_fee'
          ? 'Only the bank knows about it. Run reconciliation to see a balance mismatch.'
          : 'No payout sent it. Advance a tick so it settles, then run reconciliation.',
      tone: 'warning',
    })
  }

  return (
    <div className="space-y-2 rounded-md border border-border p-3">
      <p className="text-sm font-medium">Book something only the bank knows about</p>
      <p className="text-xs text-muted-foreground">
        These write straight to the simulated bank's books, never to the ledger, so reconciliation has something to find.
      </p>
      <div className="flex flex-wrap items-end gap-2">
        <div className="w-36 space-y-1">
          <Label htmlFor="inject-amount" className="text-xs">
            Amount
          </Label>
          <MoneyInput
            id="inject-amount"
            asset="MXN"
            value={amount}
            aria-invalid={!valid}
            onChange={(e) => {
              setAmount(e.target.value)
            }}
          />
        </div>
        <Button variant="outline" size="sm" disabled={!valid || action.pending} onClick={() => void run('bank_fee')}>
          {which === 'bank_fee' ? <LoaderCircle className="animate-spin" /> : <Receipt />}
          Inject bank fee
        </Button>
        <Button variant="outline" size="sm" disabled={!valid || action.pending} onClick={() => void run('manual_order')}>
          {which === 'manual_order' ? <LoaderCircle className="animate-spin" /> : <UserPen />}
          Inject manual order
        </Button>
      </div>
      {action.error && (
        <Alert tone="danger" role="alert">
          {action.error.message}
        </Alert>
      )}
    </div>
  )
}

/** Toggles for the six fault switches, plus bank-side injections. */
export function FaultLab() {
  const sandbox = useSandbox()
  const { setSandbox, bump, toast } = useApp()
  const save = useAction(setFaults)

  async function toggle(name: FaultName) {
    const next = { ...sandbox.faults, [name]: !sandbox.faults[name] }
    const result = await save.run(next)
    if (!result.ok) {
      toast({ title: 'Could not update faults', description: result.error.message, tone: 'danger' })
      return
    }
    setSandbox(result.data)
    bump()
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <FlaskConical className="size-4 text-warning" aria-hidden />
          Fault lab
        </CardTitle>
        <CardDescription>
          Each switch is <strong className="font-medium text-foreground">consumed by the one event it affects</strong> and then
          turns itself off: arm it, cause the event (a deposit, a payout, a tick), and it is spent. Chain faults corrupt the
          simulated indexer or chain; rail faults make the simulated bank misbehave.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {(['chain', 'rail'] as const).map((layer) => (
          <div key={layer}>
            <p className="mb-2 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
              {layer === 'chain' ? 'Chain' : 'SPEI rail'}
            </p>
            <ul className="space-y-2">
              {FAULTS.filter((f) => f.layer === layer).map((f) => (
                <FaultRow
                  key={f.name}
                  fault={f}
                  armed={sandbox.faults[f.name]}
                  pending={save.pending}
                  onToggle={() => void toggle(f.name)}
                />
              ))}
            </ul>
          </div>
        ))}
        <InjectPanel />
      </CardContent>
    </Card>
  )
}
