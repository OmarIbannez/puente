package payout

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// 100 USDC: fee 0.50 USDC, total 100.50 USDC, $1,845.23 MXN at 18.4523.
var (
	hundred      = usdc(100_000_000)
	hundredFee   = int64(500_000)
	hundredTotal = int64(100_500_000)
	hundredMXN   = int64(184_523)
)

func TestQuote(t *testing.T) {
	cfg := DefaultConfig()
	q, err := cfg.Quote(hundred)
	if err != nil {
		t.Fatal(err)
	}
	if q.Fee.Minor != hundredFee || q.Total.Minor != hundredTotal || q.MXN != mxn(hundredMXN) || q.Rate != 18_452_300 {
		t.Fatalf("Quote(100 USDC) = %+v", q)
	}
	mxnb, err := cfg.Quote(money.New(money.MXNB, 2_500_009))
	if err != nil || mxnb.MXN != mxn(250) || mxnb.Fee.Minor != 12_500 {
		t.Fatalf("Quote(2.500009 MXNB) = %+v, %v", mxnb, err)
	}
	noFee, err := Config{}.Quote(hundred)
	if err != nil || noFee.Fee.Minor != 0 || noFee.Total != hundred {
		t.Fatalf("zero-fee quote = %+v, %v", noFee, err)
	}
	for _, tc := range []struct {
		amount money.Amount
		want   error
	}{
		{money.New(money.MXNB, 9_999), ErrInvalidAmount}, // under a centavo
		{usdc(0), ErrInvalidAmount},
		{usdc(-5), ErrInvalidAmount},
		{mxn(100), ErrUnsupportedAsset},
		{money.New("DOGE", 1), ErrUnsupportedAsset},
	} {
		if _, err := cfg.Quote(tc.amount); !errors.Is(err, tc.want) {
			t.Errorf("Quote(%+v) = %v, want %v", tc.amount, err, tc.want)
		}
	}
	if d := DefaultBackoff(1); d != time.Second {
		t.Errorf("DefaultBackoff(1) = %s", d)
	}
	if d := DefaultBackoff(4); d != 8*time.Second {
		t.Errorf("DefaultBackoff(4) = %s", d)
	}
	if d := DefaultBackoff(1000); d != maxBackoff {
		t.Errorf("DefaultBackoff(1000) = %s", d)
	}
}

