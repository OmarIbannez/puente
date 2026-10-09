package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/chain/evm"
	"github.com/OmarIbannez/puente/internal/engine"
	"github.com/OmarIbannez/puente/internal/money"

	"github.com/ethereum/go-ethereum/crypto"
)

// errNoHotWalletKey is returned by chain send without PUENTE_HOT_WALLET_KEY.
var errNoHotWalletKey = errors.New(envHotWalletKey + " is not set")

// liveNetwork resolves --network to a live network and its RPC URL from the
// environment.
func liveNetwork(key string) (chain.Network, string, error) {
	n, ok := chain.NetworkByKey(key)
	if !ok || n.Simulated {
		return chain.Network{}, "", fmt.Errorf("%w: --network must be one of %s, got %q",
			errUsage, strings.Join(liveKeys, ", "), key)
	}
	return n, os.Getenv(rpcEnv[key]), nil
}

func chainScanCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("chain scan", stderr)
	key := fs.String("network", "", "live network: "+strings.Join(liveKeys, " or ")+" (required)")
	address := fs.String("address", os.Getenv(envLiveWatch), "watched address (env "+envLiveWatch+")")
	sbID := fs.String("sandbox", "", "the live sandbox's id, to assert it (optional)")
	db := dbFlag(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := required([2]string{"network", *key}, [2]string{"address", *address}); err != nil {
		return err
	}
	n, url, err := liveNetwork(*key)
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("%w: %s is not set", errUsage, rpcEnv[n.Key])
	}
	addr, err := chain.NormalizeAddress(*address)
	if err != nil {
		return fmt.Errorf("%w: --address: %w", errUsage, err)
	}
	confirmations, err := liveConfirmations()
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, *db)
	if err != nil {
		return err
	}
	defer pool.Close()
	ln, closeFn, err := dialLive(ctx, n.Key, url)
	if err != nil {
		return err
	}
	defer closeFn()
	live := liveConfig{networks: []engine.LiveNetwork{ln}, address: addr, confirmations: confirmations}
	eng, err := newEngine(pool, newLogger(stderr), live)
	if err != nil {
		return err
	}
	sb, err := eng.EnsureLive(ctx)
	if err != nil {
		return err
	}
	if *sbID != "" && *sbID != sb.ID.String() {
		return fmt.Errorf("%w: --sandbox %s is not the live sandbox (%s)", errUsage, *sbID, sb.ID)
	}
	rep, err := eng.Tick(ctx, sb, time.Now())
	if err != nil {
		return err
	}
	res := rep.Deposits[n.Key]
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"network", fmt.Sprintf("%s (chain %d)", n.Name, n.ChainID)},
		{"sandbox", sb.ID.String()},
		{"address", addr},
		{"head", fmt.Sprintf("%d %s", res.Scan.Head.Number, res.Scan.Head.Hash)},
		{"blocks", fmt.Sprintf("%d..%d", res.Scan.From, res.Scan.Next)},
		{"fetched", fmt.Sprint(res.Scan.Fetched)},
		{"new", fmt.Sprint(res.Scan.New)},
		{"confirmed", fmt.Sprint(res.Scan.Confirmed)},
		{"orphaned", fmt.Sprint(res.Scan.Orphaned)},
		{"credited", fmt.Sprint(res.Credit.Credited)},
		{"reversed", fmt.Sprint(res.Credit.Reversed)},
	}
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1])
	}
	return tw.Flush()
}

func chainSendCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("chain send", stderr)
	key := fs.String("network", "", "live network: "+strings.Join(liveKeys, " or ")+" (required)")
	to := fs.String("to", "", "recipient address 0x... (required)")
	amountStr := fs.String("amount", "", "decimal amount of the network's token (required)")
	yes := fs.Bool("yes", false, "sign and broadcast; without it only the summary is printed")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := required([2]string{"network", *key}, [2]string{"to", *to}, [2]string{"amount", *amountStr}); err != nil {
		return err
	}
	n, url, err := liveNetwork(*key)
	if err != nil {
		return err
	}
	toAddr, err := chain.NormalizeAddress(*to)
	if err != nil {
		return fmt.Errorf("%w: --to: %w", errUsage, err)
	}
	amount, err := money.Parse(n.Asset, *amountStr)
	if err != nil {
		return fmt.Errorf("%w: --amount: %w", errUsage, err)
	}
	if amount.Minor <= 0 {
		return fmt.Errorf("%w: --amount must be positive", errUsage)
	}
	hexKey := strings.TrimPrefix(strings.TrimSpace(os.Getenv(envHotWalletKey)), "0x")
	if hexKey == "" {
		return errNoHotWalletKey
	}
	priv, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		// Never echo the key, or the parser's view of it.
		return fmt.Errorf("%s is not a valid hex secp256k1 private key", envHotWalletKey)
	}
	from := crypto.PubkeyToAddress(priv.PublicKey).Hex()

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	for _, r := range [][2]string{
		{"network", fmt.Sprintf("%s (%s, chain %d)", n.Name, n.Key, n.ChainID)},
		{"token", n.Token},
		{"from", from},
		{"to", chain.ChecksumAddress(toAddr)},
		{"amount", amount.String()},
	} {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1])
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if !*yes {
		_, _ = fmt.Fprintln(stdout, "dry run: nothing sent; pass --yes to sign and broadcast")
		return nil
	}
	if url == "" {
		return fmt.Errorf("%w: %s is not set", errUsage, rpcEnv[n.Key])
	}
	client, err := evm.Dial(ctx, url)
	if err != nil {
		return err
	}
	defer client.Close()
	hash, err := evm.Send(ctx, client, priv, n, toAddr, amount)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "sent: %s\n", hash)
	if u := n.TxURL(hash); u != "" {
		_, _ = fmt.Fprintln(stdout, u)
	}
	return nil
}
