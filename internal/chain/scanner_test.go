package chain_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/sim"
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
	alice   = "0x00000000000000000000000000000000000a11ce"
	deposit = "0x000000000000000000000000000000000000d3a0"
	other   = "0x0000000000000000000000000000000000000bbb"
)

var simUSDC, _ = chain.NetworkByKey(chain.KeySimUSDC)

func usdc(minor int64) money.Amount { return money.New(money.USDC, minor) }

func inTx(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := store.InTx(t.Context(), pool, fn); err != nil {
		t.Fatal(err)
	}
}

func simDeposit(t *testing.T, sb uuid.UUID, to string, amount money.Amount) chain.Transfer {
	t.Helper()
	var tr chain.Transfer
	inTx(t, func(tx pgx.Tx) error {
		var err error
		tr, err = sim.New(tx, sb, simUSDC).Deposit(t.Context(), tx, alice, to, amount)
		return err
	})
	return tr
}

func mine(t *testing.T, sb uuid.UUID, n int) chain.Head {
	t.Helper()
	var h chain.Head
	inTx(t, func(tx pgx.Tx) error {
		var err error
		h, err = sim.New(tx, sb, simUSDC).Mine(t.Context(), tx, n)
		return err
	})
	return h
}

func scanWith(t *testing.T, s chain.Scanner, sb uuid.UUID, net chain.Network, src chain.Source, confirmations uint64) chain.ScanResult {
	t.Helper()
	var res chain.ScanResult
	inTx(t, func(tx pgx.Tx) error {
		var err error
		res, err = s.Scan(t.Context(), tx, sb, net, src, []string{deposit}, confirmations)
		return err
	})
	return res
}

func scan(t *testing.T, sb uuid.UUID, confirmations uint64) chain.ScanResult {
	t.Helper()
	return scanWith(t, chain.DefaultScanner, sb, simUSDC, sim.New(pool, sb, simUSDC), confirmations)
}

func setFaults(t *testing.T, sb uuid.UUID, f store.Faults) {
	t.Helper()
	if _, err := store.SetFaults(t.Context(), pool, sb, f); err != nil {
		t.Fatal(err)
	}
}

func faults(t *testing.T, sb uuid.UUID) store.Faults {
	t.Helper()
	s, err := store.GetSandbox(t.Context(), pool, sb)
	if err != nil {
		t.Fatal(err)
	}
	return s.Faults
}

