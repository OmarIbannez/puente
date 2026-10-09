package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// healthTimeout bounds the database ping of /api/health.
const healthTimeout = 2 * time.Second

// maxAdvanceTicks caps POST /api/sandbox/advance.
const maxAdvanceTicks = 10

// advanceTimeout bounds one POST /api/sandbox/advance.
const advanceTimeout = 20 * time.Second

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	DB      string `json:"db"`
	Live    bool   `json:"live"`
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	res := healthResponse{Status: "ok", Version: s.version, DB: "ok", Live: s.engine.LiveEnabled()}
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	status := http.StatusOK
	if err := s.pool.Ping(ctx); err != nil {
		s.log.WarnContext(ctx, "health: database ping failed", "err", err)
		res.Status, res.DB, status = "degraded", "error", http.StatusServiceUnavailable
	}
	writeJSON(w, status, res)
}

func (s *server) networks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"networks": chain.Networks})
}

// sandbox loads the request's sandbox.
func (s *server) sandbox(r *http.Request) (store.Sandbox, error) {
	return store.GetSandbox(r.Context(), s.pool, sandboxID(r.Context()))
}

type sandboxResponse struct {
	ID       uuid.UUID       `json:"id"`
	Faults   store.Faults    `json:"faults"`
	Networks []chain.Network `json:"networks"`
	// Rates and FeeBps quote payouts.
	Rates  map[money.Asset]string `json:"rates"`
	FeeBps int64                  `json:"fee_bps"`
}

func (s *server) sandboxInfo(sb store.Sandbox) sandboxResponse {
	cfg := s.engine.PayoutConfig()
	rates := map[money.Asset]string{}
	for asset, rate := range cfg.Rates {
		rates[asset] = rate.String()
	}
	return sandboxResponse{ID: sb.ID, Faults: sb.Faults, Networks: s.engine.Networks(sb), Rates: rates, FeeBps: cfg.FeeBps}
}

func (s *server) getSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.sandbox(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxInfo(sb))
}

