# Puente: design review

**Status:** v1, implemented · **Author:** Omar Ibañez · **Code:** this repository

Puente takes stablecoin deposits (USDC, MXNB) from a chain and pays Mexican bank accounts in
pesos over SPEI. It records every movement in a double-entry ledger exactly once, and continuously
reconciles that ledger against the chain and the bank. This document is the design review: what
was built, why, what was rejected, and where it breaks.

## 1. Context

A cross-border payout company holds customer funds as stablecoins and pays out on local rails. In
Mexico that rail is SPEI. Three independent systems each hold a version of the truth:

| System | Truth it holds | Who controls it |
|---|---|---|
| Chain | Which transfers landed, in which block | Nobody; it can reorg |
| Bank (SPEI participant) | Which orders settled, came back or were charged | The bank |
| Ledger | What Puente owes and owns | Puente |

Money is lost or duplicated in the gaps between them: an indexer that reports a log twice, an ack
lost on the way back from the bank, a reorg after a deposit was credited, a fee the bank charged
silently. Puente is designed around those gaps.

## 2. Goals and non-goals

**Goals**

1. **No double spending, no double crediting.** Every business event posts exactly once, even
   under retries, concurrent workers and crashes between steps.
2. **Books that always balance.** Per asset, enforced by the database, not only by the code.
3. **Every external effect is reconcilable.** For every movement, the ledger records what was
   intended and the outside source records what happened. A read-only job compares the two.
4. **Failure is demonstrable.** Each failure mode above can be triggered on demand in a sandbox,
   and has a test.

**Non-goals (v1)**

- Real money, mainnet, or a real SPEI/STP/Juno integration. The rail is a simulator kept on its
  own books, and the live mode only *reads* real testnets.
- FX pricing. The rates are configured (USDC 18.4523 MXN, MXNB 1:1).
- KYC/AML. A sibling project, Sentinel, covers it.
- Custody. The only private key is an operator's testnet hot wallet, used by one CLI command.

## 3. Architecture

```
            ┌──────────── engine (per-sandbox cycle) ─────────────┐
 chain ──►  │ chain.Scan ──► deposit.Credit ──► ledger.Post       │
 (sim/evm)  │                                                     │
            │ payout.Create ─► outbox ─► worker ─► spei.Submit ───┼──► rail (sim)
            │                   ▲            └──► ledger.Post     │
            │                   └── SyncRail ◄── rail.Advance     │
            │ recon.Run: ledger ⇄ chain, ledger ⇄ rail, balances  │
            └─────────────────────────────────────────────────────┘
   api (HTTP, sandbox cookie, idempotency keys) · cmd/puente (CLI) · web (React SPA)
```

| Package | Responsibility |
|---|---|
| `money` | Integer minor units per asset (USDC/MXNB 6dp, MXN 2dp). Exact conversion with big integers; the rounding "dust" is reported, never lost in a float. |
| `ledger` | Chart of accounts, journal entries, exactly-once `Post`, balances, trial balance. |
| `customer` | Customers and their deposit addresses. |
| `chain` | The `Source` port, network definitions, and the scanner (paging, confirmations, reorgs). |
| `chain/sim`, `chain/evm` | Adapters: a per-sandbox simulated chain in Postgres; go-ethereum for real testnets. |
| `deposit` | Credits confirmed transfers to customers; reverses orphaned ones. |
| `spei` | CLABE and clave de rastreo rules, the simulated SPEI participant, CEP receipts. |
| `payout` | Quote, state machine, transactional outbox and worker, rail sync. |
| `recon` | Matchers for the chain, the rail and balances; stored runs and breaks. |
| `engine` | Wires the above into one cycle per sandbox, plus the background loop. |
| `api`, `cmd/puente`, `seed` | HTTP API, CLI, demo world. |

Everything runs in one process with one Postgres. Workers coordinate through the database (row
locks, `SKIP LOCKED`), so the service can run as N replicas without a broker.

## 4. The ledger

Postings are signed integers (debit positive) in one asset. Each journal entry must **sum to zero
per asset**. This is checked in Go before inserting, and by a deferred constraint trigger
(`postings_balanced`) at commit, which catches raw SQL and bugs alike. Conversions therefore pass
through an FX clearing account: the USDC leg and the MXN leg each balance on their own.

