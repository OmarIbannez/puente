package payout

import (
	"context"
	"fmt"
	"slices"

	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// edges is the state machine: the legal targets of each status.
var edges = map[Status][]Status{
	StatusReserved:    {StatusSubmitted, StatusReleased},
	StatusSubmitted:   {StatusLiquidated, StatusRejected, StatusNeedsReview},
	StatusNeedsReview: {StatusLiquidated, StatusRejected},
	StatusRejected:    {StatusReleased},
	StatusLiquidated:  {StatusReturned},
}

// CanTransition reports whether the state machine has the edge from → to.
func CanTransition(from, to Status) bool {
	return slices.Contains(edges[from], to)
}

// Journal entry steps, the last part of an entry ref "payout:<id>:<step>".
const (
	stepReserve = "reserve"
	stepSubmit  = "submit"
	stepSettle  = "settle"
	stepRelease = "release"
	stepReturn  = "return"
)

// transition moves payout id to status to inside tx: it locks the row,
// checks the edge, posts the edge's journal entry (if it has one), updates
// the status and appends an event. An illegal edge returns
// ErrIllegalTransition before anything is written.
func transition(ctx context.Context, tx pgx.Tx, sandbox, id uuid.UUID, to Status, detail map[string]any) (Payout, error) {
	p, err := lockPayout(ctx, tx, sandbox, id)
	if err != nil {
		return Payout{}, err
	}
	from := p.Status
	if !CanTransition(from, to) {
		return Payout{}, fmt.Errorf("%w: %s → %s (payout %s)", ErrIllegalTransition, from, to, id)
	}
	if e, ok := entryFor(p, from, to); ok {
		if _, _, err := ledger.Post(ctx, tx, e); err != nil {
			return Payout{}, fmt.Errorf("payout: post %s for %s → %s: %w", e.Kind, from, to, err)
		}
	}
	p, err = scanPayout(tx.QueryRow(ctx, `UPDATE payouts SET status = $3, updated_at = now()
		WHERE sandbox_id = $1 AND id = $2 RETURNING `+payoutColumns, sandbox, id, string(to)))
	if err != nil {
		return Payout{}, fmt.Errorf("payout: update status %s → %s: %w", from, to, err)
	}
	if err := insertEvent(ctx, tx, p, from, to, detail); err != nil {
		return Payout{}, err
	}
	return p, nil
}

// entryFor returns the journal entry booked by the edge from → to, if any.
func entryFor(p Payout, from, to Status) (ledger.Entry, bool) {
	switch {
	case from == StatusReserved && to == StatusSubmitted:
		return submitEntry(p), true
	case from == StatusReserved && to == StatusReleased:
		return cancelEntry(p), true
	case to == StatusLiquidated:
		return settleEntry(p), true
	case from == StatusRejected && to == StatusReleased:
		return rejectReleaseEntry(p), true
	case from == StatusLiquidated && to == StatusReturned:
		return returnEntry(p), true
	}
	return ledger.Entry{}, false
}

// entry builds a payout entry, dropping zero-amount postings (a zero fee).
func entry(p Payout, step string, kind ledger.Kind, memo string, postings ...ledger.Posting) ledger.Entry {
	postings = slices.DeleteFunc(postings, func(x ledger.Posting) bool { return x.Amount.IsZero() })
	return ledger.Entry{
		SandboxID: p.SandboxID,
		Ref:       fmt.Sprintf("payout:%s:%s", p.ID, step),
		Kind:      kind,
		Memo:      memo,
		Subject:   &ledger.Subject{Type: "payout", ID: p.ID.String()},
		Postings:  postings,
	}
}

// reserveEntry earmarks amount plus fee: Dr 2010 / Cr 2020 (customer).
func reserveEntry(p Payout) ledger.Entry {
	c, total := p.CustomerID, p.Total()
	return entry(p, stepReserve, ledger.KindPayoutReserve, "Reserve payout "+p.Clave,
		ledger.Debit(ledger.CustomerBalances, total).For(c),
		ledger.Credit(ledger.PayoutsReserved, total).For(c))
}

// submitEntry books the fee and the conversion as the order leaves:
// Dr 2020 total; Cr 4100 fee; Cr 3900 amount; Dr 3900 / Cr 2100 pesos.
func submitEntry(p Payout) ledger.Entry {
	return entry(p, stepSubmit, ledger.KindPayoutSubmit, "Submit payout "+p.Clave,
		ledger.Debit(ledger.PayoutsReserved, p.Total()).For(p.CustomerID),
		ledger.Credit(ledger.FeeIncome, p.Fee),
		ledger.Credit(ledger.FXClearing, p.Amount),
		ledger.Debit(ledger.FXClearing, p.MXN),
		ledger.Credit(ledger.SPEIInFlight, p.MXN))
}

// settleEntry clears a liquidated order: Dr 2100 / Cr 1100.
func settleEntry(p Payout) ledger.Entry {
	return entry(p, stepSettle, ledger.KindPayoutSettle, "Settle payout "+p.Clave,
		ledger.Debit(ledger.SPEIInFlight, p.MXN),
		ledger.Credit(ledger.SPEISettlement, p.MXN))
}

// cancelEntry reverses the reserve of a payout that never left:
// Dr 2020 / Cr 2010.
func cancelEntry(p Payout) ledger.Entry {
	c, total := p.CustomerID, p.Total()
	return entry(p, stepRelease, ledger.KindPayoutRelease, "Release cancelled payout "+p.Clave,
		ledger.Debit(ledger.PayoutsReserved, total).For(c),
		ledger.Credit(ledger.CustomerBalances, total).For(c))
}

// rejectReleaseEntry reverses the submit of a rejected order and refunds
// the fee: Dr 2100 / Cr 3900 pesos; Dr 3900 amount, Dr 4100 fee / Cr 2010.
func rejectReleaseEntry(p Payout) ledger.Entry {
	return entry(p, stepRelease, ledger.KindPayoutRelease, "Release rejected payout "+p.Clave,
		ledger.Debit(ledger.SPEIInFlight, p.MXN),
		ledger.Credit(ledger.FXClearing, p.MXN),
		ledger.Debit(ledger.FXClearing, p.Amount),
		ledger.Debit(ledger.FeeIncome, p.Fee),
		ledger.Credit(ledger.CustomerBalances, p.Total()).For(p.CustomerID))
}

// returnEntry books a devolución, keeping the fee: Dr 1100 / Cr 3900
// pesos; Dr 3900 / Cr 2010 amount.
func returnEntry(p Payout) ledger.Entry {
	return entry(p, stepReturn, ledger.KindPayoutReturn, "Return of payout "+p.Clave,
		ledger.Debit(ledger.SPEISettlement, p.MXN),
		ledger.Credit(ledger.FXClearing, p.MXN),
		ledger.Debit(ledger.FXClearing, p.Amount),
		ledger.Credit(ledger.CustomerBalances, p.Amount).For(p.CustomerID))
}

// fundingEntry is the treasury prefunding the SPEI account: Dr 1100 / Cr 3000.
func fundingEntry(sandbox uuid.UUID, amount money.Amount, ref string) ledger.Entry {
	return ledger.Entry{
		SandboxID: sandbox,
		Ref:       "funding:" + ref,
		Kind:      ledger.KindFunding,
		Memo:      "Treasury funding " + ref,
		Postings: []ledger.Posting{
			ledger.Debit(ledger.SPEISettlement, amount),
			ledger.Credit(ledger.TreasuryEquity, amount),
		},
	}
}
