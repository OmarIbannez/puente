package spei

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultOrderingCLABE is Puente's own (fictional) settlement account at STP,
// the ordering account of every simulated order unless Rail overrides it.
const DefaultOrderingCLABE = "646180500000000004"

// Status is the state of an order on the rail.
type Status string

// Order statuses, as SPEI participants report them.
const (
	// StatusEnProceso: accepted, waiting for the next settlement cycle.
	StatusEnProceso Status = "en_proceso"
	// StatusLiquidado: settled; the beneficiary bank was credited.
	StatusLiquidado Status = "liquidado"
	// StatusRechazado: refused; no money moved.
	StatusRechazado Status = "rechazado"
	// StatusDevuelto: settled and later sent back by the beneficiary bank.
	StatusDevuelto Status = "devuelto"
)

// Rejection and return reasons, in Spanish as a participant reports them.
const (
	// ReasonCLABEInvalida: the beneficiary CLABE is malformed.
	ReasonCLABEInvalida = "CLABE inválida"
	// ReasonInstitucionInexistente: the CLABE's bank is not a known SPEI participant.
	ReasonInstitucionInexistente = "Institución receptora inexistente"
	// ReasonCuentaInexistente: the beneficiary bank refused the account.
	ReasonCuentaInexistente = "Cuenta inexistente"
	// ReasonCuentaBloqueada: the beneficiary bank returned the funds.
	ReasonCuentaBloqueada = "Cuenta bloqueada"
)

// StatementKind is the type of a settlement-account statement line.
type StatementKind string

// Statement line kinds.
const (
	// KindFunding: the treasury prefunded the settlement account (positive).
	KindFunding StatementKind = "funding"
	// KindLiquidationDebit: an order settled (negative).
	KindLiquidationDebit StatementKind = "liquidation_debit"
	// KindReturnCredit: a settled order came back (positive).
	KindReturnCredit StatementKind = "return_credit"
)

// Errors returned by the simulated rail.
var (
	// ErrClaveConflict: the clave is already used by an order with different
	// details.
	ErrClaveConflict = errors.New("spei: clave de rastreo already used with different details")
	// ErrTimeout: the rail accepted the order but its acknowledgement was
	// lost. The order exists; resubmit with the same clave to learn its state.
	ErrTimeout = errors.New("spei: acknowledgement timed out")
	// ErrNotFound: no order with that clave in the sandbox.
	ErrNotFound = errors.New("spei: order not found")
	// ErrBadOrder: the order is malformed (caller error, not a rail outcome).
	ErrBadOrder = errors.New("spei: invalid order")
)

// defaultLimit and maxLimit bound list queries.
const (
	defaultLimit = 100
	maxLimit     = 1000
)

// Order is a payment order as submitted to the rail.
type Order struct {
	// Clave is the clave de rastreo; the rail dedupes on it.
	Clave string `json:"clave"`
	// BeneficiaryCLABE is the destination account. An invalid one is not a
	// caller error: the rail records the order as rechazado.
	BeneficiaryCLABE string `json:"beneficiary_clabe"`
	// BeneficiaryName is the account holder's name.
	BeneficiaryName string `json:"beneficiary_name"`
	// Concepto is the free-text payment concept.
	Concepto string `json:"concepto"`
	// Amount must be a positive MXN amount.
	Amount money.Amount `json:"amount"`
}

// Record is an order as the rail keeps it.
type Record struct {
	Order
	// ID is the rail's internal row id.
	ID int64 `json:"id"`
	// OrderingCLABE is the account the funds leave from.
	OrderingCLABE string `json:"ordering_clabe"`
	// Status is the current state.
	Status Status `json:"status"`
	// Reason explains a rechazado or devuelto status.
	Reason string `json:"reason,omitempty"`
	// ReturnPending marks a liquidated order the beneficiary bank will
	// return on a later settlement cycle (the RailReturn fault).
	ReturnPending bool `json:"return_pending"`
	// SubmitCount is how many times the clave was submitted.
	SubmitCount int `json:"submit_count"`
	// CEP is the receipt, present once liquidated.
	CEP *CEP `json:"cep,omitempty"`
	// CreatedAt is when the rail first accepted the clave.
	CreatedAt time.Time `json:"created_at"`
	// LiquidatedAt is when the order settled.
	LiquidatedAt *time.Time `json:"liquidated_at,omitempty"`
	// ReturnedAt is when the order was returned.
	ReturnedAt *time.Time `json:"returned_at,omitempty"`
	// UpdatedAt is the last change.
	UpdatedAt time.Time `json:"updated_at"`
}

