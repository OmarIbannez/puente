package spei

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
)

// mexicoCity is Mexico City time (UTC-6, no daylight saving since 2022). A
// fixed zone avoids depending on the tzdata database at runtime.
var mexicoCity = time.FixedZone("CST", -6*60*60)

// CEP is a simulated Comprobante Electrónico de Pago: the receipt the rail
// issues for a liquidated order. It imitates the shape of Banxico's CEP but
// is not one, and says so in Simulado.
type CEP struct {
	// Simulado is always true: this receipt comes from a simulated rail.
	Simulado bool `json:"simulado"`
	// FechaOperacion is the settlement date in Mexico City, YYYY-MM-DD.
	FechaOperacion string `json:"fecha_operacion"`
	// HoraOperacion is the settlement time in Mexico City, HH:MM:SS.
	HoraOperacion string `json:"hora_operacion"`
	// ClaveRastreo is the order's clave de rastreo.
	ClaveRastreo string `json:"clave_rastreo"`
	// Emisor is the ordering participant's bank name.
	Emisor string `json:"emisor"`
	// Receptor is the beneficiary's bank name.
	Receptor string `json:"receptor"`
	// CuentaBeneficiario is the beneficiary CLABE, masked except the last
	// four digits.
	CuentaBeneficiario string `json:"cuenta_beneficiario"`
	// NombreBeneficiario is the beneficiary's name as ordered.
	NombreBeneficiario string `json:"nombre_beneficiario"`
	// Concepto is the payment concept as ordered.
	Concepto string `json:"concepto"`
	// Monto is the amount in pesos with two decimals, e.g. "1234.56".
	Monto string `json:"monto"`
	// Sello is the hex SHA-256 of the canonical fields (see Seal).
	Sello string `json:"sello"`
}

// NewCEP builds the sealed receipt for an order liquidated at liquidatedAt.
func NewCEP(order Order, orderingCLABE string, liquidatedAt time.Time) CEP {
	at := liquidatedAt.In(mexicoCity)
	c := CEP{
		Simulado:           true,
		FechaOperacion:     at.Format("2006-01-02"),
		HoraOperacion:      at.Format("15:04:05"),
		ClaveRastreo:       order.Clave,
		Emisor:             bankOf(orderingCLABE),
		Receptor:           bankOf(order.BeneficiaryCLABE),
		CuentaBeneficiario: maskDigits(order.BeneficiaryCLABE),
		NombreBeneficiario: order.BeneficiaryName,
		Concepto:           order.Concepto,
		Monto:              money.New(money.MXN, order.Amount.Minor).Decimal(),
	}
	c.Sello = c.Seal()
	return c
}

// Seal returns the hex SHA-256 over the receipt's canonical fields: each
// field in declaration order (Simulado as "true"/"false"), length-prefixed
// so that no two different receipts share a preimage. Sello itself is not
// part of the input, so anyone holding the receipt can check it.
func (c CEP) Seal() string {
	fields := []string{
		strconv.FormatBool(c.Simulado),
		c.FechaOperacion,
		c.HoraOperacion,
		c.ClaveRastreo,
		c.Emisor,
		c.Receptor,
		c.CuentaBeneficiario,
		c.NombreBeneficiario,
		c.Concepto,
		c.Monto,
	}
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
		b.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// bankOf names the bank of a CLABE's first three digits, or returns the code
// itself when it is not in the catalog.
func bankOf(clabe string) string {
	if len(clabe) < bankLen {
		return clabe
	}
	code := clabe[:bankLen]
	if name, ok := BankName(code); ok {
		return name
	}
	return code
}
