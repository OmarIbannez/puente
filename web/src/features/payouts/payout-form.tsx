import { KeyRound, LoaderCircle, Repeat2, RotateCcw, Send } from 'lucide-react'
import { useState, type SubmitEvent, type ReactNode } from 'react'

import { Amount } from '@/components/amount'
import { CopyButton } from '@/components/hash'
import { Alert } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input, Label, MoneyInput, Select } from '@/components/ui/input'
import { Tooltip } from '@/components/ui/tooltip'
import { useAction } from '@/hooks/use-action'
import { useApp, useSandbox } from '@/hooks/app-context'
import { useIdempotencyKey } from '@/hooks/use-idempotency-key'
import { useCustomers } from '@/hooks/use-reference'
import { createPayout, createPayoutRaw, parseApiError, type CreatePayoutInput, type Payout, type RawResponse } from '@/lib/api'
import { formatClabe, shortId } from '@/lib/format'
import { parseMoney, STABLECOINS, type Stablecoin } from '@/lib/money'
import { formatBps, formatRate, quotePayout } from '@/lib/quote'
import { navigate, routeHref } from '@/lib/route'
import { cn } from '@/lib/utils'

/** A valid demo CLABE (BBVA), the same the simulated bank uses for manual orders. */
const DEMO_CLABE = '012180015203300154'

function QuoteRow({ label, children, strong = false }: { label: ReactNode; children: ReactNode; strong?: boolean }) {
  return (
    <div className={cn('flex items-baseline justify-between gap-3 py-1', strong && 'border-t border-border pt-2')}>
      <dt className={cn('text-muted-foreground', strong && 'font-medium text-foreground')}>{label}</dt>
      <dd className={cn('tabular text-right', strong && 'text-lg font-semibold')}>{children}</dd>
    </div>
  )
}

interface Exchange {
  n: number
  res: RawResponse
}

function ExchangeCard({ exchange, firstId }: { exchange: Exchange; firstId: string | undefined }) {
  const { res, n } = exchange
  const payout = res.ok ? (res.body as Payout) : undefined
  const error = res.ok ? undefined : parseApiError(res.status, res.body)
  const same = payout !== undefined && firstId !== undefined && payout.id === firstId
  return (
    <div className="rounded-md border border-border bg-surface-1 p-3 font-mono text-xs">
      <p className="mb-1.5 font-sans text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">Request {n}</p>
      <p>
        <span className={cn('font-semibold', res.ok ? 'text-success' : 'text-destructive')}>HTTP {res.status}</span>
      </p>
      <p className="mt-1">
        <span className="text-muted-foreground">Idempotent-Replay:</span>{' '}
        {res.replay ? <span className="font-semibold text-primary">true</span> : <span className="text-muted-foreground">(absent)</span>}
      </p>
      {payout && (
        <p className="mt-1">
          <span className="text-muted-foreground">id:</span>{' '}
          <a href={routeHref({ name: 'payout', id: payout.id })} className="text-primary underline-offset-4 hover:underline">
            {shortId(payout.id)}
          </a>
          {n === 2 && same && <span className="ml-1.5 font-sans text-success">same payout</span>}
        </p>
      )}
      {error && (
        <p className="mt-1 break-words">
          <span className="text-destructive">{error.code}</span> <span className="font-sans">{error.message}</span>
        </p>
      )}
    </div>
  )
}

/**
 * Creates a payout. The quote is computed here with the same integer math
 * as the server, and the Idempotency-Key is on screen: retrying after an
 * error reuses it, and "Send twice" proves the second request is a replay.
 */