| Code | Account | Type | Assets | Sub-ledger |
|---|---|---|---|---|
| 1010 | Hot wallet (on-chain) | asset | USDC, MXNB | |
| 1100 | SPEI settlement account | asset | MXN | |
| 2010 | Customer balances | liability | USDC, MXNB | per customer |
| 2020 | Payouts reserved | liability | USDC, MXNB | per customer |
| 2100 | SPEI payouts in flight | liability | MXN | |
| 3000 | Treasury equity | equity | MXN | |
| 3900 | FX conversion clearing | equity | all | |
| 4100 | Payout fee income | revenue | USDC, MXNB | |

Postings to 2010 and 2020 must name the customer (`party_id`). Go, a CHECK and a sandbox-scoped
foreign key all enforce it.

| Entry ref | Postings |
|---|---|
| `funding:<ref>` | Dr 1100 / Cr 3000 |
| `deposit:<transfer>:<gen>` | Dr 1010 / Cr 2010·customer |
| `deposit_reversal:<transfer>:<gen>` | Dr 2010·customer / Cr 1010 |
| `payout:<id>:reserve` | Dr 2010·c / Cr 2020·c (amount + fee) |
| `payout:<id>:submit` | Dr 2020·c (total); Cr 4100 (fee), Cr 3900 (amount); Dr 3900 / Cr 2100 (MXN) |
| `payout:<id>:settle` | Dr 2100 / Cr 1100 |
| `payout:<id>:release` | reverses reserve (cancel), or submit + reserve with the fee refunded (rejected) |
| `payout:<id>:return` | Dr 1100 / Cr 3900 (MXN); Dr 3900 / Cr 2010·c (amount; the fee is kept) |

**Exactly-once.** `journal_entries` is unique on `(sandbox_id, ref)`. `ledger.Post` inserts with
`ON CONFLICT DO NOTHING`. If the ref already exists, it compares the stored postings with the new
ones: identical means the retry is a no-op (`posted=false`), different means `ErrRefConflict`,
which is always a bug. Every caller names an entry after its *business event*, not after an
attempt, so retrying any step is safe by construction.

**Invariants.** The payout tests assert all four after every scenario. The ledger, deposit and seed
tests assert 1 and 2, and the API test asserts 2 once over HTTP:

1. Every entry sums to zero per asset (`TestUnbalancedRejectedByDatabase`, `TestPerAssetBalance`).
2. The sandbox trial balance is zero per asset (`TestTrialBalanceZero`, `TestSeed…`).
3. 2020 for a customer equals that customer's reserved payouts; 2100 equals the pesos in flight.
4. Ledger 1100 equals the bank statement balance, unless the bank did something Puente didn't
   (and then recon reports it).

## 5. Deposits

The scanner (`chain.Scan`) reads the token's `Transfer` logs to watched addresses:

- **Paging.** It reads in windows of ≤200 blocks, because public RPCs reject larger `eth_getLogs`
  ranges (Base Sepolia: `-32614`). A per-network cursor makes the scan resumable.
- **Confirmations.** A transfer is `seen`, then `confirmed` after N blocks (3 on the simulated
  chain, 5 by default live).
- **Reorgs.** Every scan re-checks the stored block hash of transfers in the last 64 blocks; a
  mismatch or a missing block marks the transfer `orphaned`.
- **Re-reading the reorg window.** Every scan also re-reads the feed for that whole 64-block
  window, wherever the cursor is. A log a reorg moved into an already-scanned block, or one a
  lagging RPC reported late, is therefore found on the next scan. An orphaned transaction that
  is re-included is *revived*; on go-ethereum's simulated backend this happened in 15 of 15
  reorgs.
- **Catching up.** One scan makes at most 25 `eth_getLogs` calls, so a long catch-up commits its
  progress in chunks. Each RPC call has a 15 s HTTP timeout.
- **Idempotency.** `chain_transfers` is unique on `(sandbox, chain_id, tx_hash, log_index)`, so
  rescans add nothing.

