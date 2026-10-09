package recon

import (
	"context"
	"fmt"
	"slices"

	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxRailOrders bounds how many rail orders one run reads (spei's cap).
const maxRailOrders = 1000

// RailMatcher compares every payout's ledger entries with the rail's order
// for the same clave de rastreo.
//
// The ledger side comes from the journal: which payout steps were posted
// and how many pesos the submit entry put in flight (account 2100). The
// rail side is the simulated bank's order book. Payout statuses are not
// consulted, so a bug that moves a status without posting is still caught.
type RailMatcher struct {
	Rail spei.Rail
}

// Name implements Matcher.
func (RailMatcher) Name() string { return "rail_orders" }

// ledgerPayout is what the journal says about one payout.
type ledgerPayout struct {
	ID    uuid.UUID
	Clave string
	// Steps are the ledger kinds posted for the payout.
	Steps []ledger.Kind
	// InFlight is the pesos the submit entry credited to 2100 (positive).
	InFlight int64
}

func (p ledgerPayout) has(k ledger.Kind) bool { return slices.Contains(p.Steps, k) }

// Match implements Matcher.
func (m RailMatcher) Match(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]Break, int, error) {
	payouts, err := ledgerPayouts(ctx, tx, sandbox)
	if err != nil {
		return nil, 0, err
	}
	orders, err := m.Rail.Orders(ctx, tx, sandbox, maxRailOrders)
	if err != nil {
		return nil, 0, err
	}
	byClave := make(map[string]spei.Record, len(orders))
	for _, o := range orders {
		byClave[o.Clave] = o
	}

	var breaks []Break
	known := make(map[string]bool, len(payouts))
	for _, p := range payouts {
		known[p.Clave] = true
		if !p.has(ledger.KindPayoutSubmit) {
			continue // still reserved or cancelled: nothing should be on the rail
		}
		o, ok := byClave[p.Clave]
		if !ok {
			breaks = append(breaks, payoutBreak(p, MissingOnRail,
				"The ledger put pesos in flight for this payout, but the bank has no order with its clave.",
				money.New(money.MXN, p.InFlight).String(), "no order", nil))
			continue
		}
		if o.Amount.Minor != p.InFlight {
			breaks = append(breaks, payoutBreak(p, AmountMismatch,
				"The bank's order amount differs from what the ledger put in flight.",
				money.New(money.MXN, p.InFlight).String(), o.Amount.String(), &o))
		}
		breaks = append(breaks, stepBreaks(p, o)...)
	}
	for _, o := range orders {
		if known[o.Clave] || o.Status == spei.StatusRechazado {
			continue
		}
		breaks = append(breaks, Break{
			Kind: MissingOnLedger, Source: SourceRail, SubjectType: "spei_order", SubjectID: o.Clave,
			Summary:  "The bank shows an order no payout in Puente sent (keyed in at the bank's portal?).",
			Expected: "no order", Actual: o.Amount.String() + " " + string(o.Status),
			Evidence: orderEvidence(&o),
		})
	}
	return breaks, len(payouts) + len(orders), nil
}

// stepBreaks compares the payout steps the ledger posted with the order's
// status at the bank.
func stepBreaks(p ledgerPayout, o spei.Record) []Break {
	settled := p.has(ledger.KindPayoutSettle)
	returned := p.has(ledger.KindPayoutReturn)
	released := p.has(ledger.KindPayoutRelease)
	var out []Break
	switch o.Status {
	case spei.StatusLiquidado, spei.StatusDevuelto:
		if !settled {
			out = append(out, payoutBreak(p, MissingOnLedger,
				"The bank settled this order, but the ledger never booked the settlement.",
				"settle entry", string(o.Status), &o))
		}
		if o.Status == spei.StatusDevuelto && !returned {
			out = append(out, payoutBreak(p, MissingOnLedger,
				"The bank returned this order (devolución), but the ledger never booked the return.",
				"return entry", string(o.Status), &o))
		}
	case spei.StatusRechazado:
		if !released {
			out = append(out, payoutBreak(p, MissingOnLedger,
				"The bank rejected this order, but the ledger still holds the pesos in flight.",
				"release entry", string(o.Status), &o))
		}
	case spei.StatusEnProceso:
	}
	if settled && o.Status != spei.StatusLiquidado && o.Status != spei.StatusDevuelto {
		out = append(out, payoutBreak(p, MissingOnRail,
			"The ledger booked a settlement the bank has not made.",
			"liquidado", string(o.Status), &o))
	}
	if returned && o.Status != spei.StatusDevuelto {
		out = append(out, payoutBreak(p, MissingOnRail,
			"The ledger booked a return the bank has not made.",
			"devuelto", string(o.Status), &o))
	}
	return out
}

func payoutBreak(p ledgerPayout, kind Kind, summary, expected, actual string, o *spei.Record) Break {
	evidence := orderEvidence(o)
	evidence["clave"] = p.Clave
	evidence["ledger_steps"] = fmt.Sprint(p.Steps)
	return Break{
		Kind: kind, Source: SourceRail, SubjectType: "payout", SubjectID: p.ID.String(),
		Summary: summary, Expected: expected, Actual: actual, Evidence: evidence,
	}
}

func orderEvidence(o *spei.Record) map[string]string {
	ev := map[string]string{}
	if o == nil {
		return ev
	}
	ev["clave"] = o.Clave
	ev["rail_status"] = string(o.Status)
	ev["rail_amount"] = o.Amount.String()
	if o.Reason != "" {
		ev["rail_reason"] = o.Reason
	}
	if o.CEP != nil {
		ev["cep_sello"] = o.CEP.Sello
	}
	return ev
}

// ledgerPayouts reads, for every payout in the sandbox, the steps its
// journal entries record and the pesos put in flight.
func ledgerPayouts(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]ledgerPayout, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.clave,
		       COALESCE(array_agg(DISTINCT e.kind) FILTER (WHERE e.kind IS NOT NULL), '{}'),
		       COALESCE(-sum(po.amount_minor) FILTER (
		           WHERE e.kind = 'payout_submit' AND po.account_code = 2100), 0)::bigint
		FROM payouts p
		LEFT JOIN journal_entries e
		       ON e.sandbox_id = p.sandbox_id AND e.subject_type = 'payout' AND e.subject_id = p.id::text
		LEFT JOIN postings po ON po.entry_id = e.id
		WHERE p.sandbox_id = $1
		GROUP BY p.id, p.clave, p.created_at
		ORDER BY p.created_at, p.id`, sandbox)
	if err != nil {
		return nil, fmt.Errorf("ledger payouts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledgerPayout, error) {
		var (
			p     ledgerPayout
			steps []string
		)
		if err := row.Scan(&p.ID, &p.Clave, &steps, &p.InFlight); err != nil {
			return ledgerPayout{}, err
		}
		for _, s := range steps {
			p.Steps = append(p.Steps, ledger.Kind(s))
		}
		return p, nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger payouts: %w", err)
	}
	return out, nil
}

// BalanceMatcher compares the ledger's SPEI settlement account (1100) with
// the bank statement's balance.
type BalanceMatcher struct {
	Rail spei.Rail
}

// Name implements Matcher.
func (BalanceMatcher) Name() string { return "balances" }

// Match implements Matcher.
func (m BalanceMatcher) Match(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]Break, int, error) {
	book, err := ledger.Balance(ctx, tx, sandbox, ledger.SPEISettlement, money.MXN)
	if err != nil {
		return nil, 0, err
	}
	bank, err := m.Rail.StatementBalance(ctx, tx, sandbox)
	if err != nil {
		return nil, 0, err
	}
	if book.Minor == bank.Minor {
		return nil, 1, nil
	}
	diff := money.New(money.MXN, bank.Minor-book.Minor)
	return []Break{{
		Kind: BalanceMismatch, Source: SourceBalance,
		SubjectType: "account", SubjectID: ledger.SPEISettlement.String(),
		Summary:  "The SPEI settlement account in the ledger differs from the bank statement (unbooked bank charge?).",
		Expected: book.String(), Actual: bank.String(),
		Evidence: map[string]string{"difference": diff.String()},
	}}, 1, nil
}
