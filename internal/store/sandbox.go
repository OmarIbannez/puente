package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sandbox kinds.
const (
	// KindVisitor is a per-visitor sandbox on the simulated chain and rail.
	KindVisitor = "visitor"
	// KindLive is the single sandbox fed by the real testnets.
	KindLive = "live"
	// KindShowcase is the single seeded, read-only sandbox new visitors get
	// while sandbox creation is rate-limited.
	KindShowcase = "showcase"
)

// Faults are the fault-injection switches of a sandbox. Each one makes the
// simulated chain or rail misbehave so that a reconciliation break can be
// shown on demand.
type Faults struct {
	// DuplicateLog makes the simulated chain emit the next deposit's log twice.
	DuplicateLog bool `json:"duplicate_log"`
	// ShortDeposit makes the next simulated deposit land 1% short of what
	// the ledger was told.
	ShortDeposit bool `json:"short_deposit"`
	// Reorg replaces the block of the most recent confirmed deposit.
	Reorg bool `json:"reorg"`
	// RailTimeout makes the rail accept the next order but lose the ack.
	RailTimeout bool `json:"rail_timeout"`
	// RailReject makes the rail reject the next order.
	RailReject bool `json:"rail_reject"`
	// RailReturn makes the rail return (devolver) the next liquidated order.
	RailReturn bool `json:"rail_return"`
}

// Sandbox is an isolated copy of the world.
type Sandbox struct {
	ID         uuid.UUID
	Kind       string
	Faults     Faults
	CreatedAt  time.Time
	LastSeenAt time.Time
}

const sandboxColumns = "id, kind, faults, created_at, last_seen_at"

func scanSandbox(row pgx.Row) (Sandbox, error) {
	var (
		s      Sandbox
		faults []byte
	)
	err := row.Scan(&s.ID, &s.Kind, &faults, &s.CreatedAt, &s.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sandbox{}, ErrNotFound
	}
	if err != nil {
		return Sandbox{}, fmt.Errorf("store: scan sandbox: %w", err)
	}
	if err := json.Unmarshal(faults, &s.Faults); err != nil {
		return Sandbox{}, fmt.Errorf("store: decode sandbox faults: %w", err)
	}
	return s, nil
}

// CreateSandbox inserts a new visitor sandbox with no faults.
func CreateSandbox(ctx context.Context, q Querier) (Sandbox, error) {
	return scanSandbox(q.QueryRow(ctx,
		"INSERT INTO sandboxes DEFAULT VALUES RETURNING "+sandboxColumns))
}

// LiveSandbox returns the live sandbox, creating it on first use.
func LiveSandbox(ctx context.Context, q Querier) (Sandbox, error) {
	s, err := scanSandbox(q.QueryRow(ctx,
		`INSERT INTO sandboxes (kind) VALUES ('live')
		 ON CONFLICT (kind) WHERE kind = 'live' DO NOTHING
		 RETURNING `+sandboxColumns))
	if errors.Is(err, ErrNotFound) {
		return scanSandbox(q.QueryRow(ctx,
			"SELECT "+sandboxColumns+" FROM sandboxes WHERE kind = 'live'"))
	}
	return s, err
}

// ShowcaseSandbox returns the showcase sandbox, or ErrNotFound.
func ShowcaseSandbox(ctx context.Context, q Querier) (Sandbox, error) {
	return scanSandbox(q.QueryRow(ctx,
		"SELECT "+sandboxColumns+" FROM sandboxes WHERE kind = 'showcase'"))
}

