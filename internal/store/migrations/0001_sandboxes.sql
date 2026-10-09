-- 0001_sandboxes: every domain row belongs to a sandbox and is deleted with
-- it. Visitors each get their own seeded sandbox (kind 'visitor'); the one
-- 'live' sandbox mirrors the real testnets and is read-only over the API.
-- faults holds the per-sandbox fault-injection switches for the simulated
-- chain and rail.

CREATE TABLE sandboxes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text NOT NULL DEFAULT 'visitor' CHECK (kind IN ('visitor', 'live')),
    faults       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sandboxes_last_seen_at_idx ON sandboxes (last_seen_at);
-- At most one live sandbox.
CREATE UNIQUE INDEX sandboxes_one_live_idx ON sandboxes (kind) WHERE kind = 'live';
