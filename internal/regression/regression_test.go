// Package regression holds cross-package tests for bugs found in review.
// Each test reproduces the original failure scenario and asserts the fix.
package regression

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/sim"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/seed"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

func newEngine(p *pgxpool.Pool) *engine.Engine {
	return engine.New(engine.Config{Pool: p, Rail: spei.Rail{}, Payout: payout.DefaultConfig()})
}

func newCustomer(t *testing.T, sb store.Sandbox, name string) customer.Customer {
	t.Helper()
	c, err := customer.Create(t.Context(), pool, sb.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func inTx(t *testing.T, fn func(tx pgx.Tx) error) {
	t.Helper()
	if err := store.InTx(t.Context(), pool, fn); err != nil {
		t.Fatal(err)
	}
}

func syncSim(t *testing.T, sb store.Sandbox, net chain.Network, watch string) deposit.SyncResult {
	t.Helper()
	res, err := deposit.Sync(t.Context(), pool, sb.ID, net,
		func(tx pgx.Tx) chain.Source { return sim.New(tx, sb.ID, net) }, []string{watch}, engine.SimConfirmations)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Concurrent ticks of one sandbox used to exhaust a small pool: each sync
// held a connection and the sandbox lock while its chain source read
// through the pool on a second connection.
func TestConcurrentTicksDoNotExhaustPool(t *testing.T) {
	cfg, err := pgxpool.ParseConfig(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	// A hand-built pool, so store.Open's floor of 8 doesn't apply.
	cfg.MaxConns = 4
	tiny, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	eng := newEngine(tiny)
	sb := storetest.Sandbox(t, pool)
	c := newCustomer(t, sb, "Ana")
	if _, err := eng.SimulateDeposit(t.Context(), sb, c.ID, money.New(money.USDC, 5_000_000)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { _, errs[i] = eng.Tick(ctx, sb, time.Now()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("tick %d: %v", i, err)
		}
	}
}

// Seeding used to fail when the background loop credited a deposit first,
// because the seeder only counted credits from its own ticks.
func TestSeedSurvivesConcurrentTicker(t *testing.T) {
	eng := newEngine(pool)
	for attempt := range 3 {
		sb := storetest.Sandbox(t, pool)
		ctx, cancel := context.WithCancel(t.Context())
		var wg sync.WaitGroup
		wg.Go(func() { // a ticker hammering the sandbox, worse than engine.Loop
			for ctx.Err() == nil {
				if cur, err := store.GetSandbox(ctx, pool, sb.ID); err == nil {
					_, _ = eng.Tick(ctx, cur, time.Now())
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
		err := seed.Seed(t.Context(), pool, eng, sb.ID)
		cancel()
		wg.Wait()
		if err != nil {
			t.Fatalf("attempt %d: seed failed with a concurrent ticker: %v", attempt, err)
		}
	}
}

// A reorg that put a deposit into an already-scanned block, where no stored
// transfer lived, used to be skipped forever, and recon stayed clean.
func TestReorgAddsLogBelowCursor(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool)
	c := newCustomer(t, sb, "Ana")
	net, _ := chain.SimNetwork(money.USDC)
	addr := customer.SimAddress(sb.ID, c.ID, net.Key)
	if _, err := customer.AssignAddress(ctx, pool, sb.ID, c.ID, net.Key, addr); err != nil {
		t.Fatal(err)
	}
	inTx(t, func(tx pgx.Tx) error { _, err := sim.New(tx, sb.ID, net).Mine(ctx, tx, 5); return err })
	syncSim(t, sb, net, addr)

	// Block 5 is replaced by 5', which carries a 7 USDC deposit.
	newHash := fmt.Sprintf("0x%064x", 0xabcdef)
	inTx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sim_blocks SET canonical = false
			WHERE sandbox_id = $1 AND network = $2 AND number = 5`, sb.ID, net.Key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sim_blocks (sandbox_id, network, number, hash, canonical)
			VALUES ($1, $2, 5, $3, true)`, sb.ID, net.Key, newHash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO sim_logs (sandbox_id, network, block_number, block_hash, tx_hash,
			log_index, from_addr, to_addr, feed_amount, canonical_amount, in_feed, canonical)
			VALUES ($1, $2, 5, $3, $4, 0, $5, $6, 7000000, 7000000, true, true)`,
			sb.ID, net.Key, newHash, fmt.Sprintf("0x%064x", 0x1234), engine.SimSender, addr)
		return err
	})
	inTx(t, func(tx pgx.Tx) error { _, err := sim.New(tx, sb.ID, net).Mine(ctx, tx, 10); return err })
	syncSim(t, sb, net, addr)

	bal, err := ledger.PartyBalance(ctx, pool, sb.ID, c.ID, ledger.CustomerBalances, money.USDC)
	if err != nil || bal.Minor != -7_000_000 {
		t.Fatalf("customer balance = %v, %v; want the reorged-in 7 USDC credited", bal, err)
	}
	run, err := newEngine(pool).Reconcile(ctx, sb, recon.TriggerManual)
	if err != nil || run.BreakCount != 0 {
		t.Fatalf("recon = %+v, %v", run.Breaks, err)
	}
}

// Two real logs in one transaction to one address for one amount (a batch
// payment) used to be reported as a duplicate.
func TestBatchTransferIsNotADuplicate(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool)
	c := newCustomer(t, sb, "Ana")
	net, _ := chain.SimNetwork(money.USDC)
	addr := customer.SimAddress(sb.ID, c.ID, net.Key)
	if _, err := customer.AssignAddress(ctx, pool, sb.ID, c.ID, net.Key, addr); err != nil {
		t.Fatal(err)
	}
	var blockHash string
	inTx(t, func(tx pgx.Tx) error {
		h, err := sim.New(tx, sb.ID, net).Mine(ctx, tx, 1)
		blockHash = h.Hash
		return err
	})
	inTx(t, func(tx pgx.Tx) error {
		for idx := range 2 {
			if _, err := tx.Exec(ctx, `INSERT INTO sim_logs (sandbox_id, network, block_number, block_hash, tx_hash,
				log_index, from_addr, to_addr, feed_amount, canonical_amount, in_feed, canonical)
				VALUES ($1, $2, 1, $3, $4, $5, $6, $7, 10000000, 10000000, true, true)`,
				sb.ID, net.Key, blockHash, fmt.Sprintf("0x%064x", 0x5555), idx, engine.SimSender, addr); err != nil {
				return err
			}
		}
		_, err := sim.New(tx, sb.ID, net).Mine(ctx, tx, 5)
		return err
	})
	if res := syncSim(t, sb, net, addr); res.Credit.Credited != 2 {
		t.Fatalf("credited %d, want both logs", res.Credit.Credited)
	}
	run, err := newEngine(pool).Reconcile(ctx, sb, recon.TriggerManual)
	if err != nil || run.BreakCount != 0 {
		t.Fatalf("recon on a batch transfer = %+v, %v", run.Breaks, err)
	}
}

// Past 1,000 rail orders, the rail matcher used to report healthy payouts
// as missing on the rail.
func TestRailMatcherBeyondThousandOrders(t *testing.T) {
	ctx := t.Context()
	eng := newEngine(pool)
	sb := storetest.Sandbox(t, pool)
	c := newCustomer(t, sb, "Ana")
	inTx(t, func(tx pgx.Tx) error {
		return payout.Fund(ctx, tx, spei.Rail{}, sb.ID, money.New(money.MXN, 100_000_000_00), "t")
	})
	if _, err := eng.SimulateDeposit(ctx, sb, c.ID, money.New(money.USDC, 2_000_000_000)); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	for range engine.SimConfirmations + 1 {
		if _, err := eng.Tick(ctx, sb, later); err != nil {
			t.Fatal(err)
		}
	}
	inTx(t, func(tx pgx.Tx) error {
		for i := range 1005 {
			if _, err := payout.Create(ctx, tx, sb.ID, payout.Request{
				CustomerID: c.ID, Amount: money.New(money.USDC, 1_000_000),
				BeneficiaryCLABE: "012180015203300154", BeneficiaryName: fmt.Sprintf("B%d", i),
			}, payout.DefaultConfig()); err != nil {
				return err
			}
		}
		return nil
	})
	for range 2 {
		if _, err := eng.Tick(ctx, sb, later); err != nil {
			t.Fatal(err)
		}
	}
	run, err := eng.Reconcile(ctx, sb, recon.TriggerManual)
	if err != nil || run.BreakCount != 0 {
		t.Fatalf("healthy world with 1,005 payouts: %d breaks (%v)", run.BreakCount, err)
	}
}

// Two customers' first deposits at once used to deadlock (key-share lock
// from the address insert, then an upgrade to FOR UPDATE).
func TestConcurrentFirstDeposits(t *testing.T) {
	eng := newEngine(pool)
	for range 10 {
		sb := storetest.Sandbox(t, pool)
		cs := []customer.Customer{newCustomer(t, sb, "A"), newCustomer(t, sb, "B")}
		errs := make([]error, len(cs))
		var wg sync.WaitGroup
		for i, c := range cs {
			wg.Go(func() {
				_, errs[i] = eng.SimulateDeposit(t.Context(), sb, c.ID, money.New(money.USDC, 1_000_000))
			})
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatal(err)
		}
	}
}
