package ledger

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Page sizes for Entries.
const (
	// DefaultEntriesLimit is used when EntryFilter.Limit is zero or negative.
	DefaultEntriesLimit = 50
	// MaxEntriesLimit caps EntryFilter.Limit.
	MaxEntriesLimit = 500
)

// Balance returns the raw signed balance (debits positive) of one asset in
// account across the sandbox. Use AccountInfo.Normal to display it.
func Balance(ctx context.Context, q store.Querier, sandboxID uuid.UUID, account Account, asset money.Asset) (money.Amount, error) {
	info, ok := Lookup(account)
	if !ok {
		return money.Amount{}, fmt.Errorf("%w: %d", ErrUnknownAccount, int(account))
	}
	if !info.Holds(asset) {
		return money.Amount{}, fmt.Errorf("%w: %q in %s", ErrAssetNotAllowed, string(asset), account)
	}
	var sum int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint
		FROM postings WHERE sandbox_id = $1 AND account_code = $2 AND asset = $3`,
		sandboxID, int(account), string(asset)).Scan(&sum)
	if err != nil {
		return money.Amount{}, fmt.Errorf("ledger: balance of %s %s: %w", account, asset, err)
	}
	return money.New(asset, sum), nil
}

// PartyBalance returns the raw signed balance (debits positive) of one
// customer's asset in a sub-ledger account.
func PartyBalance(ctx context.Context, q store.Querier, sandboxID, party uuid.UUID, account Account, asset money.Asset) (money.Amount, error) {
	info, ok := Lookup(account)
	if !ok {
		return money.Amount{}, fmt.Errorf("%w: %d", ErrUnknownAccount, int(account))
	}
	if !info.PerParty {
		return money.Amount{}, fmt.Errorf("%w: %s is not a sub-ledger account", ErrParty, account)
	}
	if !info.Holds(asset) {
		return money.Amount{}, fmt.Errorf("%w: %q in %s", ErrAssetNotAllowed, string(asset), account)
	}
	var sum int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor), 0)::bigint
		FROM postings WHERE sandbox_id = $1 AND party_id = $2 AND account_code = $3 AND asset = $4`,
		sandboxID, party, int(account), string(asset)).Scan(&sum)
	if err != nil {
		return money.Amount{}, fmt.Errorf("ledger: balance of %s %s for %s: %w", account, asset, party, err)
	}
	return money.New(asset, sum), nil
}

// AccountBalance is the balance of one asset in one account of a sandbox.
type AccountBalance struct {
	AccountInfo
	// Net is the raw signed sum of postings (debits positive).
	Net money.Amount `json:"net"`
	// Balance is Net in the account's normal-balance sign, for display:
	// positive means a normal balance (debit for assets, credit for
	// liabilities, equity and revenue).
	Balance money.Amount `json:"balance"`
}

