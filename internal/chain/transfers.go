package chain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Transfer statuses in chain_transfers.
const (
	// StatusSeen is a transfer in the feed with too few confirmations.
	StatusSeen = "seen"
	// StatusConfirmed is a transfer with enough confirmations to credit.
	StatusConfirmed = "confirmed"
	// StatusOrphaned is a transfer whose block was reorganized away.
	StatusOrphaned = "orphaned"
)

// Page sizes for Transfers.
const (
	// DefaultTransfersLimit is used when TransferFilter.Limit is zero or negative.
	DefaultTransfersLimit = 50
	// MaxTransfersLimit caps TransferFilter.Limit.
	MaxTransfersLimit = 500
)

// ErrBadStatus is returned for a filter status that is not a known status.
var ErrBadStatus = errors.New("chain: unknown transfer status")

// Record is a stored chain transfer.
type Record struct {
	Transfer
	// ID is the row id, stable across rescans.
	ID int64 `json:"id"`
	// SandboxID is the owning sandbox.
	SandboxID uuid.UUID `json:"sandbox_id"`
	// Network is the network key, e.g. "sim-usdc".
	Network string `json:"network"`
	// Status is seen, confirmed or orphaned.
	Status string `json:"status"`
	// CreatedAt is when the scanner first stored the transfer.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last status change.
	UpdatedAt time.Time `json:"updated_at"`
	// ConfirmedAt is when the transfer was last confirmed, if it was.
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	// OrphanedAt is when the transfer was last orphaned, if it was.
	OrphanedAt *time.Time `json:"orphaned_at,omitempty"`
}

// TransferFilter selects a page of transfers, newest first.
type TransferFilter struct {
	// Network keeps only one network's transfers when not empty.
	Network string
	// Status keeps only transfers in this status when not empty.
	Status string
	// Limit is the page size (DefaultTransfersLimit if ≤ 0, capped at
	// MaxTransfersLimit).
	Limit int
	// BeforeID returns transfers with id < BeforeID when positive (keyset
	// pagination).
	BeforeID int64
}

const recordColumns = `id, sandbox_id, network, chain_id, asset, token, tx_hash, log_index,
	block_number, block_hash, from_addr, to_addr, amount_minor, status, created_at, updated_at,
	confirmed_at, orphaned_at`

func scanRecord(row pgx.Row) (Record, error) {
	var (
		r               Record
		asset           string
		logIndex, block int64
	)
	err := row.Scan(&r.ID, &r.SandboxID, &r.Network, &r.ChainID, &asset, &r.Token, &r.TxHash,
		&logIndex, &block, &r.BlockHash, &r.From, &r.To, &r.Amount.Minor, &r.Status,
		&r.CreatedAt, &r.UpdatedAt, &r.ConfirmedAt, &r.OrphanedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, store.ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("chain: scan transfer: %w", err)
	}
	r.Amount.Asset = money.Asset(asset)
	idx, err := ToUint64(logIndex)
	if err != nil {
		return Record{}, fmt.Errorf("chain: transfer %d log index: %w", r.ID, err)
	}
	r.LogIndex = uint(idx)
	if r.BlockNumber, err = ToUint64(block); err != nil {
		return Record{}, fmt.Errorf("chain: transfer %d block: %w", r.ID, err)
	}
	return r, nil
}

// Transfers returns a page of the sandbox's transfers, newest first.
func Transfers(ctx context.Context, q store.Querier, sandbox uuid.UUID, f TransferFilter) ([]Record, error) {
	switch f.Status {
	case "", StatusSeen, StatusConfirmed, StatusOrphaned:
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadStatus, f.Status)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultTransfersLimit
	}
	limit = min(limit, MaxTransfersLimit)
	rows, err := q.Query(ctx, `SELECT `+recordColumns+` FROM chain_transfers
		WHERE sandbox_id = $1
		  AND ($2 = '' OR network = $2)
		  AND ($3 = '' OR status = $3)
		  AND ($4 <= 0 OR id < $4)
		ORDER BY id DESC
		LIMIT $5`, sandbox, f.Network, f.Status, f.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("chain: transfers: %w", err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chain: transfers: %w", err)
	}
	return out, nil
}

// TransferByID returns the sandbox's transfer with id, or store.ErrNotFound.
func TransferByID(ctx context.Context, q store.Querier, sandbox uuid.UUID, id int64) (Record, error) {
	return scanRecord(q.QueryRow(ctx,
		`SELECT `+recordColumns+` FROM chain_transfers WHERE sandbox_id = $1 AND id = $2`, sandbox, id))
}
