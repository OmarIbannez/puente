package engine_test

import (
	"crypto/ecdsa"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/evm"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient/simulated"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

const simChainID = 1337

// testnet is go-ethereum's in-process chain with TestToken deployed: a
// stand-in for a real testnet behind the live sandbox.
type testnet struct {
	t      *testing.T
	sim    *simulated.Backend
	client simulated.Client
	key    *ecdsa.PrivateKey
	from   common.Address
	token  common.Address
}

func newTestnet(t *testing.T) *testnet {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	sim := simulated.NewBackend(types.GenesisAlloc{from: {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)}})
	t.Cleanup(func() { _ = sim.Close() })
	n := &testnet{t: t, sim: sim, client: sim.Client(), key: key, from: from}
	bin, err := os.ReadFile("../../contracts/TestToken.bin")
	if err != nil {
		t.Fatal(err)
	}
	n.token = n.send(nil, common.FromHex(strings.TrimSpace(string(bin)))).ContractAddress
	return n
}

func (n *testnet) send(to *common.Address, data []byte) *types.Receipt {
	n.t.Helper()
	ctx := n.t.Context()
	nonce, err := n.client.PendingNonceAt(ctx, n.from)
	if err != nil {
		n.t.Fatal(err)
	}
	head, err := n.client.HeaderByNumber(ctx, nil)
	if err != nil {
		n.t.Fatal(err)
	}
	tip := big.NewInt(1_000_000_000)
	tx, err := types.SignNewTx(n.key, types.LatestSignerForChainID(big.NewInt(simChainID)), &types.DynamicFeeTx{
		ChainID: big.NewInt(simChainID), Nonce: nonce, GasTipCap: tip,
		GasFeeCap: new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip),
		Gas:       1_000_000, To: to, Value: new(big.Int), Data: data,
	})
	if err != nil {
		n.t.Fatal(err)
	}
	if err := n.client.SendTransaction(ctx, tx); err != nil {
		n.t.Fatal(err)
	}
	n.sim.Commit()
	r, err := n.client.TransactionReceipt(ctx, tx.Hash())
	if err != nil || r.Status != types.ReceiptStatusSuccessful {
		n.t.Fatalf("tx %s: %v", tx.Hash(), err)
	}
	return r
}

func (n *testnet) mint(to common.Address, amount int64) {
	n.t.Helper()
	data := crypto.Keccak256([]byte("mint(address,uint256)"))[:4]
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(amount).Bytes(), 32)...)
	n.send(&n.token, data)
}

func TestLiveSandboxFollowsTestnet(t *testing.T) {
	ctx := t.Context()
	tn := newTestnet(t)
	watched := common.HexToAddress("0x000000000000000000000000000000000000d3a0")
	net := chain.Network{
		Key: "evm-test", Name: "TestToken", ChainID: simChainID, Asset: money.USDC,
		Token: strings.ToLower(tn.token.Hex()),
	}
	eng := engine.New(engine.Config{
		Pool: pool, Payout: payout.DefaultConfig(), LiveConfirmations: 2,
		Live:        []engine.LiveNetwork{{Network: net, Source: evm.New(tn.client, net)}},
		LiveAddress: strings.ToLower(watched.Hex()),
	})
	sb, err := eng.EnsureLive(ctx)
	if err != nil || sb.Kind != store.KindLive {
		t.Fatalf("EnsureLive = %+v, %v", sb, err)
	}
	again, err := eng.EnsureLive(ctx)
	if err != nil || again.ID != sb.ID {
		t.Fatalf("EnsureLive is not stable: %v", err)
	}
	owner, err := customer.Owner(ctx, pool, sb.ID, net.Key, watched.Hex())
	if err != nil {
		t.Fatal(err)
	}

	tn.mint(watched, 7_500_000)
	tn.sim.Commit()
	tn.sim.Commit()
	if _, err := eng.Tick(ctx, sb, time.Now()); err != nil {
		t.Fatal(err)
	}
	views, err := deposit.List(ctx, pool, sb.ID, deposit.Filter{})
	if err != nil || len(views) != 1 || views[0].State != deposit.StateCredited {
		t.Fatalf("live deposits = %+v, %v", views, err)
	}
	bal, err := ledger.PartyBalance(ctx, pool, sb.ID, owner, ledger.CustomerBalances, money.USDC)
	if err != nil || bal.Minor != -7_500_000 {
		t.Fatalf("live customer balance = %v, %v", bal, err)
	}
	run, err := eng.Reconcile(ctx, sb, "manual")
	if err != nil || run.BreakCount != 0 {
		t.Fatalf("live recon = %+v, %v", run, err)
	}
	if _, err := eng.SimulateDeposit(ctx, sb, owner, money.New(money.USDC, 1)); err == nil {
		t.Fatal("simulated deposit accepted on the live sandbox")
	}
}
