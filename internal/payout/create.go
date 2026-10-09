package payout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Field limits, as SPEI carries them.
const (
	// MaxNameLength is the longest beneficiary name, in characters.
	MaxNameLength = 40
	// MaxConceptoLength is the longest concepto, in characters.
	MaxConceptoLength = 40
)

// Request asks for a payout of Amount (a stablecoin) to a CLABE.
type Request struct {
	CustomerID       uuid.UUID    `json:"customer_id"`
	Amount           money.Amount `json:"amount"`
	BeneficiaryCLABE string       `json:"beneficiary_clabe"`
	BeneficiaryName  string       `json:"beneficiary_name"`
	Concepto         string       `json:"concepto"`
}

// Create validates and quotes req, then, inside tx: locks the customer row
// (so one customer's payouts are reserved one at a time), checks that the
// available balance covers amount plus fee, inserts the payout as reserved
// with a fresh clave de rastreo, posts the reserve entry and queues the
// submit job. Nothing is visible to the worker until tx commits.
func Create(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, req Request, cfg Config) (Payout, error) {
	clabe, err := spei.ValidateCLABE(req.BeneficiaryCLABE)
	if err != nil {
		return Payout{}, fmt.Errorf("%w: %w", ErrInvalidCLABE, err)
	}
	name := strings.TrimSpace(req.BeneficiaryName)
	if n := utf8.RuneCountInString(name); n == 0 || n > MaxNameLength {
		return Payout{}, fmt.Errorf("%w: beneficiary name must be 1-%d characters", ErrInvalidRequest, MaxNameLength)
	}
	concepto := strings.TrimSpace(req.Concepto)
	if utf8.RuneCountInString(concepto) > MaxConceptoLength {
		return Payout{}, fmt.Errorf("%w: concepto must be at most %d characters", ErrInvalidRequest, MaxConceptoLength)
	}
	q, err := cfg.Quote(req.Amount)
	if err != nil {
		return Payout{}, err
	}

	// NO KEY UPDATE serializes spending by this customer without blocking
	// the foreign-key checks of concurrent postings to their sub-ledger.
	var locked uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM customers WHERE sandbox_id = $1 AND id = $2 FOR NO KEY UPDATE`,
		sandbox, req.CustomerID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payout{}, fmt.Errorf("%w: %s", ErrCustomerNotFound, req.CustomerID)
	}
	if err != nil {
		return Payout{}, fmt.Errorf("payout: lock customer: %w", err)
	}
	avail, err := Available(ctx, tx, sandbox, req.CustomerID, q.Amount.Asset)
	if err != nil {
		return Payout{}, err
	}
	if avail.Minor < q.Total.Minor {
		return Payout{}, fmt.Errorf("%w: available %s, need %s", ErrInsufficientFunds, avail, q.Total)
	}

	var (
		seq int64
		now time.Time
	)
	if err := tx.QueryRow(ctx, "SELECT nextval('payout_clave_seq'), now()").Scan(&seq, &now); err != nil {
		return Payout{}, fmt.Errorf("payout: next clave: %w", err)
	}
	clave := spei.NewClave(spei.DefaultClavePrefix, now.UTC(), seq)
	p, err := scanPayout(tx.QueryRow(ctx, `INSERT INTO payouts (sandbox_id, customer_id, asset,
			amount_minor, fee_minor, rate, mxn_centavos, beneficiary_clabe, beneficiary_name,
			concepto, clave)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+payoutColumns,
		sandbox, req.CustomerID, string(q.Amount.Asset), q.Amount.Minor, q.Fee.Minor, int64(q.Rate),
		q.MXN.Minor, clabe.Number, name, concepto, clave))
	if err != nil {
		return Payout{}, fmt.Errorf("payout: insert: %w", err)
	}
	if err := insertEvent(ctx, tx, p, "", StatusReserved, map[string]any{
		"total": q.Total.Decimal(), "mxn": q.MXN.Decimal(), "rate": q.Rate.String(),
	}); err != nil {
		return Payout{}, err
	}
	if _, _, err := ledger.Post(ctx, tx, reserveEntry(p)); err != nil {
		return Payout{}, fmt.Errorf("payout: post reserve: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payout_outbox (sandbox_id, payout_id) VALUES ($1, $2)`,
		sandbox, p.ID); err != nil {
		return Payout{}, fmt.Errorf("payout: queue submit: %w", err)
	}
	return p, nil
}

// Cancel releases a payout that is still reserved and whose submit job has
// not run, inside tx. Otherwise it returns ErrNotCancellable.
func Cancel(ctx context.Context, tx pgx.Tx, sandbox, id uuid.UUID) (Payout, error) {
	// Lock the job before the payout, the same order the worker uses.
	var done *time.Time
	err := tx.QueryRow(ctx, `SELECT done_at FROM payout_outbox
		WHERE sandbox_id = $1 AND payout_id = $2 AND kind = 'submit' FOR UPDATE`, sandbox, id).Scan(&done)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payout{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Payout{}, fmt.Errorf("payout: lock job of %s: %w", id, err)
	}
	p, err := lockPayout(ctx, tx, sandbox, id)
	if err != nil {
		return Payout{}, err
	}
	if done != nil || p.Status != StatusReserved {
		return Payout{}, fmt.Errorf("%w: payout %s is %s", ErrNotCancellable, id, p.Status)
	}
	p, err = transition(ctx, tx, sandbox, id, StatusReleased, map[string]any{"reason": "cancelled"})
	if err != nil {
		return Payout{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE payout_outbox SET done_at = now(), last_error = 'cancelled'
		WHERE sandbox_id = $1 AND payout_id = $2 AND kind = 'submit'`, sandbox, id); err != nil {
		return Payout{}, fmt.Errorf("payout: close job of %s: %w", id, err)
	}
	return p, nil
}

// Fund prefunds the SPEI settlement account with amount (positive MXN)
// inside tx: it posts the funding entry (ref "funding:<ref>") and books the
// matching line on the rail. It is exactly-once on ref: a repeated call
// posts nothing and does not fund the rail again.
func Fund(ctx context.Context, tx pgx.Tx, rail spei.Rail, sandbox uuid.UUID, amount money.Amount, ref string) error {
	if amount.Asset != money.MXN || amount.Minor <= 0 {
		return fmt.Errorf("%w: funding must be positive MXN, got %s", ErrInvalidAmount, amount)
	}
	_, posted, err := ledger.Post(ctx, tx, fundingEntry(sandbox, amount, ref))
	if err != nil {
		return fmt.Errorf("payout: post funding: %w", err)
	}
	if !posted {
		return nil
	}
	if _, err := rail.Fund(ctx, tx, sandbox, amount); err != nil {
		return fmt.Errorf("payout: fund rail: %w", err)
	}
	return nil
}
