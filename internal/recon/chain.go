package recon

import (
	"context"
	"fmt"
	"strconv"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SourceFunc returns the chain source for a network as seen from tx. The
// simulated chain lives in the database, so it must read through the run's
// snapshot transaction; an RPC-backed source can ignore tx.
type SourceFunc func(tx pgx.Tx, sandbox uuid.UUID) chain.Source

// ChainMatcher compares the deposits the ledger credited on one network with
// the canonical chain.
//
// The ledger credits what the indexer feed reported. The matcher re-reads
// every open credit's log from the canonical chain (Source.Lookup), so a
// feed that invented, doubled or misstated a transfer, or a reorg the
// scanner has not processed yet, shows up as a break.
type ChainMatcher struct {
	Network chain.Network
	Source  SourceFunc
}

// Name implements Matcher.
func (m ChainMatcher) Name() string { return "chain:" + m.Network.Key }

type openCredit struct {
	TransferID int64
	Generation int
	TxHash     string
	LogIndex   uint
	To         string
	Credited   money.Amount
	CustomerID uuid.UUID
}

type uncredited struct {
	TransferID int64
	TxHash     string
	To         string
	Amount     money.Amount
	Owned      bool
}

// Match implements Matcher.
func (m ChainMatcher) Match(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]Break, int, error) {
	credits, err := m.openCredits(ctx, tx, sandbox)
	if err != nil {
		return nil, 0, err
	}
	src := m.Source(tx, sandbox)

	var breaks []Break
	groups := map[string][]openCredit{}
	var order []string
	for _, c := range credits {
		key := c.TxHash + "|" + c.To + "|" + strconv.FormatInt(c.Credited.Minor, 10)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}
	for _, key := range order {
		group := groups[key]
		// Look every credit up: a batch transfer can legitimately emit two
		// logs to one address for one amount in one transaction. Only a
		// group with a credit the canonical chain lacks is a duplicate.
		var phantoms []openCredit
		for _, c := range group {
			canonical, found, err := src.Lookup(ctx, c.TxHash, c.LogIndex)
			if err != nil {
				return nil, 0, fmt.Errorf("lookup %s#%d: %w", c.TxHash, c.LogIndex, err)
			}
			switch {
			case !found:
				phantoms = append(phantoms, c)
			case canonical.Amount.Minor != c.Credited.Minor:
				breaks = append(breaks, m.creditBreak(c, AmountMismatch,
					"The canonical chain moved a different amount than the indexer reported and the ledger credited.",
					canonical.Amount.String(), c.Credited.String()))
			}
		}
		switch {
		case len(phantoms) > 0 && len(phantoms) < len(group):
			breaks = append(breaks, m.duplicateBreak(group, len(group)-len(phantoms)))
		default:
			for _, c := range phantoms {
				breaks = append(breaks, m.creditBreak(c, MissingOnChain,
					"The ledger credits a deposit the canonical chain does not have (reorged out, or never existed).",
					c.Credited.String(), "no such log"))
			}
		}
	}

	missing, err := m.uncredited(ctx, tx, sandbox)
	if err != nil {
		return nil, 0, err
	}
	for _, u := range missing {
		summary := "A confirmed deposit to a customer address has not been credited."
		if !u.Owned {
			summary = "A confirmed deposit went to an address no customer owns; it is unattributed."
		}
		breaks = append(breaks, Break{
			Kind: MissingOnLedger, Source: SourceChain,
			SubjectType: "chain_transfer", SubjectID: strconv.FormatInt(u.TransferID, 10),
			Summary: summary, Expected: "credit entry", Actual: "none",
			Evidence: m.evidence(u.TxHash, u.To, u.Amount),
		})
	}
	return breaks, len(credits) + len(missing), nil
}

