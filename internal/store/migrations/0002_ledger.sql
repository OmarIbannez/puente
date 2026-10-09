-- 0002_ledger: the multi-asset double-entry ledger.
--
-- A journal entry is identified within its sandbox by ref (for example
-- 'payout:<id>:submit' or 'deposit:<transfer_id>'). The unique (sandbox_id,
-- ref) is what makes posting exactly-once: retries insert with ON CONFLICT
-- DO NOTHING. subject_type/subject_id link an entry to the payout or chain
-- transfer it records; they are generic, so there is no foreign key.
--
-- A posting is a signed amount of one asset in minor units (debits
-- positive). Each entry must sum to zero per asset, checked at commit by
-- the deferred postings_balanced constraint trigger.

CREATE TABLE journal_entries (
    id           bigserial PRIMARY KEY,
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    ref          text NOT NULL CHECK (char_length(ref) BETWEEN 1 AND 200),
    kind         text NOT NULL CHECK (kind IN (
        'funding', 'deposit', 'deposit_reversal', 'payout_reserve', 'payout_submit',
        'payout_settle', 'payout_release', 'payout_return')),
    memo         text NOT NULL DEFAULT '',
    subject_type text NULL,
    subject_id   text NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sandbox_id, ref),
    UNIQUE (id, sandbox_id),
    CHECK ((subject_type IS NULL) = (subject_id IS NULL))
);
CREATE INDEX journal_entries_sandbox_idx ON journal_entries (sandbox_id, id);
CREATE INDEX journal_entries_subject_idx ON journal_entries (sandbox_id, subject_type, subject_id)
    WHERE subject_type IS NOT NULL;

CREATE TABLE postings (
    id           bigserial PRIMARY KEY,
    entry_id     bigint NOT NULL,
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    account_code int NOT NULL CHECK (account_code IN (1010, 1100, 2010, 2020, 2100, 3000, 3900, 4100)),
    asset        text NOT NULL CHECK (asset IN ('USDC', 'MXNB', 'MXN')),
    amount_minor bigint NOT NULL CHECK (amount_minor <> 0),
    FOREIGN KEY (entry_id, sandbox_id) REFERENCES journal_entries (id, sandbox_id) ON DELETE CASCADE,
    -- The assets each account may hold (mirrors the Go chart of accounts).
    CONSTRAINT postings_account_asset CHECK (
        (account_code IN (1010, 2010, 2020, 4100) AND asset IN ('USDC', 'MXNB'))
        OR (account_code IN (1100, 2100, 3000) AND asset = 'MXN')
        OR account_code = 3900)
);
CREATE INDEX postings_entry_idx ON postings (entry_id);
CREATE INDEX postings_sandbox_account_asset_idx ON postings (sandbox_id, account_code, asset);

-- Double-entry guard: at commit, the postings of every touched entry must sum
-- to zero for each asset. Deferred so that an entry's postings can be
-- inserted one by one.
CREATE FUNCTION postings_check_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    eid   bigint;
    bad   text;
    total numeric;
BEGIN
    -- An UPDATE can move a posting between entries, so check both sides.
    FOREACH eid IN ARRAY CASE TG_OP
        WHEN 'INSERT' THEN ARRAY[NEW.entry_id]
        WHEN 'DELETE' THEN ARRAY[OLD.entry_id]
        ELSE ARRAY[OLD.entry_id, NEW.entry_id]
    END LOOP
        SELECT asset, sum(amount_minor) INTO bad, total
        FROM postings WHERE entry_id = eid
        GROUP BY asset HAVING sum(amount_minor) <> 0
        ORDER BY asset LIMIT 1;
        IF FOUND THEN
            RAISE EXCEPTION 'journal entry % is unbalanced: % postings sum to %', eid, bad, total
                USING ERRCODE = 'check_violation', CONSTRAINT = 'postings_balanced';
        END IF;
    END LOOP;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER postings_balanced
    AFTER INSERT OR UPDATE OR DELETE ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION postings_check_balanced();