// PromoteShowcase turns the visitor sandbox id (already seeded) into the
// showcase. If another showcase won a race, id is deleted and the existing
// showcase is returned.
func PromoteShowcase(ctx context.Context, q Querier, id uuid.UUID) (Sandbox, error) {
	s, err := scanSandbox(q.QueryRow(ctx,
		`UPDATE sandboxes SET kind = 'showcase'
		 WHERE id = $1 AND kind = 'visitor'
		   AND NOT EXISTS (SELECT 1 FROM sandboxes WHERE kind = 'showcase')
		 RETURNING `+sandboxColumns, id))
	var pgErr *pgconn.PgError
	if errors.Is(err, ErrNotFound) || (errors.As(err, &pgErr) && pgErr.Code == "23505") {
		if err := DeleteSandbox(ctx, q, id); err != nil && !errors.Is(err, ErrNotFound) {
			return Sandbox{}, err
		}
		return ShowcaseSandbox(ctx, q)
	}
	return s, err
}

// GetSandbox returns the sandbox with id, or ErrNotFound.
func GetSandbox(ctx context.Context, q Querier, id uuid.UUID) (Sandbox, error) {
	return scanSandbox(q.QueryRow(ctx,
		"SELECT "+sandboxColumns+" FROM sandboxes WHERE id = $1", id))
}

// TouchSandbox records activity on the sandbox (last_seen_at = now) and
// returns it, or ErrNotFound.
func TouchSandbox(ctx context.Context, q Querier, id uuid.UUID) (Sandbox, error) {
	return scanSandbox(q.QueryRow(ctx,
		"UPDATE sandboxes SET last_seen_at = now() WHERE id = $1 RETURNING "+sandboxColumns, id))
}

// LockSandbox returns the sandbox with its row locked FOR UPDATE, so work on
// one sandbox in the same transaction is serialized. q should be a pgx.Tx.
func LockSandbox(ctx context.Context, q Querier, id uuid.UUID) (Sandbox, error) {
	return scanSandbox(q.QueryRow(ctx,
		"SELECT "+sandboxColumns+" FROM sandboxes WHERE id = $1 FOR UPDATE", id))
}

// SetFaults replaces the sandbox's fault switches and returns it, or
// ErrNotFound.
func SetFaults(ctx context.Context, q Querier, id uuid.UUID, f Faults) (Sandbox, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return Sandbox{}, fmt.Errorf("store: encode faults: %w", err)
	}
	return scanSandbox(q.QueryRow(ctx,
		"UPDATE sandboxes SET faults = $2, last_seen_at = now() WHERE id = $1 RETURNING "+sandboxColumns,
		id, body))
}

// ActiveSandboxes returns the ids of at most limit sandboxes seen within the
// last window, most recent first, plus the live sandbox if it exists, for
// the background worker.
func ActiveSandboxes(ctx context.Context, q Querier, window time.Duration, limit int) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx,
		`(SELECT id FROM sandboxes WHERE kind = 'live')
		 UNION ALL
		 (SELECT id FROM sandboxes
		  WHERE kind = 'visitor' AND last_seen_at > now() - make_interval(secs => $1)
		  ORDER BY last_seen_at DESC LIMIT $2)`, window.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: active sandboxes: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("store: active sandboxes: %w", err)
	}
	return ids, nil
}

// DeleteSandbox removes the sandbox and, by cascade, every row it owns.
// It returns ErrNotFound if no such sandbox exists.
func DeleteSandbox(ctx context.Context, q Querier, id uuid.UUID) error {
	tag, err := q.Exec(ctx, "DELETE FROM sandboxes WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("store: delete sandbox: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneIdleSandboxes deletes every visitor sandbox whose last_seen_at is
// more than idleDays days in the past (database time) and returns how many
// were removed. The live sandbox is never pruned.
func PruneIdleSandboxes(ctx context.Context, q Querier, idleDays int) (int64, error) {
	if idleDays < 0 {
		return 0, fmt.Errorf("store: prune idle sandboxes: negative idle days %d", idleDays)
	}
	tag, err := q.Exec(ctx,
		"DELETE FROM sandboxes WHERE kind = 'visitor' AND last_seen_at < now() - make_interval(days => $1)", idleDays)
	if err != nil {
		return 0, fmt.Errorf("store: prune idle sandboxes: %w", err)
	}
	return tag.RowsAffected(), nil
}
