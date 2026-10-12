// Package chain ingests stablecoin deposits from a blockchain into Puente's
// books. A Source reports two things: the feed (what an indexer or RPC node
// says happened in a block range, which Puente credits from) and canonical
// lookups (what the chain itself says, used for reorg checks and by
// reconciliation). The scanner pulls the feed into chain_transfers
// idempotently, tracks confirmations and marks transfers orphaned when their
// block is reorganized away.
//
// Two adapters implement Source: chain/sim (per-sandbox simulated blocks in
// PostgreSQL, with fault injection) and chain/evm (go-ethereum RPC).
package chain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/OmarIbannez/puente/internal/money"

	"github.com/ethereum/go-ethereum/common"
)

// MaxLogRange is the largest block window, inclusive of both ends, that one
// Logs call may span. Public RPC nodes reject wider eth_getLogs ranges
// (Base Sepolia: "eth_getLogs is limited to a 200 range").
const MaxLogRange = 200

// Errors returned by the helpers in this package.
var (
	// ErrBadAddress is returned for a string that is not a 20-byte hex address.
	ErrBadAddress = errors.New("chain: malformed address")
	// ErrBadHash is returned for a string that is not a 32-byte hex hash.
	ErrBadHash = errors.New("chain: malformed hash")
	// ErrRange is returned when a block number or index does not fit the
	// database's signed 64-bit columns.
	ErrRange = errors.New("chain: number out of range")
)

// Transfer is one ERC-20 Transfer log of a watched token. Addresses and
// hashes are lowercase 0x-prefixed hex.
type Transfer struct {
	// ChainID is the EIP-155 chain id.
	ChainID int64 `json:"chain_id"`
	// Token is the ERC-20 contract address.
	Token string `json:"token"`
	// TxHash is the hash of the transaction that emitted the log.
	TxHash string `json:"tx_hash"`
	// LogIndex is the log's position within its block.
	LogIndex uint `json:"log_index"`
	// BlockNumber is the number of the block holding the log.
	BlockNumber uint64 `json:"block_number"`
	// BlockHash is the hash of the block holding the log.
	BlockHash string `json:"block_hash"`
	// From is the sender.
	From string `json:"from"`
	// To is the recipient (a watched deposit address).
	To string `json:"to"`
	// Amount is the value transferred, in the asset's minor units.
	Amount money.Amount `json:"amount"`
}

// Head is the tip of a chain.
type Head struct {
	// Number is the latest block number.
	Number uint64 `json:"number"`
	// Hash is the latest block's hash (lowercase 0x hex).
	Hash string `json:"hash"`
}

// Source is a chain Puente reads deposits from.
type Source interface {
	// ChainID returns the EIP-155 chain id.
	ChainID() int64
	// Head returns the current tip.
	Head(ctx context.Context) (Head, error)
	// Logs returns the feed: the Transfer logs of token to any of the
	// watched addresses in blocks from..to (inclusive), as the indexer or
	// RPC reports them. An empty watch list means every recipient. The range
	// must span at most MaxLogRange blocks.
	Logs(ctx context.Context, from, to uint64, token string, watch []string) ([]Transfer, error)
	// BlockHash returns the canonical hash of block number, and false when
	// the canonical chain has no such block.
	BlockHash(ctx context.Context, number uint64) (string, bool, error)
	// Lookup returns the canonical Transfer log at (txHash, logIndex), and
	// false when the canonical chain has no such log.
	Lookup(ctx context.Context, txHash string, logIndex uint) (Transfer, bool, error)
}

// NormalizeAddress validates a 20-byte hex address (any case, 0x prefix
// required) and returns it in lowercase storage form.
func NormalizeAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !isHex(s, 40) {
		return "", fmt.Errorf("%w: %q", ErrBadAddress, s)
	}
	return strings.ToLower(s), nil
}

// NormalizeAddresses normalizes every address in list.
func NormalizeAddresses(list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, a := range list {
		n, err := NormalizeAddress(a)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// NormalizeHash validates a 32-byte hex hash (0x prefix required) and
// returns it in lowercase storage form.
func NormalizeHash(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !isHex(s, 64) {
		return "", fmt.Errorf("%w: %q", ErrBadHash, s)
	}
	return strings.ToLower(s), nil
}

// ChecksumAddress formats a stored address in its EIP-55 mixed-case display
// form. It returns s unchanged if s is not an address.
func ChecksumAddress(s string) string {
	if !isHex(s, 40) {
		return s
	}
	return common.HexToAddress(s).Hex()
}

// ToInt64 converts a block number or log index to the signed 64-bit form the
// database stores, failing instead of wrapping.
func ToInt64(u uint64) (int64, error) {
	if u > math.MaxInt64 {
		return 0, fmt.Errorf("%w: %d", ErrRange, u)
	}
	return int64(u), nil
}

// ToUint64 converts a stored, non-negative database number back, failing on
// a negative value.
func ToUint64(i int64) (uint64, error) {
	if i < 0 {
		return 0, fmt.Errorf("%w: %d", ErrRange, i)
	}
	return uint64(i), nil
}

func isHex(s string, digits int) bool {
	if len(s) != digits+2 || (s[:2] != "0x" && s[:2] != "0X") {
		return false
	}
	for _, r := range s[2:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
