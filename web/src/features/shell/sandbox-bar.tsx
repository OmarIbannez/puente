import { FastForward, FlaskConical, LoaderCircle, Play, RefreshCw, RotateCcw } from 'lucide-react'
import { useState } from 'react'

import { Alert } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogTitle } from '@/components/ui/dialog'
import { Tooltip } from '@/components/ui/tooltip'
import { useAction } from '@/hooks/use-action'
import { useApp } from '@/hooks/app-context'
import { advance, resetSandbox } from '@/lib/api'
import { plural, shortId } from '@/lib/format'
import { FAULTS } from '@/lib/labels'
import { navigate } from '@/lib/route'
import { summarizeTicks } from '@/lib/ticks'

function ResetDialog() {
  const [open, setOpen] = useState(false)
  const { setSandbox, bump, toast } = useApp()
  const reset = useAction(resetSandbox)

  async function confirm() {
    const result = await reset.run()
    if (!result.ok) return
    setSandbox(result.data)
    bump()
    setOpen(false)
    navigate({ name: 'overview' })
    toast({ title: 'Sandbox reset', description: 'Fresh customers, deposits and payouts. No faults armed.', tone: 'success' })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        reset.clearError()
      }}
    >
      <Button
        variant="ghost"
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
        title="Reset sandbox"
      >
        <RotateCcw />
        <span className="hidden sm:inline">Reset</span>
      </Button>
      <DialogContent>
        <DialogTitle>Reset your sandbox?</DialogTitle>
        <DialogDescription>
          This deletes your sandbox (its simulated chain, bank and ledger) and seeds a new one with three customers, USDC and
          MXNB deposits, and payouts in every state. Other visitors are not affected.
        </DialogDescription>
        {reset.error && (
          <Alert tone="danger" role="alert" className="mt-4">
            {reset.error.message}
          </Alert>
        )}
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline">Keep my sandbox</Button>
          </DialogClose>
          <Button variant="destructive" onClick={() => void confirm()} disabled={reset.pending}>
            {reset.pending ? <LoaderCircle className="animate-spin" /> : <RotateCcw />}
            Reset sandbox
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ArmedFaults() {
  const { sandbox } = useApp()
  if (!sandbox) return null
  const armed = FAULTS.filter((f) => sandbox.faults[f.name])
  if (armed.length === 0) {
    return (
      <a href="#/recon" className="inline-flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground">
        <FlaskConical className="size-3.5" aria-hidden />
        No faults armed
      </a>
    )
  }
  return (
    <a href="#/recon" className="flex min-w-0 flex-wrap items-center gap-1.5" aria-label={`${plural(armed.length, 'fault')} armed`}>
      <FlaskConical className="size-3.5 shrink-0 text-warning" aria-hidden />
      {armed.map((f) => (
        <Badge key={f.name} variant="warning">
          {f.label}
        </Badge>
      ))}
    </a>
  )
}

/** The demo's controls: the clock, refresh, reset, and what is armed. */
export function SandboxBar() {
  const { sandbox, bump, toast } = useApp()
  const tick = useAction(advance)
  const [running, setRunning] = useState<number | null>(null)

  async function run(n: number) {
    setRunning(n)
    const result = await tick.run(n)
    setRunning(null)
    bump()
    if (result.ok) {
      toast({ title: `Advanced ${plural(n, 'tick')}`, description: summarizeTicks(result.data), tone: 'info' })
    } else {
      toast({ title: 'Advance failed', description: result.error.message, tone: 'danger' })
    }
  }

  return (
    <div className="sticky top-0 z-30 border-b border-border bg-background/85 backdrop-blur-md">
      <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2 sm:px-6">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
          <Tooltip content="Your own sandbox, kept in a cookie: a simulated chain, a simulated SPEI bank and a ledger. Nobody else sees it.">
            <span tabIndex={0} className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
              <span className="size-2 rounded-full bg-success" aria-hidden />
              Sandbox <span className="font-mono text-foreground">{sandbox ? shortId(sandbox.id) : '…'}</span>
            </span>
          </Tooltip>
          <ArmedFaults />
        </div>
        <div className="flex items-center gap-1">
          <Tooltip content="Run one engine cycle: mine a simulated block, scan and credit deposits, submit queued payouts and sync the bank.">
            <Button variant="outline" size="sm" onClick={() => void run(1)} disabled={tick.pending}>
              {running === 1 ? <LoaderCircle className="animate-spin" /> : <Play />}
              Advance 1 tick
            </Button>
          </Tooltip>
          <Button variant="outline" size="sm" onClick={() => void run(5)} disabled={tick.pending} title="Run five engine cycles">
            {running === 5 ? <LoaderCircle className="animate-spin" /> : <FastForward />}
            Advance 5
          </Button>
          <Button variant="ghost" size="icon-sm" onClick={bump} aria-label="Refresh data" title="Refresh data">
            <RefreshCw />
          </Button>
          <ResetDialog />
        </div>
      </div>
    </div>
  )
}
