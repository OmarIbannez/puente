package api_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/OmarIbannez/puente/internal/api"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
)

const clabe = "012180015203300154"

type sandboxInfo struct {
	ID     uuid.UUID    `json:"id"`
	Faults store.Faults `json:"faults"`
}

type customerInfo struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Available map[string]struct {
		Minor int64 `json:"minor"`
	} `json:"available"`
}

func (c *client) sandbox() sandboxInfo {
	c.t.Helper()
	var sb sandboxInfo
	c.do(http.MethodGet, "/api/sandbox", nil).want(c.t, http.StatusOK, &sb)
	return sb
}

func (c *client) customers() map[string]customerInfo {
	c.t.Helper()
	var res struct{ Customers []customerInfo }
	c.do(http.MethodGet, "/api/customers", nil).want(c.t, http.StatusOK, &res)
	out := map[string]customerInfo{}
	for _, cu := range res.Customers {
		out[cu.Name] = cu
	}
	return out
}

func (c *client) advance(ticks int) {
	c.t.Helper()
	c.do(http.MethodPost, "/api/sandbox/advance", map[string]int{"ticks": ticks}).want(c.t, http.StatusOK, nil)
}

func key() string { return "Idempotency-Key: " + uuid.NewString() }

// fund deposits amount USDC to the customer on the simulated chain and
// advances until it is credited.
func (c *client) fund(customerID uuid.UUID, amount string) {
	c.t.Helper()
	c.do(http.MethodPost, "/api/deposits/simulate",
		map[string]any{"customer_id": customerID, "asset": "USDC", "amount": amount}, key()).
		want(c.t, http.StatusCreated, nil)
	c.advance(4)
}

func payoutBody(customerID uuid.UUID, amount string) map[string]any {
	return map[string]any{
		"customer_id": customerID, "asset": "USDC", "amount": amount,
		"beneficiary_clabe": clabe, "beneficiary_name": "Luis Pérez", "concepto": "Renta octubre",
	}
}

func TestHealthSPAAndFallbacks(t *testing.T) {
	c := newClient(t)
	var h struct{ Status, DB string }
	c.do(http.MethodGet, "/api/health", nil).want(t, http.StatusOK, &h)
	if h.Status != "ok" || h.DB != "ok" {
		t.Fatalf("health = %+v", h)
	}
	if r := c.do(http.MethodGet, "/payouts/123", nil); r.status != http.StatusOK || string(r.body) != indexHTML {
		t.Fatalf("SPA route = %d %q", r.status, r.body)
	}
	c.do(http.MethodGet, "/api/nope", nil).wantError(t, http.StatusNotFound, api.CodeNotFound)
	c.do(http.MethodDelete, "/api/payouts", nil).wantError(t, http.StatusMethodNotAllowed, api.CodeMethodNotAllowed)
	var nets struct{ Networks []struct{ Key string } }
	c.do(http.MethodGet, "/api/networks", nil).want(t, http.StatusOK, &nets)
	if len(nets.Networks) != 4 {
		t.Fatalf("networks = %+v", nets)
	}
}

