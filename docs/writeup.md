# Three sources of truth: a stablecoin payout ledger

*Draft post for the Puente launch.*

Paying a Mexican bank account from a stablecoin balance sounds like two API calls: receive USDC,
send SPEI. In practice there are three systems that each think they know what happened, and money
gets lost or duplicated in the gaps between them:
- the chain, which can reorg;
- the bank, which can lose an acknowledgement or charge a fee without telling you;
- your own ledger.

I built [Puente](https://github.com/OmarIbannez/puente) to work through those gaps end to end, in
Go and Postgres. Each visitor of the demo gets a sandbox with a simulated chain and a simulated
SPEI bank, and a *fault lab* that breaks them on purpose.

## Name every entry after the event, not the attempt

The single most useful decision was naming every journal entry after the business event it
records: `payout:<id>:submit`, `deposit:<transfer>:<generation>`. The table is unique on
`(sandbox, ref)`, and posting inserts with `ON CONFLICT DO NOTHING`. If the ref already exists,
`Post` compares the stored postings with the new ones. Identical means a retry, which is a silent
no-op. Different means a bug, which is a loud error. Once every caller names its entries that way,
"retry anything" stops being scary: the worker, the deposit crediter and the rail sync can each
run twice and the books don't move.

The ledger is multi-asset. Every entry balances per asset, and a deferred constraint trigger
re-checks that at commit, so even raw SQL can't leave the books unbalanced. A USDC→MXN conversion
therefore passes through an FX clearing account. Conversion uses integer math with big integers;
the sub-centavo "dust" is reported rather than silently rounded into someone's balance.

## The ack that never came back

The interesting payout failure isn't a rejection. It's a timeout: the bank accepted the order but
the answer got lost. Retrying naïvely pays twice. Puente fixes the clave de rastreo when the payout
is created, so every retry sends the same clave. The (simulated) bank deduplicates on it, and the
ledger deduplicates on the ref. The fault lab's *Rail timeout* switch shows the result: the bank's
order says "submitted 2 times", the ledger has one submit entry, and reconciliation finds nothing.

The worker claims jobs from a transactional outbox with `FOR UPDATE SKIP LOCKED`. The job is
written in the same transaction that reserves the customer's funds, so a payout can't exist
without its job, or the other way round. Four workers on twenty payouts submit each one exactly
once; that's a test.

## Deposits are claims, not facts

An indexer telling you "100 USDC arrived" is a claim. Puente's simulated chain keeps two views
apart:
- the **feed**, what the indexer reports and Puente credits;
- the **canonical** chain, what really happened.

Faults corrupt only the feed: a log reported twice, an amount that's 1% off, a reorg that drops
the block. Reconciliation re-reads every open credit from the canonical chain and reports
`duplicate`, `amount_mismatch` or `missing_on_chain`.

Reorgs taught me something I hadn't planned for. On go-ethereum's simulated backend, a transaction
dropped by a reorg is re-included in the next block every time. So a transfer can go confirmed →
orphaned → confirmed under the same id. Credits are therefore keyed by *generation*:
`deposit:<id>:1`, `deposit_reversal:<id>:1`, `deposit:<id>:2`.

## Reconcile, don't repair

Reconciliation reads the ledger, the chain tables and the bank's tables in one REPEATABLE READ
snapshot, so they are compared as of a single instant while the worker keeps moving money. It
reports breaks with their evidence: the tx hash and explorer link, the clave, the CEP seal, which
ledger steps were posted. It never fixes anything. A break means the world disagreed with your
books, and the evidence is worth more than a quiet correction.

The bank side is checked from the journal, not from payout statuses, so a bug that flips a status
without posting is still caught. Two injections make the bank misbehave on demand: a fee it
charges silently, and an order someone keyed into the bank's portal by hand.

## Real chains, read-only

Simulation makes faults reproducible, but it's nice to see the real thing too. With a watch
address and two public RPCs configured, a shared live sandbox follows real USDC on Base Sepolia and
Bitso's MXNB on Arbitrum Sepolia. The scanner reads ≤200 blocks per call because the public RPC
rejects more, waits for confirmations, and reconciles those deposits like any other. Sending is
deliberately not possible from the web.

## What I'd change for production

- Run reconciliation as its own scheduled job, with a workflow for breaks: acknowledge, assign,
  resolve with a linked correcting entry.
- Replace the simulated rail with a real participant (STP, or Juno for MXNB on- and off-ramps)
  behind the same interface, and use their webhooks instead of polling.
- Price FX instead of configuring it, and hedge the position that the clearing account makes
  visible.
- Add a USD leg over ACH/RTP. The ledger, outbox and reconciliation core don't change.

The code, the design review and a one-command local setup are on
[GitHub](https://github.com/OmarIbannez/puente).
