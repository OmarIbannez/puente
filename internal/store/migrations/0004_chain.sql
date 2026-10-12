-- 0004_chain: on-chain deposits and the simulated chain.
--
-- chain_transfers holds every ERC-20 Transfer log the scanner pulled from a
-- network's feed. (sandbox_id, chain_id, tx_hash, log_index) is unique, so a
-- rescan inserts nothing. status moves seen -> confirmed once the block has
-- enough confirmations, and to orphaned when the stored block_hash is no
-- longer canonical. An orphaned transfer whose log the feed reports again in
-- a block the canonical chain confirms (the transaction was re-included after
-- a reorg) goes back to seen with that block. Addresses and hashes are lowercase 0x hex.
--
-- chain_cursors stores, per sandbox and network, the next block to scan.

CREATE TABLE chain_transfers (
    id           bigserial PRIMARY KEY,
    sandbox_id   uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    network      text NOT NULL CHECK (char_length(network) BETWEEN 1 AND 64),
    chain_id     bigint NOT NULL CHECK (chain_id > 0),
    asset        text NOT NULL CHECK (asset IN ('USDC', 'MXNB')),
    token        text NOT NULL CHECK (token ~ '^0x[0-9a-f]{40}$'),
    tx_hash      text NOT NULL CHECK (tx_hash ~ '^0x[0-9a-f]{64}$'),
    log_index    bigint NOT NULL CHECK (log_index >= 0),
    block_number bigint NOT NULL CHECK (block_number >= 0),
    block_hash   text NOT NULL CHECK (block_hash ~ '^0x[0-9a-f]{64}$'),
    from_addr    text NOT NULL CHECK (from_addr ~ '^0x[0-9a-f]{40}$'),
    to_addr      text NOT NULL CHECK (to_addr ~ '^0x[0-9a-f]{40}$'),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    status       text NOT NULL DEFAULT 'seen' CHECK (status IN ('seen', 'confirmed', 'orphaned')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz NULL,
    orphaned_at  timestamptz NULL,
    UNIQUE (sandbox_id, chain_id, tx_hash, log_index)
);
CREATE INDEX chain_transfers_sandbox_idx ON chain_transfers (sandbox_id, id);
CREATE INDEX chain_transfers_block_idx ON chain_transfers (sandbox_id, network, block_number)
    WHERE status <> 'orphaned';

CREATE TABLE chain_cursors (
    sandbox_id uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    network    text NOT NULL,
    next_block bigint NOT NULL CHECK (next_block >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sandbox_id, network)
);

-- The simulated chain (internal/chain/sim). Each sandbox has its own blocks
-- per simulated network. A reorg keeps the replaced block with canonical =
-- false and inserts its replacement at the same number.
CREATE TABLE sim_blocks (
    sandbox_id uuid NOT NULL REFERENCES sandboxes ON DELETE CASCADE,
    network    text NOT NULL,
    number     bigint NOT NULL CHECK (number > 0),
    hash       text NOT NULL CHECK (hash ~ '^0x[0-9a-f]{64}$'),
    canonical  boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sandbox_id, network, hash)
);
CREATE UNIQUE INDEX sim_blocks_canonical_idx ON sim_blocks (sandbox_id, network, number)
    WHERE canonical;

-- sim_logs keeps the feed and the canonical chain apart explicitly:
--   in_feed          the indexer reports this log (with feed_amount);
--   canonical        the log exists on chain (with canonical_amount);
-- A log is visible only while its block is canonical. Faults diverge the
-- two: a duplicated log is in the feed but not canonical, and a short
-- deposit has canonical_amount < feed_amount.
CREATE TABLE sim_logs (
    id               bigserial PRIMARY KEY,
    sandbox_id       uuid NOT NULL,
    network          text NOT NULL,
    block_number     bigint NOT NULL CHECK (block_number > 0),
    block_hash       text NOT NULL,
    tx_hash          text NOT NULL CHECK (tx_hash ~ '^0x[0-9a-f]{64}$'),
    log_index        bigint NOT NULL CHECK (log_index >= 0),
    from_addr        text NOT NULL CHECK (from_addr ~ '^0x[0-9a-f]{40}$'),
    to_addr          text NOT NULL CHECK (to_addr ~ '^0x[0-9a-f]{40}$'),
    feed_amount      bigint NOT NULL CHECK (feed_amount > 0),
    canonical_amount bigint NULL CHECK (canonical_amount > 0),
    in_feed          boolean NOT NULL,
    canonical        boolean NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (sandbox_id, network, block_hash)
        REFERENCES sim_blocks (sandbox_id, network, hash) ON DELETE CASCADE,
    UNIQUE (sandbox_id, network, tx_hash, log_index),
    CHECK (canonical = (canonical_amount IS NOT NULL))
);
CREATE INDEX sim_logs_block_idx ON sim_logs (sandbox_id, network, block_number);
