-- 0006_payouts: stablecoin-to-SPEI payouts, their state history and the
-- transactional outbox that hands them to the rail.
--
-- A payout is quoted once at creation (fee, FX rate, peso amount) and keeps
-- those numbers for life, so every retry and every compensating entry uses
-- the same amounts. Its clave de rastreo is fixed at creation from a global
-- sequence, so a resubmission after a lost ack reuses it and the rail
-- dedupes.

CREATE SEQUENCE payout_clave_seq;

CREATE TABLE payouts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sandbox_id        uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    customer_id       uuid NOT NULL,
    asset             text NOT NULL CHECK (asset IN ('USDC', 'MXNB')),
    amount_minor      bigint NOT NULL CHECK (amount_minor > 0),
    fee_minor         bigint NOT NULL CHECK (fee_minor >= 0),
    -- MXN per whole unit of asset, scaled by 1e6.
    rate              bigint NOT NULL CHECK (rate > 0),
    mxn_centavos      bigint NOT NULL CHECK (mxn_centavos > 0),
    beneficiary_clabe text NOT NULL CHECK (beneficiary_clabe ~ '^[0-9]{18}$'),
    beneficiary_name  text NOT NULL CHECK (char_length(beneficiary_name) BETWEEN 1 AND 40),
    concepto          text NOT NULL DEFAULT '' CHECK (char_length(concepto) <= 40),
    clave             text NOT NULL CHECK (clave ~ '^[A-Z0-9]{1,30}$'),
    status            text NOT NULL DEFAULT 'reserved' CHECK (status IN (
        'reserved', 'submitted', 'liquidated', 'rejected', 'released', 'returned', 'needs_review')),
    -- attempts counts submissions to the rail; last_error is the latest failure.
    attempts          integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sandbox_id, clave),
    UNIQUE (id, sandbox_id),
    FOREIGN KEY (customer_id, sandbox_id) REFERENCES customers (id, sandbox_id) ON DELETE CASCADE
);
CREATE INDEX payouts_sandbox_created_idx ON payouts (sandbox_id, created_at DESC, id DESC);
CREATE INDEX payouts_sandbox_status_idx ON payouts (sandbox_id, status);
CREATE INDEX payouts_sandbox_customer_idx ON payouts (sandbox_id, customer_id, created_at DESC);

-- payout_events: append-only state history. from_status is NULL for the
-- creation event.
CREATE TABLE payout_events (
    id          bigserial PRIMARY KEY,
    sandbox_id  uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    payout_id   uuid NOT NULL,
    from_status text NULL,
    to_status   text NOT NULL,
    detail      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (payout_id, sandbox_id) REFERENCES payouts (id, sandbox_id) ON DELETE CASCADE
);
CREATE INDEX payout_events_payout_idx ON payout_events (payout_id, id);

-- payout_outbox: work for the worker, written in the same transaction as the
-- payout. A job is claimed with FOR UPDATE SKIP LOCKED once available_at has
-- passed and is finished when done_at is set.
CREATE TABLE payout_outbox (
    id           bigserial PRIMARY KEY,
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    payout_id    uuid NOT NULL,
    kind         text NOT NULL DEFAULT 'submit' CHECK (kind = 'submit'),
    available_at timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    done_at      timestamptz NULL,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (payout_id, kind),
    FOREIGN KEY (payout_id, sandbox_id) REFERENCES payouts (id, sandbox_id) ON DELETE CASCADE
);
CREATE INDEX payout_outbox_pending_idx ON payout_outbox (sandbox_id, available_at, id)
    WHERE done_at IS NULL;
