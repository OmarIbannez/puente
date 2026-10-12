package customer_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/OmarIbannez/puente/internal/customer"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { os.Exit(storetest.Main(m, &pool)) }

func TestCustomersAndAddresses(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	alice, err := customer.Create(ctx, pool, sb, "  Alice  ")
	if err != nil || alice.Name != "Alice" {
		t.Fatalf("Create = %+v, %v", alice, err)
	}
	bob, err := customer.Create(ctx, pool, sb, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	list, err := customer.List(ctx, pool, sb)
	if err != nil || len(list) != 2 || list[0].ID != alice.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if got, err := customer.Get(ctx, pool, sb, bob.ID); err != nil || got.Name != "Bob" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := customer.Get(ctx, pool, storetest.Sandbox(t, pool).ID, bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get from another sandbox = %v", err)
	}

	addr := customer.SimAddress(sb, alice.ID, "sim-usdc")
	if addr != customer.SimAddress(sb, alice.ID, "sim-usdc") || addr == customer.SimAddress(sb, bob.ID, "sim-usdc") {
		t.Fatal("SimAddress is not deterministic per customer")
	}
	for range 2 { // idempotent
		if _, err := customer.AssignAddress(ctx, pool, sb, alice.ID, "sim-usdc", addr); err != nil {
			t.Fatal(err)
		}
	}
	upper := "0x" + strings.ToUpper(addr[2:])
	owner, err := customer.Owner(ctx, pool, sb, "sim-usdc", upper)
	if err != nil || owner != alice.ID {
		t.Fatalf("Owner = %s, %v", owner, err)
	}
	if _, err := customer.Owner(ctx, pool, sb, "sim-mxnb", addr); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Owner on another network = %v", err)
	}
	addrs, err := customer.Addresses(ctx, pool, sb, "")
	if err != nil || len(addrs) != 1 || addrs[0].Address != addr {
		t.Fatalf("Addresses = %+v, %v", addrs, err)
	}
	if _, err := customer.AssignAddress(ctx, pool, sb, uuid.New(), "sim-usdc", "0x1234"); !errors.Is(err, customer.ErrBadAddress) {
		t.Fatalf("bad address = %v", err)
	}
}

func TestSetAddressReplaces(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	c, err := customer.Create(ctx, pool, sb, "Treasury")
	if err != nil {
		t.Fatal(err)
	}
	first := "0x00000000000000000000000000000000000000a1"
	second := "0x00000000000000000000000000000000000000a2"
	if _, err := customer.SetAddress(ctx, pool, sb, c.ID, "live", first); err != nil {
		t.Fatal(err)
	}
	if _, err := customer.SetAddress(ctx, pool, sb, c.ID, "live", second); err != nil {
		t.Fatalf("changing the address: %v", err)
	}
	addrs, err := customer.Addresses(ctx, pool, sb, "live")
	if err != nil || len(addrs) != 1 || addrs[0].Address != second {
		t.Fatalf("addresses = %+v, %v", addrs, err)
	}
}
