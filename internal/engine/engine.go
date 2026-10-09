// Package engine wires the domain packages into the per-sandbox cycle the
// service runs: sync each chain (scan, confirm, credit), move payouts
// (outbox and rail sync), and reconcile. The API, the CLI and the
// background loop all go through it, so a sandbox behaves the same however
// it is driven.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/sim"
	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/deposit"
	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/payout"
	"github.com/OmarIbannez/puente/internal/recon"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Defaults.
const (
	// SimConfirmations is how many simulated blocks confirm a deposit.
	SimConfirmations = 3
	// DefaultLiveConfirmations confirms a live testnet deposit.
	DefaultLiveConfirmations = 5
	// SimSender is the external address simulated deposits come from.
	SimSender = "0x00000000000000000000000000000000c0ffee01"
	// LiveCustomerName owns the live sandbox's watched address.
	LiveCustomerName = "Testnet treasury"
)

// ErrNoSimNetwork is returned when an asset has no simulated network.
var ErrNoSimNetwork = errors.New("engine: no simulated network for asset")

// ErrLiveReadOnly is returned for a write attempted on the live sandbox.
var ErrLiveReadOnly = errors.New("engine: the live sandbox is read-only")

// LiveNetwork is a real testnet the live sandbox follows.
type LiveNetwork struct {
	Network chain.Network
	Source  chain.Source
}

// Config configures an Engine.
type Config struct {
	Pool   *pgxpool.Pool
	Rail   spei.Rail
	Payout payout.Config
	// Live lists the real testnets the live sandbox watches; empty disables it.
	Live []LiveNetwork
	// LiveAddress is the address watched on every live network.
	LiveAddress string
	// LiveConfirmations confirms live deposits (DefaultLiveConfirmations if 0).
	LiveConfirmations uint64
	Logger            *slog.Logger
}

// Engine runs sandboxes.
type Engine struct {
	cfg    Config
	worker payout.Worker
	log    *slog.Logger

	liveMu sync.Mutex
	live   *store.Sandbox // set once EnsureLive succeeds
}

