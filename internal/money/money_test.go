package money_test

import (
	"errors"
	"math"
	"testing"

	"github.com/OmarIbannez/puente/internal/money"
)

func TestParseAndFormat(t *testing.T) {
	cases := []struct {
		asset   money.Asset
		in      string
		minor   int64
		decimal string
		str     string
	}{
		{money.USDC, "25", 25_000_000, "25.000000", "25.000000 USDC"},
		{money.USDC, "0.000001", 1, "0.000001", "0.000001 USDC"},
		{money.MXNB, "-1.5", -1_500_000, "-1.500000", "-1.500000 MXNB"},
		{money.MXN, "1234567.8", 123_456_780, "1234567.80", "$1,234,567.80 MXN"},
		{money.MXN, "-0.05", -5, "-0.05", "-$0.05 MXN"},
		{money.MXN, ".5", 50, "0.50", "$0.50 MXN"},
	}
	for _, c := range cases {
		got, err := money.Parse(c.asset, c.in)
		if err != nil {
			t.Fatalf("Parse(%s, %q): %v", c.asset, c.in, err)
		}
		if got.Minor != c.minor || got.Decimal() != c.decimal || got.String() != c.str {
			t.Errorf("Parse(%s, %q) = %d %q %q, want %d %q %q",
				c.asset, c.in, got.Minor, got.Decimal(), got.String(), c.minor, c.decimal, c.str)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		asset money.Asset
		in    string
		want  error
	}{
		{money.MXN, "1.234", money.ErrPrecision},
		{money.USDC, "0.0000001", money.ErrPrecision},
		{money.USDC, "", money.ErrBadAmount},
		{money.USDC, "1.", money.ErrBadAmount},
		{money.USDC, "1e6", money.ErrBadAmount},
		{money.USDC, "99999999999999", money.ErrOverflow},
		{money.Asset("BTC"), "1", money.ErrUnknownAsset},
	}
	for _, c := range cases {
		if _, err := money.Parse(c.asset, c.in); !errors.Is(err, c.want) {
			t.Errorf("Parse(%s, %q) error = %v, want %v", c.asset, c.in, err, c.want)
		}
	}
}

func TestMinInt64Formats(t *testing.T) {
	if got := money.New(money.MXN, math.MinInt64).Decimal(); got != "-92233720368547758.08" {
		t.Errorf("MinInt64 = %q", got)
	}
}

func TestConvert(t *testing.T) {
	rate, err := money.ParseRate("18.4523")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		src  money.Amount
		rate money.Rate
		want int64
		dust int64
	}{
		// 100 USDC × 18.4523 = 1,845.23 MXN exactly.
		{"exact", money.New(money.USDC, 100_000_000), rate, 184_523, 0},
		// 0.000001 USDC × 18.4523 = 0.0000184523 MXN → 0 centavos, dust 0.001845 centavo.
		{"sub-centavo", money.New(money.USDC, 1), rate, 0, 1845},
		// 12.345678 USDC × 18.4523 = 227.8061541594 MXN → 227.80, dust 0.615415 centavo.
		{"rounds down", money.New(money.USDC, 12_345_678), rate, 22_780, 615_415},
		// MXNB→MXN at par drops the last four decimals.
		{"mxnb par", money.New(money.MXNB, 1_234_567), money.RateScale, 123, 456_700},
		// 1M USDC at 20 does not overflow.
		{"large", money.New(money.USDC, 1_000_000_000_000), 20 * money.RateScale, 2_000_000_000, 0},
	}
	for _, c := range cases {
		got, err := money.Convert(c.src, c.rate, money.MXN)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Out.Asset != money.MXN || got.Out.Minor != c.want || got.Dust != c.dust {
			t.Errorf("%s: Convert = %+v, want %d centavos dust %d", c.name, got, c.want, c.dust)
		}
	}
}

func TestConvertErrors(t *testing.T) {
	if _, err := money.Convert(money.New(money.USDC, 1), 0, money.MXN); !errors.Is(err, money.ErrBadRate) {
		t.Errorf("zero rate: %v", err)
	}
	if _, err := money.Convert(money.New(money.USDC, math.MaxInt64), 100*money.RateScale, money.USDC); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("overflow: %v", err)
	}
}

func TestParseRate(t *testing.T) {
	r, err := money.ParseRate("18.4523")
	if err != nil || r != 18_452_300 || r.String() != "18.452300" {
		t.Fatalf("ParseRate = %d %q %v", r, r.String(), err)
	}
	for _, bad := range []string{"0", "-1", "abc", "1.0000001"} {
		if _, err := money.ParseRate(bad); !errors.Is(err, money.ErrBadRate) {
			t.Errorf("ParseRate(%q) = %v, want ErrBadRate", bad, err)
		}
	}
}

func TestApplyBps(t *testing.T) {
	if got := money.ApplyBps(money.New(money.USDC, 100_000_000), 50); got.Minor != 500_000 {
		t.Errorf("0.5%% of 100 USDC = %d", got.Minor)
	}
}

func TestParseAsset(t *testing.T) {
	if a, err := money.ParseAsset(" usdc "); err != nil || a != money.USDC {
		t.Errorf("ParseAsset = %q %v", a, err)
	}
}
