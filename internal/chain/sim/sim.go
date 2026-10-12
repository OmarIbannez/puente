// Package sim is a per-sandbox simulated chain stored in PostgreSQL. It
// implements chain.Source so the scanner, the ledger and reconciliation run
// against it exactly as they do against a real testnet, with no network.
//
// The simulated chain keeps what the indexer reports (the feed, returned by
// Logs) apart from what the chain says (BlockHash and Lookup). The sandbox's
// fault switches corrupt the feed or rewrite the chain on demand, so every
// reconciliation break can be demonstrated:
//
//   - DuplicateLog: the next deposit appears twice in the feed (same
//     transaction, log indexes 0 and 1); the chain has only log 0.
//   - ShortDeposit: the next deposit's canonical amount is 1% less than the
//     feed reports.
//   - Reorg: the next Mine replaces the block of the most recent confirmed
//     deposit with an empty block under a new hash.
//
// Each fault is consumed by the operation that applies it. Hashes are
// SHA-256 digests of the sandbox, network, block number and a salt, so runs
// are reproducible.
package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Errors returned by the simulated chain.
var (
	// ErrNotSimulated is returned by New's methods for a live network.
	ErrNotSimulated = errors.New("sim: network is not simulated")
	// ErrBadRange is returned by Logs for an empty or too wide block range.
	ErrBadRange = errors.New("sim: bad block range")
	// ErrBadAmount is returned by Deposit for a non-positive amount or an
	// amount of the wrong asset.
	ErrBadAmount = errors.New("sim: bad deposit amount")
)

// simLog is one log row to insert: canonical is nil for a feed-only log.
type simLog struct {
	index     int64
	canonical *int64
}

// shortBps is how much a ShortDeposit fault shaves off the canonical
// amount: 100 bps = 1%.
const shortBps = 100

// Chain is one sandbox's simulated chain for one network.
type Chain struct {
	q       store.Querier
	sandbox uuid.UUID
	net     chain.Network
}

var _ chain.Source = (*Chain)(nil)

// New returns the simulated chain of net in sandbox. Reads go through q;
// Deposit and Mine take the transaction to write in.
func New(q store.Querier, sandbox uuid.UUID, net chain.Network) *Chain {
	return &Chain{q: q, sandbox: sandbox, net: net}
}

// ChainID returns the network's chain id.
func (c *Chain) ChainID() int64 { return c.net.ChainID }

// Head returns the latest canonical block, or the genesis block (number 0)
// of an empty chain.
func (c *Chain) Head(ctx context.Context) (chain.Head, error) {
	return c.head(ctx, c.q)
}

// BlockHash returns the canonical hash of block number.
func (c *Chain) BlockHash(ctx context.Context, number uint64) (string, bool, error) {
	if number == 0 {
		return c.hash("block", 0, 0), true, nil
	}
	n, err := chain.ToInt64(number)
	if err != nil {
		return "", false, fmt.Errorf("sim: block hash: %w", err)
	}
	var hash string
	err = c.q.QueryRow(ctx, `SELECT hash FROM sim_blocks
		WHERE sandbox_id = $1 AND network = $2 AND number = $3 AND canonical`,
		c.sandbox, c.net.Key, n).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sim: block hash %d: %w", number, err)
	}
	return hash, true, nil
}

// logColumns and logFrom select sim_logs rows joined to their block; the
// caller appends the amount column (feed or canonical) in between.
const (
	logColumns = `SELECT l.tx_hash, l.log_index, l.block_number, l.block_hash, l.from_addr, l.to_addr, `
	logFrom    = `
	FROM sim_logs l
	JOIN sim_blocks b ON b.sandbox_id = l.sandbox_id AND b.network = l.network AND b.hash = l.block_hash
	WHERE l.sandbox_id = $1 AND l.network = $2 AND b.canonical AND `
)

