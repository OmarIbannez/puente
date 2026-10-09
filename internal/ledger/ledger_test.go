package ledger_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

func usdc(minor int64) money.Amount { return money.New(money.USDC, minor) }
func mxnb(minor int64) money.Amount { return money.New(money.MXNB, minor) }
func mxn(minor int64) money.Amount  { return money.New(money.MXN, minor) }

// parties caches one test customer per sandbox.
var parties sync.Map

// party returns the sandbox's test customer, creating it on first use.
func party(t testing.TB, sb uuid.UUID) uuid.UUID {
	t.Helper()
	if id, ok := parties.Load(sb); ok {
		return id.(uuid.UUID)
	}
	c, err := customer.Create(t.Context(), pool, sb, "Test customer")
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := parties.LoadOrStore(sb, c.ID)
	return actual.(uuid.UUID)
}

// withParty assigns the sandbox's test customer to every sub-ledger posting
// of e that has none, so tests can build entries with plain Debit/Credit.
func withParty(t testing.TB, e ledger.Entry) ledger.Entry {
	t.Helper()
	e.Postings = slices.Clone(e.Postings)
	for i, p := range e.Postings {
		if info, ok := ledger.Lookup(p.Account); ok && info.PerParty && p.Party == uuid.Nil && e.SandboxID != uuid.Nil {
			e.Postings[i] = p.For(party(t, e.SandboxID))
		}
	}
	return e
}

// post posts e in its own transaction and fails the test on error.
func post(t *testing.T, e ledger.Entry) (int64, bool) {
	t.Helper()
	e = withParty(t, e)
	var (
		id     int64
		posted bool
	)
	err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		id, posted, err = ledger.Post(t.Context(), tx, e)
		return err
	})
	if err != nil {
		t.Fatalf("post %s: %v", e.Ref, err)
	}
	return id, posted
}

func tryPost(t *testing.T, e ledger.Entry) error {
	t.Helper()
	e = withParty(t, e)
	return store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, _, err := ledger.Post(t.Context(), tx, e)
		return err
	})
}

func assertBalanceViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("commit error = %v, want a postgres error", err)
	}
	if pgErr.Code != "23514" || pgErr.ConstraintName != "postings_balanced" {
		t.Fatalf("commit error = %s %s (%s), want 23514 postings_balanced",
			pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}

func countEntries(t *testing.T, sb uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM journal_entries WHERE sandbox_id = $1", sb).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChart(t *testing.T) {
	accounts := ledger.All()
	if len(accounts) != 8 {
		t.Fatalf("chart has %d accounts, want 8", len(accounts))
	}
	for i := 1; i < len(accounts); i++ {
		if accounts[i-1].Code >= accounts[i].Code {
			t.Fatalf("chart not in code order at %s", accounts[i].Code)
		}
	}
	fx, ok := ledger.Lookup(ledger.FXClearing)
	if !ok || fx.Type != ledger.TypeEquity || !slices.Equal(fx.Assets, money.Assets) {
		t.Fatalf("lookup 3900 = %+v, %v", fx, ok)
	}
	wallet, _ := ledger.Lookup(ledger.HotWallet)
	if !wallet.Holds(money.USDC) || !wallet.Holds(money.MXNB) || wallet.Holds(money.MXN) {
		t.Fatalf("1010 assets = %v", wallet.Assets)
	}
	// Lookup and All return copies.
	wallet.Assets[0] = money.MXN
	if again, _ := ledger.Lookup(ledger.HotWallet); again.Assets[0] != money.USDC {
		t.Fatal("Lookup leaked the chart's asset slice")
	}
	if _, ok := ledger.Lookup(1011); ok {
		t.Fatal("1011 must be unknown")
	}
	if got := ledger.HotWallet.String(); got != "1010 Hot wallet (on-chain)" {
		t.Fatalf("HotWallet.String() = %q", got)
	}
	if got := ledger.Account(9).String(); got != "9" {
		t.Fatalf("Account(9).String() = %q", got)
	}
	signs := map[ledger.AccountType]int64{
		ledger.TypeAsset: 1, ledger.TypeLiability: -1, ledger.TypeEquity: -1, ledger.TypeRevenue: -1,
	}
	for typ, want := range signs {
		if got := typ.NormalSign(); got != want {
			t.Errorf("%s.NormalSign() = %d, want %d", typ, got, want)
		}
	}
	liab, _ := ledger.Lookup(ledger.CustomerBalances)
	if got := liab.Normal(usdc(-5)); got != usdc(5) {
		t.Fatalf("normal balance of a 5 credit in 2010 = %v", got)
	}
	if len(ledger.Kinds()) != 8 {
		t.Fatalf("got %d kinds, want 8", len(ledger.Kinds()))
	}
	for _, k := range ledger.Kinds() {
		if !k.Valid() {
			t.Errorf("%s must be valid", k)
		}
	}
	if ledger.Kind("payment").Valid() {
		t.Fatal("payment must be unknown")
	}
}

func TestValidateErrors(t *testing.T) {
	sb := uuid.New()
	who := uuid.New()
	good := []ledger.Posting{
		ledger.Debit(ledger.HotWallet, usdc(100)), ledger.Credit(ledger.CustomerBalances, usdc(100)).For(who),
	}
	valid := func() ledger.Entry {
		return ledger.Entry{SandboxID: sb, Ref: "deposit:1", Kind: ledger.KindDeposit, Postings: slices.Clone(good)}
	}
	cases := []struct {
		name   string
		mutate func(*ledger.Entry)
		want   error
	}{
		{"unknown kind", func(e *ledger.Entry) { e.Kind = "bogus" }, ledger.ErrUnknownKind},
		{"no sandbox", func(e *ledger.Entry) { e.SandboxID = uuid.Nil }, ledger.ErrMissingField},
		{"empty ref", func(e *ledger.Entry) { e.Ref = "" }, ledger.ErrBadRef},
		{"long ref", func(e *ledger.Entry) { e.Ref = strings.Repeat("x", ledger.MaxRefLen+1) }, ledger.ErrBadRef},
		{"invalid utf8 ref", func(e *ledger.Entry) { e.Ref = "deposit:\xff" }, ledger.ErrBadRef},
		{"incomplete subject", func(e *ledger.Entry) { e.Subject = &ledger.Subject{Type: "payout"} }, ledger.ErrMissingField},
		{"no postings", func(e *ledger.Entry) { e.Postings = nil }, ledger.ErrTooFewPostings},
		{"single posting", func(e *ledger.Entry) { e.Postings = e.Postings[:1] }, ledger.ErrTooFewPostings},
		{"unknown account", func(e *ledger.Entry) { e.Postings[0].Account = 1999 }, ledger.ErrUnknownAccount},
		{"asset not allowed", func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.SPEISettlement, usdc(1)), ledger.Credit(ledger.FXClearing, usdc(1)),
			}
		}, ledger.ErrAssetNotAllowed},
		{"unknown asset", func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.FXClearing, money.New("EUR", 1)), ledger.Credit(ledger.FXClearing, money.New("EUR", 1)),
			}
		}, ledger.ErrAssetNotAllowed},
		{"zero amount", func(e *ledger.Entry) {
			e.Postings = append(e.Postings, ledger.Debit(ledger.HotWallet, usdc(0)))
		}, ledger.ErrZeroAmount},
		{"unbalanced", func(e *ledger.Entry) { e.Postings[1] = ledger.Credit(ledger.CustomerBalances, usdc(99)).For(who) }, ledger.ErrUnbalanced},
		{"party outside the sub-ledger", func(e *ledger.Entry) { e.Postings[0] = e.Postings[0].For(who) }, ledger.ErrParty},
		{"balanced in total, not per asset", func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.FXClearing, usdc(5)), ledger.Credit(ledger.FXClearing, mxn(5)),
			}
		}, ledger.ErrUnbalanced},
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid entry: %v", err)
	}
	maxRef := valid()
	maxRef.Ref = strings.Repeat("é", ledger.MaxRefLen)
	maxRef.Subject = &ledger.Subject{Type: "chain_transfer", ID: "1"}
	if err := maxRef.Validate(); err != nil {
		t.Fatalf("%d-character ref: %v", ledger.MaxRefLen, err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid()
			tc.mutate(&e)
			if err := e.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
			// Post rejects it in Go, before touching the database.
			if err := tryPost(t, e); !errors.Is(err, tc.want) {
				t.Fatalf("Post() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUnbalancedRejectedByDatabase(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID

	// A raw unbalanced insert, bypassing Go, fails at commit.
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH e AS (INSERT INTO journal_entries (sandbox_id, ref, kind)
				VALUES ($1, 'funding', 'funding') RETURNING id)
			INSERT INTO postings (entry_id, sandbox_id, account_code, asset, amount_minor)
			SELECT e.id, $1, a, 'MXN', m FROM e, (VALUES (1100, 10000), (3000, -9999)) AS v(a, m)`, sb)
		return err
	})
	assertBalanceViolation(t, err)

	// A balanced entry posted through Post, then tampered with in the same
	// transaction, is rejected too.
	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		id, _, err := ledger.Post(ctx, tx, ledger.Entry{
			SandboxID: sb, Ref: "funding", Kind: ledger.KindFunding,
			Postings: []ledger.Posting{
				ledger.Debit(ledger.SPEISettlement, mxn(500)), ledger.Credit(ledger.TreasuryEquity, mxn(500)),
			},
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE postings SET amount_minor = 499 WHERE entry_id = $1 AND amount_minor = 500`, id)
		return err
	})
	assertBalanceViolation(t, err)

	// Deleting one leg of a committed entry is rejected.
	id, _ := post(t, ledger.Entry{
		SandboxID: sb, Ref: "funding", Kind: ledger.KindFunding,
		Postings: []ledger.Posting{
			ledger.Debit(ledger.SPEISettlement, mxn(500)), ledger.Credit(ledger.TreasuryEquity, mxn(500)),
		},
	})
	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM postings WHERE entry_id = $1 AND amount_minor > 0`, id)
		return err
	})
	assertBalanceViolation(t, err)

	// Nothing from the failed transactions persisted.
	if n := countEntries(t, sb); n != 1 {
		t.Fatalf("%d entries persisted, want 1", n)
	}

	// The schema also refuses an asset the account may not hold.
	_, err = pool.Exec(ctx, `INSERT INTO postings (entry_id, sandbox_id, account_code, asset, amount_minor)
		VALUES ($1, $2, 1100, 'USDC', 1)`, id, sb)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "postings_account_asset" {
		t.Fatalf("USDC in 1100: %v, want postings_account_asset violation", err)
	}

	// Deleting the sandbox cascades through the balanced entries.
	if err := store.DeleteSandbox(ctx, pool, sb); err != nil {
		t.Fatal(err)
	}
}

func TestPerAssetBalance(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	e := ledger.Entry{
		SandboxID: sb, Ref: "payout:x:submit", Kind: ledger.KindPayoutSubmit,
		Postings: []ledger.Posting{
			ledger.Debit(ledger.FXClearing, usdc(5)), ledger.Credit(ledger.FXClearing, mxn(5)),
		},
	}
	if err := e.Validate(); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("Validate() = %v, want ErrUnbalanced", err)
	}
	if err := tryPost(t, e); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("Post() = %v, want ErrUnbalanced", err)
	}

	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH e AS (INSERT INTO journal_entries (sandbox_id, ref, kind)
				VALUES ($1, 'payout:x:submit', 'payout_submit') RETURNING id)
			INSERT INTO postings (entry_id, sandbox_id, account_code, asset, amount_minor)
			SELECT e.id, $1, 3900, s, m FROM e, (VALUES ('USDC', 5), ('MXN', -5)) AS v(s, m)`, sb)
		return err
	})
	assertBalanceViolation(t, err)
	if n := countEntries(t, sb); n != 0 {
		t.Fatalf("%d entries persisted, want 0", n)
	}

	// Two assets, each balanced on its own, are accepted.
	post(t, ledger.Entry{
		SandboxID: sb, Ref: "payout:y:submit", Kind: ledger.KindPayoutSubmit,
		Postings: []ledger.Posting{
			ledger.Debit(ledger.PayoutsReserved, usdc(5)), ledger.Credit(ledger.FXClearing, usdc(5)),
			ledger.Debit(ledger.FXClearing, mxn(92)), ledger.Credit(ledger.SPEIInFlight, mxn(92)),
		},
	})
}

