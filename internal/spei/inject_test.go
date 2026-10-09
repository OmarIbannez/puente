package spei_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/OmarIbannez/puente/internal/money"
	"github.com/OmarIbannez/puente/internal/spei"
	"github.com/OmarIbannez/puente/internal/store"
	"github.com/OmarIbannez/puente/internal/store/storetest"

	"github.com/jackc/pgx/v5"
)

func TestInjectBankFeeAndManualOrder(t *testing.T) {
	ctx := t.Context()
	sb := storetest.Sandbox(t, pool).ID
	fund(t, sb, 100_000)

	err := store.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := rail.InjectBankFee(ctx, tx, sb, mxn(1_500)); err != nil {
			return err
		}
		rec, err := rail.InjectManualOrder(ctx, tx, sb, beneficiary, "Proveedor X", mxn(20_000))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(rec.Clave, spei.ManualClavePrefix) || rec.Status != spei.StatusLiquidado || rec.CEP == nil {
			t.Errorf("manual order = %+v", rec)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBalance(t, sb, 100_000-1_500-20_000)

	err = store.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := rail.InjectBankFee(ctx, tx, sb, money.New(money.USDC, 1))
		return err
	})
	if !errors.Is(err, spei.ErrBadInjection) {
		t.Fatalf("non-MXN fee = %v, want ErrBadInjection", err)
	}
}