// Logs returns the feed: logs the indexer reports in canonical blocks
// from..to, with the amount the indexer reports.
func (c *Chain) Logs(ctx context.Context, from, to uint64, token string, watch []string) ([]chain.Transfer, error) {
	if !c.net.Simulated {
		return nil, ErrNotSimulated
	}
	if to < from || to-from >= chain.MaxLogRange {
		return nil, fmt.Errorf("%w: %d..%d", ErrBadRange, from, to)
	}
	if t, err := chain.NormalizeAddress(token); err != nil || t != c.net.Token {
		return nil, fmt.Errorf("sim: logs: unknown token %q", token)
	}
	watch, err := chain.NormalizeAddresses(watch)
	if err != nil {
		return nil, fmt.Errorf("sim: logs: %w", err)
	}
	lo, err := chain.ToInt64(from)
	if err != nil {
		return nil, fmt.Errorf("sim: logs: %w", err)
	}
	hi, err := chain.ToInt64(to)
	if err != nil {
		return nil, fmt.Errorf("sim: logs: %w", err)
	}
	rows, err := c.q.Query(ctx, logColumns+"l.feed_amount"+logFrom+`l.in_feed
		AND l.block_number BETWEEN $3 AND $4
		AND (cardinality($5::text[]) = 0 OR l.to_addr = ANY($5))
		ORDER BY l.block_number, l.log_index`, c.sandbox, c.net.Key, lo, hi, watch)
	if err != nil {
		return nil, fmt.Errorf("sim: logs: %w", err)
	}
	defer rows.Close()
	out := []chain.Transfer{}
	for rows.Next() {
		t, err := c.scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sim: logs: %w", err)
	}
	return out, nil
}

// Lookup returns the canonical log at (txHash, logIndex) with its canonical
// amount, and false if the chain has no such log (it was only in the feed,
// or its block was reorganized away).
func (c *Chain) Lookup(ctx context.Context, txHash string, logIndex uint) (chain.Transfer, bool, error) {
	h, err := chain.NormalizeHash(txHash)
	if err != nil {
		return chain.Transfer{}, false, fmt.Errorf("sim: lookup: %w", err)
	}
	idx, err := chain.ToInt64(uint64(logIndex))
	if err != nil {
		return chain.Transfer{}, false, fmt.Errorf("sim: lookup: %w", err)
	}
	t, err := c.scanLog(c.q.QueryRow(ctx, logColumns+"l.canonical_amount"+logFrom+`l.canonical
		AND l.tx_hash = $3 AND l.log_index = $4`, c.sandbox, c.net.Key, h, idx))
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Transfer{}, false, nil
	}
	if err != nil {
		return chain.Transfer{}, false, err
	}
	return t, true, nil
}

func (c *Chain) scanLog(row pgx.Row) (chain.Transfer, error) {
	var (
		t               chain.Transfer
		logIndex, block int64
		amount          int64
	)
	if err := row.Scan(&t.TxHash, &logIndex, &block, &t.BlockHash, &t.From, &t.To, &amount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return chain.Transfer{}, err
		}
		return chain.Transfer{}, fmt.Errorf("sim: scan log: %w", err)
	}
	idx, err := chain.ToUint64(logIndex)
	if err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: scan log: %w", err)
	}
	if t.BlockNumber, err = chain.ToUint64(block); err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: scan log: %w", err)
	}
	t.LogIndex = uint(idx)
	t.ChainID = c.net.ChainID
	t.Token = c.net.Token
	t.Amount = money.New(c.net.Asset, amount)
	return t, nil
}