// New returns an engine.
func New(cfg Config) *Engine {
	if cfg.LiveConfirmations == 0 {
		cfg.LiveConfirmations = DefaultLiveConfirmations
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Engine{cfg: cfg, worker: payout.Worker{Rail: cfg.Rail, Config: cfg.Payout}, log: log}
}

// Rail returns the SPEI rail.
func (e *Engine) Rail() spei.Rail { return e.cfg.Rail }

// PayoutConfig returns the payout configuration.
func (e *Engine) PayoutConfig() payout.Config { return e.cfg.Payout }

// LiveEnabled reports whether any live network is configured.
func (e *Engine) LiveEnabled() bool { return len(e.cfg.Live) > 0 && e.cfg.LiveAddress != "" }

// network is one chain a sandbox follows, with how to read it.
type network struct {
	net           chain.Network
	confirmations uint64
	// source reads the chain through q (the simulated chain lives in the
	// database; a live source ignores q).
	source func(q store.Querier, sandbox uuid.UUID) chain.Source
}

// Networks returns the networks a sandbox follows: the simulated chains for
// a visitor, the configured testnets for the live sandbox.
func (e *Engine) Networks(sb store.Sandbox) []chain.Network {
	nets := e.networks(sb)
	out := make([]chain.Network, len(nets))
	for i, n := range nets {
		out[i] = n.net
	}
	return out
}

func (e *Engine) networks(sb store.Sandbox) []network {
	if sb.Kind == store.KindLive {
		out := make([]network, len(e.cfg.Live))
		for i, l := range e.cfg.Live {
			src := l.Source
			out[i] = network{
				net: l.Network, confirmations: e.cfg.LiveConfirmations,
				source: func(store.Querier, uuid.UUID) chain.Source { return src },
			}
		}
		return out
	}
	var out []network
	for _, n := range chain.Networks {
		if !n.Simulated {
			continue
		}
		out = append(out, network{
			net: n, confirmations: SimConfirmations,
			source: func(q store.Querier, sandbox uuid.UUID) chain.Source { return sim.New(q, sandbox, n) },
		})
	}
	return out
}

// TickReport summarizes one cycle of a sandbox.
type TickReport struct {
	Deposits map[string]deposit.SyncResult `json:"deposits"`
	Payouts  payout.TickResult             `json:"payouts"`
}

// Tick runs one cycle of the sandbox: for a visitor, the simulated chains
// mine a block when something is waiting for confirmations; then every
// chain is synced and credited, and payouts move.
func (e *Engine) Tick(ctx context.Context, sb store.Sandbox, now time.Time) (TickReport, error) {
	report := TickReport{Deposits: map[string]deposit.SyncResult{}}
	for _, n := range e.networks(sb) {
		if n.net.Simulated {
			if err := e.mineIfWaiting(ctx, sb, n.net); err != nil {
				return report, err
			}
		}
		watch, err := deposit.WatchAddresses(ctx, e.cfg.Pool, sb.ID, n.net)
		if err != nil {
			return report, err
		}
		if len(watch) == 0 {
			continue
		}
		res, err := deposit.Sync(ctx, e.cfg.Pool, sb.ID, n.net, n.source(e.cfg.Pool, sb.ID), watch, n.confirmations)
		if err != nil {
			return report, fmt.Errorf("engine: sync %s: %w", n.net.Key, err)
		}
		report.Deposits[n.net.Key] = res
	}
	if sb.Kind == store.KindLive {
		return report, nil
	}
	res, err := e.worker.Tick(ctx, e.cfg.Pool, sb.ID, now)
	if err != nil {
		return report, fmt.Errorf("engine: payouts: %w", err)
	}
	report.Payouts = res
	return report, nil
}

// mineIfWaiting mines one simulated block when a transfer awaits
// confirmations or a reorg fault is armed, so the simulated chain only
// grows while something is happening.
func (e *Engine) mineIfWaiting(ctx context.Context, sb store.Sandbox, net chain.Network) error {
	return store.InTx(ctx, e.cfg.Pool, func(tx pgx.Tx) error {
		var waiting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chain_transfers
			WHERE sandbox_id = $1 AND network = $2 AND status = 'seen')`, sb.ID, net.Key).Scan(&waiting); err != nil {
			return fmt.Errorf("engine: pending transfers: %w", err)
		}
		if !waiting {
			var credited bool
			if sb.Faults.Reorg {
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chain_transfers
					WHERE sandbox_id = $1 AND network = $2 AND status = 'confirmed')`, sb.ID, net.Key).Scan(&credited); err != nil {
					return fmt.Errorf("engine: confirmed transfers: %w", err)
				}
			}
			if !credited {
				return nil
			}
		}
		_, err := sim.New(tx, sb.ID, net).Mine(ctx, tx, 1)
		return err
	})
}

// Reconcile runs a reconciliation of the sandbox.
func (e *Engine) Reconcile(ctx context.Context, sb store.Sandbox, trigger string) (recon.Run, error) {
	var matchers []recon.Matcher
	for _, n := range e.networks(sb) {
		matchers = append(matchers, recon.ChainMatcher{
			Network: n.net,
			Source:  func(tx pgx.Tx, sandbox uuid.UUID) chain.Source { return n.source(tx, sandbox) },
		})
	}
	return recon.New(e.cfg.Rail, matchers...).Run(ctx, e.cfg.Pool, sb.ID, trigger)
}

// SimulateDeposit sends amount of asset to the customer's address on the
// simulated chain (assigning the address on first use). Faults armed on the
// sandbox apply. The deposit is credited once confirmed by later ticks.
func (e *Engine) SimulateDeposit(ctx context.Context, sb store.Sandbox, customerID uuid.UUID, amount money.Amount) (chain.Transfer, error) {
	if sb.Kind == store.KindLive {
		return chain.Transfer{}, ErrLiveReadOnly
	}
	net, ok := chain.SimNetwork(amount.Asset)
	if !ok {
		return chain.Transfer{}, fmt.Errorf("%w: %s", ErrNoSimNetwork, amount.Asset)
	}
	var t chain.Transfer
	err := store.InTx(ctx, e.cfg.Pool, func(tx pgx.Tx) error {
		if _, err := customer.Get(ctx, tx, sb.ID, customerID); err != nil {
			return err
		}
		addr, err := customer.AssignAddress(ctx, tx, sb.ID, customerID, net.Key, customer.SimAddress(sb.ID, customerID, net.Key))
		if err != nil {
			return err
		}
		t, err = sim.New(tx, sb.ID, net).Deposit(ctx, tx, SimSender, addr.Address, amount)
		return err
	})
	return t, err
}

