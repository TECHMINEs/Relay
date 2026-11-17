// Package ratelimit implements a Redis-backed token bucket rate limiter using
// an atomic Lua script evaluated via EVALSHA (with EVAL fallback).
//
// Design rationale for the Lua approach
// ──────────────────────────────────────
// The naïve two-command approach (INCR then EXPIRE) has a critical race:
//
//  1. Between INCR and EXPIRE there is a window where the key exists but has
//     no TTL.  If the process crashes after INCR, the counter leaks forever.
//  2. Two concurrent instances can both execute INCR before either executes
//     EXPIRE; both observe a count of N-1 before the limit and both admit
//     the request, over-admitting by N × (number of racy pairs).
//
// The Lua script runs as a single indivisible Redis command.  No other client
// command can interleave with it, so the read-compute-write cycle is atomic.
// EVALSHA sends only the 40-byte SHA-1 of the compiled script after the first
// load, reducing network overhead.  On NOSCRIPT errors (e.g. Redis restart or
// cluster node failover) the middleware falls back to EVAL and re-caches the SHA.
package ratelimit

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TECHMINEs/Relay/internal/auth"
	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/redis/go-redis/v9"
)

//go:embed token_bucket.lua
var tokenBucketLua string

// ttlSeconds is how long an idle bucket key lives in Redis before expiry.
const ttlSeconds = 86400 // 24 h

// Result holds the response from one Lua script evaluation.
type Result struct {
	Allowed      bool
	Remaining    int64 // floor of tokens left after this call
	RetryAfterMS int64 // ms until next token available (0 when Allowed)
	ResetMS      int64 // Unix ms when bucket reaches full burst capacity
}

// Limiter is a Redis-backed token bucket rate limiter.
type Limiter struct {
	rdb       redis.UniversalClient
	scriptSHA string
	mu        sync.RWMutex // protects scriptSHA
}

// New creates a Limiter connected to the provided Redis client.
// It pre-loads the Lua script so the first real request can use EVALSHA.
func New(ctx context.Context, rdb redis.UniversalClient) (*Limiter, error) {
	sha, err := loadScript(ctx, rdb)
	if err != nil {
		return nil, fmt.Errorf("ratelimit: failed to pre-load Lua script: %w", err)
	}
	return &Limiter{rdb: rdb, scriptSHA: sha}, nil
}

// NewUnchecked creates a Limiter without pre-loading the script (useful in
// tests where the script is loaded lazily on the first call).
func NewUnchecked(rdb redis.UniversalClient) *Limiter {
	return &Limiter{rdb: rdb}
}

// Allow evaluates the token bucket for the given key with the configured rate,
// burst, and cost.  Returns a Result describing whether the request is allowed.
func (l *Limiter) Allow(ctx context.Context, bucketKey string, ratePerSec float64, burst, cost int) (Result, error) {
	nowMS := time.Now().UnixMilli()
	args := []any{ratePerSec, burst, cost, nowMS, ttlSeconds}
	keys := []string{bucketKey}

	vals, err := l.evalWithFallback(ctx, keys, args)
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: script eval: %w", err)
	}

	return parseResult(vals)
}

// evalWithFallback tries EVALSHA first; on NOSCRIPT it reloads and retries.
func (l *Limiter) evalWithFallback(ctx context.Context, keys []string, args []any) ([]any, error) {
	sha := l.getSHA()

	if sha != "" {
		vals, err := evalSHA(ctx, l.rdb, sha, keys, args)
		if err == nil {
			return vals, nil
		}
		if !isNoscript(err) {
			return nil, err
		}
		// Script was flushed (e.g. Redis restart). Fall through to EVAL.
	}

	newSHA, err := loadScript(ctx, l.rdb)
	if err != nil {
		return nil, err
	}
	l.setSHA(newSHA)

	return evalSHA(ctx, l.rdb, newSHA, keys, args)
}

func (l *Limiter) getSHA() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.scriptSHA
}

func (l *Limiter) setSHA(sha string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scriptSHA = sha
}

// loadScript uploads the Lua source via SCRIPT LOAD and returns the SHA-1.
func loadScript(ctx context.Context, rdb redis.UniversalClient) (string, error) {
	sha, err := rdb.ScriptLoad(ctx, tokenBucketLua).Result()
	if err != nil {
		return "", fmt.Errorf("SCRIPT LOAD: %w", err)
	}
	return sha, nil
}

// evalSHA calls EVALSHA and returns the raw slice result.
func evalSHA(ctx context.Context, rdb redis.UniversalClient, sha string, keys []string, args []any) ([]any, error) {
	raw, err := rdb.EvalSha(ctx, sha, keys, args...).Result()
	if err != nil {
		return nil, err
	}
	vals, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("unexpected script return type %T", raw)
	}
	return vals, nil
}

