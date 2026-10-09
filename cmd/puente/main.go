// Command puente runs Puente, the stablecoin-to-SPEI payout engine.
//
//	puente migrate                          apply the database migrations
//	puente serve                            run the HTTP API, the web app and the worker loop
//	puente seed                             create one seeded sandbox and print its id
//	puente payout create --sandbox ... ...  reserve a payout
//	puente recon run --sandbox ID           reconcile a sandbox and print the breaks
//	puente ledger balance --sandbox ID      print account balances and the trial balance
//	puente chain scan --network KEY         sync the live sandbox once from a testnet
//	puente chain send --network KEY ...     operator-only: send testnet tokens from the hot wallet
//	puente prune --idle-days N              delete visitor sandboxes idle for more than N days
//	puente version                          print the version
//
// Configuration is 12-factor: every command reads DATABASE_URL (or
// --database-url); serve also reads PORT, PUENTE_STATIC_DIR,
// PUENTE_SEED_DEMO, PUENTE_USDC_MXN_RATE and the live-mode variables
// PUENTE_LIVE_WATCH_ADDRESS, PUENTE_LIVE_USDC_RPC, PUENTE_LIVE_MXNB_RPC and
// PUENTE_LIVE_CONFIRMATIONS. PUENTE_HOT_WALLET_KEY is read only by
// `chain send`; the HTTP API can never send funds. See .env.example.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const version = "0.1.0"

// Environment variables.
const (
	envDatabaseURL       = "DATABASE_URL"
	envPort              = "PORT"
	envStaticDir         = "PUENTE_STATIC_DIR"
	envSeedDemo          = "PUENTE_SEED_DEMO"
	envUSDCRate          = "PUENTE_USDC_MXN_RATE"
	envLiveWatch         = "PUENTE_LIVE_WATCH_ADDRESS"
	envLiveUSDCRPC       = "PUENTE_LIVE_USDC_RPC"
	envLiveMXNBRPC       = "PUENTE_LIVE_MXNB_RPC"
	envLiveConfirmations = "PUENTE_LIVE_CONFIRMATIONS"
	envHotWalletKey      = "PUENTE_HOT_WALLET_KEY"
)

// Operational defaults.
const (
	defaultAddr     = ":8000"
	defaultIdleDays = 7
	connectTimeout  = 30 * time.Second
)

// errUsage marks a command-line error (exit status 2).
var errUsage = errors.New("usage error")

