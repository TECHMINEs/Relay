-- Migration 001: api_keys and request_log tables
--
-- api_keys stores hashed API keys with per-key rate limit and quota settings.
-- request_log stores per-request audit records written asynchronously.

BEGIN;

-- Enable pgvector extension (already available on pgvector/pgvector:pg16 image).
CREATE EXTENSION IF NOT EXISTS vector;

-- api_keys: one row per issued key.
-- The raw key is NEVER stored; only a SHA-256 hash (hex-encoded) is persisted.
CREATE TABLE IF NOT EXISTS api_keys (
    id               BIGSERIAL        PRIMARY KEY,
    key_hash         TEXT             NOT NULL UNIQUE,        -- SHA-256(raw_key), hex
    name             TEXT             NOT NULL DEFAULT '',    -- human label
    requests_per_sec DOUBLE PRECISION NOT NULL DEFAULT 10,   -- token bucket rate (tokens/s)
    burst            INTEGER          NOT NULL DEFAULT 20,    -- token bucket capacity
    daily_token_quota BIGINT          NOT NULL DEFAULT 1000000, -- max LLM tokens/day, 0=unlimited
    is_active        BOOLEAN          NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys (key_hash);

-- request_log: written asynchronously in batches by the gateway.
-- Partitioning by day is left for a future migration once volume demands it.
CREATE TABLE IF NOT EXISTS request_log (
    id              BIGSERIAL        PRIMARY KEY,
    api_key_id      BIGINT           REFERENCES api_keys(id) ON DELETE SET NULL,
    model           TEXT             NOT NULL DEFAULT '',
    prompt_tokens   INTEGER          NOT NULL DEFAULT 0,
    completion_tokens INTEGER        NOT NULL DEFAULT 0,
    total_tokens    INTEGER          NOT NULL DEFAULT 0,
    latency_ms      INTEGER          NOT NULL DEFAULT 0,      -- wall-clock ms for the upstream call
    status_code     SMALLINT         NOT NULL DEFAULT 200,
    provider        TEXT             NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_request_log_api_key_id ON request_log (api_key_id);
CREATE INDEX IF NOT EXISTS idx_request_log_created_at ON request_log (created_at);

COMMIT;
