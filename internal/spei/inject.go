package spei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ManualClavePrefix marks orders keyed in at the bank's portal rather than
// sent by Puente.
const ManualClavePrefix = "MANUAL"

// ErrBadInjection is returned for a non-positive or non-MXN injected amount.
var ErrBadInjection = errors.New("spei: injected amount must be positive MXN")

// InjectBankFee books a bank charge on the settlement-account statement.
// Puente's ledger knows nothing about it, so reconciliation reports a
// balance break. It exists only for the demo's fault controls.
func (r Rail) InjectBankFee(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, amount money.Amount) (StatementLine, error) {
	if amount.Asset != money.MXN || amount.Minor <= 0 {
		return StatementLine{}, ErrBadInjection
	}
	if _, err := store.LockSandbox(ctx, tx, sandbox); err != nil {
		return StatementLine{}, fmt.Errorf("spei: lock sandbox: %w", err)
	}
	return insertStatement(ctx, tx, sandbox, "", KindBankFee, -amount.Minor)
}

// InjectManualOrder settles an order that someone keyed in at the bank's
// portal: it is liquidated at once and debited from the statement, but no
// payout in Puente has its clave, so reconciliation reports it as missing
// on the ledger. It exists only for the demo's fault controls.
func (r Rail) InjectManualOrder(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, beneficiaryCLABE, name string, amount money.Amount) (Record, error) {
	if amount.Asset != money.MXN || amount.Minor <= 0 {
		return Record{}, ErrBadInjection
	}
	ordering, err := r.ordering()
	if err != nil {
		return Record{}, err
	}
	if _, err := store.LockSandbox(ctx, tx, sandbox); err != nil {
		return Record{}, fmt.Errorf("spei: lock sandbox: %w", err)
	}
	var (
		now time.Time
		seq int64
	)
	if err := tx.QueryRow(ctx,
		"SELECT now(), count(*) + 1 FROM spei_orders WHERE sandbox_id = $1", sandbox).Scan(&now, &seq); err != nil {
		return Record{}, fmt.Errorf("spei: read clock: %w", err)
	}
	o := Order{
		Clave:            ManualClavePrefix + now.UTC().Format("20060102") + strconv.FormatInt(seq, 10),
		BeneficiaryCLABE: beneficiaryCLABE,
		BeneficiaryName:  name,
		Concepto:         "Transferencia manual",
		Amount:           amount,
	}
	if o, err = normalizeOrder(o); err != nil {
		return Record{}, err
	}
	body, err := json.Marshal(NewCEP(o, ordering, now))
	if err != nil {
		return Record{}, fmt.Errorf("spei: encode CEP: %w", err)
	}
	rec, err := scanRecord(tx.QueryRow(ctx,
		`INSERT INTO spei_orders (sandbox_id, clave, ordering_clabe, beneficiary_clabe,
		     beneficiary_name, concepto, amount_centavos, status, cep, liquidated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'liquidado', $8, $9)
		 RETURNING `+orderColumns,
		sandbox, o.Clave, ordering, o.BeneficiaryCLABE, o.BeneficiaryName, o.Concepto,
		o.Amount.Minor, body, now))
	if err != nil {
		return Record{}, fmt.Errorf("spei: insert manual order: %w", err)
	}
	if _, err := insertStatement(ctx, tx, sandbox, o.Clave, KindLiquidationDebit, -o.Amount.Minor); err != nil {
		return Record{}, err
	}
	return rec, nil
}
