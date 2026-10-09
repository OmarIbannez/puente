package seed_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/seed"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

func newEngine() *engine.Engine {
	return engine.New(engine.Config{Pool: pool, Payout: payout.DefaultConfig()})
}

// seeded creates and seeds a sandbox, and returns it with the seed's
// duration.
func seeded(t *testing.T, eng *engine.Engine) (store.Sandbox, time.Duration) {
	t.Helper()
	sb := storetest.Sandbox(t, pool)
	start := time.Now()
	if err := seed.Seed(t.Context(), pool, eng, sb.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return sb, time.Since(start)
}

func TestSeed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sb, took := seeded(t, newEngine())
	if took > 5*time.Second {
		t.Errorf("seeding took %s, want < 5s", took)
	}
	assertBooks(ctx, t, sb.ID)

	customers, err := customer.List(ctx, pool, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(customers) != 3 {
		t.Errorf("customers = %d, want 3", len(customers))
	}

	payouts, err := payout.List(ctx, pool, sb.ID, payout.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[payout.Status]int{}
	for _, p := range payouts {
		counts[p.Status]++
	}
	want := map[payout.Status]int{
		payout.StatusLiquidated: 4, payout.StatusSubmitted: 1, payout.StatusReturned: 1,
		payout.StatusReleased: 2, payout.StatusReserved: 1,
	}
	for status, n := range want {
		if counts[status] != n {
			t.Errorf("%s payouts = %d, want %d (all: %v)", status, counts[status], n, counts)
		}
	}

	// Every liquidated payout has a CEP at the rail.
	for _, p := range payouts {
		if p.Status != payout.StatusLiquidated {
			continue
		}
		rec, err := spei.Rail{}.Order(ctx, pool, sb.ID, p.Clave)
		if err != nil {
			t.Fatal(err)
		}
		if rec.CEP == nil {
			t.Errorf("liquidated payout %s has no CEP", p.Clave)
		}
	}

	// The BBVA test CLABE is used verbatim.
	found := false
	for _, p := range payouts {
		found = found || p.BeneficiaryCLABE == "012180015203300154"
	}
	if !found {
		t.Error("no payout to the BBVA test CLABE 012180015203300154")
	}

	latest, err := recon.Latest(ctx, pool, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.BreakCount != 0 || latest.Trigger != recon.TriggerScheduled {
		t.Errorf("latest recon run: %d breaks, trigger %q; want 0, %q", latest.BreakCount, latest.Trigger, recon.TriggerScheduled)
	}

	after, err := store.GetSandbox(ctx, pool, sb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Faults != (store.Faults{}) {
		t.Errorf("faults left armed: %+v", after.Faults)
	}
}

// assertBooks checks a zero trial balance in every asset and that the
// ledger's SPEI settlement account matches the rail statement.
func assertBooks(ctx context.Context, t *testing.T, sandbox uuid.UUID) {
	t.Helper()
	tb, err := ledger.TrialBalance(ctx, pool, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	for asset, sum := range tb {
		if sum != 0 {
			t.Errorf("trial balance %s = %d, want 0", asset, sum)
		}
	}
	book, err := ledger.Balance(ctx, pool, sandbox, ledger.SPEISettlement, money.MXN)
	if err != nil {
		t.Fatal(err)
	}
	bank, err := spei.Rail{}.StatementBalance(ctx, pool, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if book != bank {
		t.Errorf("ledger 1100 = %s, rail statement = %s", book, bank)
	}
	if book.Minor <= 0 || book.Minor >= seed.Treasury.Minor {
		t.Errorf("ledger 1100 = %s, want between 0 and the treasury %s", book, seed.Treasury)
	}
}

func TestSeedConcurrentSandboxesStayIsolated(t *testing.T) {
	t.Parallel()
	eng := newEngine()
	const n = 2
	ids := make([]uuid.UUID, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		sb := storetest.Sandbox(t, pool)
		ids[i] = sb.ID
		wg.Go(func() { errs[i] = seed.Seed(t.Context(), pool, eng, sb.ID) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	for _, id := range ids {
		assertBooks(t.Context(), t, id)
		ps, err := payout.List(t.Context(), pool, id, payout.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) != 9 {
			t.Errorf("sandbox %s has %d payouts, want 9", id, len(ps))
		}
		cs, err := customer.List(t.Context(), pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 3 {
			t.Errorf("sandbox %s has %d customers, want 3", id, len(cs))
		}
	}
}

func TestSeedRejectsLiveSandbox(t *testing.T) {
	t.Parallel()
	live, err := store.LiveSandbox(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Seed(t.Context(), pool, newEngine(), live.ID); err == nil {
		t.Fatal("seeding the live sandbox succeeded, want an error")
	}
}