const usageText = `usage: puente <command> [flags]

commands:
  migrate          apply the database migrations
  serve            run the HTTP API, the web app and the worker loop
  seed             create one seeded demo sandbox and print its id
  payout create    reserve a payout from a customer's balance to a CLABE
  recon run        reconcile a sandbox and print the breaks
  ledger balance   print a sandbox's account balances and trial balance
  chain scan       sync the live sandbox once from a testnet
  chain send       operator-only: send testnet tokens from the hot wallet
  prune            delete visitor sandboxes idle for more than --idle-days
  version          print the version
  help             print this help

Every command reads DATABASE_URL (or --database-url).
Run 'puente <command> -h' for the command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// command runs one subcommand with its arguments.
type command func(ctx context.Context, args []string, stdout, stderr io.Writer) error

// commands maps "name" or "group name" to its implementation.
var commands = map[string]command{
	"migrate":        migrateCmd,
	"serve":          serveCmd,
	"seed":           seedCmd,
	"payout create":  payoutCreateCmd,
	"recon run":      reconRunCmd,
	"ledger balance": ledgerBalanceCmd,
	"chain scan":     chainScanCmd,
	"chain send":     chainSendCmd,
	"prune":          pruneCmd,
}

// groups are the commands that take a sub-command.
var groups = map[string]bool{"payout": true, "recon": true, "ledger": true, "chain": true}

// run executes the command line args and returns the exit status: 0 on
// success, 1 on failure, 2 on a usage error. It never exits the process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "-help", "--help":
		_, _ = fmt.Fprint(stdout, usageText)
		return 0
	}
	name, rest := args[0], args[1:]
	if groups[name] {
		if len(rest) == 0 {
			_, _ = fmt.Fprintf(stderr, "puente %s: missing sub-command\n\n%s", name, usageText)
			return 2
		}
		name, rest = name+" "+rest[0], rest[1:]
	}
	cmd, ok := commands[name]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "puente: unknown command %q\n\n%s", name, usageText)
		return 2
	}
	err := cmd(ctx, rest, stdout, stderr)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintf(stderr, "puente %s: %v\n", name, err)
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "puente %s: %v\n", name, err)
		return 1
	}
}

// envBool parses a boolean environment variable, or returns def.
func envBool(key string, def bool) bool {
	if b, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return b
	}
	return def
}

// newFlagSet returns a FlagSet that reports errors instead of exiting.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("puente "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parse parses args and rejects positional arguments.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %w", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected arguments %v", errUsage, fs.Args())
	}
	return nil
}

// required reports the first empty flag value as a usage error.
func required(flags ...[2]string) error {
	for _, f := range flags {
		if f[1] == "" {
			return fmt.Errorf("%w: --%s is required", errUsage, f[0])
		}
	}
	return nil
}

// dbFlag registers --database-url. Its environment fallback is resolved by
// databaseURL after parsing, so the URL (and its password) never shows up
// as a default in -h output.
func dbFlag(fs *flag.FlagSet) *string {
	return fs.String("database-url", "", "PostgreSQL URL (default: env "+envDatabaseURL+")")
}

// databaseURL returns the --database-url value, or DATABASE_URL when empty.
func databaseURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(envDatabaseURL)
}

// openDB connects to url (or DATABASE_URL) and applies any pending
// migration.
func openDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	url = databaseURL(url)
	if url == "" {
		return nil, fmt.Errorf("%w: --database-url (or %s) is required", errUsage, envDatabaseURL)
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	pool, err := store.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// payoutConfig is the default payout configuration, with the USDC rate
// overridden by PUENTE_USDC_MXN_RATE when set.
func payoutConfig() (payout.Config, error) {
	cfg := payout.DefaultConfig()
	if s := os.Getenv(envUSDCRate); s != "" {
		r, err := money.ParseRate(s)
		if err != nil {
			return payout.Config{}, fmt.Errorf("%w: %s: %w", errUsage, envUSDCRate, err)
		}
		cfg.Rates[money.USDC] = r
	}
	return cfg, nil
}

// newEngine returns an engine on the simulated rail; live networks, if
// any, come from the caller.
func newEngine(pool *pgxpool.Pool, log *slog.Logger, live liveConfig) (*engine.Engine, error) {
	pc, err := payoutConfig()
	if err != nil {
		return nil, err
	}
	return engine.New(engine.Config{
		Pool:              pool,
		Rail:              spei.Rail{},
		Payout:            pc,
		Live:              live.networks,
		LiveAddress:       live.address,
		LiveConfirmations: live.confirmations,
		Logger:            log,
	}), nil
}

// sandboxFlag registers the required --sandbox flag.
func sandboxFlag(fs *flag.FlagSet) *string {
	return fs.String("sandbox", "", "sandbox id (required)")
}

// loadSandbox parses id and reads the sandbox.
func loadSandbox(ctx context.Context, pool *pgxpool.Pool, id string) (store.Sandbox, error) {
	sid, err := uuid.Parse(id)
	if err != nil {
		return store.Sandbox{}, fmt.Errorf("%w: --sandbox: %w", errUsage, err)
	}
	sb, err := store.GetSandbox(ctx, pool, sid)
	if err != nil {
		return store.Sandbox{}, fmt.Errorf("sandbox %s: %w", sid, err)
	}
	return sb, nil
}
