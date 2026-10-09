package chain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/money"
)

func TestNormalize(t *testing.T) {
	got, err := chain.NormalizeAddress(" 0x036CbD53842c5426634e7929541eC2318f3dCF7e ")
	if err != nil || got != "0x036cbd53842c5426634e7929541ec2318f3dcf7e" {
		t.Fatalf("NormalizeAddress = %q, %v", got, err)
	}
	for _, bad := range []string{
		"", "0x123", "036cbd53842c5426634e7929541ec2318f3dcf7e00",
		"0x036cbd53842c5426634e7929541ec2318f3dcf7g",
	} {
		if _, err := chain.NormalizeAddress(bad); !errors.Is(err, chain.ErrBadAddress) {
			t.Errorf("NormalizeAddress(%q) err = %v", bad, err)
		}
	}
	if _, err := chain.NormalizeHash("0xab"); !errors.Is(err, chain.ErrBadHash) {
		t.Errorf("short hash err = %v", err)
	}
	if got := chain.ChecksumAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"); got != "0x036CbD53842c5426634e7929541eC2318f3dCF7e" {
		t.Errorf("ChecksumAddress = %s", got)
	}
	if got := chain.ChecksumAddress("nope"); got != "nope" {
		t.Errorf("ChecksumAddress(nope) = %s", got)
	}
	if _, err := chain.ToInt64(math.MaxUint64); !errors.Is(err, chain.ErrRange) {
		t.Errorf("ToInt64 overflow err = %v", err)
	}
	if _, err := chain.ToUint64(-1); !errors.Is(err, chain.ErrRange) {
		t.Errorf("ToUint64(-1) err = %v", err)
	}
}

func TestNetworks(t *testing.T) {
	seen := map[int64]bool{}
	for _, n := range chain.Networks {
		if tok, err := chain.NormalizeAddress(n.Token); err != nil || tok != n.Token {
			t.Errorf("%s token %q is not a lowercase address", n.Key, n.Token)
		}
		if seen[n.ChainID] {
			t.Errorf("%s reuses chain id %d", n.Key, n.ChainID)
		}
		seen[n.ChainID] = true
	}
	base, ok := chain.NetworkByKey(chain.KeyBaseSepoliaUSDC)
	if !ok || base.ChainID != 84532 || base.Asset != money.USDC || base.Simulated {
		t.Fatalf("base sepolia = %+v", base)
	}
	if got := base.TxURL("0xabc"); got != "https://sepolia.basescan.org/tx/0xabc" {
		t.Fatalf("TxURL = %s", got)
	}
	arb, _ := chain.NetworkByKey(chain.KeyArbitrumSepoliaMXNB)
	if arb.ChainID != 421614 || arb.Asset != money.MXNB || arb.TxURL("0x1") != "https://sepolia.arbiscan.io/tx/0x1" {
		t.Fatalf("arbitrum sepolia = %+v", arb)
	}
	if n, ok := chain.SimNetwork(money.MXNB); !ok || n.Key != chain.KeySimMXNB || n.TxURL("x") != "" {
		t.Fatalf("SimNetwork(MXNB) = %+v", n)
	}
	if _, ok := chain.SimNetwork(money.MXN); ok {
		t.Fatal("SimNetwork(MXN) exists")
	}
	if _, ok := chain.NetworkByKey("nope"); ok {
		t.Fatal("unknown key found")
	}
}
