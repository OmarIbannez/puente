package payout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rail is the part of the SPEI participant the worker uses. spei.Rail
// implements it; tests wrap it to inject failures.
type Rail interface {
	// Submit sends an order, idempotently on its clave (see spei.Rail.Submit).
	Submit(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, o spei.Order) (spei.Ack, error)
	// Advance runs one settlement cycle (see spei.Rail.Advance).
	Advance(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]spei.StatusChange, error)
	// OrdersByClave reads orders by clave (see spei.Rail.OrdersByClave).
	OrdersByClave(ctx context.Context, q store.Querier, sandbox uuid.UUID, claves []string) (map[string]spei.Record, error)
}

// Worker submits queued payouts to the rail and applies the rail's outcomes.
type Worker struct {
	Rail   Rail
	Config Config
}

// TickResult is what one Tick did.
type TickResult struct {
	// Processed is the number of outbox jobs attempted.
	Processed int `json:"processed"`
	// Changes are the transitions applied by the rail sync.
	Changes []Change `json:"changes"`
}

// Tick is one worker cycle for the sandbox: SyncRail, then ProcessOutbox.
// Syncing first means an order submitted in this tick settles on the next
// one, as with a real settlement cycle. Both always run; their errors are
// joined.
func (w Worker) Tick(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID, now time.Time) (TickResult, error) {
	changes, serr := w.SyncRail(ctx, pool, sandbox)
	n, perr := w.ProcessOutbox(ctx, pool, sandbox, now)
	return TickResult{Processed: n, Changes: changes}, errors.Join(serr, perr)
}

// job is a claimed outbox row.
type job struct {
	id       int64
	payoutID uuid.UUID
	attempts int
}

// ProcessOutbox works through the sandbox's submit jobs that are due at now,
// each in its own transaction, and returns how many it attempted. Jobs are
// claimed with FOR UPDATE SKIP LOCKED, so concurrent workers never process
// the same job; each job is attempted at most once per call.
//
// For a reserved payout the job first moves it to submitted and posts the
// submit entry, then calls Rail.Submit with the payout's clave, all in one
// transaction, and applies the acknowledged rail status. A lost
// acknowledgement (spei.ErrTimeout) is committed as a failed attempt
// (the rail kept the order) and retried at now + Backoff(attempts) with the
// same clave; after MaxAttempts the payout goes to needs_review. Any other
// failure rolls the attempt back, is recorded on the job and retried after
// the backoff; those errors are joined into the returned error.
func (w Worker) ProcessOutbox(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID, now time.Time) (int, error) {
	cfg := w.Config.withDefaults()
	seen := []int64{}
	var errs []error
	for {
		if err := ctx.Err(); err != nil {
			return len(seen), errors.Join(append(errs, err)...)
		}
		var (
			j       job
			claimed bool
		)
		err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
			var err error
			j, claimed, err = claim(ctx, tx, sandbox, now, seen)
			if err != nil || !claimed {
				return err
			}
			return w.runJob(ctx, tx, cfg, sandbox, j, now)
		})
		if !claimed {
			return len(seen), errors.Join(append(errs, err)...)
		}
		seen = append(seen, j.id)
		if err != nil {
			errs = append(errs, err)
			if rerr := recordFailure(ctx, pool, cfg, sandbox, j, now, err); rerr != nil {
				return len(seen), errors.Join(append(errs, rerr)...)
			}
		}
	}
}

