package spei_test

import (
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

var (
	rail = spei.Rail{}
	day  = time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
)

// beneficiary is a valid BBVA CLABE.
const beneficiary = "012180015203300154"

func mxn(minor int64) money.Amount { return money.New(money.MXN, minor) }

func order(seq int64, minor int64) spei.Order {
	return spei.Order{
		Clave:            spei.NewClave(spei.DefaultClavePrefix, day, seq),
		BeneficiaryCLABE: beneficiary,
		BeneficiaryName:  "Ana López",
		Concepto:         "Pago Puente",
		Amount:           mxn(minor),
	}
}

func submit(t *testing.T, sandbox uuid.UUID, o spei.Order) (spei.Ack, error) {
	t.Helper()
	var ack spei.Ack
	// Commit even on ErrTimeout: the rail kept the order.
	var submitErr error
	err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		ack, submitErr = rail.Submit(t.Context(), tx, sandbox, o)
		if submitErr != nil && !errors.Is(submitErr, spei.ErrTimeout) {
			return submitErr
		}
		return nil
	})
	if err != nil {
		return spei.Ack{}, err
	}
	return ack, submitErr
}

func mustSubmit(t *testing.T, sandbox uuid.UUID, o spei.Order) spei.Ack {
	t.Helper()
	ack, err := submit(t, sandbox, o)
	if err != nil {
		t.Fatalf("Submit(%s): %v", o.Clave, err)
	}
	return ack
}

func advance(t *testing.T, sandbox uuid.UUID) []spei.StatusChange {
	t.Helper()
	var changes []spei.StatusChange
	if err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		changes, err = rail.Advance(t.Context(), tx, sandbox)
		return err
	}); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	return changes
}

func fund(t *testing.T, sandbox uuid.UUID, minor int64) {
	t.Helper()
	if err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := rail.Fund(t.Context(), tx, sandbox, mxn(minor))
		return err
	}); err != nil {
		t.Fatalf("Fund: %v", err)
	}
}

func getOrder(t *testing.T, sandbox uuid.UUID, clave string) spei.Record {
	t.Helper()
	rec, err := rail.Order(t.Context(), pool, sandbox, clave)
	if err != nil {
		t.Fatalf("Order(%s): %v", clave, err)
	}
	return rec
}

