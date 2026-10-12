// Package recon reconciles Puente's ledger against the two outside sources
// of truth: the chain (what deposits really landed) and the SPEI rail (what
// the bank really paid out). A run is read-only: it reads every source in
// one consistent snapshot, records the breaks it finds, and fixes nothing.
package recon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kind is the type of a break.
type Kind string

// Break kinds.
const (
	// MissingOnLedger: a source shows money moving that the ledger never booked.
	MissingOnLedger Kind = "missing_on_ledger"
	// MissingOnChain: the ledger credits a deposit the canonical chain lacks.
	MissingOnChain Kind = "missing_on_chain"
	// MissingOnRail: the ledger books a payout step the rail never did.
	MissingOnRail Kind = "missing_on_rail"
	// Duplicate: one real movement was booked more than once.
	Duplicate Kind = "duplicate"
	// AmountMismatch: both sides have the movement, with different amounts.
	AmountMismatch Kind = "amount_mismatch"
	// BalanceMismatch: an account balance differs from the source's balance.
	BalanceMismatch Kind = "balance_mismatch"
)

// Source is the outside system a break was found against.
type Source string

// Sources.
const (
	SourceChain   Source = "chain"
	SourceRail    Source = "rail"
	SourceBalance Source = "balance"
)

// Triggers record what started a run.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
	TriggerCLI       = "cli"
)

// Break is one discrepancy, with enough evidence to investigate it.
type Break struct {
	ID          int64             `json:"id"`
	Kind        Kind              `json:"kind"`
	Source      Source            `json:"source"`
	SubjectType string            `json:"subject_type"`
	SubjectID   string            `json:"subject_id"`
	Summary     string            `json:"summary"`
	Expected    string            `json:"expected,omitempty"`
	Actual      string            `json:"actual,omitempty"`
	Evidence    map[string]string `json:"evidence,omitempty"`
}

// Run is one reconciliation pass over a sandbox.
type Run struct {
	ID         int64          `json:"id"`
	Trigger    string         `json:"trigger"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Checked    map[string]int `json:"checked"`
	BreakCount int            `json:"break_count"`
	Breaks     []Break        `json:"breaks,omitempty"`
}

// Matcher compares the ledger with one source inside the run's snapshot
// transaction and returns the breaks plus how many items it checked.
type Matcher interface {
	// Name labels the matcher in Run.Checked.
	Name() string
	Match(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]Break, int, error)
}

// Reconciler runs a set of matchers.
type Reconciler struct {
	Matchers []Matcher
}

// New returns a reconciler with the rail and balance matchers plus any
// extra matchers (the chain matchers, which depend on the sandbox's networks).
func New(rail spei.Rail, extra ...Matcher) Reconciler {
	return Reconciler{Matchers: append([]Matcher{RailMatcher{Rail: rail}, BalanceMatcher{Rail: rail}}, extra...)}
}

// Run reconciles the sandbox and stores the run and its breaks.
//
// All matchers read inside one REPEATABLE READ, read-only transaction, so
// the ledger, the rail tables and the chain tables are compared as of the
// same instant even while the worker keeps moving money.
func (r Reconciler) Run(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID, trigger string) (Run, error) {
	started := time.Now()
	var (
		breaks  []Break
		checked = map[string]int{}
	)
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			for _, m := range r.Matchers {
				found, n, err := m.Match(ctx, tx, sandbox)
				if err != nil {
					return fmt.Errorf("recon: %s: %w", m.Name(), err)
				}
				breaks = append(breaks, found...)
				checked[m.Name()] += n
			}
			return nil
		})
	if err != nil {
		return Run{}, err
	}
	return save(ctx, pool, sandbox, trigger, started, checked, breaks)
}

func save(ctx context.Context, pool *pgxpool.Pool, sandbox uuid.UUID, trigger string, started time.Time, checked map[string]int, breaks []Break) (Run, error) {
	run := Run{Trigger: trigger, StartedAt: started, Checked: checked, BreakCount: len(breaks)}
	body, err := json.Marshal(checked)
	if err != nil {
		return Run{}, fmt.Errorf("recon: encode checked: %w", err)
	}
	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		var finished time.Time
		if err := tx.QueryRow(ctx, `INSERT INTO recon_runs (sandbox_id, trigger, started_at, finished_at, checked, break_count)
			VALUES ($1, $2, $3, now(), $4, $5) RETURNING id, finished_at`,
			sandbox, trigger, started, body, len(breaks)).Scan(&run.ID, &finished); err != nil {
			return fmt.Errorf("recon: insert run: %w", err)
		}
		run.FinishedAt = &finished
		for i := range breaks {
			b := &breaks[i]
			evidence, err := json.Marshal(b.Evidence)
			if err != nil {
				return fmt.Errorf("recon: encode evidence: %w", err)
			}
			if err := tx.QueryRow(ctx, `INSERT INTO recon_breaks
				(run_id, sandbox_id, kind, source, subject_type, subject_id, summary, expected, actual, evidence)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
				run.ID, sandbox, string(b.Kind), string(b.Source), b.SubjectType, b.SubjectID,
				b.Summary, b.Expected, b.Actual, evidence).Scan(&b.ID); err != nil {
				return fmt.Errorf("recon: insert break: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	run.Breaks = breaks
	return run, nil
}

// ErrNoRuns is returned by Latest when the sandbox was never reconciled.
var ErrNoRuns = errors.New("recon: no runs yet")

// Runs lists the sandbox's most recent runs (without breaks), newest first.
func Runs(ctx context.Context, q store.Querier, sandbox uuid.UUID, limit int) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return runsWhere(ctx, q, sandbox, "true ORDER BY id DESC LIMIT $2", limit)
}

