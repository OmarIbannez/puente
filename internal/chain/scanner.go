package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Defaults of DefaultScanner.
const (
	// DefaultInitialLookback is how many blocks behind the head the first
	// scan of a live network starts, instead of genesis.
	DefaultInitialLookback = 1000
	// DefaultReorgDepth is how many recent blocks the reorg check covers.
	DefaultReorgDepth = 64
)

// ErrBadScanner is returned for a Scanner with an invalid configuration.
var ErrBadScanner = errors.New("chain: invalid scanner configuration")

// Scanner pulls a Source's feed into chain_transfers. The zero value is not
// usable; start from DefaultScanner.
type Scanner struct {
	// MaxRange is the widest Logs window in blocks, 1..MaxLogRange.
	MaxRange uint64
	// InitialLookback is how far behind the head the first scan of a live
	// (non-simulated) network starts. Simulated networks start at genesis.
	InitialLookback uint64
	// ReorgDepth is how many blocks behind the head stored transfers are
	// re-checked against the canonical chain on every scan (at least 1).
	ReorgDepth uint64
}

// DefaultScanner pages at MaxLogRange, starts live networks 1,000 blocks
// back and re-checks the last 64 blocks for reorgs.
var DefaultScanner = Scanner{
	MaxRange:        MaxLogRange,
	InitialLookback: DefaultInitialLookback,
	ReorgDepth:      DefaultReorgDepth,
}

// ScanResult counts what one scan did.
type ScanResult struct {
	// Head is the chain tip the scan ran up to.
	Head Head `json:"head"`
	// From is the first block scanned; Next is the cursor afterwards.
	From uint64 `json:"from"`
	// Next is the block the next scan starts at.
	Next uint64 `json:"next"`
	// Fetched is the number of feed transfers returned by Logs.
	Fetched int64 `json:"fetched"`
	// New is the number of transfers inserted.
	New int64 `json:"new"`
	// Revived is the number of orphaned transfers whose log reappeared in a
	// different canonical block and went back to seen.
	Revived int64 `json:"revived"`
	// Confirmed is the number of transfers that reached the confirmation
	// depth during this scan.
	Confirmed int64 `json:"confirmed"`
	// Orphaned is the number of transfers whose block was reorganized away.
	Orphaned int64 `json:"orphaned"`
	// Windows is the number of Logs calls made.
	Windows int64 `json:"windows"`
}

// Scan runs DefaultScanner.Scan.
func Scan(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net Network, src Source, watch []string, confirmations uint64) (ScanResult, error) {
	return DefaultScanner.Scan(ctx, tx, sandbox, net, src, watch, confirmations)
}

// Scan brings the sandbox's chain_transfers for net up to src's head:
//
//  1. It re-checks the block hash of every seen or confirmed transfer in the
//     last ReorgDepth blocks. A transfer whose block is gone or has a new
//     hash is marked orphaned, and the cursor rewinds to rescan that block.
//  2. It pulls the feed from the cursor to the head in windows of at most
//     MaxRange blocks and inserts each transfer as seen. Rescanning a range
//     inserts nothing.
//  3. It marks seen transfers confirmed once head - block + 1 ≥
//     confirmations (0 is treated as 1).
//
// The sandbox row is locked for the rest of tx, so concurrent scans of one
// sandbox are serialized. Zero-value transfers are skipped (there is
// nothing to credit). Scan never posts to the ledger.
func (s Scanner) Scan(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net Network, src Source, watch []string, confirmations uint64) (ScanResult, error) {
	if s.MaxRange < 1 || s.MaxRange > MaxLogRange || s.ReorgDepth < 1 {
		return ScanResult{}, fmt.Errorf("%w: %+v", ErrBadScanner, s)
	}
	if src.ChainID() != net.ChainID {
		return ScanResult{}, fmt.Errorf("chain: scan %s: source chain id %d, want %d", net.Key, src.ChainID(), net.ChainID)
	}
	token, err := NormalizeAddress(net.Token)
	if err != nil {
		return ScanResult{}, fmt.Errorf("chain: scan %s token: %w", net.Key, err)
	}
	watch, err = NormalizeAddresses(watch)
	if err != nil {
		return ScanResult{}, fmt.Errorf("chain: scan %s watch list: %w", net.Key, err)
	}
	if _, err := store.LockSandbox(ctx, tx, sandbox); err != nil {
		return ScanResult{}, fmt.Errorf("chain: scan %s: lock sandbox: %w", net.Key, err)
	}
	head, err := src.Head(ctx)
	if err != nil {
		return ScanResult{}, fmt.Errorf("chain: scan %s: head: %w", net.Key, err)
	}
	res := ScanResult{Head: head}

	next, found, err := Cursor(ctx, tx, sandbox, net.Key)
	if err != nil {
		return res, err
	}
	if !found {
		next = s.start(net, head.Number)
	}
	rewind, err := s.checkReorgs(ctx, tx, sandbox, net, src, head.Number, &res)
	if err != nil {
		return res, err
	}
	if rewind < next {
		next = rewind
	}
	res.From = next

	for from := next; from <= head.Number; {
		to := head.Number
		if head.Number-from >= s.MaxRange {
			to = from + s.MaxRange - 1
		}
		logs, err := src.Logs(ctx, from, to, token, watch)
		if err != nil {
			return res, fmt.Errorf("chain: scan %s: logs %d..%d: %w", net.Key, from, to, err)
		}
		res.Windows++
		res.Fetched += int64(len(logs))
		for _, t := range logs {
			if err := upsert(ctx, tx, sandbox, net, token, t, &res); err != nil {
				return res, err
			}
		}
		if to == head.Number {
			break
		}
		from = to + 1
	}

	// The cursor always lands just past the head: if the chain shrank in a
	// reorg, this rewinds it so the replacement blocks are scanned.
	res.Next = head.Number + 1
	if err := setCursor(ctx, tx, sandbox, net.Key, res.Next); err != nil {
		return res, err
	}
	n, err := confirm(ctx, tx, sandbox, net.Key, head.Number, max(confirmations, 1))
	if err != nil {
		return res, err
	}
	res.Confirmed = n
	return res, nil
}