`deposit.Credit` posts `deposit:<transfer>:<generation>` for confirmed transfers to a customer's
address. It posts `deposit_reversal:<transfer>:<generation>` when a credited transfer is
orphaned, or comes back `seen` (orphaned and revived within one scan into a block that isn't
confirmed yet). Once the new block is confirmed, the transfer is credited again as generation 2.
A transaction re-included deep enough to confirm in the same scan simply keeps its credit. A partial unique index allows at most one open credit per
transfer. Candidate rows are locked `FOR UPDATE` and re-read after the lock, so concurrent
crediters can't double-credit; the test fails if the lock is removed. A reversal may take a
customer negative. That is deliberate: it is surfaced as a liability to collect, not hidden.

## 6. Payouts

```
reserved ─► submitted ─► liquidated ─► returned (devolución)
   │            ├──► rejected ─► released
   │            └──► needs_review ─► liquidated | rejected
   └──► released (cancel)
```

- **Create** (one transaction):
  - Lock the customer row (`FOR NO KEY UPDATE`).
  - Check available = −balance(2010·c) ≥ amount + fee.
  - Insert the payout, post `reserve` and insert an **outbox** row.
  - Ten concurrent creates against funds for three produce exactly three
    (`TestInsufficientFunds`).
- **Worker:**
  - Claims outbox rows with `FOR UPDATE SKIP LOCKED`, so N workers never take the same job
    (`TestConcurrentWorkers`: 4 workers, 20 payouts, each submitted once).
  - The first attempt moves the payout to `submitted` and posts `submit` *before* calling the rail.
    The ledger never claims money is still reserved after it may have left.
- **The rail call is idempotent on the clave de rastreo,** which is fixed per payout:
  - On a timeout (the order was accepted but the ack was lost), the job is retried with
    exponential backoff (1 s doubling, capped at 5 min) and the **same clave**. The rail returns
    the existing order instead of creating a second one, and the ledger dedupes by ref
    (`TestRetryExactlyOnce`: one order, submit_count 2, one submit entry).
  - After 5 timeouts the payout goes to `needs_review`. A later rail sync resolves it from the
    bank's answer.
- **Rail sync** advances the simulated bank and applies its outcomes to every non-terminal
  payout: settle, reject (release with the fee refunded), and return (compensating entry; no row
  is ever deleted).
- **Lock order.**
  - The worker takes the outbox job, then the sandbox, then the payout. `Rail.Submit` and the
    rail sync take the sandbox too, so they serialize behind it without deadlocking.
  - `Create` locks the customer (`FOR NO KEY UPDATE`) and touches the sandbox only through
    foreign-key share locks.
  - `SimulateDeposit` locks the sandbox before inserting anything. Taking a key-share lock first
    and upgrading it deadlocked two concurrent first deposits; `TestConcurrentFirstDeposits`
    covers this.
- **Error paths.**
  - A `lock_timeout` (10 s) and a `statement_timeout` (60 s) on every connection, plus a
    deadline on every tick, turn any lock wait that slips through into an error rather than a
    hang.
  - Non-timeout submission errors (a database error, or a clave conflict that would be a bug)
    are retried with backoff and do not move the payout to `needs_review`. The last error is
    recorded on the payout.

## 7. The simulated SPEI participant

`spei` keeps its own books in tables the ledger never reads or writes:
- `spei_orders`, unique on the clave;
- `spei_statement`, the settlement account.

That separation is what makes it a valid second source for reconciliation.

- **What it models:**
  - Statuses: `en_proceso`, `liquidado`, `rechazado`, `devuelto`.
  - Rejection reasons in Spanish.
  - CLABE validation: check digit (weights 3, 7, 1) and bank catalog.
  - Claves of at most 30 characters.
  - A CEP-like receipt for settled orders, sealed with SHA-256 and marked `simulado`.
- **It is not conformant with Banxico's specification.** The official devolución catalog and the
  CEP format were not available during discovery.
- **Fault switches**, each consumed after one use:
  - `rail_timeout`: accept the order, lose the ack;
  - `rail_reject`: reject the order;
  - `rail_return`: return the order after it settles.
- **Bank-side injections:**
  - a bank fee;
  - a manual order keyed in at the bank's portal.

## 8. Reconciliation

A run reads every source inside **one REPEATABLE READ, read-only transaction**, so the ledger, the
chain tables and the bank tables are compared as of one instant while the worker keeps running.
It writes `recon_runs` and `recon_breaks` with evidence (tx hash and explorer link, clave, CEP
seal, ledger steps), and **never fixes anything**.

