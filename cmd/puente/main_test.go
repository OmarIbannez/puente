package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

// dbURL is the test database, shared by every test.
var dbURL string

func TestMain(m *testing.M) { os.Exit(testMain(m)) }

func testMain(m *testing.M) int {
	// Tests never touch a live RPC or a real key.
	for _, k := range []string{envLiveWatch, envLiveUSDCRPC, envLiveMXNBRPC, envHotWalletKey, envUSDCRate, envPort, envStaticDir} {
		_ = os.Unsetenv(k)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pg, err := storetest.Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = pg.Terminate() }()
	dbURL = pg.URL
	return m.Run()
}

// result is one run of the CLI.
type result struct {
	code           int
	stdout, stderr string
}

// cli runs the command line args (plus --database-url when db is true).
func cli(t *testing.T, db bool, args ...string) result {
	t.Helper()
	if db {
		args = append(args, "--database-url", dbURL)
	}
	var out, errOut bytes.Buffer
	code := run(t.Context(), args, &out, &errOut)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func (r result) must(t *testing.T) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", r.code, r.stderr)
	}
	return r
}

// seedSandbox runs `puente seed` and returns the printed sandbox id.
func seedSandbox(t *testing.T) string {
	t.Helper()
	out := strings.TrimSpace(cli(t, true, "seed").must(t).stdout)
	if _, err := uuid.Parse(out); err != nil {
		t.Fatalf("seed printed %q, want a UUID", out)
	}
	return out
}

func TestUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string
		wantStderr string
	}{
		{name: "version", args: []string{"version"}, wantOut: version},
		{name: "help", args: []string{"help"}, wantOut: "chain send"},
		{name: "no args", args: nil, wantCode: 2, wantStderr: "usage: puente"},
		{name: "unknown", args: []string{"bogus"}, wantCode: 2, wantStderr: "usage: puente"},
		{name: "group without sub-command", args: []string{"payout"}, wantCode: 2, wantStderr: "missing sub-command"},
		{name: "unknown sub-command", args: []string{"chain", "mint"}, wantCode: 2, wantStderr: `unknown command "chain mint"`},
		{name: "unknown flag", args: []string{"serve", "--nope"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "serve no database", args: []string{"serve", "--database-url="}, wantCode: 2, wantStderr: "database-url"},
		{name: "serve bad bool", args: []string{"serve", "--seed-demo=perhaps"}, wantCode: 2, wantStderr: "invalid boolean"},
		{name: "serve help", args: []string{"serve", "-h"}, wantStderr: "-static-dir"},
		{name: "migrate positional", args: []string{"migrate", "now"}, wantCode: 2, wantStderr: "unexpected arguments"},
		{name: "prune negative", args: []string{"prune", "--idle-days", "-1"}, wantCode: 2, wantStderr: "must not be negative"},
		{name: "payout missing flags", args: []string{"payout", "create", "--sandbox", "x"}, wantCode: 2, wantStderr: "--customer is required"},
		{name: "recon no sandbox", args: []string{"recon", "run"}, wantCode: 2, wantStderr: "--sandbox is required"},
		{name: "scan simulated network", args: []string{"chain", "scan", "--network", "sim-usdc", "--address", "0x00000000000000000000000000000000000000aa"}, wantCode: 2, wantStderr: "--network must be one of"},
		{name: "scan without rpc", args: []string{"chain", "scan", "--network", "base-sepolia-usdc", "--address", "0x00000000000000000000000000000000000000aa"}, wantCode: 2, wantStderr: envLiveUSDCRPC + " is not set"},
		{name: "send without key", args: []string{"chain", "send", "--network", "base-sepolia-usdc", "--to", "0x00000000000000000000000000000000000000aa", "--amount", "1", "--yes"}, wantCode: 1, wantStderr: envHotWalletKey + " is not set"},
		{name: "send bad amount", args: []string{"chain", "send", "--network", "base-sepolia-usdc", "--to", "0x00000000000000000000000000000000000000aa", "--amount", "0.0000001"}, wantCode: 2, wantStderr: "--amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := cli(t, false, tt.args...)
			if r.code != tt.wantCode {
				t.Fatalf("run(%v) = %d, want %d (stderr %q)", tt.args, r.code, tt.wantCode, r.stderr)
			}
			if !strings.Contains(r.stdout, tt.wantOut) {
				t.Fatalf("stdout = %q, want it to contain %q", r.stdout, tt.wantOut)
			}
			if !strings.Contains(r.stderr, tt.wantStderr) {
				t.Fatalf("stderr = %q, want it to contain %q", r.stderr, tt.wantStderr)
			}
		})
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	for range 2 {
		if r := cli(t, true, "migrate").must(t); !strings.Contains(r.stdout, "migrations applied") {
			t.Fatalf("stdout = %q", r.stdout)
		}
	}
}