// duplicateBreak reports a group of credits for one logical transfer of
// which only onChain are on the canonical chain.
func (m ChainMatcher) duplicateBreak(group []openCredit, onChain int) Break {
	c := group[0]
	ids := ""
	for i, g := range group {
		if i > 0 {
			ids += ", "
		}
		ids += strconv.FormatInt(g.TransferID, 10)
	}
	ev := m.evidence(c.TxHash, c.To, c.Credited)
	ev["transfer_ids"] = ids
	total := money.New(c.Credited.Asset, c.Credited.Minor*int64(len(group)))
	expected := money.New(c.Credited.Asset, c.Credited.Minor*int64(onChain))
	return Break{
		Kind: Duplicate, Source: SourceChain, SubjectType: "chain_tx", SubjectID: c.TxHash,
		Summary: fmt.Sprintf("%d on-chain log(s) were credited %d times (the indexer reported a log that isn't on the chain).",
			onChain, len(group)),
		Expected: expected.String(), Actual: total.String(), Evidence: ev,
	}
}

func (m ChainMatcher) creditBreak(c openCredit, kind Kind, summary, expected, actual string) Break {
	ev := m.evidence(c.TxHash, c.To, c.Credited)
	ev["log_index"] = strconv.FormatUint(uint64(c.LogIndex), 10)
	ev["customer_id"] = c.CustomerID.String()
	ev["generation"] = strconv.Itoa(c.Generation)
	return Break{
		Kind: kind, Source: SourceChain,
		SubjectType: "chain_transfer", SubjectID: strconv.FormatInt(c.TransferID, 10),
		Summary: summary, Expected: expected, Actual: actual, Evidence: ev,
	}
}

func (m ChainMatcher) evidence(txHash, to string, amount money.Amount) map[string]string {
	ev := map[string]string{"network": m.Network.Key, "tx_hash": txHash, "to": to, "amount": amount.String()}
	if url := m.Network.TxURL(txHash); url != "" {
		ev["explorer"] = url
	}
	return ev
}

func (m ChainMatcher) openCredits(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]openCredit, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, c.generation, t.tx_hash, t.log_index, t.to_addr, c.asset, c.amount_minor, c.customer_id
		FROM deposit_credits c
		JOIN chain_transfers t ON t.id = c.transfer_id AND t.sandbox_id = c.sandbox_id
		WHERE c.sandbox_id = $1 AND t.network = $2 AND c.reversal_entry_id IS NULL
		ORDER BY t.id`, sandbox, m.Network.Key)
	if err != nil {
		return nil, fmt.Errorf("open credits: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openCredit, error) {
		var (
			c        openCredit
			logIndex int64
			asset    string
			minor    int64
		)
		if err := row.Scan(&c.TransferID, &c.Generation, &c.TxHash, &logIndex, &c.To, &asset, &minor, &c.CustomerID); err != nil {
			return openCredit{}, err
		}
		idx, err := chain.ToUint64(logIndex)
		if err != nil {
			return openCredit{}, err
		}
		c.LogIndex = uint(idx)
		c.Credited = money.New(money.Asset(asset), minor)
		return c, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open credits: %w", err)
	}
	return out, nil
}

func (m ChainMatcher) uncredited(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]uncredited, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.tx_hash, t.to_addr, t.asset, t.amount_minor,
		       EXISTS (SELECT 1 FROM deposit_addresses a
		               WHERE a.sandbox_id = t.sandbox_id AND a.network = t.network AND a.address = t.to_addr)
		FROM chain_transfers t
		WHERE t.sandbox_id = $1 AND t.network = $2 AND t.status = 'confirmed'
		  AND NOT EXISTS (SELECT 1 FROM deposit_credits c
		                  WHERE c.transfer_id = t.id AND c.reversal_entry_id IS NULL)
		ORDER BY t.id`, sandbox, m.Network.Key)
	if err != nil {
		return nil, fmt.Errorf("uncredited transfers: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (uncredited, error) {
		var (
			u     uncredited
			asset string
			minor int64
		)
		if err := row.Scan(&u.TransferID, &u.TxHash, &u.To, &asset, &minor, &u.Owned); err != nil {
			return uncredited{}, err
		}
		u.Amount = money.New(money.Asset(asset), minor)
		return u, nil
	})
	if err != nil {
		return nil, fmt.Errorf("uncredited transfers: %w", err)
	}
	return out, nil
}