| Matcher | Compares | Breaks |
|---|---|---|
| Chain (per network) | open deposit credits ↔ canonical logs (`Source.Lookup`) | `duplicate` (one log credited twice), `amount_mismatch` (feed ≠ chain), `missing_on_chain` (reorged or invented), `missing_on_ledger` (confirmed but not credited) |
| Rail | each payout's journal steps and in-flight pesos ↔ the bank's order for its clave | `missing_on_rail`, `missing_on_ledger` (settled, rejected or returned at the bank but not booked; or an order no payout sent), `amount_mismatch` |
| Balances | ledger 1100 ↔ bank statement | `balance_mismatch` |

The rail matcher reads the **journal**, not payout statuses. A bug that changes a status without
posting is still caught.

The simulated chain separates the indexer **feed** (what Puente ingests) from the **canonical**
chain (what recon checks). That is how the duplicate-log and short-deposit faults produce real
breaks. Every break kind has a scenario test in `internal/recon` that starts from a clean world
with zero breaks.

## 9. API guarantees

- **Sandboxes.**
  - The `puente_sandbox` cookie (HttpOnly, SameSite=Lax, Secure) selects a visitor sandbox. A
    request without one gets a newly seeded sandbox.
  - Every row carries `sandbox_id`. Child tables reference their parent with composite
    `(id, sandbox_id)` foreign keys, so a row can't point into another sandbox.
  - A forged cookie naming the live sandbox is ignored (`TestSandboxCookie`).
  - Creating a sandbox seeds a whole world, so it is rate-limited per client IP (a burst of
    5, then one a minute) and globally (600 an hour). Over the limit the answer is 429. Behind
    Render the client IP is the last `X-Forwarded-For` hop, which the client can't forge.
  - The background loop ticks at most the 200 most recently active sandboxes (seen in the last
    10 minutes) and prunes visitor sandboxes idle for a day.
  - A sandbox being seeded is left out of new loop cycles until the seed finishes. A cycle
    already in progress may still tick it, which the seed tolerates: it waits on database
    state.
- **Timeouts.** `/api/sandbox/advance` has a 20 s deadline. A request that runs out of time, or
  hits a lock or statement timeout, gets 503 `UNAVAILABLE` and can be retried.
- **Idempotency.** `POST /api/payouts`, `/api/payouts/{id}/cancel` and `/api/deposits/simulate`
  require an `Idempotency-Key`:
  - A replay returns the stored status and body byte for byte, with `Idempotent-Replay: true`.
    4xx outcomes are stored too.
  - The same key with a different request returns 422; a request still in flight returns 409.
  - A 5xx releases the key.
  - Ten concurrent identical requests create one payout (`TestConcurrentPayout`).
- **Errors** are always `{"error": {"code", "message"}}`.

## 10. Live testnet mode