// Ack is the rail's answer to Submit.
type Ack struct {
	Record
	// Duplicate is true when the clave was already known and no new order
	// was created.
	Duplicate bool `json:"duplicate"`
}

// StatusChange is one transition made by Advance.
type StatusChange struct {
	Clave  string `json:"clave"`
	From   Status `json:"from"`
	To     Status `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// StatementLine is one movement of the participant's settlement account.
type StatementLine struct {
	ID int64 `json:"id"`
	// Clave links the line to an order; empty for funding.
	Clave  string        `json:"clave,omitempty"`
	Kind   StatementKind `json:"kind"`
	Amount money.Amount  `json:"amount"`
	// CreatedAt is when the movement was booked.
	CreatedAt time.Time `json:"created_at"`
}

// Rail is the simulated SPEI participant. Its state lives in spei_orders and
// spei_statement, per sandbox; the zero value uses DefaultOrderingCLABE.
type Rail struct {
	// OrderingCLABE is Puente's account at the participant.
	OrderingCLABE string
}

// Fund books a funding line: the treasury prefunds the settlement account.
// amount must be positive MXN.
func (r Rail) Fund(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, amount money.Amount) (StatementLine, error) {
	if amount.Asset != money.MXN || amount.Minor <= 0 {
		return StatementLine{}, fmt.Errorf("%w: funding must be positive MXN, got %s", ErrBadOrder, amount)
	}
	return insertStatement(ctx, tx, sandbox, "", KindFunding, amount.Minor)
}

// Submit sends an order to the rail. It is idempotent on the clave:
//
//   - The same clave with identical details returns the existing order's
//     current state with Duplicate set and bumps its submit count; no new
//     order and no statement line.
//   - The same clave with different details is ErrClaveConflict.
//   - A new order with an invalid beneficiary CLABE is stored as rechazado.
//   - Otherwise the sandbox's rail faults apply, each consumed by one order:
//     RailReject stores it rechazado ("Cuenta inexistente"); RailTimeout
//     stores it en_proceso but returns ErrTimeout, as if the ack were lost;
//     RailReturn marks it to be returned after it liquidates.
//   - Otherwise the order is stored en_proceso.
//
// Submit locks the sandbox row, so concurrent submits serialize. On
// ErrTimeout the order and the consumed fault are written to tx: the caller
// must still commit tx, as a real rail would keep the order.
func (r Rail) Submit(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, o Order) (Ack, error) {
	ordering, err := r.ordering()
	if err != nil {
		return Ack{}, err
	}
	o, err = normalizeOrder(o)
	if err != nil {
		return Ack{}, err
	}
	sb, err := store.LockSandbox(ctx, tx, sandbox)
	if err != nil {
		return Ack{}, fmt.Errorf("spei: lock sandbox: %w", err)
	}

	existing, err := r.Order(ctx, tx, sandbox, o.Clave)
	switch {
	case err == nil:
		if existing.Order != o || existing.OrderingCLABE != ordering {
			return Ack{}, fmt.Errorf("%w: %s", ErrClaveConflict, o.Clave)
		}
		rec, err := scanRecord(tx.QueryRow(ctx,
			`UPDATE spei_orders SET submit_count = submit_count + 1, updated_at = now()
			 WHERE id = $1 RETURNING `+orderColumns, existing.ID))
		if err != nil {
			return Ack{}, fmt.Errorf("spei: resubmit %s: %w", o.Clave, err)
		}
		return Ack{Record: rec, Duplicate: true}, nil
	case !errors.Is(err, ErrNotFound):
		return Ack{}, err
	}

	status, reason := StatusEnProceso, ""
	faults := sb.Faults
	var timedOut, returnPending bool
	if _, verr := ValidateCLABE(o.BeneficiaryCLABE); verr != nil {
		status, reason = StatusRechazado, rejectReason(verr)
	} else if faults.RailReject {
		status, reason = StatusRechazado, ReasonCuentaInexistente
		faults.RailReject = false
	} else {
		timedOut, returnPending = faults.RailTimeout, faults.RailReturn
		faults.RailTimeout, faults.RailReturn = false, false
	}
	if faults != sb.Faults {
		if _, err := store.SetFaults(ctx, tx, sandbox, faults); err != nil {
			return Ack{}, fmt.Errorf("spei: consume rail fault: %w", err)
		}
	}

	rec, err := scanRecord(tx.QueryRow(ctx,
		`INSERT INTO spei_orders (sandbox_id, clave, ordering_clabe, beneficiary_clabe,
		     beneficiary_name, concepto, amount_centavos, status, reason, return_pending)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+orderColumns,
		sandbox, o.Clave, ordering, o.BeneficiaryCLABE, o.BeneficiaryName, o.Concepto,
		o.Amount.Minor, string(status), reason, returnPending))
	if err != nil {
		return Ack{}, fmt.Errorf("spei: insert order %s: %w", o.Clave, err)
	}
	if timedOut {
		return Ack{}, fmt.Errorf("%w: clave %s", ErrTimeout, o.Clave)
	}
	return Ack{Record: rec}, nil
}