// Balances returns the balance of every (account, asset) pair that has
// postings in the sandbox, ordered by account code and then by asset in
// display order (money.Assets). A pair whose postings net to zero is still
// listed.
func Balances(ctx context.Context, q store.Querier, sandboxID uuid.UUID) ([]AccountBalance, error) {
	rows, err := q.Query(ctx, `SELECT account_code, asset, sum(amount_minor)::bigint
		FROM postings WHERE sandbox_id = $1 GROUP BY account_code, asset`, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("ledger: balances: %w", err)
	}
	defer rows.Close()
	var out []AccountBalance
	for rows.Next() {
		var (
			code  int
			asset string
			sum   int64
		)
		if err := rows.Scan(&code, &asset, &sum); err != nil {
			return nil, fmt.Errorf("ledger: balances: %w", err)
		}
		info, ok := Lookup(Account(code))
		if !ok {
			return nil, fmt.Errorf("ledger: balances: %w: %d", ErrUnknownAccount, code)
		}
		net := money.New(money.Asset(asset), sum)
		out = append(out, AccountBalance{AccountInfo: info, Net: net, Balance: info.Normal(net)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: balances: %w", err)
	}
	slices.SortFunc(out, func(a, b AccountBalance) int {
		return cmp.Or(cmp.Compare(a.Code, b.Code), cmp.Compare(assetOrder(a.Net.Asset), assetOrder(b.Net.Asset)))
	})
	return out, nil
}

func assetOrder(a money.Asset) int {
	if i := slices.Index(money.Assets, a); i >= 0 {
		return i
	}
	return len(money.Assets)
}

// TrialBalance returns, for every supported asset, the sum of all postings
// in that asset across the sandbox. A consistent ledger has every value
// zero; assets without postings are reported as zero.
func TrialBalance(ctx context.Context, q store.Querier, sandboxID uuid.UUID) (map[money.Asset]int64, error) {
	rows, err := q.Query(ctx, `SELECT asset, sum(amount_minor)::bigint
		FROM postings WHERE sandbox_id = $1 GROUP BY asset`, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("ledger: trial balance: %w", err)
	}
	defer rows.Close()
	out := make(map[money.Asset]int64, len(money.Assets))
	for _, a := range money.Assets {
		out[a] = 0
	}
	for rows.Next() {
		var (
			asset string
			sum   int64
		)
		if err := rows.Scan(&asset, &sum); err != nil {
			return nil, fmt.Errorf("ledger: trial balance: %w", err)
		}
		out[money.Asset(asset)] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: trial balance: %w", err)
	}
	return out, nil
}

// PostedEntry is a journal entry as stored, with its postings in insertion
// order.
type PostedEntry struct {
	ID        int64     `json:"id"`
	SandboxID uuid.UUID `json:"-"`
	Ref       string    `json:"ref"`
	Kind      Kind      `json:"kind"`
	Memo      string    `json:"memo"`
	Subject   *Subject  `json:"subject,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Postings  []Posting `json:"postings"`
}

// EntryFilter narrows and pages Entries. Zero-valued fields do not filter.
type EntryFilter struct {
	// SubjectType and SubjectID match the entry's subject; each is applied
	// only when non-empty.
	SubjectType string
	SubjectID   string
	// Kind matches the entry kind when non-empty.
	Kind Kind
	// Limit is the page size: DefaultEntriesLimit when ≤ 0, at most
	// MaxEntriesLimit.
	Limit int
	// BeforeID returns only entries with a smaller id (the next page after
	// an entry); 0 starts from the newest.
	BeforeID int64
}

const entryColumns = "id, sandbox_id, ref, kind, memo, subject_type, subject_id, created_at"

// Entries returns the sandbox's journal entries matching f, newest first,
// each with its postings. It runs two queries (entries, then all their
// postings in one batch) whatever the page size.
func Entries(ctx context.Context, q store.Querier, sandboxID uuid.UUID, f EntryFilter) ([]PostedEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultEntriesLimit
	}
	limit = min(limit, MaxEntriesLimit)
	rows, err := q.Query(ctx, `SELECT `+entryColumns+` FROM journal_entries
		WHERE sandbox_id = $1
		  AND ($2::text = '' OR subject_type = $2)
		  AND ($3::text = '' OR subject_id = $3)
		  AND ($4::text = '' OR kind = $4)
		  AND ($5::bigint = 0 OR id < $5)
		ORDER BY id DESC
		LIMIT $6`,
		sandboxID, f.SubjectType, f.SubjectID, string(f.Kind), f.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("ledger: entries: %w", err)
	}
	entries, err := pgx.CollectRows(rows, scanEntry)
	if err != nil {
		return nil, fmt.Errorf("ledger: entries: %w", err)
	}
	if err := attachPostings(ctx, q, sandboxID, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// EntryByRef returns the sandbox's entry with ref, with its postings, or an
// error wrapping store.ErrNotFound.
func EntryByRef(ctx context.Context, q store.Querier, sandboxID uuid.UUID, ref string) (PostedEntry, error) {
	rows, err := q.Query(ctx, `SELECT `+entryColumns+` FROM journal_entries
		WHERE sandbox_id = $1 AND ref = $2`, sandboxID, ref)
	if err != nil {
		return PostedEntry{}, fmt.Errorf("ledger: entry %q: %w", ref, err)
	}
	e, err := pgx.CollectExactlyOneRow(rows, scanEntry)
	if errors.Is(err, pgx.ErrNoRows) {
		return PostedEntry{}, fmt.Errorf("ledger: entry %q: %w", ref, store.ErrNotFound)
	}
	if err != nil {
		return PostedEntry{}, fmt.Errorf("ledger: entry %q: %w", ref, err)
	}
	entries := []PostedEntry{e}
	if err := attachPostings(ctx, q, sandboxID, entries); err != nil {
		return PostedEntry{}, err
	}
	return entries[0], nil
}

func scanEntry(row pgx.CollectableRow) (PostedEntry, error) {
	var (
		e                      PostedEntry
		kind                   string
		subjectType, subjectID *string
	)
	if err := row.Scan(&e.ID, &e.SandboxID, &e.Ref, &kind, &e.Memo,
		&subjectType, &subjectID, &e.CreatedAt); err != nil {
		return PostedEntry{}, err
	}
	e.Kind = Kind(kind)
	if subjectType != nil && subjectID != nil {
		e.Subject = &Subject{Type: *subjectType, ID: *subjectID}
	}
	return e, nil
}

// attachPostings loads the postings of every entry in one query and stores
// them on the entries in insertion order.
func attachPostings(ctx context.Context, q store.Querier, sandboxID uuid.UUID, entries []PostedEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]int64, len(entries))
	index := make(map[int64]int, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
		index[e.ID] = i
	}
	rows, err := q.Query(ctx, `SELECT entry_id, account_code, asset, amount_minor, party_id
		FROM postings WHERE sandbox_id = $1 AND entry_id = ANY($2)
		ORDER BY entry_id, id`, sandboxID, ids)
	if err != nil {
		return fmt.Errorf("ledger: postings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			entryID int64
			code    int
			asset   string
			minor   int64
			party   uuid.NullUUID
		)
		if err := rows.Scan(&entryID, &code, &asset, &minor, &party); err != nil {
			return fmt.Errorf("ledger: postings: %w", err)
		}
		i, ok := index[entryID]
		if !ok {
			continue
		}
		entries[i].Postings = append(entries[i].Postings,
			Posting{Account: Account(code), Amount: money.New(money.Asset(asset), minor), Party: party.UUID})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("ledger: postings: %w", err)
	}
	return nil
}
