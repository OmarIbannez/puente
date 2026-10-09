// Package deposit credits confirmed on-chain deposits to customer balances
// and reverses them when a reorg orphans the transfer.
//
// The chain scanner (internal/chain) stores what the feed reports in
// chain_transfers and moves each transfer through seen, confirmed and
// orphaned. Credit turns those statuses into ledger entries:
//
//   - a confirmed transfer to a customer's deposit address is posted as
//     deposit:<transfer_id>:<generation> (Dr 1010 / Cr 2010 for the customer);
//   - an orphaned transfer whose credit is still open is posted as
//     deposit_reversal:<transfer_id>:<generation>, the inverse. The customer
//     may go negative; reconciliation reports it rather than hiding it;
//   - a confirmed transfer to an address no customer owns is left alone and
//     counted, for reconciliation to flag.
//
// A reorg can re-include an orphaned transaction, and the scanner then
// revives the same transfer row. Credits are therefore keyed by generation:
// the revived transfer is credited again under the next generation, and at
// most one credit per transfer is open at a time.
package deposit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubjectType is the journal entry subject type of deposit entries; the
// subject id is the chain transfer id.
const SubjectType = "chain_transfer"

// Result counts what one Credit call did.
type Result struct {
	// Credited is the number of transfers credited.
	Credited int `json:"credited"`
	// Reversed is the number of credits reversed.
	Reversed int `json:"reversed"`
	// Unattributed is the number of confirmed transfers of the network to
	// addresses no customer owns. They are never credited.
	Unattributed int `json:"unattributed"`
}

// SyncResult is what one Sync did: the scan, then the crediting.
type SyncResult struct {
	// Scan is the chain scan's result.
	Scan chain.ScanResult `json:"scan"`
	// Credit is the crediting's result.
	Credit Result `json:"credit"`
}

// CreditRef is the journal entry ref of a transfer's credit in generation.
func CreditRef(transferID int64, generation int) string {
	return fmt.Sprintf("deposit:%d:%d", transferID, generation)
}

// ReversalRef is the journal entry ref of the reversal of a transfer's
// credit in generation.
func ReversalRef(transferID int64, generation int) string {
	return fmt.Sprintf("deposit_reversal:%d:%d", transferID, generation)
}

// SourceFunc returns the chain source to scan, reading through tx. The
// simulated chain lives in the database and must use tx: reading it through
// the pool would need a second connection while tx holds the sandbox lock,
// and enough concurrent syncs would then exhaust the pool and hang. An
// RPC-backed source ignores tx.
type SourceFunc func(tx pgx.Tx) chain.Source

// Sync scans the source for net in one transaction on pool and credits what
// the scan confirmed (or reverses what it orphaned) in the same transaction.
// The worker loop calls it once per sandbox and network per tick.
func Sync(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID, net chain.Network, src SourceFunc, watch []string, confirmations uint64) (SyncResult, error) {
	var res SyncResult
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		if res.Scan, err = chain.Scan(ctx, tx, sandbox, net, src(tx), watch, confirmations); err != nil {
			return fmt.Errorf("deposit: sync %s: %w", net.Key, err)
		}
		if res.Credit, err = Credit(ctx, tx, sandbox, net); err != nil {
			return fmt.Errorf("deposit: sync %s: %w", net.Key, err)
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return res, nil
}

// WatchAddresses returns the sandbox's customer deposit addresses on net,
// the watch list to scan it with.
func WatchAddresses(ctx context.Context, q store.Querier, sandbox uuid.UUID, net chain.Network) ([]string, error) {
	addrs, err := customer.Addresses(ctx, q, sandbox, net.Key)
	if err != nil {
		return nil, fmt.Errorf("deposit: watch addresses %s: %w", net.Key, err)
	}
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.Address
	}
	return out, nil
}

