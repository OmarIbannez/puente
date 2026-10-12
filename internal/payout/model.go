// Package payout is Puente's payout engine: it turns a customer's stablecoin
// balance into a SPEI transfer in pesos.
//
// A payout is quoted and reserved in one transaction together with an
// outbox job (Create). A Worker claims outbox jobs with FOR UPDATE SKIP
// LOCKED and submits them to the rail with the payout's fixed clave de
// rastreo, retrying lost acknowledgements with backoff; SyncRail applies
// the rail's later outcomes. Every state change goes through one
// transition function that locks the payout row, checks the edge against
// the state machine, posts the edge's journal entry (exactly once, by ref
// "payout:<id>:<step>") and records an event, all in the caller's
// transaction:
//
//	reserved ─► submitted ─► liquidated ─► returned
//	   │            ├──► rejected ─► released
//	   │            └──► needs_review ─► liquidated | rejected
//	   └──► released
package payout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Status is the state of a payout.
type Status string

// Payout statuses.
const (
	// StatusReserved: funds earmarked; waiting for the worker.
	StatusReserved Status = "reserved"
	// StatusSubmitted: the order went to the rail; pesos are in flight.
	StatusSubmitted Status = "submitted"
	// StatusLiquidated: the rail settled the order.
	StatusLiquidated Status = "liquidated"
	// StatusRejected: the rail refused the order; the funds are about to be
	// released.
	StatusRejected Status = "rejected"
	// StatusReleased: the funds went back to the customer (cancel or reject).
	StatusReleased Status = "released"
	// StatusReturned: the beneficiary bank sent the pesos back after
	// settlement (devolución); the customer got the amount back, not the fee.
	StatusReturned Status = "returned"
	// StatusNeedsReview: the rail never acknowledged the order after every
	// retry; a later rail sync resolves it.
	StatusNeedsReview Status = "needs_review"
)