func wantBalance(t *testing.T, sandbox uuid.UUID, minor int64) {
	t.Helper()
	bal, err := rail.StatementBalance(t.Context(), pool, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if bal != mxn(minor) {
		t.Fatalf("statement balance = %s, want %s", bal, mxn(minor))
	}
	// The balance is exactly the sum of the lines.
	lines, err := rail.Statement(t.Context(), pool, sandbox, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, l := range lines {
		sum += l.Amount.Minor
	}
	if sum != minor {
		t.Fatalf("statement lines sum to %d, want %d", sum, minor)
	}
}

func setFaults(t *testing.T, sandbox uuid.UUID, f store.Faults) {
	t.Helper()
	if _, err := store.SetFaults(t.Context(), pool, sandbox, f); err != nil {
		t.Fatal(err)
	}
}

func faults(t *testing.T, sandbox uuid.UUID) store.Faults {
	t.Helper()
	s, err := store.GetSandbox(t.Context(), pool, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	return s.Faults
}

func TestSubmitIsIdempotentOnClave(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	o := order(1, 125_000)
	first := mustSubmit(t, sb, o)
	if first.Duplicate || first.Status != spei.StatusEnProceso || first.SubmitCount != 1 {
		t.Fatalf("first ack = %+v", first)
	}
	if first.OrderingCLABE != spei.DefaultOrderingCLABE {
		t.Fatalf("ordering CLABE = %q", first.OrderingCLABE)
	}
	// Separators in the CLABE do not make it a different order.
	again := o
	again.BeneficiaryCLABE = "012-180-01520330015-4"
	second := mustSubmit(t, sb, again)
	if !second.Duplicate || second.ID != first.ID || second.SubmitCount != 2 || second.Status != spei.StatusEnProceso {
		t.Fatalf("second ack = %+v", second)
	}
	orders, err := rail.Orders(t.Context(), pool, sb, 0)
	if err != nil || len(orders) != 1 {
		t.Fatalf("Orders = %d, %v", len(orders), err)
	}
	wantBalance(t, sb, 0)

	for name, mutate := range map[string]func(*spei.Order){
		"amount":   func(o *spei.Order) { o.Amount = mxn(125_001) },
		"name":     func(o *spei.Order) { o.BeneficiaryName = "Otro" },
		"concepto": func(o *spei.Order) { o.Concepto = "Otro" },
		"clabe":    func(o *spei.Order) { o.BeneficiaryCLABE = "002010077777777771" },
	} {
		changed := o
		mutate(&changed)
		if _, err := submit(t, sb, changed); !errors.Is(err, spei.ErrClaveConflict) {
			t.Errorf("%s changed: %v, want ErrClaveConflict", name, err)
		}
	}
	// A different ordering account is a conflict too.
	other := spei.Rail{OrderingCLABE: "646180157000000004"}
	err = store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := other.Submit(t.Context(), tx, sb, o)
		return err
	})
	if !errors.Is(err, spei.ErrClaveConflict) {
		t.Errorf("ordering changed: %v", err)
	}
	if got := getOrder(t, sb, o.Clave); got.SubmitCount != 2 {
		t.Fatalf("conflicts bumped submit_count to %d", got.SubmitCount)
	}
}

func TestSubmitRejectsMalformedOrders(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	bad := order(1, 100)
	bad.Clave = "bad-clave"
	if _, err := submit(t, sb, bad); !errors.Is(err, spei.ErrBadClave) {
		t.Errorf("bad clave: %v", err)
	}
	for _, amt := range []money.Amount{mxn(0), mxn(-5), money.New(money.USDC, 100)} {
		o := order(2, 1)
		o.Amount = amt
		if _, err := submit(t, sb, o); !errors.Is(err, spei.ErrBadOrder) {
			t.Errorf("amount %s: %v", amt, err)
		}
	}
	err := store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := spei.Rail{OrderingCLABE: "002010077777777772"}.Submit(t.Context(), tx, sb, order(3, 1))
		return err
	})
	if !errors.Is(err, spei.ErrCheckDigit) {
		t.Errorf("bad ordering CLABE: %v", err)
	}
	err = store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := rail.Fund(t.Context(), tx, sb, money.New(money.MXNB, 100))
		return err
	})
	if !errors.Is(err, spei.ErrBadOrder) {
		t.Errorf("fund in MXNB: %v", err)
	}
	if _, err := submit(t, uuid.New(), order(4, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown sandbox: %v", err)
	}
	orders, err := rail.Orders(t.Context(), pool, sb, 0)
	if err != nil || len(orders) != 0 {
		t.Fatalf("malformed orders stored: %d, %v", len(orders), err)
	}
}

func TestSubmitInvalidBeneficiaryIsRechazado(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	setFaults(t, sb, store.Faults{RailReject: true})
	cases := map[string]string{
		"002010077777777772": spei.ReasonCLABEInvalida,          // bad check digit
		"12345":              spei.ReasonCLABEInvalida,          // wrong length
		"999180000000000002": spei.ReasonInstitucionInexistente, // unknown bank
	}
	seq := int64(0)
	for clabe, reason := range cases {
		seq++
		o := order(seq, 500)
		o.BeneficiaryCLABE = clabe
		ack := mustSubmit(t, sb, o)
		if ack.Status != spei.StatusRechazado || ack.Reason != reason {
			t.Errorf("CLABE %s: %+v", clabe, ack)
		}
	}
	// Validation rejections do not consume the reject fault.
	if !faults(t, sb).RailReject {
		t.Fatal("RailReject consumed by an invalid CLABE")
	}
	if changes := advance(t, sb); len(changes) != 0 {
		t.Fatalf("rechazado orders advanced: %+v", changes)
	}
	wantBalance(t, sb, 0)
}

