package recon_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/sim"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

const (
	confirmations = 3
	beneficiary   = "012180015203300154"
	sender        = "0x00000000000000000000000000000000000000f1"
)

var (
	rail   = spei.Rail{}
	usdc   = mustNet(money.USDC)
	worker = payout.Worker{Rail: rail, Config: payout.DefaultConfig()}
)

func mustNet(a money.Asset) chain.Network {
	n, ok := chain.SimNetwork(a)
	if !ok {
		panic("no sim network for " + string(a))
	}
	return n
}

// world is one sandbox with a funded treasury and a customer that has a
// deposit address on the simulated USDC network.
type world struct {
	t        *testing.T
	sb       uuid.UUID
	customer uuid.UUID
	address  string
	now      time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := t.Context()
	w := &world{t: t, sb: storetest.Sandbox(t, pool).ID, now: time.Now()}
	c, err := customer.Create(ctx, pool, w.sb, "Ana")
	if err != nil {
		t.Fatal(err)
	}
	w.customer = c.ID
	w.address = customer.SimAddress(w.sb, c.ID, usdc.Key)
	if _, err := customer.AssignAddress(ctx, pool, w.sb, c.ID, usdc.Key, w.address); err != nil {
		t.Fatal(err)
	}
	w.tx(func(tx pgx.Tx) error {
		return payout.Fund(ctx, tx, rail, w.sb, money.New(money.MXN, 10_000_000), "seed")
	})
	return w
}

func (w *world) tx(fn func(pgx.Tx) error) {
	w.t.Helper()
	if err := store.InTx(w.t.Context(), pool, fn); err != nil {
		w.t.Fatal(err)
	}
}

// deposit lands minor micro-USDC at the customer's address and mines enough
// blocks to confirm it, without crediting.
func (w *world) deposit(minor int64) {
	w.t.Helper()
	ctx := w.t.Context()
	w.tx(func(tx pgx.Tx) error {
		c := sim.New(tx, w.sb, usdc)
		if _, err := c.Deposit(ctx, tx, sender, w.address, money.New(money.USDC, minor)); err != nil {
			return err
		}
		_, err := c.Mine(ctx, tx, confirmations)
		return err
	})
}

func (w *world) sync() {
	w.t.Helper()
	ctx := w.t.Context()
	src := func(tx pgx.Tx) chain.Source { return sim.New(tx, w.sb, usdc) }
	if _, err := deposit.Sync(ctx, pool, w.sb, usdc, src, []string{w.address}, confirmations); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) payout(minor int64) payout.Payout {
	w.t.Helper()
	var p payout.Payout
	w.tx(func(tx pgx.Tx) error {
		var err error
		p, err = payout.Create(w.t.Context(), tx, w.sb, payout.Request{
			CustomerID: w.customer, Amount: money.New(money.USDC, minor),
			BeneficiaryCLABE: beneficiary, BeneficiaryName: "Luis Pérez", Concepto: "Renta",
		}, payout.DefaultConfig())
		return err
	})
	return p
}

func (w *world) tick() {
	w.t.Helper()
	w.now = w.now.Add(time.Minute)
	if _, err := worker.Tick(w.t.Context(), pool, w.sb, w.now); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) faults(f store.Faults) {
	w.t.Helper()
	if _, err := store.SetFaults(w.t.Context(), pool, w.sb, f); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) reconcile() recon.Run {
	w.t.Helper()
	r := recon.New(rail, recon.ChainMatcher{Network: usdc, Source: func(tx pgx.Tx, sb uuid.UUID) chain.Source {
		return sim.New(tx, sb, usdc)
	}})
	run, err := r.Run(w.t.Context(), pool, w.sb, recon.TriggerManual)
	if err != nil {
		w.t.Fatal(err)
	}
	return run
}

// happy builds a clean world: one credited deposit and one settled payout.
func happy(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.deposit(100_000_000)
	w.sync()
	p := w.payout(10_000_000)
	w.tick() // submit
	w.tick() // settle
	got, err := payout.Get(t.Context(), pool, w.sb, p.ID)
	if err != nil || got.Status != payout.StatusLiquidated {
		t.Fatalf("payout = %s, %v; want liquidated", got.Status, err)
	}
	return w
}

func wantBreaks(t *testing.T, run recon.Run, want ...recon.Kind) []recon.Break {
	t.Helper()
	if len(run.Breaks) != len(want) || run.BreakCount != len(want) {
		t.Fatalf("got %d breaks %+v, want %v", len(run.Breaks), run.Breaks, want)
	}
	for i, k := range want {
		if run.Breaks[i].Kind != k {
			t.Fatalf("break %d = %s (%s), want %s", i, run.Breaks[i].Kind, run.Breaks[i].Summary, k)
		}
	}
	return run.Breaks
}

func TestCleanWorldHasNoBreaks(t *testing.T) {
	w := happy(t)
	run := w.reconcile()
	wantBreaks(t, run)
	if run.Checked["rail_orders"] != 2 || run.Checked["balances"] != 1 || run.Checked["chain:"+usdc.Key] != 1 {
		t.Fatalf("checked = %v", run.Checked)
	}
}

func TestDuplicateLog(t *testing.T) {
	w := newWorld(t)
	w.faults(store.Faults{DuplicateLog: true})
	w.deposit(5_000_000)
	w.sync()
	b := wantBreaks(t, w.reconcile(), recon.Duplicate)[0]
	if b.Source != recon.SourceChain || b.Evidence["transfer_ids"] == "" || b.Actual != "10.000000 USDC" {
		t.Fatalf("duplicate break = %+v", b)
	}
}