// Deposit mines one block holding a transfer of amount from from to to and
// returns the transfer as the feed reports it. It applies and consumes the
// sandbox's DuplicateLog and ShortDeposit faults. The sandbox row stays
// locked for the rest of tx.
func (c *Chain) Deposit(ctx context.Context, tx pgx.Tx, from, to string, amount money.Amount) (chain.Transfer, error) {
	if !c.net.Simulated {
		return chain.Transfer{}, ErrNotSimulated
	}
	if amount.Asset != c.net.Asset || amount.Minor <= 0 {
		return chain.Transfer{}, fmt.Errorf("%w: %s on %s", ErrBadAmount, amount, c.net.Key)
	}
	fromAddr, err := chain.NormalizeAddress(from)
	if err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: deposit from: %w", err)
	}
	toAddr, err := chain.NormalizeAddress(to)
	if err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: deposit to: %w", err)
	}
	sb, err := store.LockSandbox(ctx, tx, c.sandbox)
	if err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: deposit: lock sandbox: %w", err)
	}
	faults := sb.Faults

	canonical := amount.Minor
	if faults.ShortDeposit {
		canonical -= max(money.ApplyBps(amount, shortBps).Minor, 1)
		if canonical <= 0 {
			return chain.Transfer{}, fmt.Errorf("%w: %s is too small to short", ErrBadAmount, amount)
		}
	}

	head, err := c.head(ctx, tx)
	if err != nil {
		return chain.Transfer{}, err
	}
	number := head.Number + 1
	block, salt, err := c.mineBlock(ctx, tx, number)
	if err != nil {
		return chain.Transfer{}, err
	}
	txHash := c.hash("tx", number, salt)
	// Log 0 is the real transfer. A duplicated log 1 exists only in the feed.
	logs := []simLog{{index: 0, canonical: &canonical}}
	if faults.DuplicateLog {
		logs = append(logs, simLog{index: 1})
	}
	n, err := chain.ToInt64(number)
	if err != nil {
		return chain.Transfer{}, fmt.Errorf("sim: deposit: %w", err)
	}
	for _, l := range logs {
		if _, err := tx.Exec(ctx, `INSERT INTO sim_logs
			(sandbox_id, network, block_number, block_hash, tx_hash, log_index, from_addr, to_addr,
			 feed_amount, canonical_amount, in_feed, canonical)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, true, $11)`,
			c.sandbox, c.net.Key, n, block, txHash, l.index, fromAddr, toAddr,
			amount.Minor, l.canonical, l.canonical != nil); err != nil {
			return chain.Transfer{}, fmt.Errorf("sim: deposit: insert log: %w", err)
		}
	}

	if faults.DuplicateLog || faults.ShortDeposit {
		faults.DuplicateLog, faults.ShortDeposit = false, false
		if _, err := store.SetFaults(ctx, tx, c.sandbox, faults); err != nil {
			return chain.Transfer{}, fmt.Errorf("sim: deposit: consume faults: %w", err)
		}
	}
	return chain.Transfer{
		ChainID:     c.net.ChainID,
		Token:       c.net.Token,
		TxHash:      txHash,
		LogIndex:    0,
		BlockNumber: number,
		BlockHash:   block,
		From:        fromAddr,
		To:          toAddr,
		Amount:      amount,
	}, nil
}

// Mine adds n empty blocks and returns the new head. If the sandbox's Reorg
// fault is set and there is a deposit to reorganize, it first replaces the
// block of the most recent confirmed deposit (or, if nothing was confirmed
// yet, the most recent deposit) with an empty block under a new hash, and
// consumes the fault. Until there is a deposit the fault stays set.
func (c *Chain) Mine(ctx context.Context, tx pgx.Tx, n int) (chain.Head, error) {
	if !c.net.Simulated {
		return chain.Head{}, ErrNotSimulated
	}
	if n < 0 {
		return chain.Head{}, fmt.Errorf("sim: mine: negative block count %d", n)
	}
	sb, err := store.LockSandbox(ctx, tx, c.sandbox)
	if err != nil {
		return chain.Head{}, fmt.Errorf("sim: mine: lock sandbox: %w", err)
	}
	if sb.Faults.Reorg {
		done, err := c.reorg(ctx, tx)
		if err != nil {
			return chain.Head{}, err
		}
		if done {
			faults := sb.Faults
			faults.Reorg = false
			if _, err := store.SetFaults(ctx, tx, c.sandbox, faults); err != nil {
				return chain.Head{}, fmt.Errorf("sim: mine: consume reorg: %w", err)
			}
		}
	}
	head, err := c.head(ctx, tx)
	if err != nil {
		return chain.Head{}, err
	}
	for range n {
		head.Number++
		if head.Hash, _, err = c.mineBlock(ctx, tx, head.Number); err != nil {
			return chain.Head{}, err
		}
	}
	return head, nil
}