// EnsureLive creates the live sandbox and its customer, and assigns the
// watched address on every live network, the first time it is called; later
// calls return the cached sandbox. It is a no-op when live mode is off.
func (e *Engine) EnsureLive(ctx context.Context) (store.Sandbox, error) {
	if !e.LiveEnabled() {
		return store.Sandbox{}, nil
	}
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.live != nil {
		return *e.live, nil
	}
	var sb store.Sandbox
	err := store.InTx(ctx, e.cfg.Pool, func(tx pgx.Tx) error {
		var err error
		if sb, err = store.LiveSandbox(ctx, tx); err != nil {
			return err
		}
		if _, err := store.LockSandbox(ctx, tx, sb.ID); err != nil {
			return err
		}
		customers, err := customer.List(ctx, tx, sb.ID)
		if err != nil {
			return err
		}
		var owner uuid.UUID
		if len(customers) > 0 {
			owner = customers[0].ID
		} else {
			c, err := customer.Create(ctx, tx, sb.ID, LiveCustomerName)
			if err != nil {
				return err
			}
			owner = c.ID
		}
		for _, l := range e.cfg.Live {
			if _, err := customer.SetAddress(ctx, tx, sb.ID, owner, l.Network.Key, e.cfg.LiveAddress); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return store.Sandbox{}, err
	}
	e.live = &sb
	return sb, nil
}

// LoopConfig paces the background loop.
type LoopConfig struct {
	// Interval between cycles over the active sandboxes.
	Interval time.Duration
	// ActiveWindow: sandboxes seen within it are ticked.
	ActiveWindow time.Duration
	// ReconEvery runs a scheduled reconciliation of each active sandbox.
	ReconEvery time.Duration
	// PruneIdleDays deletes visitor sandboxes idle for longer (0 disables).
	PruneIdleDays int
	// LiveEvery paces the live sandbox, whose ticks call public RPCs.
	LiveEvery time.Duration
}

// DefaultLoop is the loop pacing of `puente serve`.
var DefaultLoop = LoopConfig{
	Interval: 2 * time.Second, ActiveWindow: 30 * time.Minute,
	ReconEvery: 5 * time.Minute, PruneIdleDays: 7, LiveEvery: 15 * time.Second,
}

// Loop ticks every active sandbox each interval, reconciles them every
// ReconEvery and prunes idle ones hourly, until ctx is cancelled. Errors are
// logged per sandbox; one failing sandbox never stops the others.
func (e *Engine) Loop(ctx context.Context, lc LoopConfig) {
	ticker := time.NewTicker(lc.Interval)
	defer ticker.Stop()
	var lastRecon, lastPrune, lastLive time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			reconcile := lc.ReconEvery > 0 && now.Sub(lastRecon) >= lc.ReconEvery
			live := e.LiveEnabled() && now.Sub(lastLive) >= lc.LiveEvery
			e.cycle(ctx, lc, now, reconcile, live)
			if reconcile {
				lastRecon = now
			}
			if live {
				lastLive = now
			}
			if lc.PruneIdleDays > 0 && now.Sub(lastPrune) >= time.Hour {
				lastPrune = now
				if n, err := store.PruneIdleSandboxes(ctx, e.cfg.Pool, lc.PruneIdleDays); err != nil {
					e.log.ErrorContext(ctx, "prune sandboxes", "err", err)
				} else if n > 0 {
					e.log.InfoContext(ctx, "pruned idle sandboxes", "count", n)
				}
			}
		}
	}
}

func (e *Engine) cycle(ctx context.Context, lc LoopConfig, now time.Time, reconcile, live bool) {
	ids, err := store.ActiveSandboxes(ctx, e.cfg.Pool, lc.ActiveWindow)
	if err != nil {
		e.log.ErrorContext(ctx, "list active sandboxes", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		sb, err := store.GetSandbox(ctx, e.cfg.Pool, id)
		if err != nil {
			continue // pruned meanwhile
		}
		if sb.Kind == store.KindLive && !live {
			continue
		}
		if _, err := e.Tick(ctx, sb, now); err != nil {
			e.log.ErrorContext(ctx, "tick sandbox", "sandbox", id.String(), "err", err)
			continue
		}
		if reconcile {
			if _, err := e.Reconcile(ctx, sb, recon.TriggerScheduled); err != nil {
				e.log.ErrorContext(ctx, "reconcile sandbox", "sandbox", id.String(), "err", err)
			}
		}
	}
}