// Statuses lists every payout status.
var Statuses = []Status{
	StatusReserved, StatusSubmitted, StatusLiquidated, StatusRejected,
	StatusReleased, StatusReturned, StatusNeedsReview,
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Errors returned by the payout engine.
var (
	// ErrInsufficientFunds: the customer's available balance does not cover
	// amount plus fee.
	ErrInsufficientFunds = errors.New("payout: insufficient funds")
	// ErrInvalidCLABE: the beneficiary CLABE failed validation.
	ErrInvalidCLABE = errors.New("payout: invalid beneficiary CLABE")
	// ErrUnsupportedAsset: the asset cannot be paid out (or has no rate).
	ErrUnsupportedAsset = errors.New("payout: unsupported asset")
	// ErrInvalidAmount: the amount is not positive, too large, or too small
	// to convert to at least one centavo.
	ErrInvalidAmount = errors.New("payout: invalid amount")
	// ErrInvalidRequest: another request field is missing or malformed.
	ErrInvalidRequest = errors.New("payout: invalid request")
	// ErrIllegalTransition: the state machine has no such edge.
	ErrIllegalTransition = errors.New("payout: illegal transition")
	// ErrNotFound: no such payout in the sandbox.
	ErrNotFound = fmt.Errorf("payout: not found: %w", store.ErrNotFound)
	// ErrCustomerNotFound: no such customer in the sandbox.
	ErrCustomerNotFound = fmt.Errorf("payout: customer not found: %w", store.ErrNotFound)
	// ErrNotCancellable: the payout already left the reserved state.
	ErrNotCancellable = errors.New("payout: not cancellable")
)

// Payout is one payout with its fixed quote.
type Payout struct {
	ID         uuid.UUID `json:"id"`
	SandboxID  uuid.UUID `json:"-"`
	CustomerID uuid.UUID `json:"customer_id"`
	// Amount is what the beneficiary receives, in the source asset.
	Amount money.Amount `json:"amount"`
	// Fee is Puente's fee, in the source asset.
	Fee money.Amount `json:"fee"`
	// Rate is MXN per whole unit of the source asset.
	Rate money.Rate `json:"rate"`
	// MXN is Amount converted at Rate, rounded down to the centavo.
	MXN              money.Amount `json:"mxn"`
	BeneficiaryCLABE string       `json:"beneficiary_clabe"`
	BeneficiaryName  string       `json:"beneficiary_name"`
	Concepto         string       `json:"concepto"`
	// Clave is the clave de rastreo, fixed for every submission.
	Clave  string `json:"clave"`
	Status Status `json:"status"`
	// Attempts counts submissions to the rail.
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Total is Amount plus Fee: what leaves the customer's balance.
func (p Payout) Total() money.Amount {
	return money.New(p.Amount.Asset, p.Amount.Minor+p.Fee.Minor)
}

// Event is one entry of a payout's state history. From is empty for the
// creation event.
type Event struct {
	ID        int64           `json:"id"`
	PayoutID  uuid.UUID       `json:"payout_id"`
	From      Status          `json:"from,omitempty"`
	To        Status          `json:"to"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}

// Change is one transition applied by the worker or a rail sync.
type Change struct {
	PayoutID uuid.UUID `json:"payout_id"`
	Clave    string    `json:"clave"`
	From     Status    `json:"from"`
	To       Status    `json:"to"`
}

// Page sizes for List.
const (
	// DefaultListLimit is used when Filter.Limit is zero or negative.
	DefaultListLimit = 50
	// MaxListLimit caps Filter.Limit.
	MaxListLimit = 500
)

// Filter narrows and pages List. Zero-valued fields do not filter.
type Filter struct {
	Status     Status
	CustomerID uuid.UUID
	// Limit is the page size: DefaultListLimit when ≤ 0, at most MaxListLimit.
	Limit int
	// BeforeTime returns only payouts created strictly before it.
	BeforeTime time.Time
}

const payoutColumns = `id, sandbox_id, customer_id, asset, amount_minor, fee_minor, rate, mxn_centavos,
	beneficiary_clabe, beneficiary_name, concepto, clave, status, attempts, last_error,
	created_at, updated_at`

func scanPayout(row pgx.Row) (Payout, error) {
	var (
		p                  Payout
		asset, status      string
		amount, fee, cents int64
		rate               int64
	)
	if err := row.Scan(&p.ID, &p.SandboxID, &p.CustomerID, &asset, &amount, &fee, &rate, &cents,
		&p.BeneficiaryCLABE, &p.BeneficiaryName, &p.Concepto, &p.Clave, &status, &p.Attempts,
		&p.LastError, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Payout{}, err
	}
	p.Amount = money.New(money.Asset(asset), amount)
	p.Fee = money.New(money.Asset(asset), fee)
	p.Rate = money.Rate(rate)
	p.MXN = money.New(money.MXN, cents)
	p.Status = Status(status)
	return p, nil
}

func collectPayout(row pgx.CollectableRow) (Payout, error) { return scanPayout(row) }

// Get returns the sandbox's payout with id, or an error wrapping ErrNotFound.
func Get(ctx context.Context, q store.Querier, sandbox, id uuid.UUID) (Payout, error) {
	return getPayout(ctx, q, sandbox, id, "")
}

// lockPayout returns the payout with its row locked FOR UPDATE.
func lockPayout(ctx context.Context, tx pgx.Tx, sandbox, id uuid.UUID) (Payout, error) {
	return getPayout(ctx, tx, sandbox, id, " FOR UPDATE")
}

func getPayout(ctx context.Context, q store.Querier, sandbox, id uuid.UUID, suffix string) (Payout, error) {
	p, err := scanPayout(q.QueryRow(ctx,
		"SELECT "+payoutColumns+" FROM payouts WHERE sandbox_id = $1 AND id = $2"+suffix, sandbox, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Payout{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Payout{}, fmt.Errorf("payout: get %s: %w", id, err)
	}
	return p, nil
}

// List returns the sandbox's payouts matching f, newest first.
func List(ctx context.Context, q store.Querier, sandbox uuid.UUID, f Filter) ([]Payout, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	limit = min(limit, MaxListLimit)
	var before *time.Time
	if !f.BeforeTime.IsZero() {
		before = &f.BeforeTime
	}
	var customer *uuid.UUID
	if f.CustomerID != uuid.Nil {
		customer = &f.CustomerID
	}
	rows, err := q.Query(ctx, `SELECT `+payoutColumns+` FROM payouts
		WHERE sandbox_id = $1
		  AND ($2::text = '' OR status = $2)
		  AND ($3::uuid IS NULL OR customer_id = $3)
		  AND ($4::timestamptz IS NULL OR created_at < $4)
		ORDER BY created_at DESC, id DESC
		LIMIT $5`,
		sandbox, string(f.Status), customer, before, limit)
	if err != nil {
		return nil, fmt.Errorf("payout: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, collectPayout)
	if err != nil {
		return nil, fmt.Errorf("payout: list: %w", err)
	}
	return out, nil
}

// Events returns a payout's state history, oldest first.
func Events(ctx context.Context, q store.Querier, sandbox, payoutID uuid.UUID) ([]Event, error) {
	rows, err := q.Query(ctx, `SELECT id, payout_id, COALESCE(from_status, ''), to_status, detail, created_at
		FROM payout_events WHERE sandbox_id = $1 AND payout_id = $2 ORDER BY id`, sandbox, payoutID)
	if err != nil {
		return nil, fmt.Errorf("payout: events: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var (
			e        Event
			from, to string
			detail   []byte
		)
		if err := row.Scan(&e.ID, &e.PayoutID, &from, &to, &detail, &e.CreatedAt); err != nil {
			return Event{}, err
		}
		e.From, e.To, e.Detail = Status(from), Status(to), detail
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("payout: events: %w", err)
	}
	return out, nil
}

// Available returns what the customer can pay out in asset: the credit
// balance of their 2010 customer-balances sub-ledger, as a positive amount
// (negative if the customer owes Puente).
func Available(ctx context.Context, q store.Querier, sandbox, customer uuid.UUID, asset money.Asset) (money.Amount, error) {
	raw, err := ledger.PartyBalance(ctx, q, sandbox, customer, ledger.CustomerBalances, asset)
	if err != nil {
		return money.Amount{}, fmt.Errorf("payout: available: %w", err)
	}
	return raw.Neg(), nil
}

// insertEvent appends to the payout's history; from is empty on creation.
func insertEvent(ctx context.Context, tx pgx.Tx, p Payout, from, to Status, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("payout: encode event detail: %w", err)
	}
	var fromArg *string
	if from != "" {
		s := string(from)
		fromArg = &s
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payout_events (sandbox_id, payout_id, from_status, to_status, detail)
		VALUES ($1, $2, $3, $4, $5)`, p.SandboxID, p.ID, fromArg, string(to), body); err != nil {
		return fmt.Errorf("payout: insert event %s→%s: %w", from, to, err)
	}
	return nil
}
