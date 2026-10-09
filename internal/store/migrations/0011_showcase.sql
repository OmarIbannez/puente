-- 0011_showcase: a third sandbox kind. When the sandbox-creation budget is
-- exhausted, new visitors browse one shared, seeded, read-only showcase
-- sandbox instead of getting an error. It is never ticked or pruned.

ALTER TABLE sandboxes DROP CONSTRAINT sandboxes_kind_check;
ALTER TABLE sandboxes ADD CONSTRAINT sandboxes_kind_check CHECK (kind IN ('visitor', 'live', 'showcase'));
CREATE UNIQUE INDEX sandboxes_one_showcase_idx ON sandboxes (kind) WHERE kind = 'showcase';