export function PayoutForm() {
  const sandbox = useSandbox()
  const { customers } = useCustomers()
  const { bump, toast } = useApp()
  const [customerId, setCustomerId] = useState('')
  const [asset, setAsset] = useState<Stablecoin>('USDC')
  const [amount, setAmount] = useState('100')
  const [clabe, setClabe] = useState(DEMO_CLABE)
  const [name, setName] = useState('Ana López')
  const [concepto, setConcepto] = useState('Pago proveedor')
  const [key, rotate] = useIdempotencyKey()
  const create = useAction(createPayout)
  const [twice, setTwice] = useState<{ pending: boolean; exchanges: Exchange[] }>({ pending: false, exchanges: [] })

  const selected = customerId === '' ? customers?.[0] : customers?.find((c) => c.id === customerId)
  const available = selected?.available[asset]
  const parsed = parseMoney(asset, amount)
  const quote = parsed.ok ? quotePayout(asset, parsed.minor, sandbox.rates[asset], sandbox.fee_bps) : undefined
  const amountError = amount === '' ? undefined : !parsed.ok ? parsed.error : quote && !quote.ok ? quote.error : undefined
  const overdraw = quote?.ok === true && available !== undefined && quote.quote.total.minor > available.minor
  const clabeError = /^\d{18}$/.test(clabe) ? undefined : 'A CLABE has 18 digits'
  const busy = create.pending || twice.pending
  const canSubmit = selected !== undefined && quote?.ok === true && clabeError === undefined && name.trim() !== '' && !busy

  function input(): CreatePayoutInput | undefined {
    if (!selected || !parsed.ok) return undefined
    return { customer_id: selected.id, asset, amount: parsed.decimal, beneficiary_clabe: clabe, beneficiary_name: name, concepto }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const body = input()
    if (!body) return
    setTwice({ pending: false, exchanges: [] })
    const result = await create.run(body, key)
    if (!result.ok) return
    rotate()
    bump()
    toast({
      title: 'Payout reserved',
      description: 'Amount + fee moved to 2020 in the same transaction as the outbox job. Advance a tick to submit it to SPEI.',
      tone: 'success',
    })
    navigate({ name: 'payout', id: result.data.id })
  }

  async function sendTwice() {
    const body = input()
    if (!body) return
    create.clearError()
    setTwice({ pending: true, exchanges: [] })
    const exchanges: Exchange[] = []
    try {
      // Back to back with the same key: the first executes, the second is
      // answered from the stored response without touching the ledger.
      for (const n of [1, 2]) {
        exchanges.push({ n, res: await createPayoutRaw(body, key) })
        setTwice({ pending: n === 1, exchanges: [...exchanges] })
      }
    } catch (err) {
      toast({ title: 'Request failed', description: err instanceof Error ? err.message : String(err), tone: 'danger' })
      setTwice({ pending: false, exchanges })
      return
    }
    rotate()
    bump()
  }

  const firstId = (() => {
    const first = twice.exchanges[0]
    return first?.res.ok ? (first.res.body as Payout).id : undefined
  })()

  return (
    <Card>
      <CardHeader>
        <CardTitle>New payout</CardTitle>
        <CardDescription>Pay a Mexican bank account in pesos from a customer's stablecoin balance.</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="space-y-3" onSubmit={(e) => void submit(e)}>
          <div className="space-y-1.5">
            <Label htmlFor="po-customer">Customer</Label>
            <Select
              id="po-customer"
              value={selected?.id ?? ''}
              onChange={(e) => {
                setCustomerId(e.target.value)
              }}
            >
              {(customers ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          </div>
          <div className="grid grid-cols-[7rem_1fr] gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="po-asset">Pay from</Label>
              <Select
                id="po-asset"
                value={asset}
                onChange={(e) => {
                  setAsset(e.target.value === 'MXNB' ? 'MXNB' : 'USDC')
                }}
              >
                {STABLECOINS.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="po-amount">Amount</Label>
              <MoneyInput
                id="po-amount"
                asset={asset}
                value={amount}
                aria-invalid={amountError !== undefined}
                aria-describedby="po-amount-hint"
                onChange={(e) => {
                  setAmount(e.target.value)
                }}
              />
            </div>
          </div>
          <p id="po-amount-hint" className={cn('text-xs', amountError || overdraw ? 'text-destructive' : 'text-muted-foreground')}>
            {amountError ??
              (available ? (
                <>
                  Available <Amount value={available} />
                  {overdraw && ' (not enough: the server will answer 422)'}
                </>
              ) : (
                ' '
              ))}
          </p>

          <dl className="rounded-md border border-border bg-surface-1 px-3 py-2 text-sm" aria-live="polite" aria-label="Quote">
            {quote?.ok ? (
              <>
                <QuoteRow label="Beneficiary receives the value of">
                  <Amount value={quote.quote.amount} full />
                </QuoteRow>
                <QuoteRow label={`Fee (${formatBps(quote.quote.feeBps)}, rounded down)`}>
                  <Amount value={quote.quote.fee} full />
                </QuoteRow>
                <QuoteRow label="Reserved from balance">
                  <Amount value={quote.quote.total} full />
                </QuoteRow>
                <QuoteRow label="Rate">
                  {formatRate(quote.quote.rate)} MXN / {asset}
                </QuoteRow>
                <QuoteRow label="Sent over SPEI" strong>
                  <Amount value={quote.quote.mxn} />
                </QuoteRow>
                {quote.quote.dust > 0 && (
                  <p className="pb-1 text-[11px] text-muted-foreground">
                    Rounded toward zero to the centavo; {String(quote.quote.dust)} millionths of a centavo stay in FX clearing
                    (3900).
                  </p>
                )}
              </>
            ) : (
              <p className="py-2 text-muted-foreground">Enter an amount to see the quote.</p>
            )}
          </dl>

          <div className="space-y-1.5">
            <Label htmlFor="po-clabe">Beneficiary CLABE</Label>
            <Input
              id="po-clabe"
              inputMode="numeric"
              className="font-mono"
              value={clabe}
              aria-invalid={clabeError !== undefined}
              onChange={(e) => {
                setClabe(e.target.value.replace(/\s/g, ''))
              }}
            />
            <p className={cn('text-xs', clabeError ? 'text-destructive' : 'text-muted-foreground')}>
              {clabeError ?? `${formatClabe(clabe)} · bank, plaza, account, check digit. The server validates the check digit.`}
            </p>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="po-name">Beneficiary name</Label>
              <Input
                id="po-name"
                maxLength={40}
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                }}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="po-concepto">Concepto</Label>
              <Input
                id="po-concepto"
                maxLength={40}
                value={concepto}
                onChange={(e) => {
                  setConcepto(e.target.value)
                }}
              />
            </div>
          </div>

          <div className="rounded-md border border-dashed border-primary/40 bg-primary/5 p-3">
            <div className="flex items-center justify-between gap-2">
              <Tooltip content="One key per intent. A retry after a network error reuses it, so the payout is created at most once. A new key is drawn after each success.">
                <span tabIndex={0} className="inline-flex cursor-help items-center gap-1.5 text-xs font-medium">
                  <KeyRound className="size-3.5 text-primary" aria-hidden />
                  Idempotency-Key
                </span>
              </Tooltip>
              <div className="flex items-center">
                <CopyButton value={key} label="Copy key" />
                <button
                  type="button"
                  onClick={rotate}
                  disabled={busy}
                  title="Draw a new key"
                  aria-label="Draw a new key"
                  className="inline-grid size-6 cursor-pointer place-items-center rounded-sm text-muted-foreground hover:bg-surface-2 hover:text-foreground disabled:opacity-50"
                >
                  <RotateCcw className="size-3.5" />
                </button>
              </div>
            </div>
            <p className="mt-1 break-all font-mono text-xs text-muted-foreground">{key}</p>
          </div>

          {create.error && (
            <Alert tone="danger" role="alert">
              <span>
                {create.error.message}
                <span className="mt-0.5 block font-mono text-[11px] text-muted-foreground">
                  {create.error.code} · HTTP {create.error.status} · retrying reuses the same key
                </span>
              </span>
            </Alert>
          )}

          <div className="grid gap-2 sm:grid-cols-2">
            <Button type="submit" variant="pay" disabled={!canSubmit}>
              {create.pending ? <LoaderCircle className="animate-spin" /> : <Send />}
              Create payout
            </Button>
            <Tooltip content="Fires the same request twice with the same Idempotency-Key. One payout, one reserve entry; the second response is a replay.">
              <Button variant="outline" disabled={!canSubmit} onClick={() => void sendTwice()}>
                {twice.pending ? <LoaderCircle className="animate-spin" /> : <Repeat2 />}
                Send twice
              </Button>
            </Tooltip>
          </div>

          {twice.exchanges.length > 0 && (
            <div className="space-y-2" aria-live="polite">
              <div className="grid gap-2 sm:grid-cols-2">
                {twice.exchanges.map((x) => (
                  <ExchangeCard key={x.n} exchange={x} firstId={firstId} />
                ))}
              </div>
              {twice.exchanges.length === 2 && twice.exchanges[1]?.res.replay && (
                <p className="text-xs text-muted-foreground">
                  Both requests carried the same key. The second was answered from the stored response: no second payout,
                  no second reserve entry.{' '}
                  {firstId && (
                    <a href={routeHref({ name: 'payout', id: firstId })} className="text-primary underline-offset-4 hover:underline">
                      Open the payout
                    </a>
                  )}
                </p>
              )}
            </div>
          )}
        </form>
      </CardContent>
    </Card>
  )
}
