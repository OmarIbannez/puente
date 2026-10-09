package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/ledger"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/seed"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func migrateCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("migrate", stderr)
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	pool.Close()
	_, _ = fmt.Fprintln(stdout, "migrations applied")
	return nil
}

func seedCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("seed", stderr)
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	defer pool.Close()
	eng, err := newEngine(pool, newLogger(stderr), liveConfig{})
	if err != nil {
		return err
	}
	sb, err := store.CreateSandbox(ctx, pool)
	if err != nil {
		return err
	}
	if err := seed.Seed(ctx, pool, eng, sb.ID); err != nil {
		return errors.Join(err, store.DeleteSandbox(context.WithoutCancel(ctx), pool, sb.ID))
	}
	_, _ = fmt.Fprintln(stdout, sb.ID)
	return nil
}

func pruneCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("prune", stderr)
	idle := fs.Int("idle-days", defaultIdleDays, "delete visitor sandboxes idle for more than this many days")
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if *idle < 0 {
		return fmt.Errorf("%w: --idle-days must not be negative", errUsage)
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	defer pool.Close()
	n, err := store.PruneIdleSandboxes(ctx, pool, *idle)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "pruned %d sandboxes idle for more than %d days\n", n, *idle)
	return nil
}

func payoutCreateCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("payout create", stderr)
	sbID := sandboxFlag(fs)
	cust := fs.String("customer", "", "customer id or exact name (required)")
	assetStr := fs.String("asset", "", "stablecoin to pay out: USDC or MXNB (required)")
	amountStr := fs.String("amount", "", "decimal amount the beneficiary receives, in --asset (required)")
	clabe := fs.String("clabe", "", "beneficiary's 18-digit CLABE (required)")
	name := fs.String("name", "", "beneficiary's name (required)")
	concepto := fs.String("concepto", "", "payment concept shown on the SPEI transfer")
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := required([2]string{"sandbox", *sbID}, [2]string{"customer", *cust}, [2]string{"asset", *assetStr},
		[2]string{"amount", *amountStr}, [2]string{"clabe", *clabe}, [2]string{"name", *name}); err != nil {
		return err
	}
	asset, err := money.ParseAsset(*assetStr)
	if err != nil {
		return fmt.Errorf("%w: --asset: %w", errUsage, err)
	}
	amount, err := money.Parse(asset, *amountStr)
	if err != nil {
		return fmt.Errorf("%w: --amount: %w", errUsage, err)
	}
	cfg, err := payoutConfig()
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	defer pool.Close()
	sb, err := loadSandbox(ctx, pool, *sbID)
	if err != nil {
		return err
	}
	if sb.Kind == store.KindLive {
		return engine.ErrLiveReadOnly
	}
	var p payout.Payout
	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		cid, err := resolveCustomer(ctx, tx, sb.ID, *cust)
		if err != nil {
			return err
		}
		p, err = payout.Create(ctx, tx, sb.ID, payout.Request{
			CustomerID: cid, Amount: amount, BeneficiaryCLABE: *clabe, BeneficiaryName: *name, Concepto: *concepto,
		}, cfg)
		return err
	})
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"payout", p.ID.String()},
		{"status", string(p.Status)},
		{"clave", p.Clave},
		{"amount", p.Amount.String()},
		{"fee", p.Fee.String()},
		{"total", p.Total().String()},
		{"rate", p.Rate.String() + " MXN/" + string(p.Amount.Asset)},
		{"mxn", p.MXN.String()},
		{"to", p.BeneficiaryName + " · " + p.BeneficiaryCLABE},
	}
	if p.Concepto != "" {
		rows = append(rows, [2]string{"concepto", p.Concepto})
	}
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1])
	}
	return tw.Flush()
}

// resolveCustomer accepts a customer id or an exact (case-insensitive) name.
func resolveCustomer(ctx context.Context, q store.Querier, sandbox uuid.UUID, ref string) (uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	list, err := customer.List(ctx, q, sandbox)
	if err != nil {
		return uuid.Nil, err
	}
	var found []uuid.UUID
	for _, c := range list {
		if strings.EqualFold(c.Name, strings.TrimSpace(ref)) {
			found = append(found, c.ID)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return uuid.Nil, fmt.Errorf("%w: %q", payout.ErrCustomerNotFound, ref)
	default:
		return uuid.Nil, fmt.Errorf("%w: %d customers are named %q; use the id", errUsage, len(found), ref)
	}
}

func reconRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("recon run", stderr)
	sbID := sandboxFlag(fs)
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := required([2]string{"sandbox", *sbID}); err != nil {
		return err
	}
	pool, eng, sb, err := openSandbox(ctx, *db, *sbID, stderr)
	if err != nil {
		return err
	}
	defer pool.Close()
	run, err := eng.Reconcile(ctx, sb, recon.TriggerCLI)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "recon run %d on sandbox %s: %d breaks\n", run.ID, sb.ID, run.BreakCount)
	for _, k := range slices.Sorted(maps.Keys(run.Checked)) {
		_, _ = fmt.Fprintf(stdout, "  checked %-14s %d\n", k, run.Checked[k])
	}
	if len(run.Breaks) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "\nKIND\tSOURCE\tSUBJECT\tEXPECTED\tACTUAL\tSUMMARY")
	for _, b := range run.Breaks {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s\t%s\t%s\n",
			b.Kind, b.Source, b.SubjectType, b.SubjectID, dash(b.Expected), dash(b.Actual), b.Summary)
	}
	return tw.Flush()
}

func ledgerBalanceCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ledger balance", stderr)
	sbID := sandboxFlag(fs)
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := required([2]string{"sandbox", *sbID}); err != nil {
		return err
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	defer pool.Close()
	sb, err := loadSandbox(ctx, pool, *sbID)
	if err != nil {
		return err
	}
	balances, err := ledger.Balances(ctx, pool, sb.ID)
	if err != nil {
		return err
	}
	tb, err := ledger.TrialBalance(ctx, pool, sb.ID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "ledger of sandbox %s\n\n", sb.ID)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintln(tw, "ACCOUNT\tNAME\tTYPE\tBALANCE\t")
	for _, b := range balances {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t\n", int(b.Code), b.Name, b.Type, b.Balance)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "\ntrial balance (sum of postings per asset; zero when balanced):")
	balanced := true
	for _, a := range money.Assets {
		_, _ = fmt.Fprintf(stdout, "  %-5s %d\n", a, tb[a])
		balanced = balanced && tb[a] == 0
	}
	if !balanced {
		return errors.New("trial balance is not zero")
	}
	_, _ = fmt.Fprintln(stdout, "balanced")
	return nil
}

// openSandbox opens the database, reads the sandbox and builds an engine on
// the simulated rail (no live networks: the live sandbox is reconciled
// against the rail and balances only).
func openSandbox(ctx context.Context, dbURL, sbID string, stderr io.Writer) (*pgxpool.Pool, *engine.Engine, store.Sandbox, error) {
	pool, err := openDB(ctx, dbURL)
	if err != nil {
		return nil, nil, store.Sandbox{}, err
	}
	sb, err := loadSandbox(ctx, pool, sbID)
	if err != nil {
		pool.Close()
		return nil, nil, store.Sandbox{}, err
	}
	eng, err := newEngine(pool, newLogger(stderr), liveConfig{})
	if err != nil {
		pool.Close()
		return nil, nil, store.Sandbox{}, err
	}
	return pool, eng, sb, nil
}

// dash returns s, or "-" when it is empty.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