func TestPostExactlyOnce(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	deposit := ledger.Entry{
		SandboxID: sb, Ref: "deposit:42", Kind: ledger.KindDeposit, Memo: "first",
		Subject: &ledger.Subject{Type: "chain_transfer", ID: "42"},
		Postings: []ledger.Posting{
			ledger.Debit(ledger.HotWallet, usdc(25_000_000)), ledger.Credit(ledger.CustomerBalances, usdc(25_000_000)),
		},
	}
	id, posted := post(t, deposit)
	if !posted {
		t.Fatal("first post: posted = false")
	}

	// A retry with the same content (postings reordered, memo changed) is a no-op.
	retry := deposit
	retry.Memo = "retry"
	retry.Postings = []ledger.Posting{deposit.Postings[1], deposit.Postings[0]}
	again, posted := post(t, retry)
	if posted || again != id {
		t.Fatalf("retry = (%d, %v), want (%d, false)", again, posted, id)
	}

	// The same ref twice in one transaction is also a no-op.
	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		other := deposit
		other.Ref = "deposit:43"
		other = withParty(t, other)
		first, _, err := ledger.Post(ctx, tx, other)
		if err != nil {
			return err
		}
		second, posted, err := ledger.Post(ctx, tx, other)
		if err != nil {
			return err
		}
		if posted || second != first {
			t.Errorf("same-tx retry = (%d, %v), want (%d, false)", second, posted, first)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The same ref with different content is a conflict, not a silent no-op.
	conflicts := map[string]func(*ledger.Entry){
		"amount": func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.HotWallet, usdc(1)), ledger.Credit(ledger.CustomerBalances, usdc(1)),
			}
		},
		"asset": func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.HotWallet, mxnb(25_000_000)), ledger.Credit(ledger.CustomerBalances, mxnb(25_000_000)),
			}
		},
		"extra posting": func(e *ledger.Entry) {
			e.Postings = []ledger.Posting{
				ledger.Debit(ledger.HotWallet, usdc(25_000_000)),
				ledger.Credit(ledger.CustomerBalances, usdc(24_000_000)),
				ledger.Credit(ledger.CustomerBalances, usdc(1_000_000)),
			}
		},
		"kind": func(e *ledger.Entry) { e.Kind = ledger.KindPayoutReserve },
	}
	for name, mutate := range conflicts {
		t.Run(name, func(t *testing.T) {
			e := deposit
			mutate(&e)
			if err := tryPost(t, e); !errors.Is(err, ledger.ErrRefConflict) {
				t.Fatalf("Post() = %v, want ErrRefConflict", err)
			}
		})
	}
	if n := countEntries(t, sb); n != 2 {
		t.Fatalf("%d entries, want 2", n)
	}
	got, err := ledger.EntryByRef(ctx, pool, sb, "deposit:42")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Memo != "first" || len(got.Postings) != 2 {
		t.Fatalf("stored entry = %+v", got)
	}

	// Ten concurrent transactions posting the same ref: exactly one creates it.
	race := ledger.Entry{
		SandboxID: sb, Ref: "payout:7:reserve", Kind: ledger.KindPayoutReserve,
		Subject: &ledger.Subject{Type: "payout", ID: "7"},
		Postings: []ledger.Posting{
			ledger.Debit(ledger.CustomerBalances, usdc(1_000_000)), ledger.Credit(ledger.PayoutsReserved, usdc(1_000_000)),
		},
	}
	race = withParty(t, race)
	const workers = 10
	var (
		wg      sync.WaitGroup
		ids     [workers]int64
		created [workers]bool
		errs    [workers]error
	)
	start := make(chan struct{})
	for i := range workers {
		wg.Go(func() {
			<-start
			errs[i] = store.InTx(ctx, pool, func(tx pgx.Tx) error {
				var err error
				ids[i], created[i], err = ledger.Post(ctx, tx, race)
				return err
			})
		})
	}
	close(start)
	wg.Wait()
	var winners int
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("worker %d got id %d, worker 0 got %d", i, ids[i], ids[0])
		}
		if created[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d workers created the entry, want 1", winners)
	}
	var postings int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM postings WHERE entry_id = $1", ids[0]).Scan(&postings); err != nil {
		t.Fatal(err)
	}
	if postings != 2 {
		t.Fatalf("%d postings for the raced entry, want 2", postings)
	}

	// Refs are per sandbox: another sandbox may reuse one.
	elsewhere := deposit
	elsewhere.SandboxID = storetest.Sandbox(t, pool).ID
	if otherID, posted := post(t, elsewhere); !posted || otherID == id {
		t.Fatalf("other sandbox = (%d, %v), want a new entry", otherID, posted)
	}
}