// start is where the first scan of net begins.
func (s Scanner) start(net Network, head uint64) uint64 {
	if net.Simulated || head < s.InitialLookback {
		return 0
	}
	return head - s.InitialLookback
}

// checkReorgs orphans stored transfers whose block is no longer canonical
// and returns the lowest such block number (or the max uint64 if none).
func (s Scanner) checkReorgs(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net Network, src Source, head uint64, res *ScanResult) (uint64, error) {
	rewind := ^uint64(0)
	var lowest uint64
	if head >= s.ReorgDepth {
		lowest = head - s.ReorgDepth + 1
	}
	low, err := ToInt64(lowest)
	if err != nil {
		return rewind, fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	type block struct {
		Number int64
		Hash   string
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT block_number, block_hash FROM chain_transfers
		WHERE sandbox_id = $1 AND network = $2 AND status IN ('seen', 'confirmed') AND block_number >= $3
		ORDER BY block_number`, sandbox, net.Key, low)
	if err != nil {
		return rewind, fmt.Errorf("chain: scan %s: recent blocks: %w", net.Key, err)
	}
	blocks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[block])
	if err != nil {
		return rewind, fmt.Errorf("chain: scan %s: recent blocks: %w", net.Key, err)
	}
	for _, b := range blocks {
		number, err := ToUint64(b.Number)
		if err != nil {
			return rewind, fmt.Errorf("chain: scan %s: %w", net.Key, err)
		}
		hash, ok, err := src.BlockHash(ctx, number)
		if err != nil {
			return rewind, fmt.Errorf("chain: scan %s: block hash %d: %w", net.Key, number, err)
		}
		if ok && hash == b.Hash {
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE chain_transfers
			SET status = 'orphaned', orphaned_at = now(), updated_at = now()
			WHERE sandbox_id = $1 AND network = $2 AND block_number = $3 AND block_hash = $4
			  AND status IN ('seen', 'confirmed')`, sandbox, net.Key, b.Number, b.Hash)
		if err != nil {
			return rewind, fmt.Errorf("chain: scan %s: orphan block %d: %w", net.Key, number, err)
		}
		res.Orphaned += tag.RowsAffected()
		rewind = min(rewind, number)
	}
	return rewind, nil
}

