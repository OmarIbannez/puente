package payout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

// beneficiary is a valid BBVA CLABE.
const beneficiary = "012180015203300154"

// treasury is what every scenario prefunds the SPEI account with.
var treasury = mxn(10_000_000_00)

var rail = spei.Rail{}

func usdc(minor int64) money.Amount { return money.New(money.USDC, minor) }
func mxn(minor int64) money.Amount  { return money.New(money.MXN, minor) }

// scenario is one sandbox with a funded treasury and one customer.
type scenario struct {
	t        *testing.T
	ctx      context.Context
	sb       uuid.UUID
	customer uuid.UUID
	worker   Worker
	cfg      Config
}

func newScenario(t *testing.T, balance money.Amount) *scenario {
	t.Helper()
	s := &scenario{t: t, ctx: context.WithoutCancel(t.Context()), sb: storetest.Sandbox(t, pool).ID, cfg: DefaultConfig()}
	s.worker = Worker{Rail: rail, Config: s.cfg}
	s.inTx(func(tx pgx.Tx) error { return Fund(t.Context(), tx, rail, s.sb, treasury, "initial") })
	s.customer = s.newCustomer(balance)
	t.Cleanup(s.checkInvariants)
	return s
}

func (s *scenario) inTx(fn func(pgx.Tx) error) {
	s.t.Helper()
	if err := store.InTx(s.ctx, pool, fn); err != nil {
		s.t.Fatal(err)
	}
}

// newCustomer creates a customer and credits balance with a deposit entry
// (Dr 1010 / Cr 2010).
func (s *scenario) newCustomer(balance money.Amount) uuid.UUID {
	s.t.Helper()
	c, err := customer.Create(s.ctx, pool, s.sb, "Cliente "+uuid.NewString()[:8])
	if err != nil {
		s.t.Fatal(err)
	}
	if balance.Minor > 0 {
		s.inTx(func(tx pgx.Tx) error {
			_, _, err := ledger.Post(s.ctx, tx, ledger.Entry{
				SandboxID: s.sb,
				Ref:       "deposit:test:" + uuid.NewString(),
				Kind:      ledger.KindDeposit,
				Postings: []ledger.Posting{
					ledger.Debit(ledger.HotWallet, balance),
					ledger.Credit(ledger.CustomerBalances, balance).For(c.ID),
				},
			})
			return err
		})
	}
	return c.ID
}

func (s *scenario) request(cust uuid.UUID, amount money.Amount) Request {
	return Request{
		CustomerID:       cust,
		Amount:           amount,
		BeneficiaryCLABE: beneficiary,
		BeneficiaryName:  "Ana López",
		Concepto:         "Pago Puente",
	}
}

func (s *scenario) tryCreate(cust uuid.UUID, amount money.Amount) (Payout, error) {
	var p Payout
	err := store.InTx(s.ctx, pool, func(tx pgx.Tx) error {
		var err error
		p, err = Create(s.ctx, tx, s.sb, s.request(cust, amount), s.cfg)
		return err
	})
	return p, err
}

func (s *scenario) create(amount money.Amount) Payout {
	s.t.Helper()
	p, err := s.tryCreate(s.customer, amount)
	if err != nil {
		s.t.Fatalf("Create(%s): %v", amount, err)
	}
	return p
}

