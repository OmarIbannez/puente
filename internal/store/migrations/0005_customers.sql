-- 0005_customers: the customers whose stablecoin Puente holds, their deposit
-- addresses, and the customer sub-ledger. Postings to the sub-ledger accounts
-- (2010 customer balances, 2020 payouts reserved) name the customer in
-- party_id; postings to every other account must not.

CREATE TABLE customers (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sandbox_id uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    name       text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, sandbox_id)
);
CREATE INDEX customers_sandbox_idx ON customers (sandbox_id, created_at);

-- One address per (customer, network); an address belongs to one customer.
CREATE TABLE deposit_addresses (
    sandbox_id  uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    customer_id uuid NOT NULL,
    network     text NOT NULL,
    address     text NOT NULL CHECK (address ~ '^0x[0-9a-f]{40}$'),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sandbox_id, network, address),
    UNIQUE (sandbox_id, customer_id, network),
    FOREIGN KEY (customer_id, sandbox_id) REFERENCES customers (id, sandbox_id) ON DELETE CASCADE
);

ALTER TABLE postings ADD COLUMN party_id uuid NULL;
ALTER TABLE postings ADD CONSTRAINT postings_party
    CHECK ((account_code IN (2010, 2020)) = (party_id IS NOT NULL));
ALTER TABLE postings ADD CONSTRAINT postings_party_fk
    FOREIGN KEY (party_id, sandbox_id) REFERENCES customers (id, sandbox_id);
CREATE INDEX postings_party_idx ON postings (sandbox_id, party_id, account_code, asset)
    WHERE party_id IS NOT NULL;
