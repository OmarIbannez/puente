import { Amount } from '@/components/amount'
import { Hash } from '@/components/hash'
import { ChainStatusBadge, CreditStateBadge } from '@/components/status-badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { Tooltip } from '@/components/ui/tooltip'
import type { Deposit, Network } from '@/lib/api'
import { formatAgo, formatDateTime } from '@/lib/format'
import { networkName } from '@/lib/labels'

/** Chain transfers as the scanner stored them, with where each stands in the ledger. */
export function DepositsTable({ deposits, networks }: { deposits: readonly Deposit[]; networks: readonly Network[] }) {
  return (
    <Table>
      <THead>
        <tr>
          <TH>Network</TH>
          <TH>Transaction</TH>
          <TH numeric>Block</TH>
          <TH>Customer</TH>
          <TH numeric>Amount</TH>
          <TH>Chain</TH>
          <TH>Ledger</TH>
          <TH numeric>
            <Tooltip content="Credit generation. A reorged-out transfer that reappears is credited again as generation 2, under a new ref.">
              <span tabIndex={0} className="cursor-help">
                Gen
              </span>
            </Tooltip>
          </TH>
          <TH>Seen</TH>
        </tr>
      </THead>
      <TBody>
        {deposits.map((d) => (
          <TR key={d.transfer_id}>
            <TD className="whitespace-nowrap text-xs">{networkName(networks, d.network)}</TD>
            <TD>
              <Hash value={d.tx_hash} href={d.explorer_url} />
              {d.log_index > 0 && <span className="ml-1 font-mono text-[11px] text-muted-foreground">log {d.log_index}</span>}
            </TD>
            <TD numeric className="font-mono text-xs">
              {d.block_number}
            </TD>
            <TD className="whitespace-nowrap">
              {d.customer_name ?? (
                <span className="text-xs text-muted-foreground">
                  <Hash value={d.to} copy={false} />
                </span>
              )}
            </TD>
            <TD numeric>
              <Amount value={d.amount} />
            </TD>
            <TD>
              <ChainStatusBadge status={d.status} />
            </TD>
            <TD>
              <CreditStateBadge state={d.state} />
            </TD>
            <TD numeric className="text-xs text-muted-foreground">
              {d.generation > 0 ? d.generation : '—'}
            </TD>
            <TD className="whitespace-nowrap text-xs text-muted-foreground" title={formatDateTime(d.created_at)}>
              {formatAgo(d.created_at)}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  )
}