// claim locks the next due job of the sandbox that this call has not seen.
func claim(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, now time.Time, seen []int64) (job, bool, error) {
	var j job
	err := tx.QueryRow(ctx, `SELECT id, payout_id, attempts FROM payout_outbox
		WHERE sandbox_id = $1 AND done_at IS NULL AND available_at <= $2 AND NOT (id = ANY($3))
		ORDER BY available_at, id
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, sandbox, now, seen).Scan(&j.id, &j.payoutID, &j.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return job{}, false, nil
	}
	if err != nil {
		return job{}, false, fmt.Errorf("payout: claim job: %w", err)
	}
	return j, true, nil
}

// runJob submits one claimed job inside tx.
func (w Worker) runJob(ctx context.Context, tx pgx.Tx, cfg Config, sandbox uuid.UUID, j job, now time.Time) error {
	// Lock order everywhere: outbox job, sandbox, payout. SyncRail and
	// Rail.Submit take the sandbox lock too, so this avoids deadlocks.
	if _, err := store.LockSandbox(ctx, tx, sandbox); err != nil {
		return fmt.Errorf("payout: lock sandbox: %w", err)
	}
	p, err := lockPayout(ctx, tx, sandbox, j.payoutID)
	if err != nil {
		return err
	}
	attempt := j.attempts + 1
	switch p.Status {
	case StatusReserved:
		p, err = transition(ctx, tx, sandbox, p.ID, StatusSubmitted, map[string]any{"attempt": attempt})
		if err != nil {
			return err
		}
	case StatusSubmitted:
		// A retry after a lost acknowledgement: resubmit the same clave.
	case StatusLiquidated, StatusRejected, StatusReleased, StatusReturned, StatusNeedsReview:
		// Already resolved (by a rail sync, or cancelled): nothing to send.
		return closeJob(ctx, tx, j.id, j.attempts, "")
	}

	ack, err := w.Rail.Submit(ctx, tx, sandbox, spei.Order{
		Clave:            p.Clave,
		BeneficiaryCLABE: p.BeneficiaryCLABE,
		BeneficiaryName:  p.BeneficiaryName,
		Concepto:         p.Concepto,
		Amount:           p.MXN,
	})
	switch {
	case err == nil:
		if err := setAttempts(ctx, tx, p, attempt, ""); err != nil {
			return err
		}
		if _, err := applyRail(ctx, tx, p, ack.Status, ack.Reason); err != nil {
			return err
		}
		return closeJob(ctx, tx, j.id, attempt, "")
	case errors.Is(err, spei.ErrTimeout):
		msg := err.Error()
		if err := setAttempts(ctx, tx, p, attempt, msg); err != nil {
			return err
		}
		if attempt >= cfg.MaxAttempts {
			if _, err := transition(ctx, tx, sandbox, p.ID, StatusNeedsReview,
				map[string]any{"attempts": attempt, "error": msg}); err != nil {
				return err
			}
			return closeJob(ctx, tx, j.id, attempt, msg)
		}
		return deferJob(ctx, tx, j.id, attempt, now.Add(cfg.Backoff(attempt)), msg)
	default:
		return fmt.Errorf("payout: submit %s: %w", p.Clave, err)
	}
}

// recordFailure books a rolled-back attempt on the job and the payout in a
// fresh transaction, and schedules the retry.
func recordFailure(ctx context.Context, pool *pgxpool.Pool, cfg Config, sandbox uuid.UUID, j job, now time.Time, cause error) error {
	attempt := j.attempts + 1
	msg := cause.Error()
	return store.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := deferJob(ctx, tx, j.id, attempt, now.Add(cfg.Backoff(attempt)), msg); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payouts SET last_error = $3, updated_at = now()
			WHERE sandbox_id = $1 AND id = $2`, sandbox, j.payoutID, msg); err != nil {
			return fmt.Errorf("payout: record failure of %s: %w", j.payoutID, err)
		}
		return nil
	})
}

func setAttempts(ctx context.Context, tx pgx.Tx, p Payout, attempts int, lastErr string) error {
	if _, err := tx.Exec(ctx, `UPDATE payouts SET attempts = $3, last_error = $4, updated_at = now()
		WHERE sandbox_id = $1 AND id = $2`, p.SandboxID, p.ID, attempts, lastErr); err != nil {
		return fmt.Errorf("payout: record attempt of %s: %w", p.ID, err)
	}
	return nil
}

func closeJob(ctx context.Context, tx pgx.Tx, id int64, attempts int, lastErr string) error {
	if _, err := tx.Exec(ctx, `UPDATE payout_outbox SET done_at = now(), attempts = $2, last_error = $3
		WHERE id = $1`, id, attempts, lastErr); err != nil {
		return fmt.Errorf("payout: close job %d: %w", id, err)
	}
	return nil
}

