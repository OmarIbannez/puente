// Package evm reads ERC-20 deposits from an EVM chain over JSON-RPC with
// go-ethereum, and sends ERC-20 transfers for the CLI. Both the feed (Logs)
// and the canonical lookups (BlockHash, Lookup) come from the RPC node.
package evm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Errors returned by the adapter.
var (
	// ErrBadRange is returned by Logs for an empty or too wide block range.
	ErrBadRange = errors.New("evm: bad block range")
	// ErrWrongChain is returned when the RPC node serves another chain.
	ErrWrongChain = errors.New("evm: rpc serves the wrong chain")
)

// Client is the read side of an RPC client. *ethclient.Client and
// go-ethereum's simulated.Client both satisfy it.
type Client interface {
	ChainID(ctx context.Context) (*big.Int, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
}

// Dial connects to the JSON-RPC endpoint at rpcURL.
//
// HTTP calls time out after RPCTimeout each, so a hung public RPC can't hold
// a scan (and the sandbox lock it runs under) indefinitely.
func Dial(ctx context.Context, rpcURL string) (*ethclient.Client, error) {
	rc, err := rpc.DialOptions(ctx, rpcURL, rpc.WithHTTPClient(&http.Client{Timeout: RPCTimeout}))
	if err != nil {
		return nil, fmt.Errorf("evm: dial: %w", err)
	}
	return ethclient.NewClient(rc), nil
}

// RPCTimeout bounds every HTTP call Dial's client makes.
const RPCTimeout = 15 * time.Second

// Chain is a chain.Source backed by an RPC client for one network.
type Chain struct {
	client Client
	net    chain.Network
	token  common.Address
}

var _ chain.Source = (*Chain)(nil)

// New returns the Source for net read through client. Call Verify once to
// check that the node serves net's chain.
func New(client Client, net chain.Network) *Chain {
	return &Chain{client: client, net: net, token: common.HexToAddress(net.Token)}
}

// Verify checks that the RPC node serves the network's chain id.
func (c *Chain) Verify(ctx context.Context) error {
	return checkChainID(ctx, c.client, c.net)
}

// ChainID returns the network's chain id.
func (c *Chain) ChainID() int64 { return c.net.ChainID }

// Head returns the latest block.
func (c *Chain) Head(ctx context.Context) (chain.Head, error) {
	h, err := c.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return chain.Head{}, fmt.Errorf("evm: head: %w", err)
	}
	if !h.Number.IsUint64() {
		return chain.Head{}, fmt.Errorf("evm: head: block number %s out of range", h.Number)
	}
	return chain.Head{Number: h.Number.Uint64(), Hash: h.Hash().Hex()}, nil
}

// BlockHash returns the canonical hash of block number, and false if the
// node has no such block.
func (c *Chain) BlockHash(ctx context.Context, number uint64) (string, bool, error) {
	h, err := c.client.HeaderByNumber(ctx, new(big.Int).SetUint64(number))
	if errors.Is(err, ethereum.NotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("evm: block hash %d: %w", number, err)
	}
	return h.Hash().Hex(), true, nil
}

// Logs returns the token's Transfer logs to the watched addresses (any
// recipient when watch is empty) in blocks from..to, which may span at most
// chain.MaxLogRange blocks. Logs the node flags as removed are skipped.
func (c *Chain) Logs(ctx context.Context, from, to uint64, token string, watch []string) ([]chain.Transfer, error) {
	if to < from || to-from >= chain.MaxLogRange {
		return nil, fmt.Errorf("%w: %d..%d", ErrBadRange, from, to)
	}
	tok, err := chain.NormalizeAddress(token)
	if err != nil {
		return nil, fmt.Errorf("evm: logs: %w", err)
	}
	q := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: []common.Address{common.HexToAddress(tok)},
		Topics:    [][]common.Hash{{TransferTopic}, nil, nil},
	}
	for _, w := range watch {
		addr, err := chain.NormalizeAddress(w)
		if err != nil {
			return nil, fmt.Errorf("evm: logs: %w", err)
		}
		q.Topics[2] = append(q.Topics[2], common.BytesToHash(common.HexToAddress(addr).Bytes()))
	}
	logs, err := c.client.FilterLogs(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("evm: logs %d..%d: %w", from, to, err)
	}
	out := make([]chain.Transfer, 0, len(logs))
	for i := range logs {
		if logs[i].Removed {
			continue
		}
		t, err := ParseTransfer(c.net, &logs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Lookup returns the canonical Transfer log of the network's token at
// (txHash, logIndex), from the transaction's receipt. It returns false if
// the transaction is not on the canonical chain or that log is not a
// Transfer of the token.
func (c *Chain) Lookup(ctx context.Context, txHash string, logIndex uint) (chain.Transfer, bool, error) {
	h, err := chain.NormalizeHash(txHash)
	if err != nil {
		return chain.Transfer{}, false, fmt.Errorf("evm: lookup: %w", err)
	}
	r, err := c.client.TransactionReceipt(ctx, common.HexToHash(h))
	if errors.Is(err, ethereum.NotFound) {
		return chain.Transfer{}, false, nil
	}
	if err != nil {
		return chain.Transfer{}, false, fmt.Errorf("evm: lookup %s: %w", h, err)
	}
	for _, l := range r.Logs {
		if l.Index != logIndex {
			continue
		}
		if l.Address != c.token {
			return chain.Transfer{}, false, nil
		}
		t, err := ParseTransfer(c.net, l)
		if errors.Is(err, ErrNotTransfer) {
			return chain.Transfer{}, false, nil
		}
		if err != nil {
			return chain.Transfer{}, false, err
		}
		return t, true, nil
	}
	return chain.Transfer{}, false, nil
}

func checkChainID(ctx context.Context, client interface {
	ChainID(ctx context.Context) (*big.Int, error)
}, net chain.Network,
) error {
	id, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("evm: chain id: %w", err)
	}
	if !id.IsInt64() || id.Int64() != net.ChainID {
		return fmt.Errorf("%w: got %s, want %d (%s)", ErrWrongChain, id, net.ChainID, net.Key)
	}
	return nil
}
