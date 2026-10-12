// Package customer holds the customers whose stablecoin Puente keeps, and
// the on-chain deposit addresses that route a transfer to one of them.
package customer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OmarIbannez/puente/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrBadAddress is returned for an address that is not 0x plus 40 hex digits.
var ErrBadAddress = errors.New("customer: malformed address")

// Customer is an account holder.
type Customer struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Address is a customer's deposit address on one network.
type Address struct {
	CustomerID uuid.UUID `json:"customer_id"`
	Network    string    `json:"network"`
	Address    string    `json:"address"`
}

// Create inserts a customer in the sandbox.
func Create(ctx context.Context, q store.Querier, sandbox uuid.UUID, name string) (Customer, error) {
	var c Customer
	err := q.QueryRow(ctx, `INSERT INTO customers (sandbox_id, name) VALUES ($1, $2)
		RETURNING id, name, created_at`, sandbox, strings.TrimSpace(name)).Scan(&c.ID, &c.Name, &c.CreatedAt)
	if err != nil {
		return Customer{}, fmt.Errorf("customer: create: %w", err)
	}
	return c, nil
}

// Get returns one customer of the sandbox, or store.ErrNotFound.
func Get(ctx context.Context, q store.Querier, sandbox, id uuid.UUID) (Customer, error) {
	var c Customer
	err := q.QueryRow(ctx, `SELECT id, name, created_at FROM customers
		WHERE sandbox_id = $1 AND id = $2`, sandbox, id).Scan(&c.ID, &c.Name, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, store.ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("customer: get: %w", err)
	}
	return c, nil
}

// List returns the sandbox's customers, oldest first.
func List(ctx context.Context, q store.Querier, sandbox uuid.UUID) ([]Customer, error) {
	rows, err := q.Query(ctx, `SELECT id, name, created_at FROM customers
		WHERE sandbox_id = $1 ORDER BY created_at, id`, sandbox)
	if err != nil {
		return nil, fmt.Errorf("customer: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Customer, error) {
		var c Customer
		return c, r.Scan(&c.ID, &c.Name, &c.CreatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("customer: list: %w", err)
	}
	return out, nil
}

// NormalizeAddress lowercases a 0x-prefixed 20-byte hex address.
func NormalizeAddress(addr string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(addr))
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return "", fmt.Errorf("%w: %q", ErrBadAddress, addr)
	}
	if _, err := hex.DecodeString(a[2:]); err != nil {
		return "", fmt.Errorf("%w: %q", ErrBadAddress, addr)
	}
	return a, nil
}

// SimAddress derives the deterministic deposit address a customer gets on a
// simulated network. It is not a real key pair; nobody can spend from it.
func SimAddress(sandbox, customerID uuid.UUID, network string) string {
	sum := sha256.Sum256([]byte("puente-sim-address:" + sandbox.String() + ":" + customerID.String() + ":" + network))
	return "0x" + hex.EncodeToString(sum[:20])
}

// AssignAddress records addr as the customer's deposit address on network.
// Assigning the same pair again is a no-op.
func AssignAddress(ctx context.Context, q store.Querier, sandbox, customerID uuid.UUID, network, addr string) (Address, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return Address{}, err
	}
	_, err = q.Exec(ctx, `INSERT INTO deposit_addresses (sandbox_id, customer_id, network, address)
		VALUES ($1, $2, $3, $4) ON CONFLICT (sandbox_id, network, address) DO NOTHING`,
		sandbox, customerID, network, a)
	if err != nil {
		return Address{}, fmt.Errorf("customer: assign address: %w", err)
	}
	return Address{CustomerID: customerID, Network: network, Address: a}, nil
}

// Addresses returns the sandbox's deposit addresses on network (all
// networks when network is empty).
func Addresses(ctx context.Context, q store.Querier, sandbox uuid.UUID, network string) ([]Address, error) {
	rows, err := q.Query(ctx, `SELECT customer_id, network, address FROM deposit_addresses
		WHERE sandbox_id = $1 AND ($2 = '' OR network = $2) ORDER BY created_at, address`, sandbox, network)
	if err != nil {
		return nil, fmt.Errorf("customer: addresses: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Address, error) {
		var a Address
		return a, r.Scan(&a.CustomerID, &a.Network, &a.Address)
	})
	if err != nil {
		return nil, fmt.Errorf("customer: addresses: %w", err)
	}
	return out, nil
}

// Owner returns the customer whose deposit address on network is addr, or
// store.ErrNotFound when the address is not Puente's.
func Owner(ctx context.Context, q store.Querier, sandbox uuid.UUID, network, addr string) (uuid.UUID, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `SELECT customer_id FROM deposit_addresses
		WHERE sandbox_id = $1 AND network = $2 AND address = $3`, sandbox, network, a).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, store.ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("customer: owner: %w", err)
	}
	return id, nil
}

// SetAddress makes addr the customer's only deposit address on network,
// replacing a previous one. The live sandbox uses it so that changing the
// watched address between deploys takes effect instead of conflicting.
func SetAddress(ctx context.Context, q store.Querier, sandbox, customerID uuid.UUID, network, addr string) (Address, error) {
	a, err := NormalizeAddress(addr)
	if err != nil {
		return Address{}, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM deposit_addresses
		WHERE sandbox_id = $1 AND network = $2 AND customer_id = $3 AND address <> $4`,
		sandbox, network, customerID, a); err != nil {
		return Address{}, fmt.Errorf("customer: replace address: %w", err)
	}
	return AssignAddress(ctx, q, sandbox, customerID, network, a)
}
