// Package seed fills a fresh visitor sandbox with a small demo world: a
// funded SPEI treasury, three customers with confirmed USDC and MXNB
// deposits, and payouts in every state a visitor should see (liquidated
// with CEPs, en proceso, returned, released after a rejection or a cancel,
// and still reserved). It finishes with a clean scheduled reconciliation.
//
// Seeding drives the real engine (simulated chain, outbox worker, rail
// faults), so the seeded books are exactly what the engine would produce;
// nothing is written behind its back. It is deterministic and takes well
// under two seconds.
package seed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Treasury is the MXN the seed prefunds the SPEI settlement account with.
var Treasury = money.New(money.MXN, 250_000_000) // MXN 2,500,000.00

// TreasuryRef is the funding reference of the seed's treasury deposit.
const TreasuryRef = "seed"

// maxTicks bounds every wait for the engine to reach a state.
const maxTicks = 12

// ErrNotSettled is returned when the engine does not reach the expected
// state within maxTicks ticks.
var ErrNotSettled = errors.New("seed: engine did not reach the expected state")

// ErrBreaks is returned when the closing reconciliation finds breaks.
var ErrBreaks = errors.New("seed: closing reconciliation found breaks")

// Customer indexes into customers.
const (
	remesas = iota
	ana
	cafe
)

// customers are the demo account holders.
var customers = [...]string{
	remesas: "Remesas Norte SA de CV",
	ana:     "Ana Torres",
	cafe:    "Café Colibrí",
}

// deposit is one simulated stablecoin deposit.
type deposit struct {
	customer int
	amount   money.Amount
}

// deposits land on the simulated chains before any payout.
var deposits = []deposit{
	{remesas, money.New(money.USDC, 1_250_000_000)}, // 1,250 USDC
	{ana, money.New(money.USDC, 400_000_000)},       // 400 USDC
	{cafe, money.New(money.MXNB, 18_000_000_000)},   // 18,000 MXNB
}

// beneficiary is a SPEI destination: a CLABE built from a bank, a plaza
// and an account, and the holder's name.
type beneficiary struct {
	bank, plaza, account string
	name                 string
}

// order is one demo payout request.
type order struct {
	customer int
	amount   money.Amount
	to       beneficiary
	concepto string
}

// bbva is the well-known BBVA test CLABE 012180015203300154.
var bbva = beneficiary{"012", "180", "01520330015", "Distribuidora del Bajío"}

// The payouts, by the state they end in.
var (
	liquidated = []order{
		{remesas, money.New(money.USDC, 300_000_000), bbva, "Pago proveedor"},
		{ana, money.New(money.USDC, 120_000_000), beneficiary{"072", "580", "10234567891", "Luis Torres"}, "Renta octubre"},
		{cafe, money.New(money.MXNB, 6_500_000_000), beneficiary{"014", "320", "65500123456", "Finca La Esperanza"}, "Pago proveedor café"},
		{cafe, money.New(money.MXNB, 2_400_000_000), beneficiary{"638", "180", "00004417263", "María Hernández"}, "Nómina quincena"},
	}
	returned = order{
		remesas, money.New(money.USDC, 150_000_000),
		beneficiary{"002", "180", "70011223344", "Comercial Sierra Madre"},
		"Pago factura 1187",
	}
	rejected = order{
		ana, money.New(money.USDC, 80_000_000),
		beneficiary{"021", "180", "40099887766", "Colegio Montessori"},
		"Colegiatura octubre",
	}
	cancelled = order{
		ana, money.New(money.USDC, 50_000_000),
		beneficiary{"127", "180", "12345678901", "Jorge Torres"},
		"Regalo cumpleaños",
	}
	submitted = order{
		remesas, money.New(money.USDC, 200_000_000),
		beneficiary{"646", "180", "00001234567", "Logística Pacífico"},
		"Pago proveedor",
	}
	reserved = order{
		cafe, money.New(money.MXNB, 1_500_000_000),
		beneficiary{"137", "180", "55566677788", "Inmobiliaria Centro"},
		"Renta local",
	}
)

// seeder carries the state of one Seed call.
type seeder struct {
	pool      *pgxpool.Pool
	eng       *engine.Engine
	sb        store.Sandbox
	customers [len(customers)]uuid.UUID
}

