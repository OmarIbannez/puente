package spei

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Clave de rastreo limits.
const (
	// MaxClaveLength is the longest clave de rastreo SPEI accepts.
	MaxClaveLength = 30
	// MaxClavePrefix is the longest prefix NewClave keeps.
	MaxClavePrefix = 8
	// claveSeqWidth is the minimum width of the base-36 sequence.
	claveSeqWidth = 8
)

// DefaultClavePrefix is Puente's clave de rastreo prefix.
const DefaultClavePrefix = "PNTE"

// ErrBadClave is returned for a malformed clave de rastreo.
var ErrBadClave = errors.New("spei: invalid clave de rastreo")

// NewClave returns the clave de rastreo for the seq-th payout of day: the
// prefix (uppercased, alphanumerics only, at most MaxClavePrefix characters),
// the day as YYYYMMDD in day's location, and seq in base 36 zero-padded to
// eight characters, e.g. "PNTE2026100800000001". The result is a pure
// function of its inputs, so a payout that derives its clave from its own
// fixed day and id reuses the same clave on every retry. It is at most 29
// characters, under MaxClaveLength. The sign of a negative seq is dropped.
func NewClave(prefix string, day time.Time, seq int64) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(prefix) {
		if b.Len() == MaxClavePrefix {
			break
		}
		if isClaveChar(r) {
			b.WriteRune(r)
		}
	}
	b.WriteString(day.Format("20060102"))
	s := strings.ToUpper(strings.TrimPrefix(strconv.FormatInt(seq, 36), "-"))
	if pad := claveSeqWidth - len(s); pad > 0 {
		b.WriteString(strings.Repeat("0", pad))
	}
	b.WriteString(s)
	return b.String()
}

// ValidateClave checks that s is 1 to 30 uppercase ASCII letters and digits.
func ValidateClave(s string) error {
	if s == "" || len(s) > MaxClaveLength {
		return fmt.Errorf("%w: %q must be 1-%d characters", ErrBadClave, s, MaxClaveLength)
	}
	for _, r := range s {
		if !isClaveChar(r) {
			return fmt.Errorf("%w: %q must be uppercase letters and digits", ErrBadClave, s)
		}
	}
	return nil
}

func isClaveChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