func TestTransitions(t *testing.T) {
	s := newScenario(t, usdc(10_000_000_000))
	ctx := t.Context()

	// step applies one edge and checks the status and its entry.
	step := func(id uuid.UUID, to Status) {
		t.Helper()
		before := s.get(id)
		s.inTx(func(tx pgx.Tx) error {
			_, err := transition(ctx, tx, s.sb, id, to, map[string]any{"test": true})
			return err
		})
		s.wantStatus(id, to)
		if e, ok := entryFor(before, before.Status, to); ok {
			got, err := ledger.EntryByRef(ctx, pool, s.sb, e.Ref)
			if err != nil || got.Kind != e.Kind {
				t.Fatalf("%s → %s: entry %s = %+v, %v", before.Status, to, e.Ref, got, err)
			}
		}
	}
	walk := func(path ...Status) uuid.UUID {
		t.Helper()
		p := s.create(hundred)
		s.wantEntries(p.ID, ledger.KindPayoutReserve, 1)
		for _, to := range path {
			step(p.ID, to)
		}
		return p.ID
	}
	illegal := func(id uuid.UUID, to Status) {
		t.Helper()
		before := s.get(id)
		var n0, n1 int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM journal_entries WHERE sandbox_id = $1", s.sb).Scan(&n0); err != nil {
			t.Fatal(err)
		}
		err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := transition(ctx, tx, s.sb, id, to, nil)
			return err
		})
		if !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("%s → %s = %v, want ErrIllegalTransition", before.Status, to, err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM journal_entries WHERE sandbox_id = $1", s.sb).Scan(&n1); err != nil {
			t.Fatal(err)
		}
		if n1 != n0 {
			t.Fatalf("illegal %s → %s posted %d entries", before.Status, to, n1-n0)
		}
		s.wantStatus(id, before.Status)
	}

	// Together these paths cover every legal edge.
	cancelled := walk(StatusReleased)
	settled := walk(StatusSubmitted, StatusLiquidated)
	illegal(settled, StatusSubmitted)
	illegal(settled, StatusReleased)
	step(settled, StatusReturned)
	rejected := walk(StatusSubmitted, StatusRejected, StatusReleased)
	reviewed := walk(StatusSubmitted, StatusNeedsReview, StatusLiquidated, StatusReturned)
	walk(StatusSubmitted, StatusNeedsReview, StatusRejected, StatusReleased)

	illegal(cancelled, StatusSubmitted)
	illegal(rejected, StatusSubmitted)
	illegal(reviewed, StatusLiquidated)
	fresh := s.create(hundred)
	illegal(fresh.ID, StatusLiquidated)
	illegal(fresh.ID, StatusReturned)
	if _, err := Get(ctx, pool, s.sb, uuid.New()); !errors.Is(err, ErrNotFound) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing) = %v", err)
	}

	for from, tos := range edges {
		for _, to := range Statuses {
			if CanTransition(from, to) != slices.Contains(tos, to) {
				t.Fatalf("CanTransition(%s, %s) disagrees with edges", from, to)
			}
		}
	}
	if CanTransition(StatusReleased, StatusSubmitted) || CanTransition(StatusReturned, StatusLiquidated) ||
		CanTransition(StatusLiquidated, StatusSubmitted) {
		t.Fatal("terminal statuses have outgoing edges")
	}

	evs, err := Events(ctx, pool, s.sb, reviewed)
	if err != nil {
		t.Fatal(err)
	}
	var froms, tos []Status
	for _, e := range evs {
		froms, tos = append(froms, e.From), append(tos, e.To)
	}
	wantTos := []Status{StatusReserved, StatusSubmitted, StatusNeedsReview, StatusLiquidated, StatusReturned}
	wantFroms := []Status{"", StatusReserved, StatusSubmitted, StatusNeedsReview, StatusLiquidated}
	if !slices.Equal(tos, wantTos) || !slices.Equal(froms, wantFroms) {
		t.Fatalf("events from %v to %v", froms, tos)
	}
}

func TestHappyPath(t *testing.T) {
	s := newScenario(t, usdc(1_000_000_000))
	ctx := t.Context()
	// Funding is exactly-once on its ref.
	s.inTx(func(tx pgx.Tx) error { return Fund(ctx, tx, rail, s.sb, treasury, "initial") })
	if got := s.balance(ledger.SPEISettlement, money.MXN); got != treasury.Minor {
		t.Fatalf("1100 after repeated funding = %d", got)
	}

	p := s.create(hundred)
	if p.Status != StatusReserved || p.Fee.Minor != hundredFee || p.MXN != mxn(hundredMXN) || p.Clave == "" {
		t.Fatalf("created %+v", p)
	}
	if err := spei.ValidateClave(p.Clave); err != nil {
		t.Fatal(err)
	}
	if got := s.available(s.customer); got.Minor != 1_000_000_000-hundredTotal {
		t.Fatalf("available after reserve = %s", got)
	}
	s.checkInvariants()

	now := s.now()
	if res := s.tick(now); res.Processed != 1 || len(res.Changes) != 0 {
		t.Fatalf("first tick = %+v", res)
	}
	s.wantStatus(p.ID, StatusSubmitted)
	if rec := s.order(p.Clave); rec.Status != spei.StatusEnProceso || rec.Amount != p.MXN || rec.SubmitCount != 1 {
		t.Fatalf("rail order = %+v", rec)
	}
	s.checkInvariants()

	res := s.tick(now)
	if res.Processed != 0 || len(res.Changes) != 1 || res.Changes[0].To != StatusLiquidated {
		t.Fatalf("second tick = %+v", res)
	}
	got := s.wantStatus(p.ID, StatusLiquidated)
	if got.Attempts != 1 || got.LastError != "" {
		t.Fatalf("liquidated payout = %+v", got)
	}

	if a := s.available(s.customer); a.Minor != 1_000_000_000-hundredTotal {
		t.Fatalf("customer available = %s", a)
	}
	if fee := s.balance(ledger.FeeIncome, money.USDC); fee != -hundredFee {
		t.Fatalf("4100 = %d, want %d", fee, -hundredFee)
	}
	if b := s.balance(ledger.SPEIInFlight, money.MXN); b != 0 {
		t.Fatalf("2100 = %d", b)
	}
	if b := s.balance(ledger.SPEISettlement, money.MXN); b != treasury.Minor-hundredMXN {
		t.Fatalf("1100 = %d, want %d", b, treasury.Minor-hundredMXN)
	}
	for _, k := range []ledger.Kind{ledger.KindPayoutReserve, ledger.KindPayoutSubmit, ledger.KindPayoutSettle} {
		s.wantEntries(p.ID, k, 1)
	}
	// Nothing left to do.
	if res := s.tick(now.Add(time.Hour)); res.Processed != 0 || len(res.Changes) != 0 {
		t.Fatalf("idle tick = %+v", res)
	}
}

