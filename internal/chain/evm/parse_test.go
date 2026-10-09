package evm_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/evm"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// parseNet is USDC on Base Sepolia.
var parseNet, _ = chain.NetworkByKey(chain.KeyBaseSepoliaUSDC)

func transferLog(value *big.Int) *types.Log {
	return &types.Log{
		Address: common.HexToAddress(parseNet.Token),
		Topics: []common.Hash{
			evm.TransferTopic,
			common.BytesToHash(common.HexToAddress("0x00000000000000000000000000000000000A11CE").Bytes()),
			common.BytesToHash(watched.Bytes()),
		},
		Data:        common.LeftPadBytes(value.Bytes(), 32),
		BlockNumber: 42,
		BlockHash:   common.HexToHash("0xbb"),
		TxHash:      common.HexToHash("0xcc"),
		Index:       3,
	}
}

func TestParseTransfer(t *testing.T) {
	got, err := evm.ParseTransfer(parseNet, transferLog(big.NewInt(25_000_000)))
	if err != nil {
		t.Fatal(err)
	}
	want := chain.Transfer{
		ChainID: 84532, Token: parseNet.Token,
		TxHash: "0x00000000000000000000000000000000000000000000000000000000000000cc", LogIndex: 3,
		BlockNumber: 42, BlockHash: "0x00000000000000000000000000000000000000000000000000000000000000bb",
		From: "0x00000000000000000000000000000000000a11ce", To: lower(watched), Amount: usdc(25_000_000),
	}
	if got != want {
		t.Fatalf("ParseTransfer = %+v, want %+v", got, want)
	}
	if evm.TransferTopic.Hex() != "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" {
		t.Fatalf("TransferTopic = %s", evm.TransferTopic.Hex())
	}

	maxOK, err := evm.ParseTransfer(parseNet, transferLog(big.NewInt(math.MaxInt64)))
	if err != nil || maxOK.Amount.Minor != math.MaxInt64 {
		t.Fatalf("max int64 amount = %v (%v)", maxOK.Amount, err)
	}
}

func TestParseTransferRejects(t *testing.T) {
	oversized := new(big.Int).Lsh(big.NewInt(1), 63) // MaxInt64 + 1
	if _, err := evm.ParseTransfer(parseNet, transferLog(oversized)); !errors.Is(err, evm.ErrAmountRange) {
		t.Fatalf("2^63 err = %v", err)
	}
	huge := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if _, err := evm.ParseTransfer(parseNet, transferLog(huge)); !errors.Is(err, evm.ErrAmountRange) {
		t.Fatalf("2^256-1 err = %v", err)
	}

	cases := map[string]func(*types.Log){
		"no topics":        func(l *types.Log) { l.Topics = nil },
		"two topics":       func(l *types.Log) { l.Topics = l.Topics[:2] },
		"four topics":      func(l *types.Log) { l.Topics = append(l.Topics, common.Hash{}) }, // ERC-721
		"approval topic":   func(l *types.Log) { l.Topics[0] = common.HexToHash("0x8c5be1e5") },
		"dirty from pad":   func(l *types.Log) { l.Topics[1][0] = 1 },
		"dirty to pad":     func(l *types.Log) { l.Topics[2][11] = 1 },
		"short data":       func(l *types.Log) { l.Data = l.Data[:31] },
		"long data":        func(l *types.Log) { l.Data = append(l.Data, 0) },
		"no data (721ish)": func(l *types.Log) { l.Data = nil },
	}
	for name, mutate := range cases {
		l := transferLog(big.NewInt(1))
		mutate(l)
		if _, err := evm.ParseTransfer(parseNet, l); !errors.Is(err, evm.ErrNotTransfer) {
			t.Errorf("%s: err = %v, want ErrNotTransfer", name, err)
		}
	}
}

func TestTransferCalldata(t *testing.T) {
	data := evm.TransferCalldata(watched, big.NewInt(1))
	if len(data) != 68 || common.Bytes2Hex(data[:4]) != "a9059cbb" || data[67] != 1 {
		t.Fatalf("calldata %x", data)
	}
}
