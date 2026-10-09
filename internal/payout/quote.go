package payout

import (
	"fmt"
	"maps"
	"math"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
)

// Defaults used by DefaultConfig and to fill a partial Config.
const (
	// DefaultFeeBps is 0.50% of the payout amount.
	DefaultFeeBps = 50
	// DefaultMaxAttempts is how many submissions are tried before a payout
	// goes to needs_review.
	DefaultMaxAttempts = 5
	// maxBackoff caps DefaultBackoff.
	maxBackoff = 5 * time.Minute
)

// DefaultRates are the fixed demo FX rates: MXN per one whole unit.
var DefaultRates = map[money.Asset]money.Rate{
	money.USDC: 18_452_300, // 18.4523
	money.MXNB: 1_000_000,  // 1.0
}

// Config prices payouts and tunes the worker's retries.
type Config struct {
	// FeeBps is the fee in basis points of the amount; 0 means no fee.
	FeeBps int64
	// Rates maps each payable asset to its MXN rate; nil means DefaultRates.
	Rates map[money.Asset]money.Rate
	// MaxAttempts is the number of submissions (the first one included)
	// before a payout goes to needs_review; ≤ 0 means DefaultMaxAttempts.
	MaxAttempts int
	// Backoff is the delay before retry number attempt+1, after attempt
	// failed submissions; nil means DefaultBackoff.
	Backoff func(attempt int) time.Duration
}

// DefaultConfig returns the production configuration.
func DefaultConfig() Config {
	return Config{
		FeeBps:      DefaultFeeBps,
		Rates:       maps.Clone(DefaultRates),
		MaxAttempts: DefaultMaxAttempts,
		Backoff:     DefaultBackoff,
	}
}

// DefaultBackoff doubles from one second (1s, 2s, 4s, ...) up to five
// minutes.
func DefaultBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

func (c Config) withDefaults() Config {
	if c.Rates == nil {
		c.Rates = DefaultRates
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.Backoff == nil {
		c.Backoff = DefaultBackoff
	}
	return c
}

// Quote prices a payout.
type Quote struct {
	// Amount is what the beneficiary receives, in the source asset.
	Amount money.Amount `json:"amount"`
	// Fee is FeeBps of Amount, rounded down.
	Fee money.Amount `json:"fee"`
	// Total is Amount plus Fee: what is reserved from the customer.
	Total money.Amount `json:"total"`
	// Rate is MXN per one whole unit of the source asset.
	Rate money.Rate `json:"rate"`
	// MXN is Amount at Rate, rounded down to the centavo.
	MXN money.Amount `json:"mxn"`
}

// Quote prices a payout of amount: the fee, the total reserved, and the
// pesos sent. It fails with ErrUnsupportedAsset for an asset without a rate
// (or MXN), and with ErrInvalidAmount for a non-positive amount or one too
// small to convert to a centavo.
func (c Config) Quote(amount money.Amount) (Quote, error) {
	c = c.withDefaults()
	rate, ok := c.Rates[amount.Asset]
	if !ok || amount.Asset == money.MXN || !amount.Asset.Valid() {
		return Quote{}, fmt.Errorf("%w: %q", ErrUnsupportedAsset, string(amount.Asset))
	}
	if amount.Minor <= 0 {
		return Quote{}, fmt.Errorf("%w: %s must be positive", ErrInvalidAmount, amount.Decimal())
	}
	if c.FeeBps < 0 || c.FeeBps > 10_000 {
		return Quote{}, fmt.Errorf("payout: fee of %d bps out of range", c.FeeBps)
	}
	fee := money.ApplyBps(amount, c.FeeBps)
	if amount.Minor > math.MaxInt64-fee.Minor {
		return Quote{}, fmt.Errorf("%w: %s is too large", ErrInvalidAmount, amount.Decimal())
	}
	conv, err := money.Convert(amount, rate, money.MXN)
	if err != nil {
		return Quote{}, fmt.Errorf("%w: convert %s: %w", ErrInvalidAmount, amount, err)
	}
	if conv.Out.Minor <= 0 {
		return Quote{}, fmt.Errorf("%w: %s converts to less than one centavo", ErrInvalidAmount, amount)
	}
	return Quote{
		Amount: amount,
		Fee:    fee,
		Total:  money.New(amount.Asset, amount.Minor+fee.Minor),
		Rate:   rate,
		MXN:    conv.Out,
	}, nil
}
