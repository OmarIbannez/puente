package api

import (
	"net/http"
	"slices"
	"strings"
)

// routes registers every endpoint. Everything under /api except
// /api/health, /api/networks, /api/live/* and /api/sandbox/reset runs
// inside the sandbox middleware; money-moving POSTs also require an
// Idempotency-Key.
func (s *server) routes(static http.Handler) {
	sb := s.withSandbox
	idem := func(h http.HandlerFunc) http.HandlerFunc { return sb(s.idempotent(h)) }

	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /api/networks", s.networks)

	// Sandbox, faults and the demo clock.
	s.mux.HandleFunc("GET /api/sandbox", sb(s.getSandbox))
	s.mux.HandleFunc("POST /api/sandbox/reset", s.resetSandbox) // manages its own cookie
	s.mux.HandleFunc("PUT /api/sandbox/faults", sb(s.setFaults))
	s.mux.HandleFunc("POST /api/sandbox/advance", sb(s.advance))
	s.mux.HandleFunc("POST /api/sandbox/inject", sb(s.inject))

	// Overview, customers and deposits.
	s.mux.HandleFunc("GET /api/overview", sb(s.overview))
	s.mux.HandleFunc("GET /api/customers", sb(s.listCustomers))
	s.mux.HandleFunc("GET /api/deposits", sb(s.listDeposits))
	s.mux.HandleFunc("POST /api/deposits/simulate", idem(s.simulateDeposit))

	// Payouts.
	s.mux.HandleFunc("GET /api/payouts", sb(s.listPayouts))
	s.mux.HandleFunc("POST /api/payouts", idem(s.createPayout))
	s.mux.HandleFunc("GET /api/payouts/{id}", sb(s.getPayout))
	s.mux.HandleFunc("POST /api/payouts/{id}/cancel", idem(s.cancelPayout))

	// Books: ledger and the bank's side.
	s.mux.HandleFunc("GET /api/ledger/accounts", s.accounts)
	s.mux.HandleFunc("GET /api/ledger/balances", sb(s.balances))
	s.mux.HandleFunc("GET /api/ledger/entries", sb(s.entries))
	s.mux.HandleFunc("GET /api/rail/orders", sb(s.railOrders))
	s.mux.HandleFunc("GET /api/rail/statement", sb(s.railStatement))

	// Reconciliation.
	s.mux.HandleFunc("POST /api/recon/runs", sb(s.runRecon))
	s.mux.HandleFunc("GET /api/recon/runs", sb(s.listRecon))
	s.mux.HandleFunc("GET /api/recon/runs/latest", sb(s.latestRecon))
	s.mux.HandleFunc("GET /api/recon/runs/{id}", sb(s.getRecon))

	// The live sandbox, read-only for everyone.
	s.mux.HandleFunc("GET /api/live", s.live)

	// Unknown API paths and methods get JSON errors, not the SPA.
	s.mux.HandleFunc("/api/", s.apiFallback)
	s.mux.HandleFunc("/api", s.apiFallback)

	s.mux.Handle("/", spaOnly(static))
}

// probeMethods are tried to tell a 405 from a 404.
var probeMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// apiFallback answers /api requests no route matched: 405 when the path
// exists for another method, else 404.
func (s *server) apiFallback(w http.ResponseWriter, r *http.Request) {
	var allowed []string
	for _, m := range probeMethods {
		if m == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = m
		if _, pattern := s.mux.Handler(probe); pattern != "" && !isFallbackPattern(pattern) {
			allowed = append(allowed, m)
		}
	}
	if len(allowed) > 0 {
		slices.Sort(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		s.writeError(w, r, errorf(http.StatusMethodNotAllowed, CodeMethodNotAllowed,
			"%s is not allowed on %s", r.Method, r.URL.Path))
		return
	}
	s.writeError(w, r, notFound("no API route %s %s", r.Method, r.URL.Path))
}

func isFallbackPattern(p string) bool { return p == "/api/" || p == "/api" || p == "/" }

// spaOnly serves the SPA for GET and HEAD and rejects other methods.
func spaOnly(static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		static.ServeHTTP(w, r)
	})
}