func TestSandboxCookie(t *testing.T) {
	ctx := t.Context()
	a, b := newClient(t), newClient(t)
	first := a.sandbox().ID
	if a.sandbox().ID != first || b.sandbox().ID == first {
		t.Fatal("each browser must keep its own sandbox")
	}
	if len(a.customers()) != len(testCustomers) {
		t.Fatal("new sandbox was not seeded")
	}

	// A forged cookie naming the live sandbox gets a fresh visitor sandbox.
	live, err := store.LiveSandbox(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	forged := newClient(t)
	r := forged.do(http.MethodGet, "/api/sandbox", nil, "Cookie: "+api.SandboxCookie+"="+live.ID.String())
	var got sandboxInfo
	r.want(t, http.StatusOK, &got)
	if got.ID == live.ID {
		t.Fatal("a forged cookie reached the live sandbox")
	}
	forged.do(http.MethodPost, "/api/sandbox/reset", nil, "Cookie: "+api.SandboxCookie+"="+live.ID.String()).
		want(t, http.StatusOK, nil)
	if _, err := store.GetSandbox(ctx, pool, live.ID); err != nil {
		t.Fatalf("reset with a forged cookie deleted the live sandbox: %v", err)
	}

	// Reset replaces the sandbox and deletes the old one.
	var reset sandboxInfo
	a.do(http.MethodPost, "/api/sandbox/reset", nil).want(t, http.StatusOK, &reset)
	if reset.ID == first || a.sandbox().ID != reset.ID {
		t.Fatalf("reset = %s, first %s", reset.ID, first)
	}
	if _, err := store.GetSandbox(ctx, pool, first); err == nil {
		t.Fatal("old sandbox survived reset")
	}
}

func TestDepositPayoutAndRecon(t *testing.T) {
	c := newClient(t)
	ana := c.customers()["Ana"]
	c.fund(ana.ID, "100")
	if got := c.customers()["Ana"].Available["USDC"].Minor; got != 100_000_000 {
		t.Fatalf("available after deposit = %d", got)
	}
	var deps struct{ Deposits []struct{ State string } }
	c.do(http.MethodGet, "/api/deposits", nil).want(t, http.StatusOK, &deps)
	if len(deps.Deposits) != 1 || deps.Deposits[0].State != "credited" {
		t.Fatalf("deposits = %+v", deps)
	}

	var p payout.Payout
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "10"), key()).want(t, http.StatusCreated, &p)
	if p.Status != payout.StatusReserved || p.MXN.Minor != 18_452 {
		t.Fatalf("created payout = %+v", p)
	}
	c.advance(2)
	var d struct {
		payout.Payout
		Events  []payout.Event
		Entries []struct{ Kind string }
		Rail    *struct {
			Status string
			CEP    *struct{ Sello string }
		}
	}
	c.do(http.MethodGet, "/api/payouts/"+p.ID.String(), nil).want(t, http.StatusOK, &d)
	if d.Status != payout.StatusLiquidated || len(d.Entries) != 3 || d.Rail == nil ||
		d.Rail.Status != "liquidado" || d.Rail.CEP == nil || d.Rail.CEP.Sello == "" || len(d.Events) < 3 {
		t.Fatalf("payout detail = %+v", d)
	}

	var ov struct {
		TrialBalance map[string]int64 `json:"trial_balance"`
	}
	c.do(http.MethodGet, "/api/overview", nil).want(t, http.StatusOK, &ov)
	for asset, v := range ov.TrialBalance {
		if v != 0 {
			t.Fatalf("trial balance %s = %d", asset, v)
		}
	}

	var run recon.Run
	c.do(http.MethodPost, "/api/recon/runs", nil).want(t, http.StatusCreated, &run)
	if run.BreakCount != 0 {
		t.Fatalf("clean recon found %+v", run.Breaks)
	}
	c.do(http.MethodPost, "/api/sandbox/inject", map[string]string{"kind": "bank_fee", "amount": "12.50"}).
		want(t, http.StatusCreated, nil)
	c.do(http.MethodPost, "/api/recon/runs", nil).want(t, http.StatusCreated, &run)
	if run.BreakCount != 1 || run.Breaks[0].Kind != recon.BalanceMismatch {
		t.Fatalf("recon after bank fee = %+v", run.Breaks)
	}
	var latest recon.Run
	c.do(http.MethodGet, "/api/recon/runs/latest", nil).want(t, http.StatusOK, &latest)
	if latest.ID != run.ID || len(latest.Breaks) != 1 {
		t.Fatalf("latest = %+v", latest)
	}
}

func TestIdempotency(t *testing.T) {
	ctx := t.Context()
	c := newClient(t)
	ana := c.customers()["Ana"]
	c.fund(ana.ID, "50")

	k := key()
	first := c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "5"), k).want(t, http.StatusCreated, nil)
	again := c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "5"), k).want(t, http.StatusCreated, nil)
	if !bytes.Equal(first.body, again.body) || again.header.Get(api.IdempotentReplayHeader) != "true" {
		t.Fatalf("replay differs:\n%s\n%s", first.body, again.body)
	}
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "6"), k).
		wantError(t, http.StatusUnprocessableEntity, api.CodeIdempotencyKeyReused)
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "5")).
		wantError(t, http.StatusBadRequest, api.CodeIdempotencyKeyRequired)

	// A key whose first request is still running answers 409. The pending
	// row carries the same request fingerprint the middleware computes:
	// SHA-256 of method, path and body.
	body, err := json.Marshal(payoutBody(ana.ID, "5"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("POST\n/api/payouts\n"), body...))
	if _, err := pool.Exec(ctx, `INSERT INTO idempotency_keys (sandbox_id, key, request_hash, status_code, response)
		VALUES ($1, 'busy', $2, 0, 'null'::jsonb)`, c.sandbox().ID, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "5"), "Idempotency-Key: busy").
		wantError(t, http.StatusConflict, api.CodeIdempotencyInProgress)

	// A 4xx outcome is stored and replayed too.
	k2 := key()
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "1000"), k2).
		wantError(t, http.StatusUnprocessableEntity, api.CodeInsufficientFunds)
	r := c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "1000"), k2)
	if r.status != http.StatusUnprocessableEntity || r.header.Get(api.IdempotentReplayHeader) != "true" {
		t.Fatalf("4xx replay = %d %v", r.status, r.header)
	}
}