func (s *scenario) get(id uuid.UUID) Payout {
	s.t.Helper()
	p, err := Get(s.ctx, pool, s.sb, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (s *scenario) wantStatus(id uuid.UUID, want Status) Payout {
	s.t.Helper()
	p := s.get(id)
	if p.Status != want {
		s.t.Fatalf("payout %s is %s, want %s (last error %q)", p.Clave, p.Status, want, p.LastError)
	}
	return p
}

// now reads the database clock, which stamps the outbox rows.
func (s *scenario) now() time.Time {
	s.t.Helper()
	var now time.Time
	if err := pool.QueryRow(s.ctx, "SELECT now()").Scan(&now); err != nil {
		s.t.Fatal(err)
	}
	return now
}

func (s *scenario) process(w Worker, now time.Time) int {
	s.t.Helper()
	n, err := w.ProcessOutbox(s.ctx, pool, s.sb, now)
	if err != nil {
		s.t.Fatalf("ProcessOutbox: %v", err)
	}
	return n
}

func (s *scenario) tick(now time.Time) TickResult {
	s.t.Helper()
	res, err := s.worker.Tick(s.ctx, pool, s.sb, now)
	if err != nil {
		s.t.Fatalf("Tick: %v", err)
	}
	return res
}

func (s *scenario) sync() []Change {
	s.t.Helper()
	changes, err := s.worker.SyncRail(s.ctx, pool, s.sb)
	if err != nil {
		s.t.Fatalf("SyncRail: %v", err)
	}
	return changes
}

func (s *scenario) faults(f store.Faults) {
	s.t.Helper()
	if _, err := store.SetFaults(s.ctx, pool, s.sb, f); err != nil {
		s.t.Fatal(err)
	}
}

func (s *scenario) available(cust uuid.UUID) money.Amount {
	s.t.Helper()
	a, err := Available(s.ctx, pool, s.sb, cust, money.USDC)
	if err != nil {
		s.t.Fatal(err)
	}
	return a
}

// balance returns the raw (debit-positive) balance of an account.
func (s *scenario) balance(account ledger.Account, asset money.Asset) int64 {
	s.t.Helper()
	b, err := ledger.Balance(s.ctx, pool, s.sb, account, asset)
	if err != nil {
		s.t.Fatal(err)
	}
	return b.Minor
}

// entries returns the payout's journal entries of kind.
func (s *scenario) entries(id uuid.UUID, kind ledger.Kind) []ledger.PostedEntry {
	s.t.Helper()
	es, err := ledger.Entries(s.ctx, pool, s.sb, ledger.EntryFilter{
		SubjectType: "payout", SubjectID: id.String(), Kind: kind, Limit: ledger.MaxEntriesLimit,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return es
}

func (s *scenario) wantEntries(id uuid.UUID, kind ledger.Kind, n int) {
	s.t.Helper()
	if got := len(s.entries(id, kind)); got != n {
		s.t.Fatalf("payout %s has %d %s entries, want %d", id, got, kind, n)
	}
}

func (s *scenario) order(clave string) spei.Record {
	s.t.Helper()
	rec, err := rail.Order(s.ctx, pool, s.sb, clave)
	if err != nil {
		s.t.Fatal(err)
	}
	return rec
}

func (s *scenario) payouts() []Payout {
	s.t.Helper()
	ps, err := List(s.ctx, pool, s.sb, Filter{Limit: MaxListLimit})
	if err != nil {
		s.t.Fatal(err)
	}
	return ps
}

// checkInvariants asserts the ledger invariants of the sandbox: every asset
// balances; each customer's 2020 reserve equals the totals of their
// reserved payouts (zero once none is reserved); 2100 equals the pesos of
// payouts in flight; and 1100 equals the rail's statement balance.
func (s *scenario) checkInvariants() {
	t, ctx := s.t, s.ctx
	t.Helper()
	tb, err := ledger.TrialBalance(ctx, pool, s.sb)
	if err != nil {
		t.Fatal(err)
	}
	for asset, sum := range tb {
		if sum != 0 {
			t.Errorf("trial balance %s = %d, want 0", asset, sum)
		}
	}
	reserved := map[uuid.UUID]map[money.Asset]int64{}
	var inFlight int64
	for _, p := range s.payouts() {
		if reserved[p.CustomerID] == nil {
			reserved[p.CustomerID] = map[money.Asset]int64{}
		}
		switch p.Status {
		case StatusReserved:
			reserved[p.CustomerID][p.Amount.Asset] += p.Total().Minor
		case StatusSubmitted, StatusNeedsReview:
			inFlight += p.MXN.Minor
		case StatusLiquidated, StatusRejected, StatusReleased, StatusReturned:
		}
	}
	for cust, byAsset := range reserved {
		for _, asset := range []money.Asset{money.USDC, money.MXNB} {
			got, err := ledger.PartyBalance(ctx, pool, s.sb, cust, ledger.PayoutsReserved, asset)
			if err != nil {
				t.Fatal(err)
			}
			if got.Minor != -byAsset[asset] {
				t.Errorf("2020 %s for %s = %d, want %d", asset, cust, got.Minor, -byAsset[asset])
			}
		}
	}
	if got := s.balance(ledger.SPEIInFlight, money.MXN); got != -inFlight {
		t.Errorf("2100 = %d, want %d", got, -inFlight)
	}
	stmt, err := rail.StatementBalance(ctx, pool, s.sb)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.balance(ledger.SPEISettlement, money.MXN); got != stmt.Minor {
		t.Errorf("1100 = %d, rail statement = %d", got, stmt.Minor)
	}
}

// errRailDown is the failure injected by lossyRail.fails.
var errRailDown = errors.New("rail unreachable (test)")

// lossyRail is the real rail, but it fails the next fails submissions
// outright (nothing reaches the rail), then loses the acknowledgement of
// the next drops submissions (the order is still kept), as a flaky network
// would.
type lossyRail struct {
	spei.Rail
	mu    sync.Mutex
	fails int
	drops int
}

func (r *lossyRail) Submit(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, o spei.Order) (spei.Ack, error) {
	r.mu.Lock()
	if r.fails > 0 {
		r.fails--
		r.mu.Unlock()
		return spei.Ack{}, errRailDown
	}
	r.mu.Unlock()
	ack, err := r.Rail.Submit(ctx, tx, sandbox, o)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil && r.drops > 0 {
		r.drops--
		return spei.Ack{}, fmt.Errorf("%w: clave %s (lossy test rail)", spei.ErrTimeout, o.Clave)
	}
	return ack, err
}