// Amounts of the realistic sequence.
const (
	prefund     = 100_000_000 // $1,000,000.00 MXN
	deposited   = 100_000_000 // 100 USDC
	depositedNB = 40_000_000  // 40 MXNB, later reorged away
	payoutAmt   = 50_000_000  // 50 USDC
	payoutFee   = 500_000     // 0.5 USDC
	rate        = money.Rate(18_452_300)
)

// payoutEntries returns the entries of one USDC payout that is liquidated and
// then returned, with the MXN amount the conversion produced.
func payoutEntries(t *testing.T, sb uuid.UUID, payout string) ([]ledger.Entry, int64) {
	t.Helper()
	conv, err := money.Convert(usdc(payoutAmt), rate, money.MXN)
	if err != nil {
		t.Fatal(err)
	}
	pesos := conv.Out
	subject := &ledger.Subject{Type: "payout", ID: payout}
	entry := func(step string, kind ledger.Kind, ps ...ledger.Posting) ledger.Entry {
		return ledger.Entry{
			SandboxID: sb, Ref: "payout:" + payout + ":" + step, Kind: kind, Subject: subject, Postings: ps,
		}
	}
	return []ledger.Entry{
		entry("reserve", ledger.KindPayoutReserve,
			ledger.Debit(ledger.CustomerBalances, usdc(payoutAmt+payoutFee)),
			ledger.Credit(ledger.PayoutsReserved, usdc(payoutAmt+payoutFee))),
		entry("submit", ledger.KindPayoutSubmit,
			ledger.Debit(ledger.PayoutsReserved, usdc(payoutAmt+payoutFee)),
			ledger.Credit(ledger.FeeIncome, usdc(payoutFee)),
			ledger.Credit(ledger.FXClearing, usdc(payoutAmt)),
			ledger.Debit(ledger.FXClearing, pesos),
			ledger.Credit(ledger.SPEIInFlight, pesos)),
		entry("settle", ledger.KindPayoutSettle,
			ledger.Debit(ledger.SPEIInFlight, pesos), ledger.Credit(ledger.SPEISettlement, pesos)),
		entry("return", ledger.KindPayoutReturn,
			ledger.Debit(ledger.SPEISettlement, pesos), ledger.Credit(ledger.FXClearing, pesos),
			ledger.Debit(ledger.FXClearing, usdc(payoutAmt)), ledger.Credit(ledger.CustomerBalances, usdc(payoutAmt))),
	}, pesos.Minor
}