// Seed fills the visitor sandbox with the demo world. The sandbox should be
// fresh: Seed adds to whatever is there and assumes no fault is armed. It
// fails if the closing reconciliation is not clean.
func Seed(ctx context.Context, pool *pgxpool.Pool, eng *engine.Engine, sandbox uuid.UUID) error {
	sb, err := store.GetSandbox(ctx, pool, sandbox)
	if err != nil {
		return fmt.Errorf("seed: sandbox %s: %w", sandbox, err)
	}
	if sb.Kind != store.KindVisitor {
		return fmt.Errorf("seed: sandbox %s is %s: %w", sandbox, sb.Kind, engine.ErrLiveReadOnly)
	}
	// Hide the sandbox from the background loop while seeding: the loop
	// ticks only recently seen sandboxes, and a concurrent tick could
	// consume a fault armed for a specific payout. An hour back is outside
	// the loop's activity window and far from the idle-prune age.
	if _, err := pool.Exec(ctx,
		"UPDATE sandboxes SET last_seen_at = now() - interval '1 hour' WHERE id = $1", sandbox); err != nil {
		return fmt.Errorf("seed: hide sandbox: %w", err)
	}
	s := &seeder{pool: pool, eng: eng, sb: sb}
	steps := []func(context.Context) error{
		s.world, s.deposit, s.liquidate, s.returnOne, s.rejectOne, s.cancelOne, s.submitOne, s.reserveOne, s.close,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	if _, err := store.TouchSandbox(ctx, pool, sandbox); err != nil {
		return fmt.Errorf("seed: unhide sandbox: %w", err)
	}
	return nil
}

// world funds the treasury and creates the customers.
func (s *seeder) world(ctx context.Context) error {
	return store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := payout.Fund(ctx, tx, s.eng.Rail(), s.sb.ID, Treasury, TreasuryRef); err != nil {
			return fmt.Errorf("seed: fund treasury: %w", err)
		}
		for i, name := range customers {
			c, err := customer.Create(ctx, tx, s.sb.ID, name)
			if err != nil {
				return fmt.Errorf("seed: customer %q: %w", name, err)
			}
			s.customers[i] = c.ID
		}
		return nil
	})
}

