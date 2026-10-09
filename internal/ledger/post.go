package ledger

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxRefLen is the longest allowed entry ref, in characters.
const MaxRefLen = 200

// Validation and posting errors returned by Entry.Validate and Post.
var (
	ErrUnbalanced      = errors.New("ledger: postings do not sum to zero per asset")
	ErrTooFewPostings  = errors.New("ledger: an entry needs at least two postings")
	ErrUnknownAccount  = errors.New("ledger: unknown account")
	ErrAssetNotAllowed = errors.New("ledger: asset not allowed in account")
	ErrZeroAmount      = errors.New("ledger: zero posting amount")
	ErrUnknownKind     = errors.New("ledger: unknown entry kind")
	ErrMissingField    = errors.New("ledger: missing required field")
	ErrBadRef          = errors.New("ledger: invalid entry ref")
	// ErrRefConflict means an entry with the same (sandbox, ref) already
	// exists with a different kind or different postings. It always signals
	// a bug in the caller: a ref must name exactly one business event.
	ErrRefConflict = errors.New("ledger: ref already posted with different content")
)

// Posting is one line of a journal entry: a debit when Amount is positive,
// a credit when negative.
type Posting struct {
	Account Account      `json:"account"`
	Amount  money.Amount `json:"amount"`
}

// Debit returns a posting that debits amount (which should be positive) to a.
func Debit(a Account, amount money.Amount) Posting { return Posting{a, amount} }

// Credit returns a posting that credits amount (which should be positive) to a.
func Credit(a Account, amount money.Amount) Posting { return Posting{a, amount.Neg()} }

// Subject links an entry to the business object it records, such as a
// payout or a chain transfer.
type Subject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Entry is a journal entry before it is posted.
type Entry struct {
	SandboxID uuid.UUID
	// Ref names the business event, unique within the sandbox, e.g.
	// "payout:<id>:submit". Posting the same ref twice is a no-op.
	Ref     string
	Kind    Kind
	Memo    string
	Subject *Subject
	// Postings are the entry's lines. They must sum to zero per asset.
	Postings []Posting
}

// Validate checks the double-entry rules: a known kind, a sandbox, a
// non-empty ref of at most MaxRefLen characters, a complete subject if any,
// at least two postings, known accounts holding only their allowed assets,
// no zero amounts and a zero sum for every asset.
func (e Entry) Validate() error {
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownKind, e.Kind)
	}
	if e.SandboxID == uuid.Nil {
		return fmt.Errorf("%w: sandbox_id", ErrMissingField)
	}
	if n := utf8.RuneCountInString(e.Ref); n == 0 || n > MaxRefLen || !utf8.ValidString(e.Ref) {
		return fmt.Errorf("%w: %d characters, want 1 to %d", ErrBadRef, n, MaxRefLen)
	}
	if e.Subject != nil && (e.Subject.Type == "" || e.Subject.ID == "") {
		return fmt.Errorf("%w: subject type and id", ErrMissingField)
	}
	if len(e.Postings) < 2 {
		return fmt.Errorf("%w: got %d", ErrTooFewPostings, len(e.Postings))
	}
	sums := map[money.Asset]int64{}
	for i, p := range e.Postings {
		info, ok := Lookup(p.Account)
		if !ok {
			return fmt.Errorf("%w: posting %d account %d", ErrUnknownAccount, i, int(p.Account))
		}
		if !info.Holds(p.Amount.Asset) {
			return fmt.Errorf("%w: posting %d %q in %s", ErrAssetNotAllowed, i, string(p.Amount.Asset), p.Account)
		}
		if p.Amount.IsZero() {
			return fmt.Errorf("%w: posting %d account %s", ErrZeroAmount, i, p.Account)
		}
		sums[p.Amount.Asset] += p.Amount.Minor
	}
	for _, asset := range money.Assets {
		if s := sums[asset]; s != 0 {
			return fmt.Errorf("%w: %s off by %s", ErrUnbalanced, asset, money.New(asset, s).Decimal())
		}
	}
	return nil
}

