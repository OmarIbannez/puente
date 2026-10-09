package evm_test

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/evm"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

// simChainID is the chain id of go-ethereum's simulated backend.
const simChainID = 1337

var (
	watched   = common.HexToAddress("0x000000000000000000000000000000000000d3a0")
	unwatched = common.HexToAddress("0x0000000000000000000000000000000000000bbb")
)

func lower(a common.Address) string { return strings.ToLower(a.Hex()) }

func usdc(minor int64) money.Amount { return money.New(money.USDC, minor) }

// env is a simulated chain with TestToken deployed by a funded key.
type env struct {
	t      *testing.T
	sim    *simulated.Backend
	client simulated.Client
	key    *ecdsa.PrivateKey
	from   common.Address
	net    chain.Network
	src    *evm.Chain
}

func newEnv(t *testing.T) *env {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	balance := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)
	sim := simulated.NewBackend(types.GenesisAlloc{from: {Balance: balance}})
	t.Cleanup(func() { _ = sim.Close() })
	e := &env{t: t, sim: sim, client: sim.Client(), key: key, from: from}

	bin, err := os.ReadFile("../../../contracts/TestToken.bin")
	if err != nil {
		t.Fatal(err)
	}
	r := e.send(nil, common.FromHex(strings.TrimSpace(string(bin))))
	e.net = chain.Network{
		Key:     "evm-test",
		Name:    "TestToken on the simulated backend",
		ChainID: simChainID,
		Asset:   money.USDC,
		Token:   lower(r.ContractAddress),
	}
	e.src = evm.New(e.client, e.net)
	return e
}

// send signs a transaction from the env key, mines it and returns its
// successful receipt.
func (e *env) send(to *common.Address, data []byte) *types.Receipt {
	e.t.Helper()
	ctx := e.t.Context()
	nonce, err := e.client.PendingNonceAt(ctx, e.from)
	if err != nil {
		e.t.Fatal(err)
	}
	head, err := e.client.HeaderByNumber(ctx, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	tip := big.NewInt(1_000_000_000)
	tx, err := types.SignNewTx(e.key, types.LatestSignerForChainID(big.NewInt(simChainID)), &types.DynamicFeeTx{
		ChainID:   big.NewInt(simChainID),
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip),
		Gas:       1_000_000,
		To:        to,
		Value:     new(big.Int),
		Data:      data,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.client.SendTransaction(ctx, tx); err != nil {
		e.t.Fatal(err)
	}
	e.sim.Commit()
	r, err := e.client.TransactionReceipt(ctx, tx.Hash())
	if err != nil {
		e.t.Fatal(err)
	}
	if r.Status != types.ReceiptStatusSuccessful {
		e.t.Fatalf("tx %s failed", tx.Hash())
	}
	return r
}

func (e *env) mint(to common.Address, amount int64) *types.Receipt {
	e.t.Helper()
	data := crypto.Keccak256([]byte("mint(address,uint256)"))[:4]
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)...)
	token := common.HexToAddress(e.net.Token)
	return e.send(&token, data)
}