func TestSeedThenLedgerAndRecon(t *testing.T) {
	t.Parallel()
	sb := seedSandbox(t)

	ledger := cli(t, true, "ledger", "balance", "--sandbox", sb).must(t).stdout
	for _, want := range []string{"1100", "SPEI settlement account", "2010", "trial balance", "balanced"} {
		if !strings.Contains(ledger, want) {
			t.Errorf("ledger balance output lacks %q:\n%s", want, ledger)
		}
	}

	recon := cli(t, true, "recon", "run", "--sandbox", sb).must(t).stdout
	if !strings.Contains(recon, "0 breaks") {
		t.Errorf("recon run output lacks %q:\n%s", "0 breaks", recon)
	}

	if r := cli(t, true, "recon", "run", "--sandbox", uuid.NewString()); r.code != 1 || !strings.Contains(r.stderr, "not found") {
		t.Errorf("recon run on an unknown sandbox: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestPayoutCreate(t *testing.T) {
	t.Parallel()
	sb := seedSandbox(t)
	base := []string{
		"payout", "create", "--sandbox", sb, "--customer", "Ana Torres", "--asset", "USDC",
		"--clabe", "012180015203300154", "--name", "Luis Torres", "--concepto", "Renta noviembre",
	}

	out := cli(t, true, append(base, "--amount", "10")...).must(t).stdout
	for _, want := range []string{"reserved", "10.000000 USDC", "0.050000 USDC", "$184.52 MXN", "Renta noviembre"} {
		if !strings.Contains(out, want) {
			t.Errorf("payout create output lacks %q:\n%s", want, out)
		}
	}

	r := cli(t, true, append(base, "--amount", "100000")...)
	if r.code != 1 || !strings.Contains(r.stderr, "insufficient funds") || r.stdout != "" {
		t.Errorf("overdraft: exit %d, stdout %q, stderr %q; want exit 1 with insufficient funds", r.code, r.stdout, r.stderr)
	}

	r = cli(t, true, "payout", "create", "--sandbox", sb, "--customer", "Ana Torres", "--asset", "USDC",
		"--amount", "1", "--clabe", "012180015203300155", "--name", "Luis Torres")
	if r.code != 1 || !strings.Contains(r.stderr, "CLABE") {
		t.Errorf("bad CLABE: exit %d, stderr %q", r.code, r.stderr)
	}

	// The books still balance after the CLI's payout.
	if !strings.Contains(cli(t, true, "recon", "run", "--sandbox", sb).must(t).stdout, "0 breaks") {
		t.Error("recon after payout create found breaks")
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	out := cli(t, true, "prune", "--idle-days", "30").must(t).stdout
	if !strings.Contains(out, "pruned") {
		t.Errorf("stdout = %q", out)
	}
}

// TestChainSendDryRun sets the env, so it cannot run in parallel.
func TestChainSendDryRun(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envHotWalletKey, hex.EncodeToString(crypto.FromECDSA(key)))
	to := "0x00000000000000000000000000000000000000aa"

	r := cli(t, false, "chain", "send", "--network", "base-sepolia-usdc", "--to", to, "--amount", "2.5").must(t)
	from := crypto.PubkeyToAddress(key.PublicKey).Hex()
	for _, want := range []string{"dry run", "nothing sent", from, "2.500000 USDC", "84532"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout+r.stderr, hex.EncodeToString(crypto.FromECDSA(key))) {
		t.Error("the private key was printed")
	}

	// --yes without an RPC URL fails before any network access.
	r = cli(t, false, "chain", "send", "--network", "base-sepolia-usdc", "--to", to, "--amount", "2.5", "--yes")
	if r.code != 2 || !strings.Contains(r.stderr, envLiveUSDCRPC+" is not set") {
		t.Errorf("send --yes without rpc: exit %d, stderr %q", r.code, r.stderr)
	}

	t.Setenv(envHotWalletKey, "not-hex")
	r = cli(t, false, "chain", "send", "--network", "base-sepolia-usdc", "--to", to, "--amount", "2.5")
	if r.code != 1 || !strings.Contains(r.stderr, "not a valid") || strings.Contains(r.stderr, "not-hex") {
		t.Errorf("bad key: exit %d, stderr %q", r.code, r.stderr)
	}
}

// TestUSDCRateOverride sets the env, so it cannot run in parallel.
func TestUSDCRateOverride(t *testing.T) {
	t.Setenv(envUSDCRate, "20")
	sb := seedSandbox(t)
	out := cli(t, true, "payout", "create", "--sandbox", sb, "--customer", "Ana Torres", "--asset", "USDC",
		"--amount", "10", "--clabe", "012180015203300154", "--name", "Luis Torres").must(t).stdout
	if !strings.Contains(out, "$200.00 MXN") {
		t.Errorf("payout at the overridden rate lacks $200.00 MXN:\n%s", out)
	}
	t.Setenv(envUSDCRate, "cheap")
	if r := cli(t, true, "seed"); r.code != 2 || !strings.Contains(r.stderr, envUSDCRate) {
		t.Errorf("bad rate: exit %d, stderr %q", r.code, r.stderr)
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// listenAddr returns the address from the "puente listening" log line.
func listenAddr(logs string) string {
	sc := bufio.NewScanner(strings.NewReader(logs))
	for sc.Scan() {
		var line struct {
			Msg  string `json:"msg"`
			Addr string `json:"addr"`
		}
		if json.Unmarshal(sc.Bytes(), &line) == nil && line.Msg == "puente listening" {
			return line.Addr
		}
	}
	return ""
}

func TestServe(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--database-url", dbURL}, &stdout, &stderr)
	}()

	var addr string
	deadline := time.Now().Add(20 * time.Second)
	for addr == "" {
		select {
		case code := <-done:
			t.Fatalf("serve exited early with %d:\n%s", code, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not start:\n%s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
		addr = listenAddr(stderr.String())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
		Live    bool   `json:"live"`
	}
	err = json.NewDecoder(res.Body).Decode(&health)
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || health.Status != "ok" || health.Version != version || health.Live {
		t.Errorf("health = %d %+v, want 200 ok %s, live off", res.StatusCode, health, version)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exited with %d:\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
	if !strings.Contains(stderr.String(), `"msg":"stopped"`) {
		t.Errorf("no graceful stop logged:\n%s", stderr.String())
	}
}
