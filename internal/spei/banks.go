package spei

import "sort"

// Bank is a SPEI participant: its three-digit Banxico institution code
// (the first three digits of a CLABE) and its short name.
type Bank struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// banks is the catalog of institutions Puente recognizes, ported from xolo's
// validators.BANKS. It covers the banks and participants people meet most;
// it is not Banxico's full participant list.
var banks = map[string]string{
	"002": "BANAMEX",
	"006": "BANCOMEXT",
	"009": "BANOBRAS",
	"012": "BBVA MEXICO",
	"014": "SANTANDER",
	"019": "BANJERCITO",
	"021": "HSBC",
	"030": "BAJIO",
	"036": "INBURSA",
	"042": "MIFEL",
	"044": "SCOTIABANK",
	"058": "BANREGIO",
	"059": "INVEX",
	"060": "BANSI",
	"062": "AFIRME",
	"072": "BANORTE",
	"106": "BANK OF AMERICA",
	"127": "AZTECA",
	"130": "COMPARTAMOS",
	"137": "BANCOPPEL",
	"166": "BANCO DEL BIENESTAR",
	"638": "NU MEXICO",
	"646": "STP",
	"722": "MERCADO PAGO W",
}

// BankName returns the short name of the institution with the three-digit
// code, and whether it is in the catalog.
func BankName(code string) (string, bool) {
	name, ok := banks[code]
	return name, ok
}

// Banks returns the catalog ordered by code.
func Banks() []Bank {
	out := make([]Bank, 0, len(banks))
	for code, name := range banks {
		out = append(out, Bank{Code: code, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
