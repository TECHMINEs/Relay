// Package store provides Postgres data access for api_keys, request_log,
// and daily_token_usage tables.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// APIKey holds a row from the api_keys table.
type APIKey struct {
	ID              int64
	KeyHash         string
	Name            string
	RequestsPerSec  float64
	Burst           int
	DailyTokenQuota int64
	IsActive        bool
	CreatedAt       time.Time
}

// RequestLogEntry holds data for one row in the request_log table.
type RequestLogEntry struct {
	APIKeyID         int64
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMS        int
	StatusCode       int
	Provider         string
	CreatedAt        time.Time
}

type contextKey int

const (
	keyAPIKey contextKey = iota
)

// ContextWithAPIKey stores the *APIKey in the context.
func ContextWithAPIKey(ctx context.Context, key *APIKey) context.Context {
	return context.WithValue(ctx, keyAPIKey, key)
}

// APIKeyFromContext extracts the *APIKey from the context.
// Returns nil if not present.
func APIKeyFromContext(ctx context.Context) *APIKey {
	v, _ := ctx.Value(keyAPIKey).(*APIKey)
	return v
}

// Store is the data-access layer backed by a pgxpool connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a Store using the supplied connection pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// NewPool opens a pgxpool from a DSN string.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: failed to open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: failed to ping database: %w", err)
	}
	return pool, nil
}

// GetAPIKeyByHash looks up an API key by its SHA-256 hash.
// Returns (nil, nil) if the key does not exist.
func (s *Store) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	const q = `
		SELECT id, key_hash, name, requests_per_sec, burst, daily_token_quota, is_active, created_at
		FROM api_keys
		WHERE key_hash = $1
		LIMIT 1
	`
	row := s.pool.QueryRow(ctx, q, hash)
	var k APIKey
	if err := row.Scan(
		&k.ID, &k.KeyHash, &k.Name,
		&k.RequestsPerSec, &k.Burst, &k.DailyTokenQuota,
		&k.IsActive, &k.CreatedAt,
	); err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: GetAPIKeyByHash: %w", err)
	}
	return &k, nil
}

// InsertRequestLogBatch inserts a batch of request log entries in a single
// transaction using pgx CopyFrom for efficiency.
func (s *Store) InsertRequestLogBatch(ctx context.Context, entries []RequestLogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: InsertRequestLogBatch begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows := make([][]any, len(entries))
	for i, e := range entries {
		ts := e.CreatedAt
		if ts.IsZero() {
			ts = time.Now()
		}
		rows[i] = []any{
			e.APIKeyID, e.Model,
			e.PromptTokens, e.CompletionTokens, e.TotalTokens,
			e.LatencyMS, e.StatusCode, e.Provider, ts,
		}
	}

	cols := []string{
		"api_key_id", "model",
		"prompt_tokens", "completion_tokens", "total_tokens",
		"latency_ms", "status_code", "provider", "created_at",
	}

	_, err = tx.CopyFrom(
		ctx,
		pgx.Identifier{"request_log"},
		cols,
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("store: InsertRequestLogBatch CopyFrom: %w", err)
	}

	return tx.Commit(ctx)
}

// UpsertDailyTokenUsage adds delta tokens to the daily_token_usage row for
// the given api_key_id and date (UTC), inserting if absent.
func (s *Store) UpsertDailyTokenUsage(ctx context.Context, keyID int64, date time.Time, delta int64) error {
	const q = `
		INSERT INTO daily_token_usage (api_key_id, usage_date, tokens_used, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (api_key_id, usage_date)
		DO UPDATE SET tokens_used = daily_token_usage.tokens_used + EXCLUDED.tokens_used,
		              updated_at  = NOW()
	`
	day := date.UTC().Truncate(24 * time.Hour)
	if _, err := s.pool.Exec(ctx, q, keyID, day, delta); err != nil {
		return fmt.Errorf("store: UpsertDailyTokenUsage: %w", err)
	}
	return nil
}

// GetDailyTokenUsage returns the total tokens consumed by keyID on date (UTC).
func (s *Store) GetDailyTokenUsage(ctx context.Context, keyID int64, date time.Time) (int64, error) {
	const q = `
		SELECT COALESCE(tokens_used, 0)
		FROM daily_token_usage
		WHERE api_key_id = $1 AND usage_date = $2
	`
	day := date.UTC().Truncate(24 * time.Hour)
	var total int64
	err := s.pool.QueryRow(ctx, q, keyID, day).Scan(&total)
	if err != nil {
		if isNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("store: GetDailyTokenUsage: %w", err)
	}
	return total, nil
}

// isNotFound returns true when err is a pgx "no rows" sentinel.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "no rows in result set"
}