// upsert inserts one feed transfer, or revives an orphaned one whose log
// reappeared in a different block. Anything else already stored is left as
// it is.
func upsert(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, net Network, token string, t Transfer, res *ScanResult) error {
	if err := validateFeed(net, token, &t); err != nil {
		return err
	}
	if t.Amount.Minor == 0 {
		return nil
	}
	logIndex, err := ToInt64(uint64(t.LogIndex))
	if err != nil {
		return fmt.Errorf("chain: scan %s: log index: %w", net.Key, err)
	}
	block, err := ToInt64(t.BlockNumber)
	if err != nil {
		return fmt.Errorf("chain: scan %s: block number: %w", net.Key, err)
	}
	var inserted bool
	err = tx.QueryRow(ctx, `INSERT INTO chain_transfers
		(sandbox_id, network, chain_id, asset, token, tx_hash, log_index, block_number, block_hash,
		 from_addr, to_addr, amount_minor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (sandbox_id, chain_id, tx_hash, log_index) DO UPDATE SET
			block_number = EXCLUDED.block_number, block_hash = EXCLUDED.block_hash,
			from_addr = EXCLUDED.from_addr, to_addr = EXCLUDED.to_addr,
			amount_minor = EXCLUDED.amount_minor, status = 'seen',
			confirmed_at = NULL, updated_at = now()
		WHERE chain_transfers.status = 'orphaned' AND chain_transfers.block_hash <> EXCLUDED.block_hash
		RETURNING xmax = 0`,
		sandbox, net.Key, t.ChainID, string(t.Amount.Asset), t.Token, t.TxHash, logIndex, block,
		t.BlockHash, t.From, t.To, t.Amount.Minor).Scan(&inserted)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil // already stored
	case err != nil:
		return fmt.Errorf("chain: scan %s: store transfer %s/%d: %w", net.Key, t.TxHash, t.LogIndex, err)
	case inserted:
		res.New++
	default:
		res.Revived++
	}
	return nil
}

// validateFeed checks and normalizes a transfer reported by a Source.
func validateFeed(net Network, token string, t *Transfer) error {
	if t.ChainID != net.ChainID {
		return fmt.Errorf("chain: scan %s: transfer on chain %d", net.Key, t.ChainID)
	}
	if t.Amount.Asset != net.Asset || t.Amount.Minor < 0 {
		return fmt.Errorf("chain: scan %s: transfer amount %s", net.Key, t.Amount)
	}
	var err error
	if t.Token, err = NormalizeAddress(t.Token); err != nil {
		return fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	if t.Token != token {
		return fmt.Errorf("chain: scan %s: transfer of token %s, want %s", net.Key, t.Token, token)
	}
	if t.From, err = NormalizeAddress(t.From); err != nil {
		return fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	if t.To, err = NormalizeAddress(t.To); err != nil {
		return fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	if t.TxHash, err = NormalizeHash(t.TxHash); err != nil {
		return fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	if t.BlockHash, err = NormalizeHash(t.BlockHash); err != nil {
		return fmt.Errorf("chain: scan %s: %w", net.Key, err)
	}
	return nil
}

func confirm(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, network string, head, confirmations uint64) (int64, error) {
	if confirmations > head+1 {
		return 0, nil
	}
	// head - block + 1 >= confirmations  <=>  block <= head + 1 - confirmations
	upTo, err := ToInt64(head + 1 - confirmations)
	if err != nil {
		return 0, fmt.Errorf("chain: confirm %s: %w", network, err)
	}
	tag, err := tx.Exec(ctx, `UPDATE chain_transfers
		SET status = 'confirmed', confirmed_at = now(), updated_at = now()
		WHERE sandbox_id = $1 AND network = $2 AND status = 'seen' AND block_number <= $3`,
		sandbox, network, upTo)
	if err != nil {
		return 0, fmt.Errorf("chain: confirm %s: %w", network, err)
	}
	return tag.RowsAffected(), nil
}

// Cursor returns the next block to scan for the sandbox's network, and
// false when the network has never been scanned.
func Cursor(ctx context.Context, q store.Querier, sandbox uuid.UUID, network string) (uint64, bool, error) {
	var next int64
	err := q.QueryRow(ctx,
		"SELECT next_block FROM chain_cursors WHERE sandbox_id = $1 AND network = $2",
		sandbox, network).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("chain: cursor %s: %w", network, err)
	}
	u, err := ToUint64(next)
	if err != nil {
		return 0, false, fmt.Errorf("chain: cursor %s: %w", network, err)
	}
	return u, true, nil
}

func setCursor(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, network string, next uint64) error {
	n, err := ToInt64(next)
	if err != nil {
		return fmt.Errorf("chain: set cursor %s: %w", network, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO chain_cursors (sandbox_id, network, next_block)
		VALUES ($1, $2, $3)
		ON CONFLICT (sandbox_id, network) DO UPDATE SET next_block = EXCLUDED.next_block, updated_at = now()`,
		sandbox, network, n); err != nil {
		return fmt.Errorf("chain: set cursor %s: %w", network, err)
	}
	return nil
}