func transfers(t *testing.T, sb uuid.UUID, status string) []chain.Record {
	t.Helper()
	rs, err := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func record(t *testing.T, sb uuid.UUID, tr chain.Transfer) chain.Record {
	t.Helper()
	for _, r := range transfers(t, sb, "") {
		if r.TxHash == tr.TxHash && r.LogIndex == tr.LogIndex {
			return r
		}
	}
	t.Fatalf("transfer %s/%d not stored", tr.TxHash, tr.LogIndex)
	return chain.Record{}
}

func TestRescanIsIdempotent(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	for i := range 3 {
		simDeposit(t, sb, deposit, usdc(int64(i+1)*1_000_000))
	}
	simDeposit(t, sb, other, usdc(5_000_000)) // not watched

	res := scan(t, sb, 1)
	if res.Fetched != 3 || res.New != 3 || res.Next != 5 {
		t.Fatalf("first scan = %+v, want 3 fetched, 3 new, next 5", res)
	}
	res = scan(t, sb, 1)
	if res.Fetched != 0 || res.New != 0 || res.Windows != 0 {
		t.Fatalf("second scan = %+v, want nothing to do", res)
	}

	// Forget the cursor: the whole chain is rescanned and nothing is added.
	if _, err := pool.Exec(t.Context(), "DELETE FROM chain_cursors WHERE sandbox_id = $1", sb); err != nil {
		t.Fatal(err)
	}
	res = scan(t, sb, 1)
	if res.Fetched != 3 || res.New != 0 || res.Revived != 0 || res.Orphaned != 0 {
		t.Fatalf("rescan = %+v, want 3 fetched, 0 new", res)
	}
	got := transfers(t, sb, "")
	if len(got) != 3 {
		t.Fatalf("stored %d transfers, want 3", len(got))
	}
	for _, r := range got {
		if r.Status != chain.StatusConfirmed || r.To != deposit || r.Network != chain.KeySimUSDC {
			t.Fatalf("stored %+v", r)
		}
	}
}

func TestConfirmations(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	tr := simDeposit(t, sb, deposit, usdc(10_000_000)) // block 1

	if res := scan(t, sb, 3); res.New != 1 || res.Confirmed != 0 {
		t.Fatalf("scan at head 1 = %+v", res)
	}
	if r := record(t, sb, tr); r.Status != chain.StatusSeen || r.ConfirmedAt != nil {
		t.Fatalf("status at head 1 = %s", r.Status)
	}
	mine(t, sb, 1) // head 2: 2 confirmations
	if res := scan(t, sb, 3); res.Confirmed != 0 {
		t.Fatalf("scan at head 2 = %+v", res)
	}
	mine(t, sb, 1) // head 3: 3 confirmations
	if res := scan(t, sb, 3); res.Confirmed != 1 {
		t.Fatalf("scan at head 3 = %+v", res)
	}
	r := record(t, sb, tr)
	if r.Status != chain.StatusConfirmed || r.ConfirmedAt == nil {
		t.Fatalf("status at head 3 = %s", r.Status)
	}
	if res := scan(t, sb, 3); res.Confirmed != 0 {
		t.Fatalf("confirmed twice: %+v", res)
	}
}

// countingSource records every Logs window at the Source boundary.
type countingSource struct {
	chain.Source
	mu      sync.Mutex
	windows [][2]uint64
}

func (c *countingSource) Logs(ctx context.Context, from, to uint64, token string, watch []string) ([]chain.Transfer, error) {
	c.mu.Lock()
	c.windows = append(c.windows, [2]uint64{from, to})
	c.mu.Unlock()
	return c.Source.Logs(ctx, from, to, token, watch)
}

func TestPaging(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	// Deposits on both edges of 3-block windows (0-2, 3-5, 6-8, 9-11).
	want := map[uint64]bool{}
	for block := uint64(1); block <= 10; block++ {
		switch block {
		case 2, 3, 5, 6, 9, 10:
			if tr := simDeposit(t, sb, deposit, usdc(int64(block))); tr.BlockNumber != block {
				t.Fatalf("deposit landed in block %d, want %d", tr.BlockNumber, block)
			}
			want[block] = true
		default:
			mine(t, sb, 1)
		}
	}

	src := &countingSource{Source: sim.New(pool, sb, simUSDC)}
	s := chain.DefaultScanner
	s.MaxRange = 3
	res := scanWith(t, s, sb, simUSDC, src, 1)
	if res.New != 6 || res.Windows != 4 {
		t.Fatalf("scan = %+v, want 6 new in 4 windows", res)
	}
	next := uint64(0)
	for _, w := range src.windows {
		if w[0] != next || w[1] < w[0] || w[1]-w[0]+1 > 3 {
			t.Fatalf("windows %v: bad window %v", src.windows, w)
		}
		next = w[1] + 1
	}
	if next != 11 {
		t.Fatalf("windows %v stop before head 10", src.windows)
	}
	for _, r := range transfers(t, sb, "") {
		delete(want, r.BlockNumber)
	}
	if len(want) != 0 {
		t.Fatalf("missed deposits in blocks %v", want)
	}

	// The default scanner respects the real 200-block cap.
	mine(t, sb, 440) // head 450
	src = &countingSource{Source: sim.New(pool, sb, simUSDC)}
	if _, err := pool.Exec(t.Context(), "DELETE FROM chain_cursors WHERE sandbox_id = $1", sb); err != nil {
		t.Fatal(err)
	}
	res = scanWith(t, chain.DefaultScanner, sb, simUSDC, src, 1)
	if res.Windows != 3 || res.New != 0 || res.Fetched != 6 {
		t.Fatalf("default scan = %+v, want 3 windows", res)
	}
	if got := src.windows[2]; got != [2]uint64{400, 450} {
		t.Fatalf("last window %v", got)
	}

	// The sim source itself rejects windows over the cap, like a public RPC.
	if _, err := sim.New(pool, sb, simUSDC).Logs(t.Context(), 0, chain.MaxLogRange, simUSDC.Token, nil); !errors.Is(err, sim.ErrBadRange) {
		t.Fatalf("Logs over 201 blocks = %v, want ErrBadRange", err)
	}
	for _, bad := range []chain.Scanner{{MaxRange: 0, ReorgDepth: 1}, {MaxRange: 201, ReorgDepth: 1}, {MaxRange: 10}} {
		err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
			_, err := bad.Scan(t.Context(), tx, sb, simUSDC, src, nil, 1)
			return err
		})
		if !errors.Is(err, chain.ErrBadScanner) {
			t.Fatalf("scanner %+v: err = %v", bad, err)
		}
	}
}

func TestInitialLookback(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	simDeposit(t, sb, deposit, usdc(1)) // block 1: before the lookback window
	mine(t, sb, 18)
	simDeposit(t, sb, deposit, usdc(2)) // block 20

	live := simUSDC
	live.Simulated = false // pretend it is a live network with history
	s := chain.DefaultScanner
	s.InitialLookback = 5
	src := &countingSource{Source: sim.New(pool, sb, simUSDC)}
	res := scanWith(t, s, sb, live, src, 1)
	if res.From != 15 || res.New != 1 || src.windows[0][0] != 15 {
		t.Fatalf("first live scan = %+v windows %v, want start at head-5", res, src.windows)
	}
}