// Post validates e and inserts it with its postings inside tx, returning the
// entry id and whether this call created it. The database re-checks the
// per-asset balance when tx commits.
//
// Post is exactly-once on (sandbox, ref). If an entry with e's ref already
// exists in the sandbox (committed, or created earlier in tx), nothing is
// written: when it has the same kind and the same postings (compared as a
// multiset, so order does not matter) Post returns (existingID, false, nil);
// otherwise it returns ErrRefConflict. Concurrent callers posting the same
// ref in separate transactions are serialized by the unique index, and
// exactly one of them creates the entry. This is what lets the payout
// engine and the deposit ingester retry freely: a retry either posts the
// entry or confirms it was already posted, never twice. Memo and subject are
// descriptive and are not compared.
//
// tx should run at READ COMMITTED (the default), so that an entry committed
// by a concurrent transaction is visible once the insert has waited for it.
func Post(ctx context.Context, tx pgx.Tx, e Entry) (id int64, posted bool, err error) {
	if err := e.Validate(); err != nil {
		return 0, false, err
	}
	var subjectType, subjectID *string
	if e.Subject != nil {
		subjectType, subjectID = &e.Subject.Type, &e.Subject.ID
	}
	err = tx.QueryRow(ctx, `INSERT INTO journal_entries (sandbox_id, ref, kind, memo, subject_type, subject_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (sandbox_id, ref) DO NOTHING
		RETURNING id`,
		e.SandboxID, e.Ref, string(e.Kind), e.Memo, subjectType, subjectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return postExisting(ctx, tx, e)
	}
	if err != nil {
		return 0, false, fmt.Errorf("ledger: insert entry %q: %w", e.Ref, err)
	}
	accounts := make([]int, len(e.Postings))
	assets := make([]string, len(e.Postings))
	amounts := make([]int64, len(e.Postings))
	for i, p := range e.Postings {
		accounts[i] = int(p.Account)
		assets[i] = string(p.Amount.Asset)
		amounts[i] = p.Amount.Minor
	}
	_, err = tx.Exec(ctx, `INSERT INTO postings (entry_id, sandbox_id, account_code, asset, amount_minor)
		SELECT $1, $2, t.account, t.asset, t.amount
		FROM unnest($3::int[], $4::text[], $5::bigint[]) WITH ORDINALITY AS t(account, asset, amount, ord)
		ORDER BY t.ord`,
		id, e.SandboxID, accounts, assets, amounts)
	if err != nil {
		return 0, false, fmt.Errorf("ledger: insert postings of %q: %w", e.Ref, err)
	}
	return id, true, nil
}

// postExisting resolves a ref conflict: the already-posted entry must match
// e, or the post is rejected.
func postExisting(ctx context.Context, tx pgx.Tx, e Entry) (int64, bool, error) {
	existing, err := EntryByRef(ctx, tx, e.SandboxID, e.Ref)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, fmt.Errorf("ledger: ref %q conflicts with an entry this transaction cannot see", e.Ref)
	}
	if err != nil {
		return 0, false, err
	}
	if existing.Kind != e.Kind || !samePostings(existing.Postings, e.Postings) {
		return 0, false, fmt.Errorf("%w: %q is entry %d (%s)", ErrRefConflict, e.Ref, existing.ID, existing.Kind)
	}
	return existing.ID, false, nil
}

// samePostings reports whether a and b hold the same postings, ignoring order.
func samePostings(a, b []Posting) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := slices.Clone(a), slices.Clone(b)
	slices.SortFunc(sa, comparePostings)
	slices.SortFunc(sb, comparePostings)
	return slices.Equal(sa, sb)
}

func comparePostings(x, y Posting) int {
	return cmp.Or(
		cmp.Compare(x.Account, y.Account),
		cmp.Compare(x.Amount.Asset, y.Amount.Asset),
		cmp.Compare(x.Amount.Minor, y.Amount.Minor),
	)
}