func TestConcurrentPayout(t *testing.T) {
	c := newClient(t)
	ana := c.customers()["Ana"]
	c.fund(ana.ID, "50")
	k := key()
	const n = 10
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			statuses[i] = c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "5"), k).status
		})
	}
	wg.Wait()
	for i, s := range statuses {
		if s != http.StatusCreated && s != http.StatusConflict {
			t.Fatalf("request %d: status %d", i, s)
		}
	}
	var list struct{ Payouts []payout.Payout }
	c.do(http.MethodGet, "/api/payouts", nil).want(t, http.StatusOK, &list)
	if len(list.Payouts) != 1 {
		t.Fatalf("%d payouts for one idempotency key", len(list.Payouts))
	}
	if got := c.customers()["Ana"].Available["USDC"].Minor; got != 50_000_000-5_025_000 {
		t.Fatalf("available = %d, want one payout reserved", got)
	}
}

func TestValidationAndConflicts(t *testing.T) {
	c := newClient(t)
	ana := c.customers()["Ana"]
	c.fund(ana.ID, "20")

	bad := payoutBody(ana.ID, "1")
	bad["beneficiary_clabe"] = "012180015203300155"
	c.do(http.MethodPost, "/api/payouts", bad, key()).wantError(t, http.StatusUnprocessableEntity, api.CodeValidation)
	bad = payoutBody(ana.ID, "1.0000001")
	c.do(http.MethodPost, "/api/payouts", bad, key()).wantError(t, http.StatusUnprocessableEntity, api.CodeValidation)
	bad = payoutBody(ana.ID, "1")
	bad["asset"] = "BTC"
	c.do(http.MethodPost, "/api/payouts", bad, key()).wantError(t, http.StatusUnprocessableEntity, api.CodeValidation)
	c.do(http.MethodPost, "/api/payouts", payoutBody(uuid.New(), "1"), key()).wantError(t, http.StatusNotFound, api.CodeNotFound)
	c.do(http.MethodPost, "/api/payouts", `{"bogus": 1}`, key()).wantError(t, http.StatusBadRequest, api.CodeBadRequest)
	c.do(http.MethodGet, "/api/payouts/"+uuid.NewString(), nil).wantError(t, http.StatusNotFound, api.CodeNotFound)

	var p payout.Payout
	c.do(http.MethodPost, "/api/payouts", payoutBody(ana.ID, "2"), key()).want(t, http.StatusCreated, &p)
	c.do(http.MethodPost, "/api/payouts/"+p.ID.String()+"/cancel", nil, key()).want(t, http.StatusOK, &p)
	if p.Status != payout.StatusReleased {
		t.Fatalf("cancelled payout = %s", p.Status)
	}
	c.do(http.MethodPost, "/api/payouts/"+p.ID.String()+"/cancel", nil, key()).wantError(t, http.StatusConflict, api.CodeConflict)

	c.do(http.MethodPost, "/api/sandbox/advance", map[string]int{"ticks": 11}).
		wantError(t, http.StatusUnprocessableEntity, api.CodeValidation)
	c.do(http.MethodPost, "/api/sandbox/inject", map[string]string{"kind": "nope"}).
		wantError(t, http.StatusUnprocessableEntity, api.CodeValidation)
}

func TestFaultsDriveBreaks(t *testing.T) {
	c := newClient(t)
	ana := c.customers()["Ana"]
	var sb sandboxInfo
	c.do(http.MethodPut, "/api/sandbox/faults", map[string]any{"faults": store.Faults{DuplicateLog: true}}).
		want(t, http.StatusOK, &sb)
	if !sb.Faults.DuplicateLog {
		t.Fatalf("faults = %+v", sb.Faults)
	}
	c.fund(ana.ID, "10")
	if c.sandbox().Faults.DuplicateLog {
		t.Fatal("fault not consumed")
	}
	var run recon.Run
	c.do(http.MethodPost, "/api/recon/runs", nil).want(t, http.StatusCreated, &run)
	if run.BreakCount != 1 || run.Breaks[0].Kind != recon.Duplicate {
		t.Fatalf("breaks = %+v", run.Breaks)
	}
}

func TestLiveDisabled(t *testing.T) {
	c := newClient(t)
	var res struct {
		Enabled  bool
		Deposits []any
	}
	c.do(http.MethodGet, "/api/live", nil).want(t, http.StatusOK, &res)
	if res.Enabled || res.Deposits == nil {
		t.Fatalf("live = %+v", res)
	}
	if !strings.Contains(string(c.do(http.MethodGet, "/api/ledger/accounts", nil).body), "Customer balances") {
		t.Fatal("chart of accounts missing")
	}
}