func (e *env) scan(sb uuid.UUID, confirmations uint64) chain.ScanResult {
	e.t.Helper()
	var res chain.ScanResult
	if err := store.InTx(e.t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		res, err = chain.Scan(e.t.Context(), tx, sb, e.net, e.src, []string{lower(watched)}, confirmations)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return res
}

func record(t *testing.T, sb uuid.UUID, txHash common.Hash) chain.Record {
	t.Helper()
	rs, err := chain.Transfers(t.Context(), pool, sb, chain.TransferFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.TxHash == txHash.Hex() {
			return r
		}
	}
	t.Fatalf("transfer %s not stored", txHash)
	return chain.Record{}
}

func TestEVMScanAndConfirm(t *testing.T) {
	e := newEnv(t)
	sb := storetest.Sandbox(t, pool).ID
	if err := e.src.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := e.mint(watched, 25_000_000)
	e.mint(unwatched, 1_000_000)

	res := e.scan(sb, 3)
	if res.New != 1 || res.Fetched != 1 || res.Confirmed != 0 {
		t.Fatalf("scan = %+v, want the watched mint only", res)
	}
	got := record(t, sb, r.TxHash)
	want := chain.Transfer{
		ChainID: simChainID, Token: e.net.Token, TxHash: r.TxHash.Hex(), LogIndex: 0,
		BlockNumber: r.BlockNumber.Uint64(), BlockHash: r.BlockHash.Hex(),
		From: lower(common.Address{}), To: lower(watched), Amount: usdc(25_000_000),
	}
	if got.Transfer != want || got.Status != chain.StatusSeen {
		t.Fatalf("stored %+v (%s), want %+v", got.Transfer, got.Status, want)
	}

	e.sim.Commit() // mint block + 2 = 3 confirmations
	if res := e.scan(sb, 3); res.Confirmed != 1 || res.New != 0 {
		t.Fatalf("scan after commits = %+v, want 1 confirmed", res)
	}
	if res := e.scan(sb, 3); res.New != 0 || res.Confirmed != 0 || res.Orphaned != 0 {
		t.Fatalf("idle rescan = %+v", res)
	}
}

func TestEVMLookup(t *testing.T) {
	e := newEnv(t)
	r := e.mint(watched, 7_000_000)
	got, ok, err := e.src.Lookup(t.Context(), r.TxHash.Hex(), 0)
	if err != nil || !ok {
		t.Fatalf("Lookup = %v, %v", ok, err)
	}
	if got.Amount != usdc(7_000_000) || got.To != lower(watched) || got.BlockHash != r.BlockHash.Hex() {
		t.Fatalf("Lookup = %+v", got)
	}
	if _, ok, err := e.src.Lookup(t.Context(), r.TxHash.Hex(), 1); err != nil || ok {
		t.Fatalf("Lookup of a missing log index = %v, %v", ok, err)
	}
	if _, ok, err := e.src.Lookup(t.Context(), "0x"+strings.Repeat("ab", 32), 0); err != nil || ok {
		t.Fatalf("Lookup of an unknown tx = %v, %v", ok, err)
	}
	if _, _, err := e.src.Lookup(t.Context(), "0x12", 0); err == nil {
		t.Fatal("Lookup accepted a malformed hash")
	}
	// A log of another contract at that index is not this token's transfer.
	other := evm.New(e.client, chain.Network{Key: "x", ChainID: simChainID, Asset: money.USDC, Token: lower(unwatched)})
	if _, ok, err := other.Lookup(t.Context(), r.TxHash.Hex(), 0); err != nil || ok {
		t.Fatalf("Lookup through another token = %v, %v", ok, err)
	}
}

func TestEVMReorg(t *testing.T) {
	e := newEnv(t)
	sb := storetest.Sandbox(t, pool).ID
	parent, err := e.client.HeaderByNumber(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := e.mint(watched, 5_000_000)
	if res := e.scan(sb, 1); res.New != 1 || res.Confirmed != 1 {
		t.Fatalf("scan = %+v", res)
	}

	// Rewind the chain to the mint's parent: the mint's block is gone.
	if err := e.sim.Fork(parent.Hash()); err != nil {
		t.Fatal(err)
	}
	res := e.scan(sb, 1)
	if res.Orphaned != 1 || res.Head.Number != parent.Number.Uint64() {
		t.Fatalf("scan after fork = %+v, want 1 orphaned at head %d", res, parent.Number)
	}
	if got := record(t, sb, r.TxHash); got.Status != chain.StatusOrphaned {
		t.Fatalf("status after fork = %s", got.Status)
	}

	// The side chain grows past the old tip with a different block where the
	// mint was. Whether or not the node re-includes the mint, the stored
	// transfer must agree with the canonical chain afterwards.
	e.sim.Commit()
	e.mint(unwatched, 1)
	e.sim.Commit()
	if hash, ok, err := e.src.BlockHash(t.Context(), r.BlockNumber.Uint64()); err != nil || !ok || hash == r.BlockHash.Hex() {
		t.Fatalf("block %d after fork: %s %v %v, want a replaced block", r.BlockNumber, hash, ok, err)
	}
	res = e.scan(sb, 1)
	got := record(t, sb, r.TxHash)
	canon, onChain, err := e.src.Lookup(t.Context(), r.TxHash.Hex(), 0)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case onChain && (res.Revived != 1 || got.Status != chain.StatusConfirmed || got.BlockHash != canon.BlockHash):
		t.Fatalf("mint re-included in %s but stored %s in %s", canon.BlockHash, got.Status, got.BlockHash)
	case !onChain && got.Status != chain.StatusOrphaned:
		t.Fatalf("mint gone but stored as %s", got.Status)
	}
	t.Logf("mint re-included after fork: %v", onChain)
}

func TestEVMSend(t *testing.T) {
	e := newEnv(t)
	sb := storetest.Sandbox(t, pool).ID
	e.mint(e.from, 100_000_000) // fund the hot wallet

	hash, err := evm.Send(t.Context(), e.client, e.key, e.net, lower(watched), usdc(12_500_000))
	if err != nil {
		t.Fatal(err)
	}
	e.sim.Commit()
	r, err := e.client.TransactionReceipt(t.Context(), common.HexToHash(hash))
	if err != nil || r.Status != types.ReceiptStatusSuccessful || r.Type != types.DynamicFeeTxType {
		t.Fatalf("receipt %+v (%v)", r, err)
	}
	if res := e.scan(sb, 1); res.New != 1 {
		t.Fatalf("scan = %+v", res)
	}
	got := record(t, sb, common.HexToHash(hash))
	if got.From != lower(e.from) || got.To != lower(watched) || got.Amount != usdc(12_500_000) {
		t.Fatalf("stored %+v", got.Transfer)
	}

	if _, err := evm.Send(t.Context(), e.client, e.key, e.net, lower(watched), money.New(money.MXNB, 1)); !errors.Is(err, evm.ErrBadSend) {
		t.Fatalf("send of the wrong asset err = %v", err)
	}
	if _, err := evm.Send(t.Context(), e.client, e.key, e.net, "nope", usdc(1)); !errors.Is(err, evm.ErrBadSend) {
		t.Fatalf("send to a bad address err = %v", err)
	}
	wrong := e.net
	wrong.ChainID = 84532
	if _, err := evm.Send(t.Context(), e.client, e.key, wrong, lower(watched), usdc(1)); !errors.Is(err, evm.ErrWrongChain) {
		t.Fatalf("send on the wrong chain err = %v", err)
	}
	if err := evm.New(e.client, wrong).Verify(t.Context()); !errors.Is(err, evm.ErrWrongChain) {
		t.Fatalf("Verify on the wrong chain err = %v", err)
	}
}

func TestEVMLogsRange(t *testing.T) {
	e := newEnv(t)
	if _, err := e.src.Logs(t.Context(), 0, chain.MaxLogRange, e.net.Token, nil); !errors.Is(err, evm.ErrBadRange) {
		t.Fatalf("Logs over 201 blocks err = %v", err)
	}
	if _, err := e.src.Logs(t.Context(), 5, 4, e.net.Token, nil); !errors.Is(err, evm.ErrBadRange) {
		t.Fatalf("Logs with to < from err = %v", err)
	}
	r := e.mint(unwatched, 3)
	all, err := e.src.Logs(t.Context(), 0, r.BlockNumber.Uint64(), e.net.Token, nil)
	if err != nil || len(all) != 1 || all[0].TxHash != r.TxHash.Hex() {
		t.Fatalf("Logs with no watch list = %+v (%v)", all, err)
	}
}