func TestReorgOrphansTransfer(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	keep := simDeposit(t, sb, deposit, usdc(1_000_000)) // block 1
	gone := simDeposit(t, sb, deposit, usdc(2_000_000)) // block 2
	mine(t, sb, 1)
	if res := scan(t, sb, 2); res.New != 2 || res.Confirmed != 2 {
		t.Fatalf("scan = %+v", res)
	}
	src := sim.New(pool, sb, simUSDC)
	oldHash, _, err := src.BlockHash(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}

	setFaults(t, sb, store.Faults{Reorg: true})
	head := mine(t, sb, 1)
	if faults(t, sb).Reorg {
		t.Fatal("reorg fault not consumed")
	}
	if head.Number != 4 {
		t.Fatalf("head after reorg = %d, want 4", head.Number)
	}
	newHash, ok, err := src.BlockHash(t.Context(), 2)
	if err != nil || !ok || newHash == oldHash {
		t.Fatalf("block 2 hash %s -> %s (%v, %v), want a new hash", oldHash, newHash, ok, err)
	}
	if _, found, err := src.Lookup(t.Context(), gone.TxHash, 0); err != nil || found {
		t.Fatalf("reorged transfer still canonical (%v, %v)", found, err)
	}

	res := scan(t, sb, 2)
	if res.Orphaned != 1 || res.From != 2 || res.New != 0 {
		t.Fatalf("scan after reorg = %+v, want 1 orphaned, rescan from 2", res)
	}
	if r := record(t, sb, gone); r.Status != chain.StatusOrphaned || r.OrphanedAt == nil {
		t.Fatalf("reorged transfer status %s", r.Status)
	}
	if r := record(t, sb, keep); r.Status != chain.StatusConfirmed {
		t.Fatalf("untouched transfer status %s", r.Status)
	}
	if res := scan(t, sb, 2); res.Orphaned != 0 || res.New != 0 || res.Revived != 0 {
		t.Fatalf("second scan after reorg = %+v", res)
	}
	if got := transfers(t, sb, chain.StatusOrphaned); len(got) != 1 {
		t.Fatalf("orphaned filter returned %d", len(got))
	}
}

func TestFeedFaults(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	src := sim.New(pool, sb, simUSDC)

	setFaults(t, sb, store.Faults{DuplicateLog: true})
	dup := simDeposit(t, sb, deposit, usdc(7_000_000))
	feed, err := src.Logs(t.Context(), dup.BlockNumber, dup.BlockNumber, simUSDC.Token, []string{deposit})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed) != 2 || feed[0].TxHash != feed[1].TxHash || feed[0].LogIndex == feed[1].LogIndex {
		t.Fatalf("duplicate feed = %+v, want one tx twice", feed)
	}
	if _, ok, err := src.Lookup(t.Context(), dup.TxHash, 0); err != nil || !ok {
		t.Fatalf("canonical log 0 missing (%v, %v)", ok, err)
	}
	if _, ok, err := src.Lookup(t.Context(), dup.TxHash, 1); err != nil || ok {
		t.Fatalf("duplicate log 1 is canonical (%v, %v)", ok, err)
	}
	if res := scan(t, sb, 1); res.New != 2 {
		t.Fatalf("scan = %+v, want both feed logs stored", res)
	}

	setFaults(t, sb, store.Faults{ShortDeposit: true})
	short := simDeposit(t, sb, deposit, usdc(100_000_000))
	if short.Amount != usdc(100_000_000) {
		t.Fatalf("feed amount %s", short.Amount)
	}
	canon, ok, err := src.Lookup(t.Context(), short.TxHash, 0)
	if err != nil || !ok {
		t.Fatalf("short lookup (%v, %v)", ok, err)
	}
	if canon.Amount != usdc(99_000_000) {
		t.Fatalf("canonical amount %s, want 99.000000 USDC", canon.Amount)
	}
	scan(t, sb, 1)
	if r := record(t, sb, short); r.Amount != usdc(100_000_000) {
		t.Fatalf("stored amount %s, want the feed's", r.Amount)
	}
}

