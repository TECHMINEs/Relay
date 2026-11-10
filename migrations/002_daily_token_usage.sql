-- Migration 002: daily token quota reconciliation table
--
-- daily_token_usage caches per-(key, date) token counts so they can be
-- reconciled from Redis into Postgres for durability and reporting.

BEGIN;

CREATE TABLE IF NOT EXISTS daily_token_usage (
    api_key_id  BIGINT       NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    usage_date  DATE         NOT NULL DEFAULT CURRENT_DATE,
    tokens_used BIGINT       NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (api_key_id, usage_date)
);

CREATE INDEX IF NOT EXISTS idx_daily_token_usage_date ON daily_token_usage (usage_date);

COMMIT;
