package spei_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"
)

func TestCEP(t *testing.T) {
	o := spei.Order{
		Clave:            "PNTE2026100800000001",
		BeneficiaryCLABE: "012180015203300154",
		BeneficiaryName:  "Ana López",
		Concepto:         "Pago Puente",
		Amount:           money.New(money.MXN, 123_456),
	}
	at := time.Date(2026, 10, 8, 18, 30, 5, 0, time.UTC)
	c := spei.NewCEP(o, spei.DefaultOrderingCLABE, at)
	want := spei.CEP{
		Simulado:           true,
		FechaOperacion:     "2026-10-08",
		HoraOperacion:      "12:30:05", // Mexico City, UTC-6
		ClaveRastreo:       o.Clave,
		Emisor:             "STP",
		Receptor:           "BBVA MEXICO",
		CuentaBeneficiario: "**************0154",
		NombreBeneficiario: "Ana López",
		Concepto:           "Pago Puente",
		Monto:              "1234.56",
		Sello:              c.Sello,
	}
	if c != want {
		t.Fatalf("NewCEP = %+v\nwant %+v", c, want)
	}
	if len(c.Sello) != 64 || c.Sello != c.Seal() {
		t.Fatalf("sello %q does not verify", c.Sello)
	}
	if spei.NewCEP(o, spei.DefaultOrderingCLABE, at) != c {
		t.Fatal("NewCEP is not deterministic")
	}
	tampered := c
	tampered.Monto = "1234.57"
	if tampered.Seal() == c.Sello {
		t.Fatal("changing the amount kept the sello")
	}
	// Moving a character between adjacent fields changes the sello too.
	shifted := c
	shifted.NombreBeneficiario, shifted.Concepto = "Ana LópezP", "ago Puente"
	if shifted.Seal() == c.Sello {
		t.Fatal("field boundaries are not part of the sello")
	}
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m["simulado"] != true {
		t.Fatalf("CEP JSON = %s (%v)", body, err)
	}
}
