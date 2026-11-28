-- Per-contract ingestion progress. One row per watched contract, tracking
-- the resume point so a restart continues from each contract's own cursor
-- rather than from a single global position.
CREATE TABLE IF NOT EXISTS contract_ingest_state (
    contract_id TEXT PRIMARY KEY,
    last_ledger BIGINT NOT NULL DEFAULT 0,
    last_cursor TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);