func TestRetryExactlyOnce(t *testing.T) {
	t.Run("timeout then retry", func(t *testing.T) {
		s := newScenario(t, usdc(1_000_000_000))
		p := s.create(hundred)
		s.faults(store.Faults{RailTimeout: true})

		now := s.now()
		if n := s.process(s.worker, now); n != 1 {
			t.Fatalf("first attempt processed %d", n)
		}
		got := s.wantStatus(p.ID, StatusSubmitted)
		if got.Attempts != 1 || got.LastError == "" {
			t.Fatalf("after timeout = %+v", got)
		}
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 1)
		s.checkInvariants()

		// Not due before the backoff.
		if n := s.process(s.worker, now); n != 0 {
			t.Fatalf("retried before backoff: %d", n)
		}
		retryAt := now.Add(DefaultBackoff(1))
		if n := s.process(s.worker, retryAt); n != 1 {
			t.Fatalf("retry processed %d", n)
		}
		got = s.wantStatus(p.ID, StatusSubmitted)
		if got.Attempts != 2 || got.LastError != "" {
			t.Fatalf("after retry = %+v", got)
		}
		rec := s.order(p.Clave)
		if rec.SubmitCount != 2 {
			t.Fatalf("submit_count = %d, want 2", rec.SubmitCount)
		}
		orders, err := rail.Orders(t.Context(), pool, s.sb, 0)
		if err != nil || len(orders) != 1 {
			t.Fatalf("rail orders = %d, %v", len(orders), err)
		}
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 1)

		s.tick(retryAt)
		s.wantStatus(p.ID, StatusLiquidated)
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 1)
		s.wantEntries(p.ID, ledger.KindPayoutSettle, 1)
	})

	t.Run("failed submit rolls back and retries", func(t *testing.T) {
		s := newScenario(t, usdc(1_000_000_000))
		w := Worker{Rail: &lossyRail{fails: 1}, Config: s.cfg}
		p := s.create(hundred)

		now := s.now()
		n, err := w.ProcessOutbox(t.Context(), pool, s.sb, now)
		if n != 1 || !errors.Is(err, errRailDown) {
			t.Fatalf("ProcessOutbox = %d, %v; want 1 and the rail error", n, err)
		}
		// The whole attempt rolled back: still reserved, no submit entry.
		got := s.wantStatus(p.ID, StatusReserved)
		if got.LastError == "" {
			t.Fatal("failure not recorded on the payout")
		}
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 0)
		s.checkInvariants()

		if n := s.process(w, now); n != 0 {
			t.Fatalf("retried before backoff: %d", n)
		}
		if n := s.process(w, now.Add(DefaultBackoff(1))); n != 1 {
			t.Fatalf("retry processed %d", n)
		}
		got = s.wantStatus(p.ID, StatusSubmitted)
		if got.Attempts != 2 || got.LastError != "" {
			t.Fatalf("after retry = %+v", got)
		}
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 1)
		if rec := s.order(p.Clave); rec.SubmitCount != 1 {
			t.Fatalf("submit_count = %d", rec.SubmitCount)
		}
	})

	t.Run("five timeouts then needs review", func(t *testing.T) {
		s := newScenario(t, usdc(1_000_000_000))
		lossy := &lossyRail{drops: 1000}
		w := Worker{Rail: lossy, Config: s.cfg}
		p := s.create(hundred)

		at := s.now()
		for attempt := 1; attempt <= DefaultMaxAttempts; attempt++ {
			if n := s.process(w, at); n != 1 {
				t.Fatalf("attempt %d processed %d", attempt, n)
			}
			at = at.Add(DefaultBackoff(attempt))
		}
		got := s.wantStatus(p.ID, StatusNeedsReview)
		if got.Attempts != DefaultMaxAttempts || got.LastError == "" {
			t.Fatalf("needs_review payout = %+v", got)
		}
		if n := s.process(w, at.Add(time.Hour)); n != 0 {
			t.Fatalf("job still active after needs_review: %d", n)
		}
		if rec := s.order(p.Clave); rec.SubmitCount != DefaultMaxAttempts {
			t.Fatalf("submit_count = %d", rec.SubmitCount)
		}
		s.wantEntries(p.ID, ledger.KindPayoutSubmit, 1)
		s.checkInvariants()

		// The rail did keep the order: a sync resolves the payout.
		changes := s.sync()
		if len(changes) != 1 || changes[0].From != StatusNeedsReview || changes[0].To != StatusLiquidated {
			t.Fatalf("sync = %+v", changes)
		}
		s.wantStatus(p.ID, StatusLiquidated)
		s.wantEntries(p.ID, ledger.KindPayoutSettle, 1)
	})
}