// Credit brings the ledger in line with the sandbox's chain transfers of net:
// every confirmed transfer to a customer address without an open credit is
// credited under the next generation, and every orphaned transfer with an
// open credit is reversed, as is one that went back to seen (orphaned and
// revived within one scan, so its block changed under the credit).
//
// Credit is idempotent, and concurrent calls on one sandbox never credit a
// transfer twice: the transfer rows it acts on stay locked (FOR UPDATE, in
// id order) until tx ends, and each transfer's credit state is re-read after
// its lock is held. The ledger's exactly-once refs and the one-open-credit
// index back this up. tx should run at READ COMMITTED (the default).
func Credit(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net chain.Network) (Result, error) {
	var res Result
	rows, err := tx.Query(ctx, `SELECT t.id FROM chain_transfers t
		WHERE t.sandbox_id = $1 AND t.network = $2 AND (
			(t.status = 'confirmed'
				AND NOT EXISTS (SELECT 1 FROM deposit_credits c
					WHERE c.sandbox_id = t.sandbox_id AND c.transfer_id = t.id AND c.reversal_entry_id IS NULL)
				AND EXISTS (SELECT 1 FROM deposit_addresses a
					WHERE a.sandbox_id = t.sandbox_id AND a.network = t.network AND a.address = t.to_addr))
			OR (t.status IN ('orphaned', 'seen')
				AND EXISTS (SELECT 1 FROM deposit_credits c
					WHERE c.sandbox_id = t.sandbox_id AND c.transfer_id = t.id AND c.reversal_entry_id IS NULL)))
		ORDER BY t.id
		FOR UPDATE OF t`, sandbox, net.Key)
	if err != nil {
		return res, fmt.Errorf("deposit: credit %s: candidates: %w", net.Key, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return res, fmt.Errorf("deposit: credit %s: candidates: %w", net.Key, err)
	}
	for _, id := range ids {
		action, err := apply(ctx, tx, sandbox, net, id)
		if err != nil {
			return res, err
		}
		switch action {
		case actionCredited:
			res.Credited++
		case actionReversed:
			res.Reversed++
		case actionNone:
		}
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chain_transfers t
		WHERE t.sandbox_id = $1 AND t.network = $2 AND t.status = 'confirmed'
		  AND NOT EXISTS (SELECT 1 FROM deposit_addresses a
			WHERE a.sandbox_id = t.sandbox_id AND a.network = t.network AND a.address = t.to_addr)`,
		sandbox, net.Key).Scan(&res.Unattributed); err != nil {
		return res, fmt.Errorf("deposit: credit %s: unattributed: %w", net.Key, err)
	}
	return res, nil
}

type action int

const (
	actionNone action = iota
	actionCredited
	actionReversed
)

// state is a locked transfer as the latest committed data shows it.
type state struct {
	status     string
	txHash     string
	logIndex   int64
	amount     int64
	owner      *uuid.UUID
	generation int // the latest generation, 0 if never credited
	open       bool
	customer   uuid.UUID // of the open credit
	asset      string    // of the open credit
	credited   int64     // amount of the open credit
}

// apply credits or reverses the locked transfer id, re-reading its state
// first: a concurrent Credit may have acted on it while this one waited for
// the lock.
func apply(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net chain.Network, id int64) (action, error) {
	var (
		s          state
		customerID *uuid.UUID
		asset      *string
		credited   *int64
	)
	err := tx.QueryRow(ctx, `SELECT t.status, t.tx_hash, t.log_index, t.amount_minor, a.customer_id,
			COALESCE((SELECT max(generation) FROM deposit_credits g
				WHERE g.sandbox_id = t.sandbox_id AND g.transfer_id = t.id), 0),
			c.customer_id, c.asset, c.amount_minor
		FROM chain_transfers t
		LEFT JOIN deposit_addresses a
			ON a.sandbox_id = t.sandbox_id AND a.network = t.network AND a.address = t.to_addr
		LEFT JOIN deposit_credits c
			ON c.sandbox_id = t.sandbox_id AND c.transfer_id = t.id AND c.reversal_entry_id IS NULL
		WHERE t.sandbox_id = $1 AND t.id = $2`, sandbox, id).Scan(
		&s.status, &s.txHash, &s.logIndex, &s.amount, &s.owner, &s.generation, &customerID, &asset, &credited)
	if err != nil {
		return actionNone, fmt.Errorf("deposit: credit %s: read transfer %d: %w", net.Key, id, err)
	}
	if customerID != nil && asset != nil && credited != nil {
		s.open, s.customer, s.asset, s.credited = true, *customerID, *asset, *credited
	}
	switch {
	case s.status == chain.StatusConfirmed && !s.open && s.owner != nil:
		return actionCredited, credit(ctx, tx, sandbox, net, id, s)
	case (s.status == chain.StatusOrphaned || s.status == chain.StatusSeen) && s.open:
		// Only an orphan-and-revive takes a credited transfer back to
		// seen. Its credit came from a block that is gone, so it is
		// reversed; once the new block has its confirmations, the
		// transfer is credited again under the next generation.
		return actionReversed, reverse(ctx, tx, sandbox, net, id, s)
	default:
		return actionNone, nil
	}
}

func credit(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net chain.Network, id int64, s state) error {
	gen := s.generation + 1
	amount := money.New(net.Asset, s.amount)
	entryID, _, err := ledger.Post(ctx, tx, ledger.Entry{
		SandboxID: sandbox,
		Ref:       CreditRef(id, gen),
		Kind:      ledger.KindDeposit,
		Memo:      fmt.Sprintf("Deposit %s/%d on %s", s.txHash, s.logIndex, net.Name),
		Subject:   &ledger.Subject{Type: SubjectType, ID: strconv.FormatInt(id, 10)},
		Postings: []ledger.Posting{
			ledger.Debit(ledger.HotWallet, amount),
			ledger.Credit(ledger.CustomerBalances, amount).For(*s.owner),
		},
	})
	if err != nil {
		return fmt.Errorf("deposit: credit transfer %d: %w", id, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deposit_credits
		(sandbox_id, transfer_id, generation, customer_id, asset, amount_minor, credit_entry_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sandbox, id, gen, *s.owner, string(net.Asset), s.amount, entryID); err != nil {
		return fmt.Errorf("deposit: credit transfer %d: record credit: %w", id, err)
	}
	return nil
}

func reverse(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net chain.Network, id int64, s state) error {
	amount := money.New(money.Asset(s.asset), s.credited)
	entryID, _, err := ledger.Post(ctx, tx, ledger.Entry{
		SandboxID: sandbox,
		Ref:       ReversalRef(id, s.generation),
		Kind:      ledger.KindDepositReversal,
		Memo:      fmt.Sprintf("Reorg orphaned deposit %s/%d on %s", s.txHash, s.logIndex, net.Name),
		Subject:   &ledger.Subject{Type: SubjectType, ID: strconv.FormatInt(id, 10)},
		Postings: []ledger.Posting{
			ledger.Debit(ledger.CustomerBalances, amount).For(s.customer),
			ledger.Credit(ledger.HotWallet, amount),
		},
	})
	if err != nil {
		return fmt.Errorf("deposit: reverse transfer %d: %w", id, err)
	}
	tag, err := tx.Exec(ctx, `UPDATE deposit_credits SET reversal_entry_id = $4, reversed_at = now()
		WHERE sandbox_id = $1 AND transfer_id = $2 AND generation = $3 AND reversal_entry_id IS NULL`,
		sandbox, id, s.generation, entryID)
	if err != nil {
		return fmt.Errorf("deposit: reverse transfer %d: record reversal: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("deposit: reverse transfer %d: open credit of generation %d not found", id, s.generation)
	}
	return nil
}

// Credit states of a View.
const (
	// StatePending is a transfer to a customer address that is not credited
	// (yet): too few confirmations, waiting for the next Credit, or
	// orphaned before it was ever credited.
	StatePending = "pending"
	// StateCredited is a transfer whose latest credit is open.
	StateCredited = "credited"
	// StateReversed is a transfer whose latest credit was reversed after a
	// reorg orphaned it.
	StateReversed = "reversed"
	// StateUnattributed is a transfer to an address no customer owns.
	StateUnattributed = "unattributed"
)

// Page sizes for List.
const (
	// DefaultListLimit is used when Filter.Limit is zero or negative.
	DefaultListLimit = 50
	// MaxListLimit caps Filter.Limit.
	MaxListLimit = 500
)

// ErrUnknownNetwork is returned by List for a filter network that is not a
// known network key.
var ErrUnknownNetwork = errors.New("deposit: unknown network")

// Filter selects a page of deposits, newest first.
type Filter struct {
	// Network keeps only one network's deposits when not empty.
	Network string
	// Limit is the page size (DefaultListLimit if ≤ 0, capped at
	// MaxListLimit).
	Limit int
	// BeforeID returns transfers with id < BeforeID when positive (keyset
	// pagination).
	BeforeID int64
}

// View is one chain transfer as the deposits API shows it: what the chain
// reported, who it belongs to and where it stands in the ledger.
type View struct {
	// TransferID is the chain transfer id.
	TransferID int64 `json:"transfer_id"`
	// Network is the network key.
	Network string `json:"network"`
	// TxHash is the transaction hash.
	TxHash string `json:"tx_hash"`
	// LogIndex is the log's position in its block.
	LogIndex uint `json:"log_index"`
	// From is the sender.
	From string `json:"from"`
	// To is the recipient deposit address.
	To string `json:"to"`
	// Amount is the amount the feed reported.
	Amount money.Amount `json:"amount"`
	// BlockNumber is the block holding the log.
	BlockNumber uint64 `json:"block_number"`
	// Status is the chain status: seen, confirmed or orphaned.
	Status string `json:"status"`
	// ExplorerURL links the transaction on a public explorer; empty for
	// simulated networks.
	ExplorerURL string `json:"explorer_url,omitempty"`
	// CustomerID is the owner of the recipient address, or of the latest
	// credit; zero when unattributed.
	CustomerID uuid.UUID `json:"customer_id,omitzero"`
	// CustomerName is the customer's name; empty when unattributed.
	CustomerName string `json:"customer_name,omitempty"`
	// State is the credit state: pending, credited, reversed or
	// unattributed.
	State string `json:"state"`
	// Generation is the latest credit's generation, 0 if never credited.
	Generation int `json:"generation"`
	// CreatedAt is when the scanner first stored the transfer.
	CreatedAt time.Time `json:"created_at"`
}

// List returns a page of the sandbox's deposits (every chain transfer, with
// its owner and credit state), newest first, in one query.
func List(ctx context.Context, q store.Querier, sandbox uuid.UUID, f Filter) ([]View, error) {
	if f.Network != "" {
		if _, ok := chain.NetworkByKey(f.Network); !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownNetwork, f.Network)
		}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	rows, err := q.Query(ctx, `SELECT t.id, t.network, t.tx_hash, t.log_index, t.from_addr, t.to_addr,
			t.asset, t.amount_minor, t.block_number, t.status, t.created_at,
			cu.id, cu.name, a.customer_id IS NOT NULL,
			COALESCE(c.generation, 0), c.reversal_entry_id IS NULL
		FROM chain_transfers t
		LEFT JOIN deposit_addresses a
			ON a.sandbox_id = t.sandbox_id AND a.network = t.network AND a.address = t.to_addr
		LEFT JOIN LATERAL (SELECT generation, customer_id, reversal_entry_id FROM deposit_credits d
			WHERE d.sandbox_id = t.sandbox_id AND d.transfer_id = t.id
			ORDER BY generation DESC LIMIT 1) c ON true
		LEFT JOIN customers cu
			ON cu.sandbox_id = t.sandbox_id AND cu.id = COALESCE(c.customer_id, a.customer_id)
		WHERE t.sandbox_id = $1
		  AND ($2 = '' OR t.network = $2)
		  AND ($3 <= 0 OR t.id < $3)
		ORDER BY t.id DESC
		LIMIT $4`, sandbox, f.Network, f.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("deposit: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanView)
	if err != nil {
		return nil, fmt.Errorf("deposit: list: %w", err)
	}
	return out, nil
}

func scanView(row pgx.CollectableRow) (View, error) {
	var (
		v               View
		asset           string
		logIndex, block int64
		customerID      *uuid.UUID
		customerName    *string
		owned, open     bool
	)
	if err := row.Scan(&v.TransferID, &v.Network, &v.TxHash, &logIndex, &v.From, &v.To,
		&asset, &v.Amount.Minor, &block, &v.Status, &v.CreatedAt,
		&customerID, &customerName, &owned, &v.Generation, &open); err != nil {
		return View{}, err
	}
	v.Amount.Asset = money.Asset(asset)
	idx, err := chain.ToUint64(logIndex)
	if err != nil {
		return View{}, fmt.Errorf("transfer %d log index: %w", v.TransferID, err)
	}
	v.LogIndex = uint(idx)
	if v.BlockNumber, err = chain.ToUint64(block); err != nil {
		return View{}, fmt.Errorf("transfer %d block: %w", v.TransferID, err)
	}
	if customerID != nil {
		v.CustomerID = *customerID
	}
	if customerName != nil {
		v.CustomerName = *customerName
	}
	if net, ok := chain.NetworkByKey(v.Network); ok && !net.Simulated {
		v.ExplorerURL = net.TxURL(v.TxHash)
	}
	v.State = viewState(v.Status, owned, v.Generation, open)
	return v, nil
}

// viewState derives a View's credit state from the chain status, whether
// a customer owns the address, and the latest credit (generation 0: none).
func viewState(status string, owned bool, generation int, open bool) string {
	switch {
	case generation > 0 && open:
		return StateCredited
	case generation > 0 && status == chain.StatusOrphaned:
		return StateReversed
	case generation > 0, owned:
		// A reversed credit whose transfer was revived waits for the next
		// generation, like a never-credited transfer to a customer address.
		return StatePending
	default:
		return StateUnattributed
	}
}