// seed posts a realistic sequence: MXN funding, a USDC deposit, an MXNB
// deposit reversed by a reorg, a payout that is liquidated and returned, and
// a second payout that is released before submission.
func seed(t *testing.T, sb uuid.UUID) (pesos int64) {
	t.Helper()
	entries := []ledger.Entry{
		{
			SandboxID: sb, Ref: "funding", Kind: ledger.KindFunding, Memo: "Treasury prefunding",
			Postings: []ledger.Posting{
				ledger.Debit(ledger.SPEISettlement, mxn(prefund)), ledger.Credit(ledger.TreasuryEquity, mxn(prefund)),
			},
		},
		{
			SandboxID: sb, Ref: "deposit:1", Kind: ledger.KindDeposit,
			Subject: &ledger.Subject{Type: "chain_transfer", ID: "1"},
			Postings: []ledger.Posting{
				ledger.Debit(ledger.HotWallet, usdc(deposited)), ledger.Credit(ledger.CustomerBalances, usdc(deposited)),
			},
		},
		{
			SandboxID: sb, Ref: "deposit:2", Kind: ledger.KindDeposit,
			Subject: &ledger.Subject{Type: "chain_transfer", ID: "2"},
			Postings: []ledger.Posting{
				ledger.Debit(ledger.HotWallet, mxnb(depositedNB)), ledger.Credit(ledger.CustomerBalances, mxnb(depositedNB)),
			},
		},
		{
			SandboxID: sb, Ref: "deposit_reversal:2", Kind: ledger.KindDepositReversal,
			Subject: &ledger.Subject{Type: "chain_transfer", ID: "2"},
			Postings: []ledger.Posting{
				ledger.Debit(ledger.CustomerBalances, mxnb(depositedNB)), ledger.Credit(ledger.HotWallet, mxnb(depositedNB)),
			},
		},
	}
	p1, pesos := payoutEntries(t, sb, "p1")
	entries = append(entries, p1...)
	released := []ledger.Posting{
		ledger.Debit(ledger.CustomerBalances, usdc(10_100_000)), ledger.Credit(ledger.PayoutsReserved, usdc(10_100_000)),
	}
	p2 := &ledger.Subject{Type: "payout", ID: "p2"}
	entries = append(entries,
		ledger.Entry{SandboxID: sb, Ref: "payout:p2:reserve", Kind: ledger.KindPayoutReserve, Subject: p2, Postings: released},
		ledger.Entry{
			SandboxID: sb, Ref: "payout:p2:release", Kind: ledger.KindPayoutRelease, Subject: p2,
			Postings: []ledger.Posting{
				ledger.Debit(ledger.PayoutsReserved, usdc(10_100_000)), ledger.Credit(ledger.CustomerBalances, usdc(10_100_000)),
			},
		},
	)
	for _, e := range entries {
		if _, posted := post(t, e); !posted {
			t.Fatalf("%s: posted = false", e.Ref)
		}
	}
	return pesos
}