func TestRejectedReleasesFunds(t *testing.T) {
	const initial = 1_000_000_000
	s := newScenario(t, usdc(initial))
	p := s.create(hundred)
	s.faults(store.Faults{RailReject: true})

	now := s.now()
	if res := s.tick(now); res.Processed != 1 {
		t.Fatalf("tick = %+v", res)
	}
	s.wantStatus(p.ID, StatusReleased)
	if a := s.available(s.customer); a.Minor != initial {
		t.Fatalf("available after reject = %s, want the full %d", a, initial)
	}
	for _, acct := range []struct {
		a     ledger.Account
		asset money.Asset
	}{
		{ledger.FeeIncome, money.USDC},
		{ledger.FXClearing, money.USDC},
		{ledger.FXClearing, money.MXN},
		{ledger.SPEIInFlight, money.MXN},
	} {
		if b := s.balance(acct.a, acct.asset); b != 0 {
			t.Fatalf("%s %s = %d after reject", acct.a, acct.asset, b)
		}
	}
	if b := s.balance(ledger.SPEISettlement, money.MXN); b != treasury.Minor {
		t.Fatalf("1100 moved on a rejected order: %d", b)
	}
	s.wantEntries(p.ID, ledger.KindPayoutRelease, 1)
	evs, err := Events(t.Context(), pool, s.sb, p.ID)
	if err != nil || len(evs) != 4 || evs[2].To != StatusRejected || evs[3].To != StatusReleased {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if res := s.tick(now.Add(time.Hour)); res.Processed != 0 || len(res.Changes) != 0 {
		t.Fatalf("later tick = %+v", res)
	}
}

func TestDevolucion(t *testing.T) {
	const initial = 1_000_000_000
	s := newScenario(t, usdc(initial))
	p := s.create(hundred)
	s.faults(store.Faults{RailReturn: true})

	now := s.now()
	s.tick(now)
	s.wantStatus(p.ID, StatusSubmitted)
	s.tick(now)
	s.wantStatus(p.ID, StatusLiquidated)
	if b := s.balance(ledger.SPEISettlement, money.MXN); b != treasury.Minor-hundredMXN {
		t.Fatalf("1100 after settlement = %d", b)
	}
	s.checkInvariants()

	res := s.tick(now)
	if len(res.Changes) != 1 || res.Changes[0].From != StatusLiquidated || res.Changes[0].To != StatusReturned {
		t.Fatalf("return tick = %+v", res)
	}
	s.wantStatus(p.ID, StatusReturned)
	if a := s.available(s.customer); a.Minor != initial-hundredFee {
		t.Fatalf("available after return = %s, want initial minus fee", a)
	}
	if fee := s.balance(ledger.FeeIncome, money.USDC); fee != -hundredFee {
		t.Fatalf("fee not kept: 4100 = %d", fee)
	}
	if b := s.balance(ledger.SPEISettlement, money.MXN); b != treasury.Minor {
		t.Fatalf("1100 after return = %d", b)
	}
	// History is append-only: every entry and event is still there.
	for _, k := range []ledger.Kind{ledger.KindPayoutReserve, ledger.KindPayoutSubmit, ledger.KindPayoutSettle, ledger.KindPayoutReturn} {
		s.wantEntries(p.ID, k, 1)
	}
	evs, err := Events(t.Context(), pool, s.sb, p.ID)
	if err != nil || len(evs) != 4 {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if rec := s.order(p.Clave); rec.Status != spei.StatusDevuelto {
		t.Fatalf("rail order = %+v", rec)
	}
}

func TestInsufficientFunds(t *testing.T) {
	s := newScenario(t, usdc(10_000_000))
	ctx := t.Context()

	if _, err := s.tryCreate(s.customer, usdc(10_000_000)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("10 USDC + fee from 10 USDC = %v", err)
	}
	if ps := s.payouts(); len(ps) != 0 {
		t.Fatalf("failed create left %d payouts", len(ps))
	}
	if _, err := s.tryCreate(s.customer, usdc(9_000_000)); err != nil {
		t.Fatal(err)
	}
	if a := s.available(s.customer); a.Minor != 10_000_000-9_045_000 {
		t.Fatalf("available = %s", a)
	}

	bad := s.request(s.customer, usdc(1_000_000))
	for name, tc := range map[string]struct {
		mut  func(*Request)
		want error
	}{
		"clabe check digit": {func(r *Request) { r.BeneficiaryCLABE = "012180015203300155" }, ErrInvalidCLABE},
		"clabe short":       {func(r *Request) { r.BeneficiaryCLABE = "0121800152" }, ErrInvalidCLABE},
		"mxn":               {func(r *Request) { r.Amount = mxn(100) }, ErrUnsupportedAsset},
		"zero":              {func(r *Request) { r.Amount = usdc(0) }, ErrInvalidAmount},
		"no name":           {func(r *Request) { r.BeneficiaryName = "  " }, ErrInvalidRequest},
		"unknown customer":  {func(r *Request) { r.CustomerID = uuid.New() }, store.ErrNotFound},
	} {
		r := bad
		tc.mut(&r)
		err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := Create(ctx, tx, s.sb, r, s.cfg)
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}

	t.Run("concurrent creates", func(t *testing.T) {
		q, err := s.cfg.Quote(usdc(10_000_000))
		if err != nil {
			t.Fatal(err)
		}
		cust := s.newCustomer(usdc(3 * q.Total.Minor))
		var (
			ok, short atomic.Int32
			wg        sync.WaitGroup
			errs      = make(chan error, 10)
		)
		for range 10 {
			wg.Go(func() {
				_, err := s.tryCreate(cust, usdc(10_000_000))
				switch {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, ErrInsufficientFunds):
					short.Add(1)
				default:
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if ok.Load() != 3 || short.Load() != 7 {
			t.Fatalf("%d succeeded, %d short; want 3 and 7", ok.Load(), short.Load())
		}
		if a := s.available(cust); a.Minor != 0 {
			t.Fatalf("available after 3 payouts = %s", a)
		}
	})
}

func TestConcurrentWorkers(t *testing.T) {
	s := newScenario(t, usdc(1_000_000_000))
	ctx := t.Context()
	const payouts, workers = 20, 4
	ids := make([]uuid.UUID, payouts)
	for i := range ids {
		ids[i] = s.create(usdc(1_000_000)).ID
	}
	now := s.now()
	var (
		total atomic.Int64
		wg    sync.WaitGroup
	)
	for range workers {
		wg.Go(func() {
			n, err := s.worker.ProcessOutbox(ctx, pool, s.sb, now)
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		})
	}
	wg.Wait()
	if total.Load() != payouts {
		t.Fatalf("workers processed %d jobs, want %d", total.Load(), payouts)
	}
	orders, err := rail.Orders(ctx, pool, s.sb, 0)
	if err != nil || len(orders) != payouts {
		t.Fatalf("rail orders = %d, %v", len(orders), err)
	}
	for _, o := range orders {
		if o.SubmitCount != 1 {
			t.Fatalf("order %s submitted %d times", o.Clave, o.SubmitCount)
		}
	}
	for _, id := range ids {
		s.wantStatus(id, StatusSubmitted)
		s.wantEntries(id, ledger.KindPayoutSubmit, 1)
	}
	s.checkInvariants()
	if res := s.tick(now); len(res.Changes) != payouts {
		t.Fatalf("settlement tick = %d changes", len(res.Changes))
	}
}

func TestCancel(t *testing.T) {
	const initial = 1_000_000_000
	s := newScenario(t, usdc(initial))
	ctx := t.Context()
	cancel := func(id uuid.UUID) (Payout, error) {
		var p Payout
		err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
			var err error
			p, err = Cancel(ctx, tx, s.sb, id)
			return err
		})
		return p, err
	}

	p := s.create(hundred)
	got, err := cancel(p.ID)
	if err != nil || got.Status != StatusReleased {
		t.Fatalf("Cancel = %+v, %v", got, err)
	}
	if a := s.available(s.customer); a.Minor != initial {
		t.Fatalf("available after cancel = %s", a)
	}
	s.wantEntries(p.ID, ledger.KindPayoutRelease, 1)
	if _, err := cancel(p.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("second cancel = %v", err)
	}
	now := s.now()
	if res := s.tick(now); res.Processed != 0 {
		t.Fatalf("cancelled payout was processed: %+v", res)
	}
	if orders, _ := rail.Orders(ctx, pool, s.sb, 0); len(orders) != 0 {
		t.Fatalf("cancelled payout reached the rail: %+v", orders)
	}

	sent := s.create(hundred)
	s.tick(s.now())
	if _, err := cancel(sent.ID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("cancel after submit = %v", err)
	}
	s.wantStatus(sent.ID, StatusSubmitted)
	if _, err := cancel(uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing = %v", err)
	}
}

func TestListAndFilters(t *testing.T) {
	s := newScenario(t, usdc(1_000_000_000))
	ctx := t.Context()
	other := s.newCustomer(usdc(1_000_000_000))
	a := s.create(hundred)
	b := s.create(usdc(2_000_000))
	if _, err := s.tryCreate(other, usdc(3_000_000)); err != nil {
		t.Fatal(err)
	}
	s.tick(s.now())

	all := s.payouts()
	if len(all) != 3 {
		t.Fatalf("List = %d payouts", len(all))
	}
	mine, err := List(ctx, pool, s.sb, Filter{CustomerID: s.customer})
	if err != nil || len(mine) != 2 {
		t.Fatalf("List by customer = %d, %v", len(mine), err)
	}
	if _, err := s.tryCreate(s.customer, usdc(1_000_000)); err != nil {
		t.Fatal(err)
	}
	reserved, err := List(ctx, pool, s.sb, Filter{Status: StatusReserved})
	if err != nil || len(reserved) != 1 {
		t.Fatalf("List reserved = %d, %v", len(reserved), err)
	}
	page, err := List(ctx, pool, s.sb, Filter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != reserved[0].ID {
		t.Fatalf("List limit 1 = %+v, %v", page, err)
	}
	older, err := List(ctx, pool, s.sb, Filter{BeforeTime: page[0].CreatedAt})
	if err != nil || len(older) != 3 {
		t.Fatalf("List before = %d, %v", len(older), err)
	}
	if a.Clave == b.Clave {
		t.Fatal("claves repeat")
	}
}
