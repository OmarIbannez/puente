// Package money holds amounts as integer minor units of a fixed set of
// assets, and the exact integer arithmetic used to convert between them.
// There are no floats in money paths.
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Asset is a currency or token Puente holds.
type Asset string

// Supported assets.
const (
	// USDC is Circle's dollar stablecoin (6 decimals on-chain).
	USDC Asset = "USDC"
	// MXNB is Bitso's peso stablecoin (6 decimals on-chain).
	MXNB Asset = "MXNB"
	// MXN is the Mexican peso as moved over SPEI, in centavos.
	MXN Asset = "MXN"
)

// Assets lists every supported asset in display order.
var Assets = []Asset{USDC, MXNB, MXN}

// Errors returned by parsing and conversion.
var (
	ErrUnknownAsset = errors.New("money: unknown asset")
	ErrBadAmount    = errors.New("money: malformed amount")
	ErrPrecision    = errors.New("money: more decimals than the asset allows")
	ErrBadRate      = errors.New("money: malformed rate")
	ErrOverflow     = errors.New("money: amount out of range")
)

// Valid reports whether a is a supported asset.
func (a Asset) Valid() bool {
	switch a {
	case USDC, MXNB, MXN:
		return true
	}
	return false
}

// Scale is the number of decimal places of one minor unit: 6 for the
// stablecoins, 2 for pesos. It panics on an unknown asset.
func (a Asset) Scale() int {
	switch a {
	case USDC, MXNB:
		return 6
	case MXN:
		return 2
	}
	panic(fmt.Sprintf("money: scale of unknown asset %q", string(a)))
}

// ParseAsset returns the asset named s (case-insensitive).
func ParseAsset(s string) (Asset, error) {
	a := Asset(strings.ToUpper(strings.TrimSpace(s)))
	if !a.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnknownAsset, s)
	}
	return a, nil
}

// Amount is a quantity of one asset in its minor units (micro-USDC,
// micro-MXNB or centavos).
type Amount struct {
	Asset Asset `json:"asset"`
	Minor int64 `json:"minor"`
}

// New returns an Amount of minor units of asset.
func New(asset Asset, minor int64) Amount { return Amount{Asset: asset, Minor: minor} }

// Neg returns -a.
func (a Amount) Neg() Amount { return Amount{Asset: a.Asset, Minor: -a.Minor} }

// IsZero reports whether a is zero.
func (a Amount) IsZero() bool { return a.Minor == 0 }

// Decimal formats the amount as a plain decimal with the asset's scale,
// e.g. "12.500000" or "-0.05".
func (a Amount) Decimal() string {
	scale := a.Asset.Scale()
	v := a.Minor
	sign := ""
	if v < 0 {
		sign = "-"
	}
	digits := new(big.Int).Abs(big.NewInt(v)).String()
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	cut := len(digits) - scale
	return sign + digits[:cut] + "." + digits[cut:]
}

// String formats the amount for people: "12.500000 USDC", "$1,234.56 MXN".
func (a Amount) String() string {
	if a.Asset == MXN {
		dec := a.Decimal()
		sign := ""
		if strings.HasPrefix(dec, "-") {
			sign, dec = "-", dec[1:]
		}
		whole, frac, _ := strings.Cut(dec, ".")
		return sign + "$" + groupThousands(whole) + "." + frac + " MXN"
	}
	return a.Decimal() + " " + string(a.Asset)
}

// Parse reads a decimal string such as "25", "25.5" or "-0.000001" as an
// amount of asset. More decimals than the asset's scale is an error, never
// a silent rounding.
func Parse(asset Asset, s string) (Amount, error) {
	if !asset.Valid() {
		return Amount{}, fmt.Errorf("%w: %q", ErrUnknownAsset, string(asset))
	}
	minor, err := parseScaled(strings.TrimSpace(s), asset.Scale())
	if err != nil {
		return Amount{}, err
	}
	return Amount{Asset: asset, Minor: minor}, nil
}

// Rate is an exchange rate: pesos per one whole unit of the source asset,
// scaled by RateScale (18.452300 MXN per USDC is Rate(18452300)).
type Rate int64

// RateScale is the fixed-point denominator of Rate.
const RateScale = 1_000_000

// rateDecimals is the number of decimal places a Rate carries.
const rateDecimals = 6

// ParseRate reads a positive decimal rate such as "18.4523".
func ParseRate(s string) (Rate, error) {
	v, err := parseScaled(strings.TrimSpace(s), rateDecimals)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrBadRate, s)
	}
	return Rate(v), nil
}

// String formats r with six decimals, e.g. "18.452300".
func (r Rate) String() string {
	return Amount{Asset: USDC, Minor: int64(r)}.Decimal()
}

// Conversion is the result of converting an amount into another asset.
type Conversion struct {
	// Out is the converted amount, rounded toward zero to the target
	// asset's minor unit.
	Out Amount
	// Dust is the part of the exact result lost to rounding, expressed in
	// millionths of one target minor unit (0 ≤ Dust < 1,000,000). It stays
	// in the FX position; Puente never pays out fractions of a centavo.
	Dust int64
}

// Convert turns src into the target asset at rate (target units per one
// whole source unit), rounding toward zero. The arithmetic is exact (big
// integers), so no realistic amount overflows silently.
func Convert(src Amount, rate Rate, target Asset) (Conversion, error) {
	if !src.Asset.Valid() || !target.Valid() {
		return Conversion{}, ErrUnknownAsset
	}
	if rate <= 0 {
		return Conversion{}, ErrBadRate
	}
	// out = src.Minor × rate × 10^targetScale / (10^srcScale × RateScale)
	num := new(big.Int).Mul(big.NewInt(src.Minor), big.NewInt(int64(rate)))
	num.Mul(num, pow10(target.Scale()))
	den := new(big.Int).Mul(pow10(src.Asset.Scale()), big.NewInt(RateScale))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if !q.IsInt64() {
		return Conversion{}, ErrOverflow
	}
	// Dust in millionths of a target minor unit: |r| × 10^6 / den.
	dust := new(big.Int).Abs(r)
	dust.Mul(dust, big.NewInt(1_000_000))
	dust.Quo(dust, den)
	return Conversion{Out: Amount{Asset: target, Minor: q.Int64()}, Dust: dust.Int64()}, nil
}

// ApplyBps returns a × bps / 10,000, rounded toward zero.
func ApplyBps(a Amount, bps int64) Amount {
	v := new(big.Int).Mul(big.NewInt(a.Minor), big.NewInt(bps))
	v.Quo(v, big.NewInt(10_000))
	return Amount{Asset: a.Asset, Minor: v.Int64()}
}

func parseScaled(s string, scale int) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("%w: empty", ErrBadAmount)
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && frac == "" || (hasDot && frac == "") || !allDigits(whole) || !allDigits(frac) {
		return 0, fmt.Errorf("%w: %q", ErrBadAmount, s)
	}
	if len(frac) > scale {
		return 0, fmt.Errorf("%w: %q has %d decimals, max %d", ErrPrecision, s, len(frac), scale)
	}
	if whole == "" {
		whole = "0"
	}
	digits := whole + frac + strings.Repeat("0", scale-len(frac))
	v, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrOverflow, s)
	}
	if neg {
		v = -v
	}
	return v, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func groupThousands(digits string) string {
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