func TestRailRejectFault(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	setFaults(t, sb, store.Faults{RailReject: true, Reorg: true})
	ack := mustSubmit(t, sb, order(1, 1_000))
	if ack.Status != spei.StatusRechazado || ack.Reason != spei.ReasonCuentaInexistente {
		t.Fatalf("rejected ack = %+v", ack)
	}
	if f := faults(t, sb); f.RailReject || !f.Reorg {
		t.Fatalf("faults after reject = %+v (want only RailReject consumed)", f)
	}
	if next := mustSubmit(t, sb, order(2, 1_000)); next.Status != spei.StatusEnProceso {
		t.Fatalf("fault applied twice: %+v", next)
	}
	// A resubmit of the rejected clave reports the rejection, not a new order.
	if dup := mustSubmit(t, sb, order(1, 1_000)); !dup.Duplicate || dup.Status != spei.StatusRechazado {
		t.Fatalf("resubmit of rejected = %+v", dup)
	}
	changes := advance(t, sb)
	if len(changes) != 1 || changes[0].Clave != order(2, 0).Clave {
		t.Fatalf("Advance = %+v", changes)
	}
}

func TestRailTimeoutFault(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	setFaults(t, sb, store.Faults{RailTimeout: true})
	o := order(1, 2_500)
	if _, err := submit(t, sb, o); !errors.Is(err, spei.ErrTimeout) {
		t.Fatalf("Submit with RailTimeout = %v", err)
	}
	// The ack was lost but the order exists.
	rec := getOrder(t, sb, o.Clave)
	if rec.Status != spei.StatusEnProceso || rec.SubmitCount != 1 {
		t.Fatalf("timed-out order = %+v", rec)
	}
	if faults(t, sb).RailTimeout {
		t.Fatal("RailTimeout not consumed")
	}
	// The retry with the same clave is safe: same order, no new money moves.
	retry := mustSubmit(t, sb, o)
	if !retry.Duplicate || retry.ID != rec.ID || retry.SubmitCount != 2 {
		t.Fatalf("retry ack = %+v", retry)
	}
	fund(t, sb, 10_000)
	advance(t, sb)
	mustSubmit(t, sb, o)
	advance(t, sb)
	wantBalance(t, sb, 10_000-2_500)
	lines, err := rail.Statement(t.Context(), pool, sb, 0)
	if err != nil || len(lines) != 2 {
		t.Fatalf("statement has %d lines (%v), want funding + one debit", len(lines), err)
	}
}

func TestRailReturnFaultAndLifecycle(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	fund(t, sb, 1_000_000)
	wantBalance(t, sb, 1_000_000)

	setFaults(t, sb, store.Faults{RailReturn: true})
	ret := order(1, 300_000)
	ack := mustSubmit(t, sb, ret)
	if ack.Status != spei.StatusEnProceso || !ack.ReturnPending {
		t.Fatalf("return-pending ack = %+v", ack)
	}
	if faults(t, sb).RailReturn {
		t.Fatal("RailReturn not consumed")
	}
	keep := order(2, 200_000)
	if a := mustSubmit(t, sb, keep); a.ReturnPending {
		t.Fatalf("fault applied twice: %+v", a)
	}
	wantBalance(t, sb, 1_000_000)

	// Cycle 1: both liquidate; the return waits for a later cycle.
	changes := advance(t, sb)
	want := []spei.StatusChange{
		{Clave: ret.Clave, From: spei.StatusEnProceso, To: spei.StatusLiquidado},
		{Clave: keep.Clave, From: spei.StatusEnProceso, To: spei.StatusLiquidado},
	}
	assertChanges(t, changes, want)
	wantBalance(t, sb, 1_000_000-300_000-200_000)
	rec := getOrder(t, sb, ret.Clave)
	if rec.Status != spei.StatusLiquidado || rec.LiquidatedAt == nil || rec.CEP == nil {
		t.Fatalf("liquidated order = %+v", rec)
	}
	cep := *rec.CEP
	if !cep.Simulado || cep.ClaveRastreo != ret.Clave || cep.Monto != "3000.00" ||
		cep.Emisor != "STP" || cep.Receptor != "BBVA MEXICO" ||
		cep.CuentaBeneficiario != "**************0154" || cep.Sello != cep.Seal() || len(cep.Sello) != 64 {
		t.Fatalf("CEP = %+v", cep)
	}

	// Cycle 2: the marked order comes back.
	changes = advance(t, sb)
	assertChanges(t, changes, []spei.StatusChange{
		{Clave: ret.Clave, From: spei.StatusLiquidado, To: spei.StatusDevuelto, Reason: spei.ReasonCuentaBloqueada},
	})
	wantBalance(t, sb, 1_000_000-200_000)
	rec = getOrder(t, sb, ret.Clave)
	if rec.Status != spei.StatusDevuelto || rec.ReturnedAt == nil || rec.Reason != spei.ReasonCuentaBloqueada || rec.CEP == nil {
		t.Fatalf("returned order = %+v", rec)
	}
	if got := getOrder(t, sb, keep.Clave); got.Status != spei.StatusLiquidado {
		t.Fatalf("kept order = %+v", got)
	}

	// Cycle 3: nothing left to do; a resubmit just reports the final state.
	if changes := advance(t, sb); len(changes) != 0 {
		t.Fatalf("idle Advance = %+v", changes)
	}
	if dup := mustSubmit(t, sb, ret); !dup.Duplicate || dup.Status != spei.StatusDevuelto {
		t.Fatalf("resubmit after return = %+v", dup)
	}
	wantBalance(t, sb, 1_000_000-200_000)

	lines, err := rail.Statement(t.Context(), pool, sb, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]spei.StatementKind, len(lines))
	for i, l := range lines {
		kinds[i] = l.Kind
	}
	wantKinds := []spei.StatementKind{spei.KindReturnCredit, spei.KindLiquidationDebit, spei.KindLiquidationDebit, spei.KindFunding}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("statement kinds = %v, want %v", kinds, wantKinds)
	}
	if lines[0].Clave != ret.Clave || lines[0].Amount != mxn(300_000) || lines[3].Clave != "" {
		t.Fatalf("statement lines = %+v", lines)
	}
}

