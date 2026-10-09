package evm

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/money"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TransferTopic is topic 0 of an ERC-20 Transfer event:
// keccak256("Transfer(address,address,uint256)").
var TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// Parse errors.
var (
	// ErrNotTransfer is returned for a log that is not a well-formed ERC-20
	// Transfer event (wrong topic 0, topic count, padding or data length).
	ErrNotTransfer = errors.New("evm: not an ERC-20 Transfer log")
	// ErrAmountRange is returned for a transfer value that does not fit
	// int64 minor units.
	ErrAmountRange = errors.New("evm: transfer amount out of range")
)

// maxMinor is the largest amount Puente stores (int64 minor units).
var maxMinor = big.NewInt(math.MaxInt64)

// ParseTransfer decodes an ERC-20 Transfer log of net's token. The value is
// taken as minor units of net.Asset (both stablecoins have 6 decimals) and
// must fit int64.
func ParseTransfer(net chain.Network, l *types.Log) (chain.Transfer, error) {
	if len(l.Topics) != 3 || l.Topics[0] != TransferTopic || len(l.Data) != 32 {
		return chain.Transfer{}, fmt.Errorf("%w: tx %s log %d", ErrNotTransfer, l.TxHash.Hex(), l.Index)
	}
	from, ok := topicAddress(l.Topics[1])
	if !ok {
		return chain.Transfer{}, fmt.Errorf("%w: tx %s log %d: bad from topic", ErrNotTransfer, l.TxHash.Hex(), l.Index)
	}
	to, ok := topicAddress(l.Topics[2])
	if !ok {
		return chain.Transfer{}, fmt.Errorf("%w: tx %s log %d: bad to topic", ErrNotTransfer, l.TxHash.Hex(), l.Index)
	}
	value := new(big.Int).SetBytes(l.Data)
	if value.Cmp(maxMinor) > 0 {
		return chain.Transfer{}, fmt.Errorf("%w: tx %s log %d: %s", ErrAmountRange, l.TxHash.Hex(), l.Index, value)
	}
	return chain.Transfer{
		ChainID:     net.ChainID,
		Token:       strings.ToLower(l.Address.Hex()),
		TxHash:      l.TxHash.Hex(),
		LogIndex:    l.Index,
		BlockNumber: l.BlockNumber,
		BlockHash:   l.BlockHash.Hex(),
		From:        strings.ToLower(from.Hex()),
		To:          strings.ToLower(to.Hex()),
		Amount:      money.New(net.Asset, value.Int64()),
	}, nil
}

// topicAddress decodes an indexed address topic: 12 zero bytes, then the
// 20-byte address.
func topicAddress(h common.Hash) (common.Address, bool) {
	for _, b := range h[:12] {
		if b != 0 {
			return common.Address{}, false
		}
	}
	return common.BytesToAddress(h[12:]), true
}