// Get returns one run with its breaks, or store.ErrNotFound.
func Get(ctx context.Context, q store.Querier, sandbox uuid.UUID, id int64) (Run, error) {
	runs, err := runsWhere(ctx, q, sandbox, "id = $2", id)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, store.ErrNotFound
	}
	return withBreaks(ctx, q, sandbox, runs[0])
}

// Latest returns the newest run with its breaks, or ErrNoRuns.
func Latest(ctx context.Context, q store.Querier, sandbox uuid.UUID) (Run, error) {
	runs, err := Runs(ctx, q, sandbox, 1)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, ErrNoRuns
	}
	return withBreaks(ctx, q, sandbox, runs[0])
}

// runsWhere reads runs of the sandbox matching cond, an SQL fragment whose
// single parameter is $2.
func runsWhere(ctx context.Context, q store.Querier, sandbox uuid.UUID, cond string, arg any) ([]Run, error) {
	rows, err := q.Query(ctx, `SELECT id, trigger, started_at, finished_at, checked, break_count
		FROM recon_runs WHERE sandbox_id = $1 AND `+cond, sandbox, arg)
	if err != nil {
		return nil, fmt.Errorf("recon: run: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		var (
			r    Run
			body []byte
		)
		if err := row.Scan(&r.ID, &r.Trigger, &r.StartedAt, &r.FinishedAt, &body, &r.BreakCount); err != nil {
			return Run{}, err
		}
		return r, json.Unmarshal(body, &r.Checked)
	})
	if err != nil {
		return nil, fmt.Errorf("recon: run: %w", err)
	}
	return out, nil
}

func withBreaks(ctx context.Context, q store.Querier, sandbox uuid.UUID, r Run) (Run, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, source, subject_type, subject_id, summary, expected, actual, evidence
		FROM recon_breaks WHERE sandbox_id = $1 AND run_id = $2 ORDER BY id`, sandbox, r.ID)
	if err != nil {
		return Run{}, fmt.Errorf("recon: breaks: %w", err)
	}
	breaks, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Break, error) {
		var (
			b        Break
			evidence []byte
		)
		if err := row.Scan(&b.ID, &b.Kind, &b.Source, &b.SubjectType, &b.SubjectID,
			&b.Summary, &b.Expected, &b.Actual, &evidence); err != nil {
			return Break{}, err
		}
		return b, json.Unmarshal(evidence, &b.Evidence)
	})
	if err != nil {
		return Run{}, fmt.Errorf("recon: breaks: %w", err)
	}
	r.Breaks = breaks
	return r, nil
}
