// Package store owns the PostgreSQL connection pool, the embedded schema
// migrations and sandbox bookkeeping. Every domain table carries a
// sandbox_id and is deleted together with its sandbox.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// Querier is the subset of pgx shared by *pgxpool.Pool, *pgxpool.Conn and
// pgx.Tx, so store functions run either standalone or inside a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Session limits set on every connection. They bound how long any request
// or tick can wait on a row lock or run a statement, so a burst of
// contending requests degrades into errors instead of a hung pool.
var sessionLimits = map[string]string{
	"lock_timeout":                        "10s",
	"statement_timeout":                   "60s",
	"idle_in_transaction_session_timeout": "120s",
}

// minPoolConns is the floor for the pool size (pgx defaults to the CPU
// count, which is 1 or 2 on small instances).
const minPoolConns = 8

// Open connects a pool to the database at url and verifies it with a ping.
// Every connection gets sessionLimits; use a direct (not transaction-pooled)
// connection string, since poolers may reject startup parameters.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	for k, v := range sessionLimits {
		if _, set := cfg.ConnConfig.RuntimeParams[k]; !set {
			cfg.ConnConfig.RuntimeParams[k] = v
		}
	}
	cfg.MaxConns = max(cfg.MaxConns, minPoolConns)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping database: %w", err)
	}
	return pool, nil
}

// InTx runs fn inside a transaction on pool. The transaction commits when fn
// returns nil and rolls back otherwise.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