func TestFaultConsumption(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID

	// Reorg with nothing to reorganize stays armed.
	setFaults(t, sb, store.Faults{DuplicateLog: true, ShortDeposit: true, Reorg: true, RailReject: true})
	mine(t, sb, 1)
	if f := faults(t, sb); !f.Reorg {
		t.Fatal("reorg consumed with no deposit")
	}

	first := simDeposit(t, sb, deposit, usdc(50_000_000))
	if f := faults(t, sb); f.DuplicateLog || f.ShortDeposit || !f.Reorg || !f.RailReject {
		t.Fatalf("after deposit faults = %+v, want only reorg and rail switches left", f)
	}
	second := simDeposit(t, sb, deposit, usdc(50_000_000))
	src := sim.New(pool, sb, simUSDC)
	if feed, _ := src.Logs(t.Context(), second.BlockNumber, second.BlockNumber, simUSDC.Token, nil); len(feed) != 1 {
		t.Fatalf("second deposit feed has %d logs, want 1", len(feed))
	}
	if c, _, _ := src.Lookup(t.Context(), second.TxHash, 0); c.Amount != second.Amount {
		t.Fatalf("second deposit still short: %s", c.Amount)
	}

	mine(t, sb, 1)
	if f := faults(t, sb); f.Reorg || !f.RailReject {
		t.Fatalf("after mine faults = %+v", f)
	}
	// Nothing was confirmed, so the reorg took the most recent deposit.
	if _, ok, _ := src.Lookup(t.Context(), second.TxHash, 0); ok {
		t.Fatal("most recent deposit survived the reorg")
	}
	if _, ok, _ := src.Lookup(t.Context(), first.TxHash, 0); !ok {
		t.Fatal("older deposit lost in the reorg")
	}
	mine(t, sb, 1)
	if _, ok, _ := src.Lookup(t.Context(), first.TxHash, 0); !ok {
		t.Fatal("a consumed reorg fired again")
	}
}

func TestSandboxIsolation(t *testing.T) {
	a := storetest.Sandbox(t, pool).ID
	b := storetest.Sandbox(t, pool).ID
	ta := simDeposit(t, a, deposit, usdc(3_000_000))
	if res := scan(t, a, 1); res.New != 1 {
		t.Fatalf("scan a = %+v", res)
	}
	if res := scan(t, b, 1); res.Fetched != 0 || res.Head.Number != 0 {
		t.Fatalf("scan b = %+v, want an empty chain", res)
	}
	if got := transfers(t, b, ""); len(got) != 0 {
		t.Fatalf("sandbox b sees %d transfers", len(got))
	}
	tb := simDeposit(t, b, deposit, usdc(3_000_000))
	if ta.TxHash == tb.TxHash || ta.BlockHash == tb.BlockHash {
		t.Fatal("sandboxes share hashes")
	}
	if _, ok, _ := sim.New(pool, b, simUSDC).Lookup(t.Context(), ta.TxHash, 0); ok {
		t.Fatal("sandbox b can look up a's transfer")
	}
	r := record(t, a, ta)
	if _, err := chain.TransferByID(t.Context(), pool, b, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TransferByID across sandboxes = %v", err)
	}
	if err := store.DeleteSandbox(t.Context(), pool, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM chain_transfers WHERE sandbox_id = $1) +
		(SELECT count(*) FROM chain_cursors WHERE sandbox_id = $1) +
		(SELECT count(*) FROM sim_blocks WHERE sandbox_id = $1) +
		(SELECT count(*) FROM sim_logs WHERE sandbox_id = $1)`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows survived the sandbox delete", n)
	}
}

func TestDeterministicHashes(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	tr := simDeposit(t, sb, deposit, usdc(1))
	h, ok, err := sim.New(pool, sb, simUSDC).BlockHash(t.Context(), 1)
	if err != nil || !ok || h != tr.BlockHash {
		t.Fatalf("BlockHash(1) = %s, deposit said %s", h, tr.BlockHash)
	}
	if !strings.HasPrefix(tr.TxHash, "0x") || len(tr.TxHash) != 66 {
		t.Fatalf("tx hash %q", tr.TxHash)
	}
}

func TestTransfersQuery(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	for i := range 5 {
		simDeposit(t, sb, deposit, usdc(int64(i+1)))
	}
	scan(t, sb, 1)
	page, err := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{Network: chain.KeySimUSDC, Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID <= page[1].ID {
		t.Fatalf("first page %+v (%v)", page, err)
	}
	rest, err := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{BeforeID: page[1].ID})
	if err != nil || len(rest) != 3 {
		t.Fatalf("rest %d (%v)", len(rest), err)
	}
	if got, _ := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{Network: chain.KeySimMXNB}); len(got) != 0 {
		t.Fatalf("other network returned %d", len(got))
	}
	if _, err := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{Status: "bogus"}); !errors.Is(err, chain.ErrBadStatus) {
		t.Fatalf("bad status err = %v", err)
	}
	r, err := chain.TransferByID(t.Context(), pool, sb, page[0].ID)
	if err != nil || r.TxHash != page[0].TxHash || r.Amount != usdc(5) {
		t.Fatalf("TransferByID = %+v (%v)", r, err)
	}
}
