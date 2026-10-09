package spei_test

import (
	"errors"
	"testing"

	"github.com/OmarIbannez/puente/internal/spei"
)

// Golden vectors computed with xolo's validators.validate_clabe, so both
// projects agree on the check digit. xolo accepts a valid check digit with an
// unknown bank (bank=None); Puente rejects it with ErrUnknownBank.
func TestValidateCLABEGolden(t *testing.T) {
	valid := []struct {
		raw, number, bank, name, plaza, account string
	}{
		{"002010077777777771", "002010077777777771", "002", "BANAMEX", "010", "07777777777"},
		{"002 010 07777777777 1", "002010077777777771", "002", "BANAMEX", "010", "07777777777"},
		{"646180157000000004", "646180157000000004", "646", "STP", "180", "15700000000"},
		{"012180015203300154", "012180015203300154", "012", "BBVA MEXICO", "180", "01520330015"},
		{"072-580-00543214321-1", "072580005432143211", "072", "BANORTE", "580", "00543214321"},
		{"127180001234567894", "127180001234567894", "127", "AZTECA", "180", "00123456789"},
		{"638180000123456784", "638180000123456784", "638", "NU MEXICO", "180", "00012345678"},
	}
	for _, tc := range valid {
		c, err := spei.ValidateCLABE(tc.raw)
		if err != nil {
			t.Errorf("ValidateCLABE(%q) = %v", tc.raw, err)
			continue
		}
		want := spei.CLABE{Number: tc.number, BankCode: tc.bank, BankName: tc.name, Plaza: tc.plaza, Account: tc.account}
		if c != want {
			t.Errorf("ValidateCLABE(%q) = %+v, want %+v", tc.raw, c, want)
		}
	}

	invalid := []struct {
		raw  string
		want error
	}{
		{"002010077777777772", spei.ErrCheckDigit},    // xolo: should be 1
		{"012180015203300156", spei.ErrCheckDigit},    // xolo: should be 4
		{"072-580-00543214321-5", spei.ErrCheckDigit}, // xolo: should be 1
		{"032180000118359719", spei.ErrUnknownBank},   // valid digit, 032 not in catalog
		{"999180000000000002", spei.ErrUnknownBank},
		{"12345", spei.ErrCLABELength},
		{"", spei.ErrCLABELength},
		{"0020100777777777710", spei.ErrCLABELength},
		{"00201007777777777X", spei.ErrCLABEDigits},
		{"002010O77777777771", spei.ErrCLABEDigits},
	}
	for _, tc := range invalid {
		if _, err := spei.ValidateCLABE(tc.raw); !errors.Is(err, tc.want) {
			t.Errorf("ValidateCLABE(%q) = %v, want %v", tc.raw, err, tc.want)
		}
	}
}

func TestCheckDigit(t *testing.T) {
	cases := map[string]byte{
		"00201007777777777": '1',
		"01218001520330015": '4',
		"99918000000000000": '2',
		"64618050000000000": '4',
	}
	for in, want := range cases {
		got, err := spei.CheckDigit(in)
		if err != nil || got != want {
			t.Errorf("CheckDigit(%q) = %c, %v; want %c", in, got, err, want)
		}
	}
	if _, err := spei.CheckDigit("123"); !errors.Is(err, spei.ErrCLABELength) {
		t.Errorf("short input: %v", err)
	}
	if _, err := spei.CheckDigit("0020100777777777A"); !errors.Is(err, spei.ErrCLABEDigits) {
		t.Errorf("non-digit input: %v", err)
	}
}

func TestNewCLABERoundTrips(t *testing.T) {
	for _, b := range spei.Banks() {
		c, err := spei.NewCLABE(b.Code, "180", "00012345678")
		if err != nil {
			t.Fatalf("NewCLABE(%s): %v", b.Code, err)
		}
		back, err := spei.ValidateCLABE(c.Number)
		if err != nil || back != c {
			t.Fatalf("round trip %s: %+v, %v", c.Number, back, err)
		}
		if c.BankName != b.Name || c.Plaza != "180" || c.Account != "00012345678" {
			t.Fatalf("NewCLABE(%s) = %+v", b.Code, c)
		}
	}
	if c, err := spei.NewCLABE("002", "010", "07777777777"); err != nil || c.Number != "002010077777777771" {
		t.Fatalf("NewCLABE golden = %+v, %v", c, err)
	}
	if _, err := spei.NewCLABE("999", "180", "00000000000"); !errors.Is(err, spei.ErrUnknownBank) {
		t.Errorf("unknown bank: %v", err)
	}
	if _, err := spei.NewCLABE("02", "180", "00000000000"); !errors.Is(err, spei.ErrCLABELength) {
		t.Errorf("short bank: %v", err)
	}
	if _, err := spei.NewCLABE("002", "18A", "00000000000"); !errors.Is(err, spei.ErrCLABEDigits) {
		t.Errorf("letter in plaza: %v", err)
	}
}

func TestDefaultOrderingCLABEIsSTP(t *testing.T) {
	c, err := spei.ValidateCLABE(spei.DefaultOrderingCLABE)
	if err != nil || c.BankCode != "646" {
		t.Fatalf("DefaultOrderingCLABE = %+v, %v", c, err)
	}
	if got := c.Masked(); got != "**************"+c.Number[14:] {
		t.Fatalf("Masked = %q", got)
	}
}

func TestBankCatalog(t *testing.T) {
	banks := spei.Banks()
	if len(banks) != 24 {
		t.Fatalf("catalog has %d banks, want 24 (xolo's BANKS)", len(banks))
	}
	for i := 1; i < len(banks); i++ {
		if banks[i-1].Code >= banks[i].Code {
			t.Fatalf("Banks not sorted at %d: %v", i, banks)
		}
	}
	if name, ok := spei.BankName("646"); !ok || name != "STP" {
		t.Fatalf("BankName(646) = %q, %v", name, ok)
	}
	if _, ok := spei.BankName("999"); ok {
		t.Fatal("BankName(999) found")
	}
}