// Advance runs one simulated settlement cycle for the sandbox and returns the
// transitions it made, in order. Every liquidado order marked ReturnPending
// (liquidated in an earlier cycle) becomes devuelto with a return_credit
// line; then every en_proceso order becomes liquidado with a
// liquidation_debit line and a CEP. There is no randomness: the outcome
// depends only on the stored orders. The settlement account may go negative;
// that is for reconciliation to notice.
func (r Rail) Advance(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID) ([]StatusChange, error) {
	if _, err := store.LockSandbox(ctx, tx, sandbox); err != nil {
		return nil, fmt.Errorf("spei: lock sandbox: %w", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return nil, fmt.Errorf("spei: read clock: %w", err)
	}

	// Returns first, so an order liquidated below waits for a later cycle.
	returning, err := ordersWhere(ctx, tx, sandbox,
		"status = 'liquidado' AND return_pending")
	if err != nil {
		return nil, err
	}
	changes := make([]StatusChange, 0, len(returning))
	for _, rec := range returning {
		if _, err := tx.Exec(ctx,
			`UPDATE spei_orders SET status = 'devuelto', reason = $2, return_pending = false,
			     returned_at = $3, updated_at = $3
			 WHERE id = $1`, rec.ID, ReasonCuentaBloqueada, now); err != nil {
			return nil, fmt.Errorf("spei: return %s: %w", rec.Clave, err)
		}
		if _, err := insertStatement(ctx, tx, sandbox, rec.Clave, KindReturnCredit, rec.Amount.Minor); err != nil {
			return nil, err
		}
		changes = append(changes, StatusChange{
			Clave: rec.Clave, From: StatusLiquidado, To: StatusDevuelto, Reason: ReasonCuentaBloqueada,
		})
	}

	pending, err := ordersWhere(ctx, tx, sandbox, "status = 'en_proceso'")
	if err != nil {
		return nil, err
	}
	for _, rec := range pending {
		body, err := json.Marshal(NewCEP(rec.Order, rec.OrderingCLABE, now))
		if err != nil {
			return nil, fmt.Errorf("spei: encode CEP %s: %w", rec.Clave, err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE spei_orders SET status = 'liquidado', cep = $2, liquidated_at = $3, updated_at = $3
			 WHERE id = $1`, rec.ID, body, now); err != nil {
			return nil, fmt.Errorf("spei: liquidate %s: %w", rec.Clave, err)
		}
		if _, err := insertStatement(ctx, tx, sandbox, rec.Clave, KindLiquidationDebit, -rec.Amount.Minor); err != nil {
			return nil, err
		}
		changes = append(changes, StatusChange{Clave: rec.Clave, From: StatusEnProceso, To: StatusLiquidado})
	}
	return changes, nil
}

// Order returns the order with clave in the sandbox, or ErrNotFound.
func (r Rail) Order(ctx context.Context, q store.Querier, sandbox uuid.UUID, clave string) (Record, error) {
	rec, err := scanRecord(q.QueryRow(ctx,
		"SELECT "+orderColumns+" FROM spei_orders WHERE sandbox_id = $1 AND clave = $2",
		sandbox, clave))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, clave)
	}
	if err != nil {
		return Record{}, fmt.Errorf("spei: read order %s: %w", clave, err)
	}
	return rec, nil
}

// Orders returns the sandbox's most recent orders, newest first. A limit
// of zero or less means 100; it is capped at 1000.
func (r Rail) Orders(ctx context.Context, q store.Querier, sandbox uuid.UUID, limit int) ([]Record, error) {
	rows, err := q.Query(ctx,
		"SELECT "+orderColumns+" FROM spei_orders WHERE sandbox_id = $1 ORDER BY id DESC LIMIT $2",
		sandbox, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("spei: list orders: %w", err)
	}
	recs, err := pgx.CollectRows(rows, collectRecord)
	if err != nil {
		return nil, fmt.Errorf("spei: list orders: %w", err)
	}
	return recs, nil
}

// OrdersByClave returns the sandbox's orders whose clave is in claves, keyed
// by clave, in one query. Claves with no order are absent from the map.
func (r Rail) OrdersByClave(ctx context.Context, q store.Querier, sandbox uuid.UUID, claves []string) (map[string]Record, error) {
	out := make(map[string]Record, len(claves))
	if len(claves) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx,
		"SELECT "+orderColumns+" FROM spei_orders WHERE sandbox_id = $1 AND clave = ANY($2)",
		sandbox, claves)
	if err != nil {
		return nil, fmt.Errorf("spei: orders by clave: %w", err)
	}
	recs, err := pgx.CollectRows(rows, collectRecord)
	if err != nil {
		return nil, fmt.Errorf("spei: orders by clave: %w", err)
	}
	for _, rec := range recs {
		out[rec.Clave] = rec
	}
	return out, nil
}

// StatementBalance returns the settlement account's balance in MXN:
// funding minus liquidations plus returns.
func (r Rail) StatementBalance(ctx context.Context, q store.Querier, sandbox uuid.UUID) (money.Amount, error) {
	var minor int64
	if err := q.QueryRow(ctx,
		"SELECT COALESCE(sum(amount_centavos), 0)::bigint FROM spei_statement WHERE sandbox_id = $1",
		sandbox).Scan(&minor); err != nil {
		return money.Amount{}, fmt.Errorf("spei: statement balance: %w", err)
	}
	return money.New(money.MXN, minor), nil
}

// Statement returns the settlement account's most recent lines, newest
// first. A limit of zero or less means 100; it is capped at 1000.
func (r Rail) Statement(ctx context.Context, q store.Querier, sandbox uuid.UUID, limit int) ([]StatementLine, error) {
	rows, err := q.Query(ctx,
		"SELECT "+statementColumns+" FROM spei_statement WHERE sandbox_id = $1 ORDER BY id DESC LIMIT $2",
		sandbox, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("spei: statement: %w", err)
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StatementLine, error) {
		return scanStatementLine(row)
	})
	if err != nil {
		return nil, fmt.Errorf("spei: statement: %w", err)
	}
	return lines, nil
}

func (r Rail) ordering() (string, error) {
	raw := r.OrderingCLABE
	if raw == "" {
		raw = DefaultOrderingCLABE
	}
	c, err := ValidateCLABE(raw)
	if err != nil {
		return "", fmt.Errorf("spei: ordering account: %w", err)
	}
	return c.Number, nil
}

// normalizeOrder checks the caller-side invariants and strips separators
// from the beneficiary CLABE so equal accounts compare equal.
func normalizeOrder(o Order) (Order, error) {
	if err := ValidateClave(o.Clave); err != nil {
		return Order{}, err
	}
	if o.Amount.Asset != money.MXN || o.Amount.Minor <= 0 {
		return Order{}, fmt.Errorf("%w: amount must be positive MXN, got %s", ErrBadOrder, o.Amount)
	}
	o.BeneficiaryCLABE = normalizeCLABE(o.BeneficiaryCLABE)
	return o, nil
}

func rejectReason(err error) string {
	if errors.Is(err, ErrUnknownBank) {
		return ReasonInstitucionInexistente
	}
	return ReasonCLABEInvalida
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	return min(limit, maxLimit)
}

const orderColumns = `id, clave, ordering_clabe, beneficiary_clabe, beneficiary_name, concepto,
	amount_centavos, status, reason, return_pending, submit_count, cep,
	created_at, liquidated_at, returned_at, updated_at`

func scanRecord(row pgx.Row) (Record, error) {
	var (
		rec    Record
		minor  int64
		status string
		cep    []byte
	)
	if err := row.Scan(&rec.ID, &rec.Clave, &rec.OrderingCLABE, &rec.BeneficiaryCLABE,
		&rec.BeneficiaryName, &rec.Concepto, &minor, &status, &rec.Reason,
		&rec.ReturnPending, &rec.SubmitCount, &cep,
		&rec.CreatedAt, &rec.LiquidatedAt, &rec.ReturnedAt, &rec.UpdatedAt); err != nil {
		return Record{}, err
	}
	rec.Amount = money.New(money.MXN, minor)
	rec.Status = Status(status)
	if cep != nil {
		rec.CEP = new(CEP)
		if err := json.Unmarshal(cep, rec.CEP); err != nil {
			return Record{}, fmt.Errorf("spei: decode CEP: %w", err)
		}
	}
	return rec, nil
}

func collectRecord(row pgx.CollectableRow) (Record, error) { return scanRecord(row) }

// ordersWhere returns the sandbox's orders matching cond (a fixed SQL
// predicate, never user input) in id order, locked for update.
func ordersWhere(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, cond string) ([]Record, error) {
	rows, err := tx.Query(ctx,
		"SELECT "+orderColumns+" FROM spei_orders WHERE sandbox_id = $1 AND "+cond+" ORDER BY id FOR UPDATE",
		sandbox)
	if err != nil {
		return nil, fmt.Errorf("spei: select orders: %w", err)
	}
	recs, err := pgx.CollectRows(rows, collectRecord)
	if err != nil {
		return nil, fmt.Errorf("spei: select orders: %w", err)
	}
	return recs, nil
}

const statementColumns = "id, clave, kind, amount_centavos, created_at"

func scanStatementLine(row pgx.Row) (StatementLine, error) {
	var (
		l     StatementLine
		clave *string
		kind  string
		minor int64
	)
	if err := row.Scan(&l.ID, &clave, &kind, &minor, &l.CreatedAt); err != nil {
		return StatementLine{}, err
	}
	if clave != nil {
		l.Clave = *clave
	}
	l.Kind = StatementKind(kind)
	l.Amount = money.New(money.MXN, minor)
	return l, nil
}

func insertStatement(ctx context.Context, tx pgx.Tx, sandbox uuid.UUID, clave string, kind StatementKind, minor int64) (StatementLine, error) {
	var claveArg *string
	if clave != "" {
		claveArg = &clave
	}
	l, err := scanStatementLine(tx.QueryRow(ctx,
		`INSERT INTO spei_statement (sandbox_id, clave, kind, amount_centavos)
		 VALUES ($1, $2, $3, $4) RETURNING `+statementColumns,
		sandbox, claveArg, string(kind), minor))
	if err != nil {
		return StatementLine{}, fmt.Errorf("spei: book %s %s: %w", kind, clave, err)
	}
	return l, nil
}