When `PUENTE_LIVE_WATCH_ADDRESS` and an RPC are configured, a single shared, read-only **live
sandbox** follows:
- USDC on Base Sepolia (`0x036C…CF7e`);
- MXNB on Arbitrum Sepolia (Bitso's proxy `0x82B9…247D`).

Both addresses were verified on-chain during discovery. It runs the same scanner, crediting and
reconciliation as the visitor sandboxes, paced every 15 s to respect public RPC limits. Anyone can
send testnet USDC to the watched address and watch it get credited and reconciled. Sending is
only possible from `puente chain send --yes` with an operator key; the API has no route to it.

## 11. Failure modes

| Failure | What happens | Where it's tested |
|---|---|---|
| Rail ack lost | Retry with the same clave; one order, one entry | `payout` `TestRetryExactlyOnce` |
| Rail never answers | `needs_review`, resolved by a later sync | same |
| Process crash mid-attempt | The attempt is one transaction (with the simulated rail in the same database), so it rolls back whole and the job is retried. With an external rail, the submit entry and the call can't share a transaction; the fixed clave plus exactly-once `Post` make the retry safe | outbox + `TestRetryExactlyOnce` |
| Crash between handler commit and storing the idempotent response | The key stays pending and answers 409 rather than risk a second execution | `api` middleware (documented trade-off) |
| Two workers, one job | `SKIP LOCKED` | `TestConcurrentWorkers` |
| Indexer duplicates a log | Credited twice → `duplicate` break | `recon` `TestDuplicateLog` |
| Indexer misreports an amount | `amount_mismatch` | `TestShortDeposit` |
| Reorg after credit | `missing_on_chain` until the rescan, then reversed | `TestReorgBeforeAndAfterRescan` |
| Bank charges a fee | `balance_mismatch` | `TestBankFee` |
| Someone pays from the bank portal | `missing_on_ledger` + `balance_mismatch` | `TestManualOrder` |
| Settlement not yet synced | `missing_on_ledger` until the next sync | `TestSettlementNotYetBooked` |
| Unbalanced write by any path | Rejected at commit | `TestUnbalancedRejectedByDatabase` |
| A reorg moves a deposit into an already-scanned block | Found when the reorg window is re-read | `regression` `TestReorgAddsLogBelowCursor` |
| A batch transfer: two real logs, same recipient and amount | Credited twice, correctly; no break | `TestBatchTransferIsNotADuplicate` |
| Many parallel ticks of one sandbox | Serialized on the sandbox lock; no pool exhaustion | `TestConcurrentTicksDoNotExhaustPool` |
| A request floods new sandboxes | 429 after the per-client burst | `api` `TestSandboxCreationRateLimit` |
| The background loop ticks a sandbox mid-seed | The sandbox is hidden until seeded | `TestSeedSurvivesConcurrentTicker` |

## 12. Alternatives considered

- **Live testnet only, no simulated chain.**
  - Rejected: it can't run offline (`docker compose up`), it can't inject faults, so
    reconciliation would be untestable on demand, and it is fragile under public RPC limits.
  - The simulated chain plus a read-only live mode keeps the realism without the fragility.
- **Hand-rolled JSON-RPC and transaction signing instead of go-ethereum.**
  - Reads are trivial, but signing (RLP, EIP-1559, recovery ids) is a correctness risk with no
    showcase value. go-ethereum also provides `ethclient/simulated`, an in-process EVM that makes
    the reorg tests possible.
  - The cost is accepted: about 177 modules (314 including test-only deps) and a 13 MB binary.
- **Stellar or XRPL as the first chain.**
  - Deferred. MXNB's documented testnet is Arbitrum Sepolia, and one EVM adapter serves both
    assets by configuration. A Stellar adapter is a new `chain.Source`.
- **A message broker for the outbox.**
  - Rejected for v1: Postgres `SKIP LOCKED` gives at-least-once delivery with transactional
    enqueue and no extra infrastructure. The refs make the effects exactly-once.
- **Auto-healing reconciliation.**
  - Rejected: a break means something unexpected happened, and fixing it blindly destroys the
    evidence. Repairs belong to explicit, audited operations.

## 13. Open questions and next steps

- **Idempotency outcomes are stored outside the business transaction.**
  - If the money effect commits but the connection drops during COMMIT, the handler answers
    5xx, the key is released, and a retry can create a second payout.
  - If storing the outcome fails after a good commit, the key answers 409 forever.
  - The fix is to write the idempotency row in the same transaction as the effect; v1 accepts
    the window.
- **Global sandbox-creation budget.** Rate limits stop a flood, but enough distinct addresses
  can drain the hourly global budget, and then new visitors get 429 until it refills. Existing
  visitors keep working. Options: serve a shared read-only sandbox when the budget runs out, or
  raise the cap and alert.
- **Client IP on Render.** It is taken from the last `X-Forwarded-For` hop; check this against
  real traffic on the first deploy.
- **Changing the live watch address** leaves earlier deposits to the old address unattributed.
  Reconciliation reports them as `missing_on_ledger` from then on.
- **Session limits are startup parameters,** so `DATABASE_URL` must be a direct connection, not a
  transaction-pooled one (for Neon, the non-`-pooler` host).

- **Scheduled reconciliation runs only while the free Render instance is awake.** In production
  it would be its own scheduled job.
- **`SyncRail` rescans every liquidated payout to catch devoluciones.** It's fine at demo scale;
  it needs a time window, or rail webhooks, at volume.
- **Live mode is deposits only.** Payouts would need a real off-ramp (Juno/Bitso, which requires
  KYB).
- **Break workflow.** Acknowledge, assign, resolve with a linked correcting entry.
- **Multi-currency payouts.** USD over ACH/RTP from the same core; this is the planned "US–MX
  cross-border payout orchestrator".