// deposit sends the simulated deposits and ticks until all are credited.
func (s *seeder) deposit(ctx context.Context) error {
	for _, d := range deposits {
		if _, err := s.eng.SimulateDeposit(ctx, s.sb, s.customers[d.customer], d.amount); err != nil {
			return fmt.Errorf("seed: deposit %s: %w", d.amount, err)
		}
	}
	// Count credits in the database, not in this seeder's tick reports, so
	// a credit made by any other tick still counts.
	return s.tickUntil(ctx, "deposits credited", func(engine.TickReport) (bool, error) {
		var credited int
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM deposit_credits
			WHERE sandbox_id = $1 AND reversal_entry_id IS NULL`, s.sb.ID).Scan(&credited)
		return credited >= len(deposits), err
	})
}

// liquidate creates the payouts that settle and ticks until they have.
func (s *seeder) liquidate(ctx context.Context) error {
	ids := make([]uuid.UUID, 0, len(liquidated))
	for _, o := range liquidated {
		p, err := s.create(ctx, o)
		if err != nil {
			return err
		}
		ids = append(ids, p.ID)
	}
	return s.tickUntilStatus(ctx, payout.StatusLiquidated, ids...)
}

// returnOne arms the RailReturn fault for one payout and ticks until the
// beneficiary bank has sent the pesos back.
func (s *seeder) returnOne(ctx context.Context) error {
	return s.withFault(ctx, store.Faults{RailReturn: true}, returned, payout.StatusReturned)
}

// rejectOne arms the RailReject fault for one payout and ticks until its
// funds are released.
func (s *seeder) rejectOne(ctx context.Context) error {
	return s.withFault(ctx, store.Faults{RailReject: true}, rejected, payout.StatusReleased)
}

// cancelOne creates and cancels a payout in one transaction, so no worker
// can submit it in between.
func (s *seeder) cancelOne(ctx context.Context) error {
	return store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := s.createTx(ctx, tx, cancelled)
		if err != nil {
			return err
		}
		if _, err := payout.Cancel(ctx, tx, s.sb.ID, p.ID); err != nil {
			return fmt.Errorf("seed: cancel %s: %w", p.ID, err)
		}
		return nil
	})
}

// submitOne creates a payout and ticks once: the rail has it en proceso.
func (s *seeder) submitOne(ctx context.Context) error {
	if _, err := s.create(ctx, submitted); err != nil {
		return err
	}
	_, err := s.tick(ctx)
	return err
}

// reserveOne creates a payout and leaves it waiting for the worker.
func (s *seeder) reserveOne(ctx context.Context) error {
	_, err := s.create(ctx, reserved)
	return err
}

// close disarms every fault and requires a clean scheduled reconciliation.
func (s *seeder) close(ctx context.Context) error {
	if _, err := store.SetFaults(ctx, s.pool, s.sb.ID, store.Faults{}); err != nil {
		return fmt.Errorf("seed: clear faults: %w", err)
	}
	run, err := s.eng.Reconcile(ctx, s.sb, recon.TriggerScheduled)
	if err != nil {
		return fmt.Errorf("seed: reconcile: %w", err)
	}
	if run.BreakCount != 0 {
		return fmt.Errorf("%w: %d in run %d", ErrBreaks, run.BreakCount, run.ID)
	}
	return nil
}

// withFault arms f right before creating o (faults are consumed by the
// next order submitted, and o is the only one queued), then ticks until o
// reaches want.
func (s *seeder) withFault(ctx context.Context, f store.Faults, o order, want payout.Status) error {
	if _, err := store.SetFaults(ctx, s.pool, s.sb.ID, f); err != nil {
		return fmt.Errorf("seed: arm faults: %w", err)
	}
	p, err := s.create(ctx, o)
	if err != nil {
		return err
	}
	return s.tickUntilStatus(ctx, want, p.ID)
}

// create places o in its own transaction.
func (s *seeder) create(ctx context.Context, o order) (payout.Payout, error) {
	var p payout.Payout
	err := store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		p, err = s.createTx(ctx, tx, o)
		return err
	})
	return p, err
}

func (s *seeder) createTx(ctx context.Context, tx pgx.Tx, o order) (payout.Payout, error) {
	clabe, err := spei.NewCLABE(o.to.bank, o.to.plaza, o.to.account)
	if err != nil {
		return payout.Payout{}, fmt.Errorf("seed: clabe for %s: %w", o.to.name, err)
	}
	p, err := payout.Create(ctx, tx, s.sb.ID, payout.Request{
		CustomerID:       s.customers[o.customer],
		Amount:           o.amount,
		BeneficiaryCLABE: clabe.Number,
		BeneficiaryName:  o.to.name,
		Concepto:         o.concepto,
	}, s.eng.PayoutConfig())
	if err != nil {
		return payout.Payout{}, fmt.Errorf("seed: payout %q: %w", o.concepto, err)
	}
	return p, nil
}

// tick runs one engine cycle at the database's clock, so a job queued at
// the database's now() is always due even if the clocks disagree.
func (s *seeder) tick(ctx context.Context) (engine.TickReport, error) {
	var now time.Time
	if err := s.pool.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return engine.TickReport{}, fmt.Errorf("seed: read clock: %w", err)
	}
	rep, err := s.eng.Tick(ctx, s.sb, now)
	if err != nil {
		return rep, fmt.Errorf("seed: tick: %w", err)
	}
	return rep, nil
}

// tickUntil ticks until done reports true, at most maxTicks times.
func (s *seeder) tickUntil(ctx context.Context, what string, done func(engine.TickReport) (bool, error)) error {
	for range maxTicks {
		rep, err := s.tick(ctx)
		if err != nil {
			return err
		}
		ok, err := done(rep)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("%w: %s after %d ticks", ErrNotSettled, what, maxTicks)
}

// tickUntilStatus ticks until every payout in ids has status want.
func (s *seeder) tickUntilStatus(ctx context.Context, want payout.Status, ids ...uuid.UUID) error {
	return s.tickUntil(ctx, "payouts "+string(want), func(engine.TickReport) (bool, error) {
		for _, id := range ids {
			p, err := payout.Get(ctx, s.pool, s.sb.ID, id)
			if err != nil {
				return false, fmt.Errorf("seed: %w", err)
			}
			if p.Status != want {
				return false, nil
			}
		}
		return true, nil
	})
}
