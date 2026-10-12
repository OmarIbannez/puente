-- 0007_deposits: credits of confirmed chain transfers to customer balances.
--
-- A deposit credit records that a confirmed transfer was posted to the
-- ledger (entry 'deposit:<transfer_id>:<generation>', Dr 1010 / Cr 2010).
-- When the transfer is orphaned by a reorg the credit is reversed
-- ('deposit_reversal:<transfer_id>:<generation>') and stays as history. A
-- reorg can re-include the transaction and the scanner then revives the same
-- transfer row, so a transfer may be credited again under the next
-- generation. At most one credit per transfer is open (not reversed) at a
-- time.

CREATE TABLE deposit_credits (
    sandbox_id        uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    transfer_id       bigint NOT NULL REFERENCES chain_transfers ON DELETE CASCADE,
    generation        int NOT NULL CHECK (generation > 0),
    customer_id       uuid NOT NULL,
    asset             text NOT NULL CHECK (asset IN ('USDC', 'MXNB')),
    amount_minor      bigint NOT NULL CHECK (amount_minor > 0),
    credit_entry_id   bigint NOT NULL,
    reversal_entry_id bigint NULL,
    credited_at       timestamptz NOT NULL DEFAULT now(),
    reversed_at       timestamptz NULL,
    PRIMARY KEY (sandbox_id, transfer_id, generation),
    FOREIGN KEY (customer_id, sandbox_id) REFERENCES customers (id, sandbox_id),
    FOREIGN KEY (credit_entry_id, sandbox_id) REFERENCES journal_entries (id, sandbox_id),
    FOREIGN KEY (reversal_entry_id, sandbox_id) REFERENCES journal_entries (id, sandbox_id),
    CHECK ((reversal_entry_id IS NULL) = (reversed_at IS NULL))
);

-- At most one open credit per transfer.
CREATE UNIQUE INDEX deposit_credits_open_idx ON deposit_credits (sandbox_id, transfer_id)
    WHERE reversal_entry_id IS NULL;
CREATE INDEX deposit_credits_customer_idx ON deposit_credits (sandbox_id, customer_id);
