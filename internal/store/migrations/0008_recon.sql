-- 0008_recon: reconciliation runs and the breaks they find, plus a bank_fee
-- statement kind so the simulated bank can book a charge Puente never
-- expected (a balance break on demand).

ALTER TABLE spei_statement DROP CONSTRAINT spei_statement_kind_check;
ALTER TABLE spei_statement DROP CONSTRAINT spei_statement_check;
ALTER TABLE spei_statement DROP CONSTRAINT spei_statement_check1;
ALTER TABLE spei_statement ADD CONSTRAINT spei_statement_kind_check
    CHECK (kind IN ('funding', 'liquidation_debit', 'return_credit', 'bank_fee'));
ALTER TABLE spei_statement ADD CONSTRAINT spei_statement_sign_check
    CHECK ((kind IN ('liquidation_debit', 'bank_fee') AND amount_centavos < 0)
        OR (kind IN ('funding', 'return_credit') AND amount_centavos > 0));
ALTER TABLE spei_statement ADD CONSTRAINT spei_statement_clave_check
    CHECK (kind IN ('funding', 'bank_fee') OR clave IS NOT NULL);

CREATE TABLE recon_runs (
    id          bigserial PRIMARY KEY,
    sandbox_id  uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    trigger     text NOT NULL CHECK (trigger IN ('manual', 'scheduled', 'cli')),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    -- What was compared: counts per matcher, for the run summary.
    checked     jsonb NOT NULL DEFAULT '{}'::jsonb,
    break_count int NOT NULL DEFAULT 0,
    UNIQUE (id, sandbox_id)
);
CREATE INDEX recon_runs_sandbox_idx ON recon_runs (sandbox_id, id DESC);

CREATE TABLE recon_breaks (
    id           bigserial PRIMARY KEY,
    run_id       bigint NOT NULL,
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('missing_on_ledger', 'missing_on_chain',
        'missing_on_rail', 'duplicate', 'amount_mismatch', 'balance_mismatch')),
    source       text NOT NULL CHECK (source IN ('chain', 'rail', 'balance')),
    subject_type text NOT NULL,
    subject_id   text NOT NULL,
    summary      text NOT NULL,
    expected     text NOT NULL DEFAULT '',
    actual       text NOT NULL DEFAULT '',
    evidence     jsonb NOT NULL DEFAULT '{}'::jsonb,
    FOREIGN KEY (run_id, sandbox_id) REFERENCES recon_runs (id, sandbox_id) ON DELETE CASCADE
);
CREATE INDEX recon_breaks_run_idx ON recon_breaks (run_id, id);