// resetSandbox deletes the visitor's sandbox and starts a fresh, seeded one.
func (s *server) resetSandbox(w http.ResponseWriter, r *http.Request) {
	// Check the budget before deleting anything: a refused reset must leave
	// the visitor's sandbox in place.
	if err := s.allowCreate(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	if c, err := r.Cookie(SandboxCookie); err == nil {
		if old, err := uuid.Parse(c.Value); err == nil {
			s.touches.forget(old)
			if _, err := s.pool.Exec(r.Context(),
				"DELETE FROM sandboxes WHERE id = $1 AND kind = 'visitor'", old); err != nil {
				s.writeError(w, r, fmt.Errorf("api: delete sandbox: %w", err))
				return
			}
		}
	}
	id, err := s.createSandbox(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSandboxCookie(w, id)
	sb, err := store.GetSandbox(r.Context(), s.pool, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxInfo(sb))
}

func (s *server) setFaults(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Faults store.Faults `json:"faults"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	sb, err := store.SetFaults(r.Context(), s.pool, sandboxID(r.Context()), req.Faults)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxInfo(sb))
}

// advance runs one or more engine cycles at once, so the demo doesn't have
// to wait for the background loop.
func (s *server) advance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticks int `json:"ticks"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Ticks == 0 {
		req.Ticks = 1
	}
	if req.Ticks < 1 || req.Ticks > maxAdvanceTicks {
		s.writeError(w, r, validation("ticks must be between 1 and %d", maxAdvanceTicks))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), advanceTimeout)
	defer cancel()
	reports := make([]any, 0, req.Ticks)
	for range req.Ticks {
		sb, err := store.GetSandbox(ctx, s.pool, sandboxID(ctx)) // faults may have been consumed
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		rep, err := s.engine.Tick(ctx, sb, s.now())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		reports = append(reports, rep)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticks": reports})
}

// inject books something only the bank knows about, to show a rail break.
func (s *server) inject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Amount string `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Amount == "" {
		req.Amount = "125.00"
	}
	amount, err := money.Parse(money.MXN, req.Amount)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ctx := r.Context()
	sb := sandboxID(ctx)
	rail := s.engine.Rail()
	var result any
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		switch req.Kind {
		case "bank_fee":
			result, err = rail.InjectBankFee(ctx, tx, sb, amount)
		case "manual_order":
			result, err = rail.InjectManualOrder(ctx, tx, sb, "012180015203300154", "Proveedor manual", amount)
		default:
			err = validation("kind must be bank_fee or manual_order")
		}
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

type overviewResponse struct {
	TrialBalance     map[money.Asset]int64   `json:"trial_balance"`
	Balances         []ledger.AccountBalance `json:"balances"`
	StatementBalance money.Amount            `json:"statement_balance"`
	PayoutsByStatus  map[payout.Status]int   `json:"payouts_by_status"`
	Customers        int                     `json:"customers"`
	LatestRecon      *recon.Run              `json:"latest_recon,omitempty"`
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	ctx, sb := r.Context(), sandboxID(r.Context())
	var res overviewResponse
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var err error
			if res.TrialBalance, err = ledger.TrialBalance(ctx, tx, sb); err != nil {
				return err
			}
			if res.Balances, err = ledger.Balances(ctx, tx, sb); err != nil {
				return err
			}
			if res.StatementBalance, err = s.engine.Rail().StatementBalance(ctx, tx, sb); err != nil {
				return err
			}
			if res.PayoutsByStatus, err = payoutCounts(ctx, tx, sb); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM customers WHERE sandbox_id = $1", sb).Scan(&res.Customers); err != nil {
				return fmt.Errorf("api: count customers: %w", err)
			}
			run, err := recon.Latest(ctx, tx, sb)
			switch {
			case err == nil:
				res.LatestRecon = &run
			case !errors.Is(err, recon.ErrNoRuns):
				return err
			}
			return nil
		})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func payoutCounts(ctx context.Context, q store.Querier, sb uuid.UUID) (map[payout.Status]int, error) {
	rows, err := q.Query(ctx, "SELECT status, count(*) FROM payouts WHERE sandbox_id = $1 GROUP BY status", sb)
	if err != nil {
		return nil, fmt.Errorf("api: payout counts: %w", err)
	}
	defer rows.Close()
	out := map[payout.Status]int{}
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("api: payout counts: %w", err)
		}
		out[payout.Status(status)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("api: payout counts: %w", err)
	}
	return out, nil
}

type customerView struct {
	customer.Customer
	Available map[money.Asset]money.Amount `json:"available"`
	Addresses []customer.Address           `json:"addresses"`
}

func (s *server) listCustomers(w http.ResponseWriter, r *http.Request) {
	ctx, sb := r.Context(), sandboxID(r.Context())
	views, err := customerViews(ctx, s, sb)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"customers": views})
}

func customerViews(ctx context.Context, s *server, sb uuid.UUID) ([]customerView, error) {
	customers, err := customer.List(ctx, s.pool, sb)
	if err != nil {
		return nil, err
	}
	addrs, err := customer.Addresses(ctx, s.pool, sb, "")
	if err != nil {
		return nil, err
	}
	views := make([]customerView, len(customers))
	for i, c := range customers {
		v := customerView{Customer: c, Available: map[money.Asset]money.Amount{}, Addresses: []customer.Address{}}
		for _, asset := range []money.Asset{money.USDC, money.MXNB} {
			if v.Available[asset], err = payout.Available(ctx, s.pool, sb, c.ID, asset); err != nil {
				return nil, err
			}
		}
		for _, a := range addrs {
			if a.CustomerID == c.ID {
				v.Addresses = append(v.Addresses, a)
			}
		}
		views[i] = v
	}
	return views, nil
}

func (s *server) listDeposits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, err := queryInt(q.Get("before"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(q.Get("limit"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	views, err := deposit.List(r.Context(), s.pool, sandboxID(r.Context()),
		deposit.Filter{Network: q.Get("network"), Limit: int(limit), BeforeID: before})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deposits": views})
}

func (s *server) simulateDeposit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomerID uuid.UUID `json:"customer_id"`
		Asset      string    `json:"asset"`
		Amount     string    `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	asset, err := money.ParseAsset(req.Asset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	amount, err := money.Parse(asset, req.Amount)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if amount.Minor <= 0 {
		s.writeError(w, r, validation("amount must be positive"))
		return
	}
	sb, err := s.sandbox(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	t, err := s.engine.SimulateDeposit(r.Context(), sb, req.CustomerID, amount)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *server) listPayouts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := payout.Filter{Status: payout.Status(q.Get("status"))}
	if f.Status != "" && !f.Status.Valid() {
		s.writeError(w, r, validation("unknown status %q", f.Status))
		return
	}
	if c := q.Get("customer_id"); c != "" {
		id, err := uuid.Parse(c)
		if err != nil {
			s.writeError(w, r, badRequest("customer_id is not a UUID"))
			return
		}
		f.CustomerID = id
	}
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			s.writeError(w, r, badRequest("before must be an RFC 3339 time"))
			return
		}
		f.BeforeTime = t
	}
	payouts, err := payout.List(r.Context(), s.pool, sandboxID(r.Context()), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payouts": payouts})
}

func (s *server) createPayout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomerID       uuid.UUID `json:"customer_id"`
		Asset            string    `json:"asset"`
		Amount           string    `json:"amount"`
		BeneficiaryCLABE string    `json:"beneficiary_clabe"`
		BeneficiaryName  string    `json:"beneficiary_name"`
		Concepto         string    `json:"concepto"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	asset, err := money.ParseAsset(req.Asset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	amount, err := money.Parse(asset, req.Amount)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ctx := r.Context()
	var p payout.Payout
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err = payout.Create(ctx, tx, sandboxID(ctx), payout.Request{
			CustomerID: req.CustomerID, Amount: amount, BeneficiaryCLABE: req.BeneficiaryCLABE,
			BeneficiaryName: req.BeneficiaryName, Concepto: req.Concepto,
		}, s.engine.PayoutConfig())
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

type payoutDetail struct {
	payout.Payout
	Events  []payout.Event       `json:"events"`
	Entries []ledger.PostedEntry `json:"entries"`
	// Rail is the bank's order for the payout's clave, once submitted.
	Rail *spei.Record `json:"rail,omitempty"`
}

func (s *server) getPayout(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ctx, sb := r.Context(), sandboxID(r.Context())
	var d payoutDetail
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var err error
			if d.Payout, err = payout.Get(ctx, tx, sb, id); err != nil {
				return err
			}
			if d.Events, err = payout.Events(ctx, tx, sb, id); err != nil {
				return err
			}
			if d.Entries, err = ledger.Entries(ctx, tx, sb, ledger.EntryFilter{SubjectType: "payout", SubjectID: id.String()}); err != nil {
				return err
			}
			order, err := s.engine.Rail().Order(ctx, tx, sb, d.Clave)
			switch {
			case err == nil:
				d.Rail = &order
			case !errors.Is(err, spei.ErrNotFound):
				return err
			}
			return nil
		})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *server) cancelPayout(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ctx := r.Context()
	var p payout.Payout
	err = store.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err = payout.Cancel(ctx, tx, sandboxID(ctx), id)
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) accounts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"accounts": ledger.All()})
}

func (s *server) balances(w http.ResponseWriter, r *http.Request) {
	balances, err := ledger.Balances(r.Context(), s.pool, sandboxID(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": balances})
}

func (s *server) entries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, err := queryInt(q.Get("before"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(q.Get("limit"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f := ledger.EntryFilter{Kind: ledger.Kind(q.Get("kind")), BeforeID: before, Limit: int(limit)}
	if f.Kind != "" && !f.Kind.Valid() {
		s.writeError(w, r, validation("unknown entry kind %q", f.Kind))
		return
	}
	entries, err := ledger.Entries(r.Context(), s.pool, sandboxID(r.Context()), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *server) railOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.engine.Rail().Orders(r.Context(), s.pool, sandboxID(r.Context()), 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
}

func (s *server) railStatement(w http.ResponseWriter, r *http.Request) {
	ctx, sb := r.Context(), sandboxID(r.Context())
	lines, err := s.engine.Rail().Statement(ctx, s.pool, sb, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	balance, err := s.engine.Rail().StatementBalance(ctx, s.pool, sb)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance": balance, "lines": lines})
}

func (s *server) runRecon(w http.ResponseWriter, r *http.Request) {
	sb, err := s.sandbox(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	run, err := s.engine.Reconcile(r.Context(), sb, recon.TriggerManual)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *server) listRecon(w http.ResponseWriter, r *http.Request) {
	runs, err := recon.Runs(r.Context(), s.pool, sandboxID(r.Context()), 20)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *server) latestRecon(w http.ResponseWriter, r *http.Request) {
	run, err := recon.Latest(r.Context(), s.pool, sandboxID(r.Context()))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *server) getRecon(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeError(w, r, badRequest("run id must be an integer"))
		return
	}
	run, err := recon.Get(r.Context(), s.pool, sandboxID(r.Context()), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

type liveResponse struct {
	Enabled  bool            `json:"enabled"`
	Networks []chain.Network `json:"networks"`
	// Address is the watched deposit address on every live network.
	Address     string         `json:"address,omitempty"`
	Deposits    []deposit.View `json:"deposits"`
	LatestRecon *recon.Run     `json:"latest_recon,omitempty"`
}

// live shows the live sandbox: the real testnet deposits to the watched
// address and the latest reconciliation. Read-only, the same for everyone.
func (s *server) live(w http.ResponseWriter, r *http.Request) {
	res := liveResponse{Enabled: s.engine.LiveEnabled(), Networks: []chain.Network{}, Deposits: []deposit.View{}}
	if !res.Enabled {
		writeJSON(w, http.StatusOK, res)
		return
	}
	ctx := r.Context()
	sb, err := s.engine.EnsureLive(ctx)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res.Networks = s.engine.Networks(sb)
	addrs, err := customer.Addresses(ctx, s.pool, sb.ID, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(addrs) > 0 {
		res.Address = addrs[0].Address
	}
	if res.Deposits, err = deposit.List(ctx, s.pool, sb.ID, deposit.Filter{Limit: 50}); err != nil {
		s.writeError(w, r, err)
		return
	}
	run, err := recon.Latest(ctx, s.pool, sb.ID)
	switch {
	case err == nil:
		res.LatestRecon = &run
	case !errors.Is(err, recon.ErrNoRuns):
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// pathUUID parses the {name} path value as a UUID.
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, notFound("no %s %q", name, r.PathValue(name))
	}
	return id, nil
}

// queryInt parses an optional non-negative integer query parameter.
func queryInt(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, badRequest("%q is not a non-negative integer", v)
	}
	return n, nil
}
