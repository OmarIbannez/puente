package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/OmarIbannez/puente/internal/api"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	pool *pgxpool.Pool
	srv  *httptest.Server
	eng  *engine.Engine
)

const indexHTML = "<!doctype html><title>Puente SPA</title>"

// staticFiles stands in for the built SPA.
var staticFiles = fstest.MapFS{
	"index.html":               {Data: []byte(indexHTML)},
	"assets/index-B3f9a1Zc.js": {Data: []byte("console.log('puente')")},
}

// testCustomers are created in every new sandbox by testSeed.
var testCustomers = []string{"Ana", "Beto"}

// testSeed gives a sandbox two customers and a funded SPEI account.
func testSeed(ctx context.Context, p *pgxpool.Pool, sandbox uuid.UUID) error {
	return store.InTx(ctx, p, func(tx pgx.Tx) error {
		for _, name := range testCustomers {
			if _, err := customer.Create(ctx, tx, sandbox, name); err != nil {
				return err
			}
		}
		return payout.Fund(ctx, tx, spei.Rail{}, sandbox, money.New(money.MXN, 100_000_000), "seed")
	})
}

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := storetest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = pg.Terminate() }()
	pool, err = store.Open(ctx, pg.URL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	quiet := slog.New(slog.DiscardHandler)
	eng = engine.New(engine.Config{Pool: pool, Payout: payout.DefaultConfig(), Logger: quiet})
	srv = httptest.NewTLSServer(api.New(api.Deps{
		Pool: pool, Engine: eng, Static: staticFiles, Logger: quiet, Seed: testSeed, Version: "test",
	}))
	defer srv.Close()
	return m.Run()
}

// client is one browser: its own cookie jar, hence its own sandbox.
type client struct {
	t    *testing.T
	http *http.Client
}

func newClient(t *testing.T) *client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	hc := *srv.Client()
	hc.Jar = jar
	return &client{t: t, http: &hc}
}

// response is a decoded HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do sends a request; body may be nil, a string (sent verbatim) or a value
// encoded as JSON. headers are "Name: value" pairs.
func (c *client) do(method, path string, body any, headers ...string) response {
	c.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, srv.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		req.Header.Set(name, value)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return response{status: res.StatusCode, header: res.Header, body: raw}
}

// want asserts the status and decodes the JSON body into out (if not nil).
func (r response) want(t *testing.T, status int, out any) response {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	if out != nil {
		if err := json.Unmarshal(r.body, out); err != nil {
			t.Fatalf("decode %s: %v", r.body, err)
		}
	}
	return r
}

// wantError asserts a JSON error envelope with status and code.
func (r response) wantError(t *testing.T, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("error Content-Type %q", ct)
	}
	var env map[string]map[string]string
	if err := json.Unmarshal(r.body, &env); err != nil {
		t.Fatalf("error body %s: %v", r.body, err)
	}
	e, ok := env["error"]
	if !ok || len(env) != 1 || len(e) != 2 {
		t.Fatalf("error envelope %s, want exactly {error:{code,message}}", r.body)
	}
	if e["code"] != code || e["message"] == "" {
		t.Fatalf("error %s, want code %s with a message", r.body, code)
	}
}
