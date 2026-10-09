package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/OmarIbannez/puente/internal/api"
	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/evm"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/seed"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HTTP server timeouts.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 90 * time.Second // a first visit seeds a sandbox
	idleTimeout       = 120 * time.Second
	shutdownTimeout   = 10 * time.Second
	// verifyTimeout bounds checking a live RPC's chain id at startup.
	verifyTimeout = 15 * time.Second
)

// serveConfig is the parsed serve command line.
type serveConfig struct {
	addr      string
	staticDir string
	seedDemo  bool
	dbURL     string
}

func parseServe(args []string, stderr io.Writer) (serveConfig, error) {
	fs := newFlagSet("serve", stderr)
	var c serveConfig
	addr := defaultAddr
	if port := os.Getenv(envPort); port != "" {
		addr = ":" + port
	}
	fs.StringVar(&c.addr, "addr", addr, "listen address (env "+envPort+" gives :$PORT)")
	fs.StringVar(&c.staticDir, "static-dir", os.Getenv(envStaticDir),
		"built web app directory; without it (or without its index.html) a placeholder page is served (env "+envStaticDir+")")
	fs.BoolVar(&c.seedDemo, "seed-demo", envBool(envSeedDemo, true),
		"seed every new visitor sandbox with the demo world (env "+envSeedDemo+")")
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return serveConfig{}, err
	}
	c.dbURL = databaseURL(*db)
	if c.dbURL == "" {
		return serveConfig{}, fmt.Errorf("%w: --database-url (or %s) is required", errUsage, envDatabaseURL)
	}
	return c, nil
}

// staticFS returns the web build in dir, or the placeholder page when dir
// is empty or has no index.html.
func staticFS(dir string, log *slog.Logger) fs.FS {
	if dir == "" {
		log.Info("no static dir; serving the placeholder page")
		return api.PlaceholderFS()
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		log.Warn("web app not built; serving the placeholder page", "static_dir", dir)
		return api.PlaceholderFS()
	}
	log.Info("serving web app", "static_dir", dir)
	return os.DirFS(dir)
}

func serveCmd(ctx context.Context, args []string, _, stderr io.Writer) error {
	c, err := parseServe(args, stderr)
	if err != nil {
		return err
	}
	if _, err := payoutConfig(); err != nil {
		return err
	}
	log := newLogger(stderr)

	pool, err := openDB(ctx, c.dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	live := liveFromEnv(ctx, log)
	defer live.close()
	eng, err := newEngine(pool, log, live)
	if err != nil {
		return err
	}
	if eng.LiveEnabled() {
		if sb, err := eng.EnsureLive(ctx); err != nil {
			log.Error("live sandbox setup failed; continuing", "err", err)
		} else {
			log.Info("live sandbox ready", "sandbox", sb.ID.String(), "address", live.address)
		}
	}

	var seedFn api.SeedFunc
	if c.seedDemo {
		seedFn = func(ctx context.Context, p *pgxpool.Pool, sandbox uuid.UUID) error {
			return seed.Seed(ctx, p, eng, sandbox)
		}
	}
	handler := api.New(api.Deps{
		Pool:    pool,
		Engine:  eng,
		Static:  staticFS(c.staticDir, log),
		Logger:  log,
		Seed:    seedFn,
		Version: version,
	})

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", c.addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}

	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		eng.Loop(loopCtx, engine.DefaultLoop)
	}()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("puente listening", "addr", ln.Addr().String(), "version", version,
			"seed_demo", c.seedDemo, "live", eng.LiveEnabled())
		serveErr <- srv.Serve(ln)
	}()

	var runErr error
	select {
	case err := <-serveErr:
		runErr = fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down: draining HTTP requests", "timeout", shutdownTimeout.String())
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = fmt.Errorf("http shutdown: %w", err)
		}
		cancel()
	}
	stopLoop()
	<-loopDone
	log.Info("stopped")
	return runErr
}

// liveConfig is the live mode the engine runs with.
type liveConfig struct {
	networks      []engine.LiveNetwork
	address       string
	confirmations uint64
	closers       []func()
}

// close releases the RPC clients.
func (l liveConfig) close() {
	for _, c := range l.closers {
		c()
	}
}

// rpcEnv maps each live network to the environment variable of its RPC URL.
var rpcEnv = map[string]string{
	chain.KeyBaseSepoliaUSDC:     envLiveUSDCRPC,
	chain.KeyArbitrumSepoliaMXNB: envLiveMXNBRPC,
}

// liveKeys lists the live networks in display order.
var liveKeys = []string{chain.KeyBaseSepoliaUSDC, chain.KeyArbitrumSepoliaMXNB}

// liveFromEnv sets up live mode from the environment: it is on when
// PUENTE_LIVE_WATCH_ADDRESS and at least one RPC URL are set. Each RPC is
// dialled and its chain id verified; a network that fails is logged and
// skipped. Live mode never stops the server from starting.
func liveFromEnv(ctx context.Context, log *slog.Logger) liveConfig {
	watch := os.Getenv(envLiveWatch)
	hasRPC := false
	for _, k := range liveKeys {
		hasRPC = hasRPC || os.Getenv(rpcEnv[k]) != ""
	}
	if watch == "" || !hasRPC {
		log.Info("live mode off", "reason", "needs "+envLiveWatch+" and an RPC URL")
		return liveConfig{}
	}
	addr, err := chain.NormalizeAddress(watch)
	if err != nil {
		log.Error("live mode off: bad watch address", "err", err)
		return liveConfig{}
	}
	confirmations, err := liveConfirmations()
	if err != nil {
		log.Error("live mode off", "err", err)
		return liveConfig{}
	}
	lc := liveConfig{address: addr, confirmations: confirmations}
	for _, key := range liveKeys {
		url := os.Getenv(rpcEnv[key])
		if url == "" {
			continue
		}
		ln, closeFn, err := dialLive(ctx, key, url)
		if err != nil {
			log.Error("live network unavailable; skipping", "network", key, "err", err)
			continue
		}
		lc.networks = append(lc.networks, ln)
		lc.closers = append(lc.closers, closeFn)
		log.Info("live network ready", "network", key, "chain_id", ln.Network.ChainID)
	}
	if len(lc.networks) == 0 {
		log.Error("live mode off: no live network could be reached")
	}
	return lc
}

// liveConfirmations reads PUENTE_LIVE_CONFIRMATIONS (0, the engine default,
// when unset).
func liveConfirmations() (uint64, error) {
	s := os.Getenv(envLiveConfirmations)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%w: %s must be a positive integer, got %q", errUsage, envLiveConfirmations, s)
	}
	return n, nil
}

// dialLive connects to the RPC at url for the live network key and checks
// its chain id.
func dialLive(ctx context.Context, key, url string) (engine.LiveNetwork, func(), error) {
	network, ok := chain.NetworkByKey(key)
	if !ok || network.Simulated {
		return engine.LiveNetwork{}, nil, fmt.Errorf("%w: %q is not a live network", errUsage, key)
	}
	client, err := evm.Dial(ctx, url)
	if err != nil {
		return engine.LiveNetwork{}, nil, err
	}
	src := evm.New(client, network)
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	if err := src.Verify(vctx); err != nil {
		client.Close()
		return engine.LiveNetwork{}, nil, err
	}
	return engine.LiveNetwork{Network: network, Source: src}, client.Close, nil
}
