// Package ledger is Puente's multi-asset double-entry general ledger: the
// chart of accounts, validated exactly-once posting of journal entries and
// balance queries.
//
// A posting is a signed amount of one asset in its minor units: debits are
// positive and credits are negative. Every journal entry sums to zero per
// asset (USDC, MXNB and MXN each balance on their own; value only crosses
// assets through the FX conversion clearing account 3900). The rule is
// enforced here in Go and again at commit by a deferred constraint trigger
// in the database.
package ledger

import (
	"fmt"
	"slices"

	"github.com/OmarIbannez/puente/internal/money"
)

// Account is a chart-of-accounts code.
type Account int

// The chart of accounts.
const (
	// HotWallet is the operator's on-chain hot wallet (asset; USDC, MXNB).
	HotWallet Account = 1010
	// SPEISettlement is the bank account that sends SPEI payouts (asset; MXN).
	SPEISettlement Account = 1100
	// CustomerBalances is what Puente owes its customers (liability; USDC, MXNB).
	CustomerBalances Account = 2010
	// PayoutsReserved holds customer funds earmarked for a payout that has
	// not been submitted yet (liability; USDC, MXNB).
	PayoutsReserved Account = 2020
	// SPEIInFlight is pesos owed to payout beneficiaries for orders submitted
	// to the rail but not yet settled (liability; MXN).
	SPEIInFlight Account = 2100
	// TreasuryEquity is the treasury's peso prefunding of the SPEI account
	// (equity; MXN).
	TreasuryEquity Account = 3000
	// FXClearing is the FX conversion clearing position where one asset is
	// exchanged for another, and where sub-centavo conversion dust stays
	// (equity; every asset).
	FXClearing Account = 3900
	// FeeIncome is payout fee income (revenue; USDC, MXNB).
	FeeIncome Account = 4100
)

// AccountType classifies an account and determines its normal balance.
type AccountType string

// Account types.
const (
	// TypeAsset accounts are debit-normal.
	TypeAsset AccountType = "asset"
	// TypeLiability accounts are credit-normal.
	TypeLiability AccountType = "liability"
	// TypeEquity accounts are credit-normal.
	TypeEquity AccountType = "equity"
	// TypeRevenue accounts are credit-normal.
	TypeRevenue AccountType = "revenue"
)

// NormalSign is +1 for debit-normal types (asset) and -1 for credit-normal
// types (liability, equity, revenue). Multiplying a raw signed balance by it
// gives the balance as an accountant would display it: positive when the
// account carries its normal balance.
func (t AccountType) NormalSign() int64 {
	switch t {
	case TypeAsset:
		return 1
	case TypeLiability, TypeEquity, TypeRevenue:
		return -1
	}
	return 1
}

// AccountInfo describes one account of the chart.
type AccountInfo struct {
	Code Account     `json:"code"`
	Name string      `json:"name"`
	Type AccountType `json:"type"`
	// Assets lists the assets the account may hold, in display order.
	Assets []money.Asset `json:"assets"`
	// PerParty marks a customer sub-ledger account: every posting to it
	// names the customer (Posting.Party).
	PerParty bool `json:"per_party"`
}

// Holds reports whether the account may hold asset.
func (i AccountInfo) Holds(asset money.Asset) bool {
	return slices.Contains(i.Assets, asset)
}

// Normal converts a raw signed balance (debits positive) of the account into
// its normal-balance sign, for display.
func (i AccountInfo) Normal(raw money.Amount) money.Amount {
	return money.Amount{Asset: raw.Asset, Minor: raw.Minor * i.Type.NormalSign()}
}

var (
	stablecoins = []money.Asset{money.USDC, money.MXNB}
	pesos       = []money.Asset{money.MXN}
)

var chart = []AccountInfo{
	{Code: HotWallet, Name: "Hot wallet (on-chain)", Type: TypeAsset, Assets: stablecoins},
	{Code: SPEISettlement, Name: "SPEI settlement account", Type: TypeAsset, Assets: pesos},
	{Code: CustomerBalances, Name: "Customer balances", Type: TypeLiability, Assets: stablecoins, PerParty: true},
	{Code: PayoutsReserved, Name: "Payouts reserved", Type: TypeLiability, Assets: stablecoins, PerParty: true},
	{Code: SPEIInFlight, Name: "SPEI payouts in flight", Type: TypeLiability, Assets: pesos},
	{Code: TreasuryEquity, Name: "Treasury equity", Type: TypeEquity, Assets: pesos},
	{Code: FXClearing, Name: "FX conversion clearing", Type: TypeEquity, Assets: money.Assets},
	{Code: FeeIncome, Name: "Payout fee income", Type: TypeRevenue, Assets: stablecoins},
}

// All returns the chart of accounts in code order. The result is a copy the
// caller may modify.
func All() []AccountInfo {
	out := make([]AccountInfo, len(chart))
	for i, a := range chart {
		a.Assets = slices.Clone(a.Assets)
		out[i] = a
	}
	return out
}

// Lookup returns the chart entry for code and whether it exists.
func Lookup(code Account) (AccountInfo, bool) {
	for _, a := range chart {
		if a.Code == code {
			a.Assets = slices.Clone(a.Assets)
			return a, true
		}
	}
	return AccountInfo{}, false
}

// String returns "code name", e.g. "1010 Hot wallet (on-chain)", or just
// the code if the account is unknown.
func (a Account) String() string {
	if info, ok := Lookup(a); ok {
		return fmt.Sprintf("%d %s", int(a), info.Name)
	}
	return fmt.Sprintf("%d", int(a))
}

// Kind is the business event a journal entry records.
type Kind string

// Journal entry kinds (the entry catalog).
const (
	// KindFunding is the treasury prefunding the SPEI account:
	// Dr 1100 / Cr 3000 (MXN).
	KindFunding Kind = "funding"
	// KindDeposit is a confirmed on-chain deposit: Dr 1010 / Cr 2010.
	KindDeposit Kind = "deposit"
	// KindDepositReversal undoes a credited deposit orphaned by a reorg.
	KindDepositReversal Kind = "deposit_reversal"
	// KindPayoutReserve earmarks amount plus fee: Dr 2010 / Cr 2020.
	KindPayoutReserve Kind = "payout_reserve"
	// KindPayoutSubmit books the fee and the FX conversion when the order
	// goes to the rail: Dr 2020 / Cr 4100 + Cr 3900 (source asset);
	// Dr 3900 / Cr 2100 (MXN).
	KindPayoutSubmit Kind = "payout_submit"
	// KindPayoutSettle clears a liquidated order: Dr 2100 / Cr 1100 (MXN).
	KindPayoutSettle Kind = "payout_settle"
	// KindPayoutRelease reverses the reserve (and the submit, when the rail
	// rejected the order).
	KindPayoutRelease Kind = "payout_release"
	// KindPayoutReturn books a devolución after liquidation:
	// Dr 1100 / Cr 3900 (MXN); Dr 3900 / Cr 2010 (source asset, fee kept).
	KindPayoutReturn Kind = "payout_return"
)

var kinds = []Kind{
	KindFunding, KindDeposit, KindDepositReversal, KindPayoutReserve,
	KindPayoutSubmit, KindPayoutSettle, KindPayoutRelease, KindPayoutReturn,
}

// Kinds returns every journal entry kind.
func Kinds() []Kind {
	return slices.Clone(kinds)
}

// Valid reports whether k is a known entry kind.
func (k Kind) Valid() bool {
	return slices.Contains(kinds, k)
}
