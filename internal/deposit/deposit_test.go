package deposit_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/sim"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

const (
	sender   = "0x00000000000000000000000000000000000a11ce"
	stranger = "0x0000000000000000000000000000000000000bbb"
	// confs is the confirmation depth the tests scan with.
	confs = 3
)

var (
	simUSDC, _  = chain.NetworkByKey(chain.KeySimUSDC)
	baseUSDC, _ = chain.NetworkByKey(chain.KeyBaseSepoliaUSDC)
)

func usdc(minor int64) money.Amount { return money.New(money.USDC, minor) }

func inTx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := store.InTx(t.Context(), pool, fn); err != nil {
		t.Fatal(err)
	}
}

// world is a fresh sandbox with one customer holding a deposit address on
// net.
type world struct {
	sb      uuid.UUID
	cust    customer.Customer
	address string
}

func newWorld(t *testing.T, net chain.Network) world {
	t.Helper()
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	c, err := customer.Create(ctx, pool, sb, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	a, err := customer.AssignAddress(ctx, pool, sb, c.ID, net.Key, customer.SimAddress(sb, c.ID, net.Key))
	if err != nil {
		t.Fatal(err)
	}
	return world{sb: sb, cust: c, address: a.Address}
}

func (w world) deposit(t *testing.T, to string, amount money.Amount) chain.Transfer {
	t.Helper()
	var tr chain.Transfer
	inTx(t, func(tx pgx.Tx) error {
		var err error
		tr, err = sim.New(tx, w.sb, simUSDC).Deposit(t.Context(), tx, sender, to, amount)
		return err
	})
	return tr
}

func (w world) mine(t *testing.T, n int) {
	t.Helper()
	inTx(t, func(tx pgx.Tx) error {
		_, err := sim.New(tx, w.sb, simUSDC).Mine(t.Context(), tx, n)
		return err
	})
}

func (w world) faults(t *testing.T, f store.Faults) {
	t.Helper()
	if _, err := store.SetFaults(t.Context(), pool, w.sb, f); err != nil {
		t.Fatal(err)
	}
}

// sync scans the simulated chain for every recipient and credits.
func (w world) sync(t *testing.T) deposit.SyncResult {
	t.Helper()
	return w.syncFrom(t, simUSDC, sim.New(pool, w.sb, simUSDC))
}

func (w world) syncFrom(t *testing.T, net chain.Network, src chain.Source) deposit.SyncResult {
	t.Helper()
	res, err := deposit.Sync(t.Context(), pool, w.sb, net, src, nil, confs)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// balance is the customer's raw 2010 balance (credits negative).
func (w world) balance(t *testing.T) int64 {
	t.Helper()
	b, err := ledger.PartyBalance(t.Context(), pool, w.sb, w.cust.ID, ledger.CustomerBalances, money.USDC)
	if err != nil {
		t.Fatal(err)
	}
	return b.Minor
}

// refs returns the sandbox's journal entry refs in posting order.
func (w world) refs(t *testing.T) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT ref FROM journal_entries WHERE sandbox_id = $1 ORDER BY id", w.sb)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func (w world) checkTrialBalance(t *testing.T) {
	t.Helper()
	tb, err := ledger.TrialBalance(t.Context(), pool, w.sb)
	if err != nil {
		t.Fatal(err)
	}
	for asset, sum := range tb {
		if sum != 0 {
			t.Errorf("trial balance %s = %d", asset, sum)
		}
	}
}

func (w world) transfers(t *testing.T, net string) []chain.Record {
	t.Helper()
	recs, err := chain.Transfers(t.Context(), pool, w.sb, chain.TransferFilter{Network: net})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(recs) // oldest first
	return recs
}

func (w world) list(t *testing.T, f deposit.Filter) []deposit.View {
	t.Helper()
	views, err := deposit.List(t.Context(), pool, w.sb, f)
	if err != nil {
		t.Fatal(err)
	}
	return views
}

func TestHappyPath(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.deposit(t, w.address, usdc(25_000_000))
	w.mine(t, confs-1)

	res := w.sync(t)
	if res.Scan.Confirmed != 1 || res.Credit != (deposit.Result{Credited: 1}) {
		t.Fatalf("sync = %+v", res)
	}
	if got := w.balance(t); got != -25_000_000 {
		t.Fatalf("2010 balance = %d, want -25000000", got)
	}
	hot, err := ledger.Balance(t.Context(), pool, w.sb, ledger.HotWallet, money.USDC)
	if err != nil || hot.Minor != 25_000_000 {
		t.Fatalf("1010 balance = %v, %v", hot, err)
	}
	w.checkTrialBalance(t)
	id := w.transfers(t, simUSDC.Key)[0].ID
	if refs := w.refs(t); !slices.Equal(refs, []string{deposit.CreditRef(id, 1)}) {
		t.Fatalf("refs = %v", refs)
	}
	entry, err := ledger.EntryByRef(t.Context(), pool, w.sb, deposit.CreditRef(id, 1))
	if err != nil || entry.Kind != ledger.KindDeposit || entry.Subject == nil || entry.Subject.Type != deposit.SubjectType {
		t.Fatalf("entry = %+v, %v", entry, err)
	}

	// Syncing again (and crediting again) changes nothing.
	if res := w.sync(t); res.Credit != (deposit.Result{}) {
		t.Fatalf("second sync = %+v", res)
	}
	inTx(t, func(tx pgx.Tx) error {
		res, err := deposit.Credit(t.Context(), tx, w.sb, simUSDC)
		if err == nil && res != (deposit.Result{}) {
			t.Errorf("third credit = %+v", res)
		}
		return err
	})
	if got := w.balance(t); got != -25_000_000 || len(w.refs(t)) != 1 {
		t.Fatalf("after resync: balance %d, refs %v", got, w.refs(t))
	}
	if refs := w.refs(t); refs[0] != "deposit:"+itoa(id)+":1" {
		t.Fatalf("ref format = %s", refs[0])
	}
}

func TestNotCreditedBeforeConfirmations(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.deposit(t, w.address, usdc(1_000_000))
	w.mine(t, confs-2) // one confirmation short

	if res := w.sync(t); res.Scan.New != 1 || res.Credit != (deposit.Result{}) {
		t.Fatalf("early sync = %+v", res)
	}
	if got := w.balance(t); got != 0 {
		t.Fatalf("balance before confirmation = %d", got)
	}
	if v := w.list(t, deposit.Filter{}); len(v) != 1 || v[0].State != deposit.StatePending || v[0].Status != chain.StatusSeen {
		t.Fatalf("list = %+v", v)
	}

	w.mine(t, 1)
	if res := w.sync(t); res.Credit.Credited != 1 {
		t.Fatalf("sync after confirmation = %+v", res)
	}
	if got := w.balance(t); got != -1_000_000 {
		t.Fatalf("balance = %d", got)
	}
}

func TestReorgReversesCredit(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.deposit(t, w.address, usdc(7_000_000))
	w.mine(t, confs-1)
	if res := w.sync(t); res.Credit.Credited != 1 {
		t.Fatalf("sync = %+v", res)
	}

	w.faults(t, store.Faults{Reorg: true})
	w.mine(t, 1)
	res := w.sync(t)
	if res.Scan.Orphaned != 1 || res.Credit != (deposit.Result{Reversed: 1}) {
		t.Fatalf("sync after reorg = %+v", res)
	}
	if got := w.balance(t); got != 0 {
		t.Fatalf("balance after reversal = %d", got)
	}
	w.checkTrialBalance(t)
	id := w.transfers(t, simUSDC.Key)[0].ID
	want := []string{"deposit:" + itoa(id) + ":1", "deposit_reversal:" + itoa(id) + ":1"}
	if refs := w.refs(t); !slices.Equal(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	if v := w.list(t, deposit.Filter{}); len(v) != 1 || v[0].State != deposit.StateReversed || v[0].Generation != 1 {
		t.Fatalf("list = %+v", v)
	}
	// The reversal is not repeated.
	if res := w.sync(t); res.Credit != (deposit.Result{}) {
		t.Fatalf("resync = %+v", res)
	}
}

// The simulated chain never re-includes a reorganized transaction, so the
// revival path runs against an in-memory source through chain.Scan.
func TestRevivedTransferCreditedAgain(t *testing.T) {
	w := newWorld(t, simUSDC)
	tr := chain.Transfer{
		ChainID: simUSDC.ChainID, Token: simUSDC.Token, TxHash: hash32('e'),
		BlockNumber: 2, BlockHash: hash32('2'), From: sender, To: w.address, Amount: usdc(4_000_000),
	}
	src := &fakeSource{
		chainID: simUSDC.ChainID,
		head:    4,
		hashes:  map[uint64]string{0: hash32('0'), 1: hash32('1'), 2: hash32('2'), 3: hash32('3'), 4: hash32('4')},
		logs:    []chain.Transfer{tr},
	}
	if res := w.syncFrom(t, simUSDC, src); res.Credit.Credited != 1 {
		t.Fatalf("first sync = %+v", res)
	}

	// Blocks 2..4 are replaced and the transaction is gone from the feed.
	src.hashes[2], src.hashes[3], src.hashes[4] = hash32('a'), hash32('b'), hash32('c')
	src.logs = nil
	if res := w.syncFrom(t, simUSDC, src); res.Scan.Orphaned != 1 || res.Credit.Reversed != 1 {
		t.Fatalf("sync after reorg = %+v", res)
	}
	if got := w.balance(t); got != 0 {
		t.Fatalf("balance after reversal = %d", got)
	}

	// The transaction is re-included in the new block 5: revived as seen,
	// not yet credited.
	tr.BlockNumber, tr.BlockHash = 5, hash32('5')
	src.head, src.hashes[5], src.logs = 5, hash32('5'), []chain.Transfer{tr}
	res := w.syncFrom(t, simUSDC, src)
	if res.Scan.Revived != 1 || res.Credit != (deposit.Result{}) {
		t.Fatalf("sync after re-inclusion = %+v", res)
	}
	if v := w.list(t, deposit.Filter{}); len(v) != 1 || v[0].State != deposit.StatePending || v[0].Generation != 1 {
		t.Fatalf("revived list = %+v", v)
	}

	// Two more blocks confirm it, and generation 2 is credited.
	src.head, src.hashes[6], src.hashes[7] = 7, hash32('6'), hash32('7')
	if res := w.syncFrom(t, simUSDC, src); res.Scan.Confirmed != 1 || res.Credit != (deposit.Result{Credited: 1}) {
		t.Fatalf("sync after reconfirmation = %+v", res)
	}
	if got := w.balance(t); got != -4_000_000 {
		t.Fatalf("balance after re-credit = %d", got)
	}
	w.checkTrialBalance(t)
	id := w.transfers(t, simUSDC.Key)[0].ID
	want := []string{deposit.CreditRef(id, 1), deposit.ReversalRef(id, 1), deposit.CreditRef(id, 2)}
	if refs := w.refs(t); !slices.Equal(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	if v := w.list(t, deposit.Filter{}); v[0].State != deposit.StateCredited || v[0].Generation != 2 {
		t.Fatalf("re-credited list = %+v", v)
	}

	// Orphaned again: generation 2 is reversed.
	src.hashes[5], src.logs = hash32('f'), nil
	if res := w.syncFrom(t, simUSDC, src); res.Credit != (deposit.Result{Reversed: 1}) {
		t.Fatalf("second reorg = %+v", res)
	}
	if refs := w.refs(t); refs[len(refs)-1] != deposit.ReversalRef(id, 2) || w.balance(t) != 0 {
		t.Fatalf("refs = %v, balance %d", refs, w.balance(t))
	}
}

// A duplicated feed log becomes a second transfer row, and both are
// credited: the double credit is what reconciliation must catch.
func TestDuplicateLogCreditedTwice(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.faults(t, store.Faults{DuplicateLog: true})
	w.deposit(t, w.address, usdc(3_000_000))
	w.mine(t, confs-1)

	if res := w.sync(t); res.Scan.New != 2 || res.Credit.Credited != 2 {
		t.Fatalf("sync = %+v", res)
	}
	recs := w.transfers(t, simUSDC.Key)
	if len(recs) != 2 || recs[0].TxHash != recs[1].TxHash || recs[0].LogIndex != 0 || recs[1].LogIndex != 1 {
		t.Fatalf("transfers = %+v", recs)
	}
	if got := w.balance(t); got != -6_000_000 {
		t.Fatalf("balance = %d, want the double credit -6000000", got)
	}
	want := []string{deposit.CreditRef(recs[0].ID, 1), deposit.CreditRef(recs[1].ID, 1)}
	if refs := w.refs(t); !slices.Equal(refs, want) {
		t.Fatalf("refs = %v", refs)
	}
}

// A short deposit is credited with the amount the feed reported, not the
// canonical one: reconciliation reports the mismatch.
func TestShortDepositCreditedWithFeedAmount(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.faults(t, store.Faults{ShortDeposit: true})
	tr := w.deposit(t, w.address, usdc(10_000_000))
	w.mine(t, confs-1)

	if res := w.sync(t); res.Credit.Credited != 1 {
		t.Fatalf("sync = %+v", res)
	}
	if got := w.balance(t); got != -10_000_000 {
		t.Fatalf("balance = %d, want the feed amount", got)
	}
	canonical, ok, err := sim.New(pool, w.sb, simUSDC).Lookup(t.Context(), tr.TxHash, 0)
	if err != nil || !ok || canonical.Amount.Minor != 9_900_000 {
		t.Fatalf("canonical = %+v, %v, %v", canonical, ok, err)
	}
}

func TestUnattributedNotCredited(t *testing.T) {
	w := newWorld(t, simUSDC)
	w.deposit(t, stranger, usdc(2_000_000))
	w.deposit(t, w.address, usdc(1_000_000))
	w.mine(t, confs-1)

	if res := w.sync(t); res.Credit != (deposit.Result{Credited: 1, Unattributed: 1}) {
		t.Fatalf("sync = %+v", res)
	}
	if got := w.balance(t); got != -1_000_000 {
		t.Fatalf("balance = %d", got)
	}
	if refs := w.refs(t); len(refs) != 1 {
		t.Fatalf("refs = %v", refs)
	}
	// Still counted, still not credited.
	if res := w.sync(t); res.Credit != (deposit.Result{Unattributed: 1}) {
		t.Fatalf("resync = %+v", res)
	}
}

func TestWatchAddresses(t *testing.T) {
	w := newWorld(t, simUSDC)
	ctx := t.Context()
	other, err := customer.Create(ctx, pool, w.sb, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := customer.AssignAddress(ctx, pool, w.sb, other.ID, chain.KeySimMXNB,
		customer.SimAddress(w.sb, other.ID, chain.KeySimMXNB)); err != nil {
		t.Fatal(err)
	}
	watch, err := deposit.WatchAddresses(ctx, pool, w.sb, simUSDC)
	if err != nil || !slices.Equal(watch, []string{w.address}) {
		t.Fatalf("WatchAddresses = %v, %v", watch, err)
	}

	// Scanning with the watch list ignores transfers to other addresses.
	w.deposit(t, stranger, usdc(2_000_000))
	w.deposit(t, w.address, usdc(1_000_000))
	w.mine(t, confs-1)
	res, err := deposit.Sync(ctx, pool, w.sb, simUSDC, sim.New(pool, w.sb, simUSDC), watch, confs)
	if err != nil || res.Scan.New != 1 || res.Credit != (deposit.Result{Credited: 1}) {
		t.Fatalf("Sync = %+v, %v", res, err)
	}
}

func TestConcurrentCreditCreditsOnce(t *testing.T) {
	w := newWorld(t, simUSDC)
	const deposits = 4
	for i := range deposits {
		w.deposit(t, w.address, usdc(int64(i+1)*1_000_000))
	}
	w.deposit(t, stranger, usdc(500_000))
	w.mine(t, confs-1)
	inTx(t, func(tx pgx.Tx) error {
		_, err := chain.Scan(t.Context(), tx, w.sb, simUSDC, sim.New(tx, w.sb, simUSDC), nil, confs)
		return err
	})

	const workers = 5
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		total   deposit.Result
		errs    []error
		results = make(chan deposit.Result, workers)
	)
	ctx := t.Context()
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
				res, err := deposit.Credit(ctx, tx, w.sb, simUSDC)
				if err == nil {
					results <- res
				}
				return err
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	for r := range results {
		total.Credited += r.Credited
		total.Reversed += r.Reversed
	}
	if total.Credited != deposits || total.Reversed != 0 {
		t.Fatalf("concurrent credits = %+v, want %d credited", total, deposits)
	}
	if got := w.balance(t); got != -10_000_000 {
		t.Fatalf("balance = %d, want -10000000", got)
	}
	if refs := w.refs(t); len(refs) != deposits {
		t.Fatalf("refs = %v", refs)
	}
	var credits int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM deposit_credits WHERE sandbox_id = $1", w.sb).Scan(&credits); err != nil || credits != deposits {
		t.Fatalf("credit rows = %d, %v", credits, err)
	}
	w.checkTrialBalance(t)
}

func TestList(t *testing.T) {
	w := newWorld(t, simUSDC)
	// 1: credited, 2: unattributed, 3: reversed by the reorg below.
	w.deposit(t, w.address, usdc(1_000_000))
	w.deposit(t, stranger, usdc(2_000_000))
	w.mine(t, confs-1)
	w.sync(t)
	w.deposit(t, w.address, usdc(3_000_000))
	w.mine(t, confs-1)
	w.sync(t)
	w.faults(t, store.Faults{Reorg: true})
	w.mine(t, 1)
	w.sync(t)
	// 4: seen, pending.
	w.deposit(t, w.address, usdc(4_000_000))
	w.sync(t)

	// 5: a live-network transfer, with an explorer link.
	live := chain.Transfer{
		ChainID: baseUSDC.ChainID, Token: baseUSDC.Token, TxHash: hash32('d'),
		BlockNumber: 1, BlockHash: hash32('1'), From: sender, To: stranger, Amount: usdc(5_000_000),
	}
	w.syncFrom(t, baseUSDC, &fakeSource{
		chainID: baseUSDC.ChainID, head: 1,
		hashes: map[uint64]string{0: hash32('0'), 1: hash32('1')}, logs: []chain.Transfer{live},
	})

	views := w.list(t, deposit.Filter{})
	type row struct {
		amount     int64
		state      string
		status     string
		generation int
		customer   bool
	}
	want := []row{
		{5_000_000, deposit.StateUnattributed, chain.StatusSeen, 0, false},
		{4_000_000, deposit.StatePending, chain.StatusSeen, 0, true},
		{3_000_000, deposit.StateReversed, chain.StatusOrphaned, 1, true},
		{2_000_000, deposit.StateUnattributed, chain.StatusConfirmed, 0, false},
		{1_000_000, deposit.StateCredited, chain.StatusConfirmed, 1, true},
	}
	if len(views) != len(want) {
		t.Fatalf("list = %+v", views)
	}
	for i, v := range views {
		got := row{v.Amount.Minor, v.State, v.Status, v.Generation, v.CustomerID != uuid.Nil}
		if got != want[i] {
			t.Errorf("view %d = %+v, want %+v", i, got, want[i])
		}
		if got.customer && (v.CustomerID != w.cust.ID || v.CustomerName != "Alice") {
			t.Errorf("view %d customer = %s %q", i, v.CustomerID, v.CustomerName)
		}
		if i > 0 && v.TransferID >= views[i-1].TransferID {
			t.Errorf("view %d is not newest first", i)
		}
	}
	if u := views[0].ExplorerURL; u != "https://sepolia.basescan.org/tx/"+hash32('d') {
		t.Errorf("live explorer url = %q", u)
	}
	for _, v := range views[1:] {
		if v.ExplorerURL != "" || v.Network != simUSDC.Key {
			t.Errorf("simulated view %+v", v)
		}
	}

	// Filters and pagination.
	if v := w.list(t, deposit.Filter{Network: simUSDC.Key, Limit: 2}); len(v) != 2 || v[0].Amount.Minor != 4_000_000 {
		t.Fatalf("filtered page = %+v", v)
	}
	if v := w.list(t, deposit.Filter{BeforeID: views[1].TransferID}); len(v) != 3 || v[0].TransferID != views[2].TransferID {
		t.Fatalf("page before %d = %+v", views[1].TransferID, v)
	}
	if v := w.list(t, deposit.Filter{Network: chain.KeySimMXNB}); len(v) != 0 {
		t.Fatalf("other network = %+v", v)
	}
	if _, err := deposit.List(t.Context(), pool, w.sb, deposit.Filter{Network: "nope"}); !errors.Is(err, deposit.ErrUnknownNetwork) {
		t.Fatalf("unknown network err = %v", err)
	}
	if v, err := deposit.List(t.Context(), pool, storetest.Sandbox(t, pool).ID, deposit.Filter{}); err != nil || len(v) != 0 {
		t.Fatalf("empty sandbox = %+v, %v", v, err)
	}
}

// fakeSource is an in-memory chain whose blocks a test rewrites at will.
type fakeSource struct {
	chainID int64
	head    uint64
	hashes  map[uint64]string
	logs    []chain.Transfer
}

func (f *fakeSource) ChainID() int64 { return f.chainID }

func (f *fakeSource) Head(context.Context) (chain.Head, error) {
	return chain.Head{Number: f.head, Hash: f.hashes[f.head]}, nil
}

func (f *fakeSource) Logs(_ context.Context, from, to uint64, _ string, _ []string) ([]chain.Transfer, error) {
	var out []chain.Transfer
	for _, l := range f.logs {
		if l.BlockNumber >= from && l.BlockNumber <= to {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeSource) BlockHash(_ context.Context, n uint64) (string, bool, error) {
	h, ok := f.hashes[n]
	return h, ok, nil
}

func (f *fakeSource) Lookup(context.Context, string, uint) (chain.Transfer, bool, error) {
	return chain.Transfer{}, false, nil
}

func hash32(c byte) string { return "0x" + strings.Repeat(string(c), 64) }

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