func assertChanges(t *testing.T, got, want []spei.StatusChange) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
}

func TestConcurrentSubmitsOfOneClave(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	o := order(7, 9_900)
	const workers = 10
	acks := make([]spei.Ack, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			errs[i] = store.InTx(t.Context(), pool, func(tx pgx.Tx) error {
				var err error
				acks[i], err = rail.Submit(t.Context(), tx, sb, o)
				return err
			})
		})
	}
	wg.Wait()
	fresh := 0
	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if !acks[i].Duplicate {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%d submits created an order, want exactly 1", fresh)
	}
	orders, err := rail.Orders(t.Context(), pool, sb, 0)
	if err != nil || len(orders) != 1 || orders[0].SubmitCount != workers {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
}

func TestSandboxIsolation(t *testing.T) {
	a := storetest.Sandbox(t, pool).ID
	b := storetest.Sandbox(t, pool).ID
	o := order(1, 4_200)
	fund(t, a, 10_000)
	mustSubmit(t, a, o)

	if _, err := rail.Order(t.Context(), pool, b, o.Clave); !errors.Is(err, spei.ErrNotFound) {
		t.Fatalf("order visible in other sandbox: %v", err)
	}
	if orders, err := rail.Orders(t.Context(), pool, b, 0); err != nil || len(orders) != 0 {
		t.Fatalf("Orders(b) = %d, %v", len(orders), err)
	}
	wantBalance(t, b, 0)
	if changes := advance(t, b); len(changes) != 0 {
		t.Fatalf("Advance(b) touched a's orders: %+v", changes)
	}
	// The same clave is free in another sandbox.
	if ack := mustSubmit(t, b, o); ack.Duplicate {
		t.Fatalf("clave shared across sandboxes: %+v", ack)
	}
	if got := getOrder(t, a, o.Clave); got.Status != spei.StatusEnProceso {
		t.Fatalf("a's order = %+v", got)
	}
	advance(t, a)
	wantBalance(t, a, 10_000-4_200)
	wantBalance(t, b, 0)

	// Deleting a sandbox deletes its rail books.
	if err := store.DeleteSandbox(t.Context(), pool, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(t.Context(),
		`SELECT (SELECT count(*) FROM spei_orders WHERE sandbox_id = $1)
		      + (SELECT count(*) FROM spei_statement WHERE sandbox_id = $1)`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rail rows survived sandbox deletion", n)
	}
}

func TestListLimits(t *testing.T) {
	sb := storetest.Sandbox(t, pool).ID
	for i := range int64(5) {
		mustSubmit(t, sb, order(i+1, 100))
	}
	got, err := rail.Orders(t.Context(), pool, sb, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("Orders limit 2 = %d, %v", len(got), err)
	}
	if got[0].Clave != order(5, 0).Clave {
		t.Fatalf("Orders not newest first: %s", got[0].Clave)
	}
}