func TestShortDeposit(t *testing.T) {
	w := newWorld(t)
	w.faults(store.Faults{ShortDeposit: true})
	w.deposit(100_000_000)
	w.sync()
	b := wantBreaks(t, w.reconcile(), recon.AmountMismatch)[0]
	if b.Expected == b.Actual || b.SubjectType != "chain_transfer" {
		t.Fatalf("amount break = %+v", b)
	}
}

func TestReorgBeforeAndAfterRescan(t *testing.T) {
	w := happy(t)
	w.faults(store.Faults{Reorg: true})
	w.tx(func(tx pgx.Tx) error {
		_, err := sim.New(tx, w.sb, usdc).Mine(t.Context(), tx, 1)
		return err
	})
	// The canonical chain dropped the deposit; the scanner hasn't noticed.
	wantBreaks(t, w.reconcile(), recon.MissingOnChain)
	// Once the scanner orphans it and the credit is reversed, books agree.
	w.sync()
	wantBreaks(t, w.reconcile())
}

func TestUncreditedDeposit(t *testing.T) {
	w := newWorld(t)
	w.deposit(1_000_000)
	w.tx(func(tx pgx.Tx) error { // scanned and confirmed, never credited
		_, err := chain.Scan(t.Context(), tx, w.sb, usdc, sim.New(tx, w.sb, usdc), []string{w.address}, confirmations)
		return err
	})
	b := wantBreaks(t, w.reconcile(), recon.MissingOnLedger)[0]
	if b.Source != recon.SourceChain {
		t.Fatalf("break = %+v", b)
	}
	w.sync()
	wantBreaks(t, w.reconcile())
}

func TestBankFee(t *testing.T) {
	w := happy(t)
	w.tx(func(tx pgx.Tx) error {
		_, err := rail.InjectBankFee(t.Context(), tx, w.sb, money.New(money.MXN, 1_250))
		return err
	})
	b := wantBreaks(t, w.reconcile(), recon.BalanceMismatch)[0]
	if b.Evidence["difference"] != "-$12.50 MXN" {
		t.Fatalf("balance break = %+v", b)
	}
}

func TestManualOrder(t *testing.T) {
	w := happy(t)
	w.tx(func(tx pgx.Tx) error {
		_, err := rail.InjectManualOrder(t.Context(), tx, w.sb, beneficiary, "Proveedor", money.New(money.MXN, 50_000))
		return err
	})
	// The bank paid it out of the settlement account, so the balance is off too.
	bs := wantBreaks(t, w.reconcile(), recon.MissingOnLedger, recon.BalanceMismatch)
	if bs[0].SubjectType != "spei_order" || bs[0].Source != recon.SourceRail {
		t.Fatalf("manual order break = %+v", bs[0])
	}
}

func TestSettlementNotYetBooked(t *testing.T) {
	w := newWorld(t)
	w.deposit(100_000_000)
	w.sync()
	w.payout(10_000_000)
	w.tick() // submitted
	// The bank settles, but Puente hasn't synced the rail yet.
	w.tx(func(tx pgx.Tx) error {
		_, err := rail.Advance(t.Context(), tx, w.sb)
		return err
	})
	wantBreaks(t, w.reconcile(), recon.MissingOnLedger, recon.BalanceMismatch)
	w.tick()
	wantBreaks(t, w.reconcile())
}

func TestRailTampering(t *testing.T) {
	ctx := t.Context()
	w := happy(t)
	if _, err := pool.Exec(ctx, `UPDATE spei_orders SET amount_centavos = amount_centavos + 1 WHERE sandbox_id = $1`, w.sb); err != nil {
		t.Fatal(err)
	}
	wantBreaks(t, w.reconcile(), recon.AmountMismatch)
	if _, err := pool.Exec(ctx, `DELETE FROM spei_statement WHERE sandbox_id = $1 AND clave IS NOT NULL`, w.sb); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM spei_orders WHERE sandbox_id = $1`, w.sb); err != nil {
		t.Fatal(err)
	}
	bs := wantBreaks(t, w.reconcile(), recon.MissingOnRail, recon.BalanceMismatch)
	if bs[0].SubjectType != "payout" {
		t.Fatalf("missing-on-rail break = %+v", bs[0])
	}
}

func TestRunsAreStored(t *testing.T) {
	ctx := t.Context()
	w := happy(t)
	if _, err := recon.Latest(ctx, pool, w.sb); !errors.Is(err, recon.ErrNoRuns) {
		t.Fatalf("Latest before any run = %v", err)
	}
	first := w.reconcile()
	w.tx(func(tx pgx.Tx) error {
		_, err := rail.InjectBankFee(ctx, tx, w.sb, money.New(money.MXN, 1))
		return err
	})
	second := w.reconcile()

	latest, err := recon.Latest(ctx, pool, w.sb)
	if err != nil || latest.ID != second.ID || len(latest.Breaks) != 1 || latest.Breaks[0].ID != second.Breaks[0].ID {
		t.Fatalf("Latest = %+v, %v", latest, err)
	}
	got, err := recon.Get(ctx, pool, w.sb, first.ID)
	if err != nil || got.BreakCount != 0 || got.FinishedAt == nil || got.Checked["balances"] != 1 {
		t.Fatalf("Get(first) = %+v, %v", got, err)
	}
	runs, err := recon.Runs(ctx, pool, w.sb, 10)
	if err != nil || len(runs) != 2 || runs[0].ID != second.ID {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if _, err := recon.Get(ctx, pool, storetest.Sandbox(t, pool).ID, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get from another sandbox = %v", err)
	}
}
