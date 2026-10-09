// Package spei models Mexico's SPEI interbank transfer system: CLABE account
// numbers, claves de rastreo, and a simulated SPEI participant that keeps its
// own books (orders and a settlement-account statement). The simulator is an
// independent source of truth for reconciliation: it never reads or writes
// ledger tables. It is a simulation and makes no claim of conformance with
// Banxico's specifications.
package spei

import (
	"errors"
	"fmt"
	"strings"
)

// CLABELength is the number of digits in a CLABE: a 3-digit bank code, a
// 3-digit plaza, an 11-digit account and one check digit.
const CLABELength = 18

const (
	bankLen    = 3
	plazaLen   = 3
	accountLen = 11
)

// Errors returned by CLABE validation.
var (
	ErrCLABELength = errors.New("spei: a CLABE is exactly 18 digits")
	ErrCLABEDigits = errors.New("spei: a CLABE contains only digits")
	ErrUnknownBank = errors.New("spei: unknown bank code")
	ErrCheckDigit  = errors.New("spei: bad CLABE check digit")
)

// clabeWeights are the repeating weights of the check digit algorithm.
var clabeWeights = [3]int{3, 7, 1}

// CLABE is a validated 18-digit Clave Bancaria Estandarizada.
type CLABE struct {
	// Number is the 18 digits with no separators.
	Number string `json:"number"`
	// BankCode is the three-digit Banxico institution code.
	BankCode string `json:"bank_code"`
	// BankName is the institution's short name from the catalog.
	BankName string `json:"bank_name"`
	// Plaza is the three-digit plaza (city) code.
	Plaza string `json:"plaza"`
	// Account is the eleven-digit account number.
	Account string `json:"account"`
}

// String returns the 18 digits.
func (c CLABE) String() string { return c.Number }

// Masked returns the CLABE with every digit but the last four replaced by
// '*', for receipts and logs.
func (c CLABE) Masked() string { return maskDigits(c.Number) }

// ValidateCLABE parses raw (spaces and dashes are ignored) and checks its
// length, digits, check digit and bank code, in that order.
func ValidateCLABE(raw string) (CLABE, error) {
	n := normalizeCLABE(raw)
	if len(n) != CLABELength {
		return CLABE{}, fmt.Errorf("%w: %q has %d characters", ErrCLABELength, raw, len(n))
	}
	if !allDigits(n) {
		return CLABE{}, fmt.Errorf("%w: %q", ErrCLABEDigits, raw)
	}
	if want := checkDigit(n[:CLABELength-1]); n[CLABELength-1] != want {
		return CLABE{}, fmt.Errorf("%w: %q should end in %c", ErrCheckDigit, n, want)
	}
	code := n[:bankLen]
	name, ok := BankName(code)
	if !ok {
		return CLABE{}, fmt.Errorf("%w: %q", ErrUnknownBank, code)
	}
	return CLABE{
		Number:   n,
		BankCode: code,
		BankName: name,
		Plaza:    n[bankLen : bankLen+plazaLen],
		Account:  n[bankLen+plazaLen : CLABELength-1],
	}, nil
}

// CheckDigit returns the check digit ('0'-'9') for the first 17 digits of a
// CLABE: the digits are weighted 3, 7, 1 repeating, each product is taken
// mod 10, and the digit is (10 - sum mod 10) mod 10.
func CheckDigit(first17 string) (byte, error) {
	if len(first17) != CLABELength-1 {
		return 0, fmt.Errorf("%w: the check digit needs 17 digits, got %d", ErrCLABELength, len(first17))
	}
	if !allDigits(first17) {
		return 0, fmt.Errorf("%w: %q", ErrCLABEDigits, first17)
	}
	return checkDigit(first17), nil
}

// NewCLABE builds a valid CLABE from a catalog bank code, a three-digit plaza
// and an eleven-digit account, appending the check digit. Seeds use it.
func NewCLABE(bankCode, plaza, account string) (CLABE, error) {
	if len(bankCode) != bankLen || len(plaza) != plazaLen || len(account) != accountLen {
		return CLABE{}, fmt.Errorf("%w: want 3+3+11 digits, got %d+%d+%d",
			ErrCLABELength, len(bankCode), len(plaza), len(account))
	}
	first17 := bankCode + plaza + account
	d, err := CheckDigit(first17)
	if err != nil {
		return CLABE{}, err
	}
	return ValidateCLABE(first17 + string(d))
}

// checkDigit assumes s is 17 ASCII digits.
func checkDigit(s string) byte {
	sum := 0
	for i := range len(s) {
		sum += int(s[i]-'0') * clabeWeights[i%3] % 10
	}
	return "0123456789"[(10-sum%10)%10]
}

func normalizeCLABE(raw string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '-':
			return -1
		}
		return r
	}, raw)
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// maskDigits replaces all but the last four characters with '*'.
func maskDigits(s string) string {
	const visible = 4
	if len(s) <= visible {
		return s
	}
	return strings.Repeat("*", len(s)-visible) + s[len(s)-visible:]
}