// reorg replaces the block of the most recent confirmed deposit, falling
// back to the most recent canonical deposit. It reports whether a block was
// replaced.
func (c *Chain) reorg(ctx context.Context, tx pgx.Tx) (bool, error) {
	var (
		number int64
		hash   string
	)
	err := tx.QueryRow(ctx, `SELECT b.number, b.hash FROM chain_transfers t
		JOIN sim_blocks b ON b.sandbox_id = t.sandbox_id AND b.network = t.network AND b.hash = t.block_hash
		WHERE t.sandbox_id = $1 AND t.network = $2 AND t.status = 'confirmed' AND b.canonical
		ORDER BY b.number DESC LIMIT 1`, c.sandbox, c.net.Key).Scan(&number, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT b.number, b.hash FROM sim_logs l
			JOIN sim_blocks b ON b.sandbox_id = l.sandbox_id AND b.network = l.network AND b.hash = l.block_hash
			WHERE l.sandbox_id = $1 AND l.network = $2 AND l.canonical AND b.canonical
			ORDER BY b.number DESC LIMIT 1`, c.sandbox, c.net.Key).Scan(&number, &hash)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sim: reorg: find deposit block: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE sim_blocks SET canonical = false
		WHERE sandbox_id = $1 AND network = $2 AND hash = $3`, c.sandbox, c.net.Key, hash); err != nil {
		return false, fmt.Errorf("sim: reorg: retire block %d: %w", number, err)
	}
	u, err := chain.ToUint64(number)
	if err != nil {
		return false, fmt.Errorf("sim: reorg: %w", err)
	}
	if _, _, err := c.mineBlock(ctx, tx, u); err != nil {
		return false, err
	}
	return true, nil
}

// mineBlock inserts a canonical block at number and returns its hash and
// salt. The salt counts earlier blocks at that number, so a replacement
// block gets a new hash.
func (c *Chain) mineBlock(ctx context.Context, q store.Querier, number uint64) (string, int64, error) {
	n, err := chain.ToInt64(number)
	if err != nil {
		return "", 0, fmt.Errorf("sim: mine block: %w", err)
	}
	var salt int64
	if err := q.QueryRow(ctx, `SELECT count(*) FROM sim_blocks
		WHERE sandbox_id = $1 AND network = $2 AND number = $3`,
		c.sandbox, c.net.Key, n).Scan(&salt); err != nil {
		return "", 0, fmt.Errorf("sim: mine block %d: %w", number, err)
	}
	hash := c.hash("block", number, salt)
	if _, err := q.Exec(ctx, `INSERT INTO sim_blocks (sandbox_id, network, number, hash, canonical)
		VALUES ($1, $2, $3, $4, true)`, c.sandbox, c.net.Key, n, hash); err != nil {
		return "", 0, fmt.Errorf("sim: mine block %d: %w", number, err)
	}
	return hash, salt, nil
}

func (c *Chain) head(ctx context.Context, q store.Querier) (chain.Head, error) {
	var (
		number int64
		hash   string
	)
	err := q.QueryRow(ctx, `SELECT number, hash FROM sim_blocks
		WHERE sandbox_id = $1 AND network = $2 AND canonical
		ORDER BY number DESC LIMIT 1`, c.sandbox, c.net.Key).Scan(&number, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Head{Number: 0, Hash: c.hash("block", 0, 0)}, nil
	}
	if err != nil {
		return chain.Head{}, fmt.Errorf("sim: head: %w", err)
	}
	u, err := chain.ToUint64(number)
	if err != nil {
		return chain.Head{}, fmt.Errorf("sim: head: %w", err)
	}
	return chain.Head{Number: u, Hash: hash}, nil
}

// hash is the deterministic 32-byte hash of a block or transaction.
func (c *Chain) hash(kind string, number uint64, salt int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "puente-sim|%s|%s|%s|%d|%d", c.sandbox, c.net.Key, kind, number, salt))
	return "0x" + hex.EncodeToString(sum[:])
}
