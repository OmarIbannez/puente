package store_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

func TestMigrateIsIdempotentAndConcurrent(t *testing.T) {
	ctx := t.Context()
	pg, err := storetest.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pg.Terminate() }()
	url, err := pg.CreateDatabase(ctx, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	const workers = 6
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() { errs[i] = store.Migrate(ctx, fresh) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate %d: %v", i, err)
		}
	}
	if err := store.Migrate(ctx, fresh); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}
	migrations, err := store.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := fresh.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(migrations))
	}
}

func TestSandboxLifecycle(t *testing.T) {
	ctx := t.Context()
	s := storetest.Sandbox(t, pool)
	if s.Kind != store.KindVisitor || s.Faults != (store.Faults{}) {
		t.Fatalf("new sandbox = %+v", s)
	}
	got, err := store.SetFaults(ctx, pool, s.ID, store.Faults{RailReject: true, Reorg: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Faults.RailReject || !got.Faults.Reorg || got.Faults.RailTimeout {
		t.Fatalf("faults = %+v", got.Faults)
	}
	reread, err := store.GetSandbox(ctx, pool, s.ID)
	if err != nil || reread.Faults != got.Faults {
		t.Fatalf("GetSandbox = %+v, %v", reread, err)
	}
	if _, err := store.TouchSandbox(ctx, pool, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSandbox(ctx, pool, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSandbox(ctx, pool, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := store.DeleteSandbox(ctx, pool, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestLiveSandboxIsSingleton(t *testing.T) {
	ctx := t.Context()
	ids := make([]uuid.UUID, 4)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Go(func() {
			s, err := store.LiveSandbox(ctx, pool)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = s.ID
		})
	}
	wg.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatalf("live sandbox ids differ: %v", ids)
		}
	}
	// Never pruned, always active.
	if _, err := pool.Exec(ctx, "UPDATE sandboxes SET last_seen_at = now() - interval '30 days' WHERE id = $1", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneIdleSandboxes(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveSandboxes(ctx, pool, time.Hour, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range active {
		found = found || id == ids[0]
	}
	if !found {
		t.Fatal("live sandbox missing from active list after prune")
	}
}

func TestPruneIdleVisitors(t *testing.T) {
	ctx := t.Context()
	old := storetest.Sandbox(t, pool)
	fresh := storetest.Sandbox(t, pool)
	if _, err := pool.Exec(ctx, "UPDATE sandboxes SET last_seen_at = now() - interval '3 days' WHERE id = $1", old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneIdleSandboxes(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSandbox(ctx, pool, old.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("idle sandbox survived: %v", err)
	}
	if _, err := store.GetSandbox(ctx, pool, fresh.ID); err != nil {
		t.Fatalf("fresh sandbox pruned: %v", err)
	}
	if _, err := store.PruneIdleSandboxes(ctx, pool, -1); err == nil {
		t.Fatal("negative idle days accepted")
	}
}

func TestInTxRollsBack(t *testing.T) {
	ctx := t.Context()
	var id uuid.UUID
	sentinel := errors.New("boom")
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		s, err := store.CreateSandbox(ctx, tx)
		if err != nil {
			return err
		}
		id = s.ID
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx = %v", err)
	}
	if _, err := store.GetSandbox(context.WithoutCancel(ctx), pool, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rolled-back sandbox exists: %v", err)
	}
}