// parseResult converts the 4-element Lua return into a Result.
func parseResult(vals []any) (Result, error) {
	if len(vals) != 4 {
		return Result{}, fmt.Errorf("expected 4 values from Lua, got %d", len(vals))
	}
	allowed, ok1 := vals[0].(int64)
	remaining, ok2 := vals[1].(int64)
	retryAfterMS, ok3 := vals[2].(int64)
	resetMS, ok4 := vals[3].(int64)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return Result{}, fmt.Errorf("unexpected Lua result types: %T %T %T %T", vals[0], vals[1], vals[2], vals[3])
	}
	return Result{
		Allowed:      allowed == 1,
		Remaining:    remaining,
		RetryAfterMS: retryAfterMS,
		ResetMS:      resetMS,
	}, nil
}

// isNoscript returns true when Redis returned a NOSCRIPT error.
func isNoscript(err error) bool {
	if err == nil {
		return false
	}
	return strings.HasPrefix(err.Error(), "NOSCRIPT")
}

// BucketKey returns the Redis key for a given API key hash.
func BucketKey(keyHash string) string {
	return "rl:bucket:" + keyHash
}

// DailyQuotaKey returns the Redis key for the daily token counter.
func DailyQuotaKey(keyHash string, date time.Time) string {
	return "rl:daily:" + keyHash + ":" + date.UTC().Format("2006-01-02")
}

// computeScriptSHA returns the expected SHA-1 of the Lua source without
// calling Redis — used for deterministic test assertions.
func computeScriptSHA() string {
	h := sha1.Sum([]byte(tokenBucketLua))
	return hex.EncodeToString(h[:])
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// KeyFunc extracts the rate-limit key and parameters from a request.
// Returning an empty key disables rate limiting for that request.
type KeyFunc func(r *http.Request) (bucketKey string, ratePerSec float64, burst int)

// DefaultKeyFunc extracts the rate-limit key and parameters from a request.
// It inspects the *store.APIKey populated by the auth middleware in the request context.
// If no APIKey is present in context, it falls back to extracting the bearer token
// from the Authorization header using auth.DefaultKeyFunc, hashing it, and applying
// default rate (10 req/s) and burst (20) limits.
func DefaultKeyFunc(r *http.Request) (string, float64, int) {
	key := store.APIKeyFromContext(r.Context())
	if key != nil && key.KeyHash != "" {
		return BucketKey(key.KeyHash), key.RequestsPerSec, key.Burst
	}
	rawKey := auth.DefaultKeyFunc(r)
	if rawKey != "" {
		hash := auth.HashKey(rawKey)
		return BucketKey(hash), 10, 20
	}
	return "", 0, 0
}

// Middleware returns an HTTP middleware that enforces the token bucket for
// each request, using the key and parameters returned by keyFn.
//
// Rate-limit headers set on every response:
//   - X-RateLimit-Limit      : burst capacity
//   - X-RateLimit-Remaining  : tokens remaining after this request
//   - X-RateLimit-Reset      : Unix timestamp (seconds) when bucket reaches burst
//
// On rejection:
//   - HTTP 429 Too Many Requests
//   - Retry-After header (seconds)
func Middleware(limiter *Limiter, keyFn func(*http.Request) (string, float64, int)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bucketKey, ratePerSec, burst := keyFn(r)
			if bucketKey == "" || ratePerSec <= 0 || burst <= 0 {
				// No rate-limiting configured for this request.
				next.ServeHTTP(w, r)
				return
			}

			res, err := limiter.Allow(r.Context(), bucketKey, ratePerSec, burst, 1)
			if err != nil {
				// On Redis failure, fail open to avoid cascading outage.
				next.ServeHTTP(w, r)
				return
			}

			resetSec := res.ResetMS / 1000

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(burst))
			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(res.Remaining, 10))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetSec, 10))

			if !res.Allowed {
				retryAfterSec := (res.RetryAfterMS + 999) / 1000 // ceil
				if retryAfterSec < 1 {
					retryAfterSec = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSec, 10))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error","retry_after_ms":%d}}`,
					res.RetryAfterMS)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ─── Daily quota ─────────────────────────────────────────────────────────────

// IncrDailyTokens atomically increments the Redis daily token counter for
// a key and returns the new total.
func IncrDailyTokens(ctx context.Context, rdb redis.UniversalClient, keyHash string, tokens int64) (int64, error) {
	redisKey := DailyQuotaKey(keyHash, time.Now())
	pipe := rdb.Pipeline()
	incrCmd := pipe.IncrBy(ctx, redisKey, tokens)
	// TTL: 25 hours ensures the key persists past midnight for reconciliation.
	pipe.Expire(ctx, redisKey, 25*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("ratelimit: IncrDailyTokens: %w", err)
	}
	return incrCmd.Val(), nil
}

// CheckDailyQuota returns true if the key still has quota remaining.
// quota == 0 means unlimited.
func CheckDailyQuota(ctx context.Context, rdb redis.UniversalClient, key *store.APIKey) (bool, int64, error) {
	if key.DailyTokenQuota == 0 {
		return true, 0, nil
	}
	redisKey := DailyQuotaKey(key.KeyHash, time.Now())
	used, err := rdb.Get(ctx, redisKey).Int64()
	if errors.Is(err, redis.Nil) {
		return true, key.DailyTokenQuota, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: CheckDailyQuota: %w", err)
	}
	remaining := key.DailyTokenQuota - used
	return remaining > 0, max(0, remaining), nil
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
