import { CopyButton } from '@/components/hash'
import { SimulatedBadge } from '@/components/status-badge'
import type { Cep } from '@/lib/api'
import { formatMoney, parseMoney } from '@/lib/money'

function Field({ label, children, mono = false }: { label: string; children: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[9.5rem_1fr] gap-2 py-1">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? 'break-all font-mono' : ''}>{children}</dd>
    </div>
  )
}

/** "1234.56" from the CEP → "$1,234.56 MXN", exact. */
function monto(text: string): string {
  const parsed = parseMoney('MXN', text)
  return parsed.ok ? formatMoney({ asset: 'MXN', minor: parsed.minor }) : text
}

/**
 * The CEP-like receipt the simulated bank issues for a liquidated order.
 * Banxico's real CEP proves a SPEI transfer settled; this one only imitates
 * its shape and says so.
 */
export function CepReceipt({ cep }: { cep: Cep }) {
  return (
    <div className="relative overflow-hidden rounded-lg border border-border bg-surface-1 p-4">
      <span
        aria-hidden
        className="pointer-events-none absolute right-4 bottom-10 -rotate-12 select-none font-mono text-4xl font-bold tracking-widest text-foreground/5"
      >
        SIMULADO
      </span>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="text-sm font-semibold">Comprobante Electrónico de Pago</p>
          <p className="text-xs text-muted-foreground">Issued by the simulated bank. Not a Banxico CEP.</p>
        </div>
        <SimulatedBadge />
      </div>
      <dl className="divide-y divide-border/60 text-xs">
        <Field label="Fecha de operación">{`${cep.fecha_operacion} ${cep.hora_operacion}`}</Field>
        <Field label="Clave de rastreo" mono>
          {cep.clave_rastreo}
        </Field>
        <Field label="Emisor">{cep.emisor}</Field>
        <Field label="Receptor">{cep.receptor}</Field>
        <Field label="Cuenta beneficiario" mono>
          {cep.cuenta_beneficiario}
        </Field>
        <Field label="Nombre beneficiario">{cep.nombre_beneficiario}</Field>
        <Field label="Concepto">{cep.concepto === '' ? '—' : cep.concepto}</Field>
        <Field label="Monto">{monto(cep.monto)}</Field>
        <div className="grid grid-cols-[9.5rem_1fr] gap-2 py-1">
          <dt className="text-muted-foreground">Sello (SHA-256)</dt>
          <dd className="flex items-start gap-1">
            <span className="break-all font-mono text-[11px]">{cep.sello}</span>
            <CopyButton value={cep.sello} label="Copy sello" />
          </dd>
        </div>
      </dl>
    </div>
  )
}
