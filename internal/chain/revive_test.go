package chain_test

import (
	"context"
	"strings"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/jackc/pgx/v5"
)

// fakeSource is an in-memory chain whose blocks a test rewrites at will.
type fakeSource struct {
	head   uint64
	hashes map[uint64]string
	logs   []chain.Transfer
}

func (f *fakeSource) ChainID() int64 { return simUSDC.ChainID }

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

// A transaction re-included in a different block after a reorg revives its
// orphaned transfer with the new block instead of being ignored.
func TestReorgReincludedTransferRevives(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	tr := chain.Transfer{
		ChainID: simUSDC.ChainID, Token: simUSDC.Token, TxHash: hash32('e'), LogIndex: 0,
		BlockNumber: 2, BlockHash: hash32('2'), From: alice, To: deposit, Amount: usdc(4_000_000),
	}
	src := &fakeSource{
		head:   3,
		hashes: map[uint64]string{0: hash32('0'), 1: hash32('1'), 2: hash32('2'), 3: hash32('3')},
		logs:   []chain.Transfer{tr},
	}
	if res := scanWith(t, chain.DefaultScanner, sb, simUSDC, src, 2); res.New != 1 || res.Confirmed != 1 {
		t.Fatalf("scan = %+v", res)
	}

	// Blocks 2 and 3 are replaced; the transaction lands in the new block 3.
	src.head = 4
	src.hashes[2], src.hashes[3], src.hashes[4] = hash32('a'), hash32('b'), hash32('c')
	tr.BlockNumber, tr.BlockHash = 3, hash32('b')
	src.logs = []chain.Transfer{tr}
	res := scanWith(t, chain.DefaultScanner, sb, simUSDC, src, 2)
	if res.Orphaned != 1 || res.Revived != 1 || res.New != 0 || res.Confirmed != 1 {
		t.Fatalf("scan after reorg = %+v, want orphaned, revived and reconfirmed", res)
	}
	r := record(t, sb, tr)
	if r.Status != chain.StatusConfirmed || r.BlockNumber != 3 || r.BlockHash != hash32('b') {
		t.Fatalf("revived transfer = %+v", r)
	}
	if res := scanWith(t, chain.DefaultScanner, sb, simUSDC, src, 2); res.Revived != 0 || res.Orphaned != 0 {
		t.Fatalf("rescan = %+v", res)
	}
}

func TestScanRejectsForeignFeed(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	good := chain.Transfer{
		ChainID: simUSDC.ChainID, Token: simUSDC.Token, TxHash: hash32('f'),
		BlockNumber: 1, BlockHash: hash32('1'), From: alice, To: deposit, Amount: usdc(1),
	}
	cases := map[string]func(*chain.Transfer){
		"other chain":   func(t *chain.Transfer) { t.ChainID = 1 },
		"other token":   func(t *chain.Transfer) { t.Token = other },
		"other asset":   func(t *chain.Transfer) { t.Amount.Asset = "MXNB" },
		"negative":      func(t *chain.Transfer) { t.Amount.Minor = -1 },
		"bad tx hash":   func(t *chain.Transfer) { t.TxHash = "0x1" },
		"bad recipient": func(t *chain.Transfer) { t.To = "nope" },
	}
	for name, mutate := range cases {
		tr := good
		mutate(&tr)
		src := &fakeSource{head: 1, hashes: map[uint64]string{1: hash32('1')}, logs: []chain.Transfer{tr}}
		err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
			_, err := chain.Scan(t.Context(), tx, sb, simUSDC, src, nil, 1)
			return err
		})
		if err == nil {
			t.Errorf("%s: scan accepted %+v", name, tr)
		}
	}

	// A zero-value transfer is skipped, not stored.
	zero := good
	zero.Amount.Minor = 0
	src := &fakeSource{head: 1, hashes: map[uint64]string{1: hash32('1')}, logs: []chain.Transfer{zero}}
	if res := scanWith(t, chain.DefaultScanner, sb, simUSDC, src, 1); res.Fetched != 1 || res.New != 0 {
		t.Fatalf("zero transfer scan = %+v", res)
	}
}
