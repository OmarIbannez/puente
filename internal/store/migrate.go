package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is the pg_advisory_xact_lock key that serializes
// concurrent Migrate calls (any constant; "puente" in ASCII).
const migrationLockID int64 = 0x7075656e7465

// Migration is one embedded schema migration.
type Migration struct {
	// Version is the file name without the .sql extension, e.g. "0001_init".
	Version string
	// SQL is the migration body.
	SQL string
}

// Migrations returns the embedded migrations in version order.
func Migrations() ([]Migration, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("store: list migrations: %w", err)
	}
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for _, name := range names {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("store: read %s: %w", name, err)
		}
		out = append(out, Migration{
			Version: strings.TrimSuffix(path.Base(name), ".sql"),
			SQL:     string(body),
		})
	}
	return out, nil
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations. It runs in a single transaction holding an advisory
// lock, so it is idempotent and safe when several processes start at once:
// later callers wait for the first and then find nothing left to apply.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := Migrations()
	if err != nil {
		return err
	}
	return InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
			return fmt.Errorf("store: migration lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			return fmt.Errorf("store: create schema_migrations: %w", err)
		}
		applied, err := appliedVersions(ctx, tx)
		if err != nil {
			return err
		}
		for _, m := range migrations {
			if applied[m.Version] {
				continue
			}
			// No arguments: pgx uses the simple protocol, which allows
			// multiple statements in one call.
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("store: apply migration %s: %w", m.Version, err)
			}
			if _, err := tx.Exec(ctx,
				"INSERT INTO schema_migrations (version) VALUES ($1)", m.Version); err != nil {
				return fmt.Errorf("store: record migration %s: %w", m.Version, err)
			}
		}
		return nil
	})
}

func appliedVersions(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	rows, err := tx.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	applied := make(map[string]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}
	return applied, nil
}