func deferJob(ctx context.Context, tx pgx.Tx, id int64, attempts int, at time.Time, lastErr string) error {
	if _, err := tx.Exec(ctx, `UPDATE payout_outbox SET attempts = $2, available_at = $3, last_error = $4
		WHERE id = $1 AND done_at IS NULL`, id, attempts, at, lastErr); err != nil {
		return fmt.Errorf("payout: reschedule job %d: %w", id, err)
	}
	return nil
}

// SyncRail runs one rail settlement cycle (Rail.Advance) and, in the same
// transaction, applies the rail status of every payout that may still
// change (submitted, needs_review, or liquidated and possibly returned)
// through the state machine, posting the matching entries. It returns the
// transitions it made. It catches every outcome the worker did not see:
// settlements, devoluciones, and the true status of needs_review payouts.
func (w Worker) SyncRail(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID) ([]Change, error) {
	var changes []Change
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		changes = nil
		if _, err := w.Rail.Advance(ctx, tx, sandbox); err != nil {
			return fmt.Errorf("payout: advance rail: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT id, clave, status FROM payouts
			WHERE sandbox_id = $1 AND status IN ('submitted', 'needs_review', 'liquidated')
			ORDER BY created_at, id`, sandbox)
		if err != nil {
			return fmt.Errorf("payout: sync candidates: %w", err)
		}
		open, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Change, error) {
			var (
				c      Change
				status string
			)
			err := row.Scan(&c.PayoutID, &c.Clave, &status)
			c.From = Status(status)
			return c, err
		})
		if err != nil {
			return fmt.Errorf("payout: sync candidates: %w", err)
		}
		claves := make([]string, len(open))
		for i, c := range open {
			claves[i] = c.Clave
		}
		orders, err := w.Rail.OrdersByClave(ctx, tx, sandbox, claves)
		if err != nil {
			return fmt.Errorf("payout: read rail orders: %w", err)
		}
		for _, c := range open {
			rec, ok := orders[c.Clave]
			if !ok {
				continue
			}
			if _, ok := nextStep(c.From, rec.Status); !ok {
				continue
			}
			p, err := lockPayout(ctx, tx, sandbox, c.PayoutID)
			if err != nil {
				return err
			}
			applied, err := applyRail(ctx, tx, p, rec.Status, rec.Reason)
			if err != nil {
				return err
			}
			changes = append(changes, applied...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

// nextStep is the transition that moves a payout in status cur toward the
// rail's status, if any.
func nextStep(cur Status, rail spei.Status) (Status, bool) {
	inFlight := cur == StatusSubmitted || cur == StatusNeedsReview
	switch rail {
	case spei.StatusLiquidado:
		if inFlight {
			return StatusLiquidated, true
		}
	case spei.StatusRechazado:
		if inFlight {
			return StatusRejected, true
		}
		if cur == StatusRejected {
			return StatusReleased, true
		}
	case spei.StatusDevuelto:
		if inFlight {
			return StatusLiquidated, true
		}
		if cur == StatusLiquidated {
			return StatusReturned, true
		}
	case spei.StatusEnProceso:
	}
	return "", false
}

// applyRail walks payout p through the state machine until it matches the
// rail status (e.g. submitted → rejected → released for rechazado), inside
// tx, and returns the transitions made.
func applyRail(ctx context.Context, tx pgx.Tx, p Payout, rail spei.Status, reason string) ([]Change, error) {
	var changes []Change
	for {
		to, ok := nextStep(p.Status, rail)
		if !ok {
			return changes, nil
		}
		detail := map[string]any{"rail_status": string(rail)}
		if reason != "" {
			detail["reason"] = reason
		}
		from := p.Status
		next, err := transition(ctx, tx, p.SandboxID, p.ID, to, detail)
		if err != nil {
			return nil, err
		}
		changes = append(changes, Change{PayoutID: p.ID, Clave: p.Clave, From: from, To: to})
		p = next
	}
}
