-- 0010_deposit_credit_fk: deposit_credits referenced chain_transfers by id
-- alone. Scope it by sandbox like every other child table, so a credit can
-- never point at another sandbox's transfer.

ALTER TABLE chain_transfers ADD CONSTRAINT chain_transfers_id_sandbox_key UNIQUE (id, sandbox_id);
ALTER TABLE deposit_credits DROP CONSTRAINT deposit_credits_transfer_id_fkey;
ALTER TABLE deposit_credits ADD CONSTRAINT deposit_credits_transfer_fk
    FOREIGN KEY (transfer_id, sandbox_id) REFERENCES chain_transfers (id, sandbox_id) ON DELETE CASCADE;
