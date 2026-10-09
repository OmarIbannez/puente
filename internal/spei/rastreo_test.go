package spei_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/spei"
)

func TestNewClave(t *testing.T) {
	day := time.Date(2026, 10, 8, 23, 59, 0, 0, time.UTC)
	if got := spei.NewClave(spei.DefaultClavePrefix, day, 1); got != "PNTE2026100800000001" {
		t.Fatalf("NewClave seq 1 = %q", got)
	}
	if got := spei.NewClave("pnte", day, 36); got != "PNTE2026100800000010" {
		t.Fatalf("NewClave seq 36 = %q", got)
	}
	// Deterministic: a retry derives the same clave.
	if a, b := spei.NewClave("PNTE", day, 12345), spei.NewClave("PNTE", day, 12345); a != b {
		t.Fatal("NewClave is not deterministic")
	}
	if spei.NewClave("PNTE", day, 1) == spei.NewClave("PNTE", day, 2) {
		t.Fatal("different seqs share a clave")
	}
	for _, tc := range []struct {
		prefix string
		seq    int64
	}{
		{"PNTE", math.MaxInt64},
		{"PNTE", math.MinInt64},
		{"a-very-long-prefix-with-junk!", math.MaxInt64},
		{"", 0},
	} {
		c := spei.NewClave(tc.prefix, day, tc.seq)
		if len(c) > spei.MaxClaveLength {
			t.Errorf("NewClave(%q, %d) = %q is %d chars", tc.prefix, tc.seq, c, len(c))
		}
		if err := spei.ValidateClave(c); err != nil {
			t.Errorf("NewClave(%q, %d) = %q invalid: %v", tc.prefix, tc.seq, c, err)
		}
	}
	if got := spei.NewClave("ab-cdefghijkl", day, 0); !strings.HasPrefix(got, "ABCDEFGH20261008") {
		t.Fatalf("prefix not sanitized: %q", got)
	}
}

func TestValidateClave(t *testing.T) {
	for _, ok := range []string{"A", "PNTE2026100800000001", strings.Repeat("Z9", 15)} {
		if err := spei.ValidateClave(ok); err != nil {
			t.Errorf("ValidateClave(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("A", 31), "pnte1", "PNTE-1", "PNTE 1", "PÑTE1"} {
		if err := spei.ValidateClave(bad); !errors.Is(err, spei.ErrBadClave) {
			t.Errorf("ValidateClave(%q) = %v, want ErrBadClave", bad, err)
		}
	}
}
