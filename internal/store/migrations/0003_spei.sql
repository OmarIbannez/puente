-- 0003_spei: the books of the simulated SPEI participant. They stand in for
-- the bank's own records and are an independent source for reconciliation,
-- so nothing here references ledger tables. Simulated; not Banxico-conformant.

-- spei_orders: one row per clave de rastreo. Submit is idempotent on
-- (sandbox_id, clave). beneficiary_clabe is stored as submitted (minus
-- separators) even when invalid, because such orders are kept as rechazado.
CREATE TABLE spei_orders (
    id                bigserial PRIMARY KEY,
    sandbox_id        uuid NOT NULL REFERENCES sandboxes (id) ON DELETE CASCADE,
    clave             text NOT NULL CHECK (clave ~ '^[A-Z0-9]{1,30}$'),
    ordering_clabe    text NOT NULL CHECK (ordering_clabe ~ '^[0-9]{18}$'),
    beneficiary_clabe text NOT NULL,
    beneficiary_name  text NOT NULL,
    concepto          text NOT NULL DEFAULT '',
    amount_centavos   bigint NOT NULL CHECK (amount_centavos > 0),
    status            text NOT NULL
        CHECK (status IN ('en_proceso', 'liquidado', 'rechazado', 'devuelto')),
    reason            text NOT NULL DEFAULT '',
    return_pending    boolean NOT NULL DEFAULT false,
    submit_count      integer NOT NULL DEFAULT 1 CHECK (submit_count >= 1),
    cep               jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    liquidated_at     timestamptz,
    returned_at       timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sandbox_id, clave),
    CHECK (status NOT IN ('liquidado', 'devuelto') OR (liquidated_at IS NOT NULL AND cep IS NOT NULL)),
    CHECK (status <> 'devuelto' OR returned_at IS NOT NULL)
);
CREATE INDEX spei_orders_sandbox_status_idx ON spei_orders (sandbox_id, status, id);

-- spei_statement: the participant's settlement account. Signed centavos:
-- funding and return_credit are positive, liquidation_debit is negative.
-- Each order is debited at most once and credited back at most once.
CREATE TABLE spei_statement (
    id              bigserial PRIMARY KEY,
    sandbox_id      uuid NOT NULL REFERENCES sandboxes (id) ON DELETE CASCADE,
    clave           text,
    kind            text NOT NULL CHECK (kind IN ('funding', 'liquidation_debit', 'return_credit')),
    amount_centavos bigint NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'liquidation_debit' AND amount_centavos < 0)
        OR (kind IN ('funding', 'return_credit') AND amount_centavos > 0)),
    CHECK (kind = 'funding' OR clave IS NOT NULL)
);
CREATE INDEX spei_statement_sandbox_idx ON spei_statement (sandbox_id, id);
CREATE UNIQUE INDEX spei_statement_once_idx ON spei_statement (sandbox_id, clave, kind)
    WHERE clave IS NOT NULL;