func TestTrialBalanceZero(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	empty := storetest.Sandbox(t, pool).ID
	pesos := seed(t, sb)
	if pesos != 92_261 { // 50 USDC × 18.4523 = 922.615 MXN, rounded toward zero
		t.Fatalf("converted payout = %d centavos, want 92261", pesos)
	}

	for _, s := range []uuid.UUID{sb, empty} {
		tb, err := ledger.TrialBalance(ctx, pool, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(tb) != len(money.Assets) {
			t.Fatalf("trial balance has %d assets, want %d", len(tb), len(money.Assets))
		}
		for asset, sum := range tb {
			if sum != 0 {
				t.Errorf("trial balance %s = %d, want 0", asset, sum)
			}
		}
	}

	// Every entry, read back, sums to zero per asset.
	entries, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{Limit: ledger.MaxEntriesLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("%d entries, want 10", len(entries))
	}
	for _, e := range entries {
		sums := map[money.Asset]int64{}
		for _, p := range e.Postings {
			sums[p.Amount.Asset] += p.Amount.Minor
		}
		for asset, s := range sums {
			if s != 0 {
				t.Errorf("entry %s: %s sums to %d", e.Ref, asset, s)
			}
		}
	}
}

func TestBalances(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	pesos := seed(t, sb)

	type key struct {
		account ledger.Account
		asset   money.Asset
	}
	// Display (normal-sign) balances after the sequence.
	want := []struct {
		key
		balance int64
	}{
		{key{ledger.HotWallet, money.USDC}, deposited},
		{key{ledger.HotWallet, money.MXNB}, 0},
		{key{ledger.SPEISettlement, money.MXN}, prefund},
		{key{ledger.CustomerBalances, money.USDC}, deposited - payoutFee}, // the returned payout keeps its fee
		{key{ledger.CustomerBalances, money.MXNB}, 0},
		{key{ledger.PayoutsReserved, money.USDC}, 0},
		{key{ledger.SPEIInFlight, money.MXN}, 0},
		{key{ledger.TreasuryEquity, money.MXN}, prefund},
		{key{ledger.FXClearing, money.USDC}, 0},
		{key{ledger.FXClearing, money.MXN}, 0},
		{key{ledger.FeeIncome, money.USDC}, payoutFee},
	}
	balances, err := ledger.Balances(ctx, pool, sb)
	if err != nil {
		t.Fatal(err)
	}
	if len(balances) != len(want) {
		t.Fatalf("got %d balances, want %d: %+v", len(balances), len(want), balances)
	}
	for i, b := range balances {
		w := want[i]
		if b.Code != w.account || b.Balance.Asset != w.asset || b.Balance.Minor != w.balance {
			t.Errorf("balance %d = %s %s %d, want %s %s %d",
				i, b.Code, b.Balance.Asset, b.Balance.Minor, w.account, w.asset, w.balance)
		}
		if b.Net.Asset != b.Balance.Asset || b.Balance.Minor != b.Net.Minor*b.Type.NormalSign() {
			t.Errorf("%s: balance %v inconsistent with net %v", b.Code, b.Balance, b.Net)
		}
	}

	// Balance returns the raw debit-positive sum.
	cases := []struct {
		account ledger.Account
		asset   money.Asset
		want    int64
	}{
		{ledger.HotWallet, money.USDC, deposited},
		{ledger.CustomerBalances, money.USDC, -(deposited - payoutFee)},
		{ledger.FeeIncome, money.USDC, -payoutFee},
		{ledger.FeeIncome, money.MXNB, 0},
		{ledger.SPEISettlement, money.MXN, prefund},
	}
	for _, c := range cases {
		got, err := ledger.Balance(ctx, pool, sb, c.account, c.asset)
		if err != nil {
			t.Fatal(err)
		}
		if got != money.New(c.asset, c.want) {
			t.Errorf("Balance(%s, %s) = %v, want %d", c.account, c.asset, got, c.want)
		}
	}
	if _, err := ledger.Balance(ctx, pool, sb, 1999, money.MXN); !errors.Is(err, ledger.ErrUnknownAccount) {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := ledger.Balance(ctx, pool, sb, ledger.HotWallet, money.MXN); !errors.Is(err, ledger.ErrAssetNotAllowed) {
		t.Fatalf("MXN in hot wallet: %v", err)
	}

	// Mid-flight: while a payout is submitted but not settled, 2100 holds it.
	p3, _ := payoutEntries(t, sb, "p3")
	for _, e := range p3[:2] {
		post(t, e)
	}
	inFlight, err := ledger.Balance(ctx, pool, sb, ledger.SPEIInFlight, money.MXN)
	if err != nil {
		t.Fatal(err)
	}
	if inFlight != mxn(-pesos) {
		t.Fatalf("2100 in flight = %v, want %d credit", inFlight, pesos)
	}

	// Another sandbox sees nothing.
	none, err := ledger.Balances(ctx, pool, storetest.Sandbox(t, pool).ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("empty sandbox has balances %+v", none)
	}
}

func TestEntries(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	seed(t, sb)

	all, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 10 {
		t.Fatalf("%d entries, want 10", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID <= all[i].ID {
			t.Fatalf("entries not newest first at %d", i)
		}
	}
	if newest := all[0]; newest.Ref != "payout:p2:release" || newest.Kind != ledger.KindPayoutRelease ||
		newest.Subject == nil || *newest.Subject != (ledger.Subject{Type: "payout", ID: "p2"}) {
		t.Fatalf("newest entry = %+v", newest)
	}
	funding := all[len(all)-1]
	if funding.Ref != "funding" || funding.Memo != "Treasury prefunding" || funding.Subject != nil ||
		funding.SandboxID != sb || funding.CreatedAt.IsZero() ||
		!slices.Equal(funding.Postings, []ledger.Posting{
			ledger.Debit(ledger.SPEISettlement, mxn(prefund)), ledger.Credit(ledger.TreasuryEquity, mxn(prefund)),
		}) {
		t.Fatalf("funding entry = %+v", funding)
	}

	// Subject filter: payout p1's four entries, with postings in insertion order.
	p1, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{SubjectType: "payout", SubjectID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	wantRefs := []string{"payout:p1:return", "payout:p1:settle", "payout:p1:submit", "payout:p1:reserve"}
	if refs := entryRefs(p1); !slices.Equal(refs, wantRefs) {
		t.Fatalf("payout p1 entries = %v, want %v", refs, wantRefs)
	}
	expected, _ := payoutEntries(t, sb, "p1")
	if !slices.Equal(p1[2].Postings, withParty(t, expected[1]).Postings) {
		t.Fatalf("submit postings = %+v, want %+v", p1[2].Postings, expected[1].Postings)
	}

	// Subject type alone, and kind.
	transfers, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{SubjectType: "chain_transfer"})
	if err != nil {
		t.Fatal(err)
	}
	if refs := entryRefs(transfers); !slices.Equal(refs, []string{"deposit_reversal:2", "deposit:2", "deposit:1"}) {
		t.Fatalf("chain transfer entries = %v", refs)
	}
	reserves, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{Kind: ledger.KindPayoutReserve})
	if err != nil {
		t.Fatal(err)
	}
	if refs := entryRefs(reserves); !slices.Equal(refs, []string{"payout:p2:reserve", "payout:p1:reserve"}) {
		t.Fatalf("reserve entries = %v", refs)
	}

	// Pagination with Limit and BeforeID walks every entry exactly once.
	var (
		paged  []string
		before int64
		pages  int
	)
	for {
		page, err := ledger.Entries(ctx, pool, sb, ledger.EntryFilter{Limit: 3, BeforeID: before})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 3 {
			t.Fatalf("page of %d, limit 3", len(page))
		}
		for _, e := range page {
			if len(e.Postings) < 2 {
				t.Fatalf("entry %s has %d postings", e.Ref, len(e.Postings))
			}
		}
		paged = append(paged, entryRefs(page)...)
		before = page[len(page)-1].ID
		pages++
	}
	if pages != 4 || !slices.Equal(paged, entryRefs(all)) {
		t.Fatalf("%d pages: %v, want 4 pages of %v", pages, paged, entryRefs(all))
	}

	// EntryByRef.
	got, err := ledger.EntryByRef(ctx, pool, sb, "payout:p1:submit")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p1[2].ID || !slices.Equal(got.Postings, p1[2].Postings) {
		t.Fatalf("EntryByRef = %+v, want %+v", got, p1[2])
	}
	if _, err := ledger.EntryByRef(ctx, pool, sb, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing ref: %v, want store.ErrNotFound", err)
	}

	// Another sandbox sees nothing.
	none, err := ledger.Entries(ctx, pool, storetest.Sandbox(t, pool).ID, ledger.EntryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("empty sandbox has entries %v", entryRefs(none))
	}
}

func entryRefs(entries []ledger.PostedEntry) []string {
	refs := make([]string, len(entries))
	for i, e := range entries {
		refs[i] = e.Ref
	}
	return refs
}

// TestSchemaAcceptsChart posts every allowed (account, asset) pair and every
// kind, so that the database CHECK constraints cannot fall behind the Go
// chart and kinds.
func TestSchemaAcceptsChart(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	n := 0
	for _, a := range ledger.All() {
		for _, asset := range a.Assets {
			n++
			post(t, ledger.Entry{
				SandboxID: sb, Ref: "pair:" + a.Code.String() + ":" + string(asset), Kind: ledger.KindFunding,
				Postings: []ledger.Posting{
					ledger.Debit(a.Code, money.New(asset, 1)), ledger.Credit(ledger.FXClearing, money.New(asset, 1)),
				},
			})
		}
	}
	for _, k := range ledger.Kinds() {
		n++
		post(t, ledger.Entry{
			SandboxID: sb, Ref: "kind:" + string(k), Kind: k,
			Postings: []ledger.Posting{
				ledger.Debit(ledger.SPEISettlement, mxn(1)), ledger.Credit(ledger.TreasuryEquity, mxn(1)),
			},
		})
	}
	if got := countEntries(t, sb); got != n {
		t.Fatalf("%d entries, want %d", got, n)
	}
}

func TestPartySubLedger(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	alice, err := customer.Create(ctx, pool, sb, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := customer.Create(ctx, pool, sb, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	deposit := func(ref string, who uuid.UUID, minor int64) ledger.Entry {
		return ledger.Entry{SandboxID: sb, Ref: ref, Kind: ledger.KindDeposit, Postings: []ledger.Posting{
			ledger.Debit(ledger.HotWallet, usdc(minor)), ledger.Credit(ledger.CustomerBalances, usdc(minor)).For(who),
		}}
	}
	post(t, deposit("deposit:a", alice.ID, 7_000_000))
	post(t, deposit("deposit:b", bob.ID, 3_000_000))

	for who, want := range map[uuid.UUID]int64{alice.ID: -7_000_000, bob.ID: -3_000_000} {
		got, err := ledger.PartyBalance(ctx, pool, sb, who, ledger.CustomerBalances, money.USDC)
		if err != nil || got.Minor != want {
			t.Errorf("PartyBalance(%s) = %v, %v; want %d", who, got, err, want)
		}
	}
	if _, err := ledger.PartyBalance(ctx, pool, sb, alice.ID, ledger.HotWallet, money.USDC); !errors.Is(err, ledger.ErrParty) {
		t.Errorf("PartyBalance on a non-sub-ledger account = %v, want ErrParty", err)
	}

	// Retrying with another party is a different event, not a replay.
	if err := tryPost(t, deposit("deposit:a", bob.ID, 7_000_000)); !errors.Is(err, ledger.ErrRefConflict) {
		t.Errorf("same ref, other party = %v, want ErrRefConflict", err)
	}
	// A missing party is rejected in Go...
	missing := deposit("deposit:c", alice.ID, 1)
	missing.Postings[1].Party = uuid.Nil
	if err := missing.Validate(); !errors.Is(err, ledger.ErrParty) {
		t.Errorf("Validate without party = %v, want ErrParty", err)
	}
	// ...and by the database, as is a party from another sandbox.
	other := storetest.Sandbox(t, pool).ID
	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH e AS (INSERT INTO journal_entries (sandbox_id, ref, kind)
			VALUES ($1, 'raw', 'deposit') RETURNING id)
			INSERT INTO postings (entry_id, sandbox_id, account_code, asset, amount_minor)
			SELECT id, $1, 2010, 'USDC', v FROM e, unnest(ARRAY[-1, 1]) AS v`, sb)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "postings_party") {
		t.Errorf("raw sub-ledger posting without party = %v, want postings_party violation", err)
	}
	foreign := deposit("deposit:x", alice.ID, 1)
	foreign.SandboxID = other
	if err := tryPost(t, foreign); err == nil || !strings.Contains(err.Error(), "postings_party_fk") {
		t.Errorf("party from another sandbox = %v, want postings_party_fk violation", err)
	}
}
