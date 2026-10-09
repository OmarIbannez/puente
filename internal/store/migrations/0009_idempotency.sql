-- 0009_idempotency: stored outcomes of money-moving API requests. A row is
-- inserted pending (status_code 0) when a request claims its key; the
-- response is stored verbatim (a JSON string, so key order survives) for
-- byte-identical replays.

CREATE TABLE idempotency_keys (
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    key          text NOT NULL,
    request_hash text NOT NULL,
    status_code  int NOT NULL,
    response     jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sandbox_id, key)
);
