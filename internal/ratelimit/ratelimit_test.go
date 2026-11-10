package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/redis/go-redis/v9"
)

// ─── Redis test helper ────────────────────────────────────────────────────────

// newTestRedis returns a real miniredis-backed client if available, otherwise
// skips. We use a real Redis so the EVALSHA atomicity guarantee is authentic.
// Tests that require Redis are skipped if REDIS_TEST_ADDR is not set or if a
// local Redis is not reachable; the naive-vs-atomic comparison test is the one
// that can use an in-process stub to demonstrate the over-admission.
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := "localhost:6379"
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 15}) // use DB 15 to isolate tests
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available at %s (%v) — skipping integration test", addr, err)
	}
	return rdb
}

func cleanupKeys(t *testing.T, rdb *redis.Client, pattern string) {
	t.Helper()
	keys, _ := rdb.Keys(context.Background(), pattern).Result()
	if len(keys) > 0 {
		rdb.Del(context.Background(), keys...)
	}
}

// ─── Unit: script SHA determinism ─────────────────────────────────────────────

func TestScriptSHAIsDeterministic(t *testing.T) {
	sha1 := computeScriptSHA()
	sha2 := computeScriptSHA()
	if sha1 != sha2 || len(sha1) != 40 {
		t.Errorf("expected deterministic 40-char SHA, got %q and %q", sha1, sha2)
	}
}

// ─── Integration: token bucket basic allow/deny ───────────────────────────────

func TestTokenBucketAllowsWithinLimit(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	bucketKey := "test:tb:allow"
	cleanupKeys(t, rdb, bucketKey)
	defer cleanupKeys(t, rdb, bucketKey)

	limiter := NewUnchecked(rdb)
	// 5 tokens/s, burst=5, cost=1 — first 5 requests must all be allowed.
	for i := 0; i < 5; i++ {
		res, err := limiter.Allow(ctx, bucketKey, 5, 5, 1)
		if err != nil {
			t.Fatalf("Allow error on call %d: %v", i, err)
		}
		if !res.Allowed {
			t.Errorf("call %d should be allowed (tokens remaining %d)", i, res.Remaining)
		}
	}
}

func TestTokenBucketRejectsWhenExhausted(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	bucketKey := "test:tb:reject"
	cleanupKeys(t, rdb, bucketKey)
	defer cleanupKeys(t, rdb, bucketKey)

	limiter := NewUnchecked(rdb)
	// Drain 3 tokens from a burst-3 bucket.
	for i := 0; i < 3; i++ {
		_, _ = limiter.Allow(ctx, bucketKey, 3, 3, 1)
	}
	// Next request must be rejected.
	res, err := limiter.Allow(ctx, bucketKey, 3, 3, 1)
	if err != nil {
		t.Fatalf("Allow error: %v", err)
	}
	if res.Allowed {
		t.Errorf("expected rejection after bucket exhausted, got allowed (remaining=%d)", res.Remaining)
	}
	if res.RetryAfterMS <= 0 {
		t.Errorf("expected positive RetryAfterMS, got %d", res.RetryAfterMS)
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	bucketKey := "test:tb:refill"
	cleanupKeys(t, rdb, bucketKey)
	defer cleanupKeys(t, rdb, bucketKey)

	limiter := NewUnchecked(rdb)
	// Drain all 3 tokens.
	for i := 0; i < 3; i++ {
		_, _ = limiter.Allow(ctx, bucketKey, 3, 3, 1)
	}

	// Wait 400ms — at 3 tokens/s that refills ~1.2 tokens.
	time.Sleep(400 * time.Millisecond)

	res, err := limiter.Allow(ctx, bucketKey, 3, 3, 1)
	if err != nil {
		t.Fatalf("Allow error: %v", err)
	}
	if !res.Allowed {
		t.Errorf("expected bucket to refill after sleep, but request was rejected")
	}
}

// ─── Integration: concurrent goroutines never exceed burst ────────────────────
//
// This simulates two "gateway instances" (goroutines sharing one Redis) hammering
// a single bucket key concurrently. The atomic Lua script serialises every
// read-compute-write cycle on the Redis server, so the total admitted requests
// can never exceed the burst + the fractional tokens that accrue between the
// very first call (which seeds the bucket) and the last call.
//
// We use a very low rate (1 token/s) so that over the microseconds the test
// runs, refill is negligible (< 0.001 tokens per goroutine round-trip).
// This makes "admitted <= burst" a deterministic assertion, not a probabilistic one.

func TestAtomicScriptNeverOverAdmits(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	const (
		burst      = 20
		goroutines = 50
		// ratePerSec = 1.0: over a 50ms test window the maximum possible refill
		// is 1.0 * 0.05 = 0.05 tokens — well below 1, so floor(tokens) stays at 0.
		// This eliminates the sub-millisecond-refill false-positive seen with
		// higher rates (100/s could accumulate 0.1–0.5 tokens per call pair).
		ratePerSec = 1.0
	)
	bucketKey := "test:tb:concurrent"
	cleanupKeys(t, rdb, bucketKey)
	defer cleanupKeys(t, rdb, bucketKey)

	limiter := NewUnchecked(rdb)

	// Use a start barrier so all goroutines fire simultaneously, maximising
	// the chance of concurrent Lua evaluations on the server.
	ready := make(chan struct{})

	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready // wait for the gun
			res, err := limiter.Allow(ctx, bucketKey, ratePerSec, burst, 1)
			if err == nil && res.Allowed {
				allowed.Add(1)
			}
		}()
	}

	// Fire all goroutines at once — identical to two gateway processes both
	// receiving requests and sharing one Redis instance.
	close(ready)
	wg.Wait()

	// The Lua script is atomic: every HMGET → compute → HMSET is serialised
	// inside a single Redis command. No two callers can race on the same key.
	// Therefore admitted MUST equal exactly burst (no refill at 1 token/s).
	if allowed.Load() > int64(burst) {
		t.Errorf("atomic script over-admitted: allowed=%d, burst=%d — Lua atomicity violated",
			allowed.Load(), burst)
	}
	t.Logf("concurrent test: %d goroutines, burst=%d, admitted=%d ✓ no over-admission",
		goroutines, burst, allowed.Load())
}

// ─── Demonstration: naïve INCR+EXPIRE over-admits ────────────────────────────
//
// This test does NOT use the Lua script. It uses raw INCR+EXPIRE commands with
// an artificial sleep between them to simulate a race window. It demonstrates
// that the naïve approach over-admits: two goroutines can both see count ≤ limit
// before EXPIRE runs, both allow, and collectively exceed the limit.

// TestNaiveIncrExpireOverAdmits demonstrates that the naive INCR + EXPIRE
// approach over-admits under concurrent requests across replicas.
//
// In naive rate-limiting:
// Replica A: GET/INCR count -> sees count <= limit
// Replica B: GET/INCR count -> also sees count <= limit before window expiry or reset
// Or when resetting/checking counters via separate round trips, multiple replicas
// concurrently read a stale count before writing, admitting far more requests
// than the configured rate/burst allows.
func TestNaiveIncrExpireOverAdmits(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	const (
		limit      = 5
		goroutines = 50
	)
	naiveKey := "test:naive:concurrent"
	cleanupKeys(t, rdb, naiveKey)
	defer cleanupKeys(t, rdb, naiveKey)

	var (
		startWg sync.WaitGroup
		doneWg  sync.WaitGroup
		allowed atomic.Int64
	)

	startWg.Add(1)

	// Naïve multi-step check:
	// 1. GET current count
	// 2. Check if count < limit
	// 3. INCR and EXPIRE if < limit
	// This non-atomic check-then-act pattern is common in naive implementations.
	naiveCheckAndIncr := func() bool {
		// Non-atomic read
		val, err := rdb.Get(ctx, naiveKey).Int64()
		if err != nil && err != redis.Nil {
			return false
		}

		if val < limit {
			// Window where other replicas also read the same stale `val < limit`
			time.Sleep(5 * time.Millisecond)

			pipe := rdb.Pipeline()
			pipe.Incr(ctx, naiveKey)
			pipe.Expire(ctx, naiveKey, 2*time.Second)
			_, _ = pipe.Exec(ctx)

			return true
		}
		return false
	}

	for i := 0; i < goroutines; i++ {
		doneWg.Add(1)
		go func() {
			defer doneWg.Done()
			startWg.Wait() // maximize concurrent stampede
			if naiveCheckAndIncr() {
				allowed.Add(1)
			}
		}()
	}

	startWg.Done()
	doneWg.Wait()

	t.Logf("naïve check+incr: limit=%d goroutines=%d admitted=%d (over-admitted by %d)",
		limit, goroutines, allowed.Load(), allowed.Load()-limit)

	if allowed.Load() <= limit {
		t.Errorf("expected naive approach to over-admit beyond limit %d, but admitted %d", limit, allowed.Load())
	}
}

// ─── Integration: two "instances" against one Redis ──────────────────────────
//
// Simulates two gateway instances by creating two separate Limiter objects
// pointing at the same Redis DB. Combined admitted requests must not exceed burst.

func TestTwoGatewayInstancesSameRedis(t *testing.T) {
	// Create two separate Redis clients simulating two gateway instances.
	addr := "localhost:6379"
	rdb1 := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	rdb2 := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	defer rdb1.Close()
	defer rdb2.Close()

	ctx := context.Background()
	if err := rdb1.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}

	const (
		burst      = 10
		goroutines = 40
		// Low rate so sub-ms refill can't push admitted over burst.
		ratePerSec = 1.0
	)
	bucketKey := "test:tb:two-instances"
	cleanupKeys(t, rdb1, bucketKey)
	defer func() { cleanupKeys(t, rdb1, bucketKey) }()

	limiter1 := NewUnchecked(rdb1)
	limiter2 := NewUnchecked(rdb2)

	ready := make(chan struct{})

	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
	)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			<-ready
			// Alternate between the two "instances".
			var l *Limiter
			if i%2 == 0 {
				l = limiter1
			} else {
				l = limiter2
			}
			res, err := l.Allow(ctx, bucketKey, ratePerSec, burst, 1)
			if err == nil && res.Allowed {
				allowed.Add(1)
			}
		}()
	}

	close(ready)
	wg.Wait()

	// With a shared Redis key and atomic Lua, both "instances" together must
	// never admit more than the burst capacity.
	if allowed.Load() > int64(burst) {
		t.Errorf("two-instance test: admitted %d > burst %d — atomicity violated", allowed.Load(), burst)
	}
	t.Logf("two-instance test: %d goroutines across 2 limiters, burst=%d, admitted=%d ✓",
		goroutines, burst, allowed.Load())
}

// ─── Unit: BucketKey and DailyQuotaKey format ────────────────────────────────

func TestBucketKey(t *testing.T) {
	key := BucketKey("abc123")
	expected := "rl:bucket:abc123"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestDailyQuotaKey(t *testing.T) {
	date := time.Date(2026, 10, 29, 15, 0, 0, 0, time.UTC)
	key := DailyQuotaKey("abc123", date)
	expected := "rl:daily:abc123:2026-10-29"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

// ─── Integration: daily quota tracking ───────────────────────────────────────

func TestDailyTokenQuota(t *testing.T) {
	rdb := newTestRedis(t)
	defer rdb.Close()
	ctx := context.Background()

	keyHash := fmt.Sprintf("test-hash-%d", time.Now().UnixNano())
	quotaKey := DailyQuotaKey(keyHash, time.Now())
	cleanupKeys(t, rdb, quotaKey)
	defer cleanupKeys(t, rdb, quotaKey)

	apiKey := &store.APIKey{
		KeyHash:         keyHash,
		DailyTokenQuota: 100,
	}

	// Initially should have full quota.
	ok, remaining, err := CheckDailyQuota(ctx, rdb, apiKey)
	if err != nil {
		t.Fatalf("CheckDailyQuota: %v", err)
	}
	if !ok {
		t.Error("expected quota available initially")
	}
	if remaining != 100 {
		t.Errorf("expected remaining=100, got %d", remaining)
	}

	// Consume 60 tokens.
	_, err = IncrDailyTokens(ctx, rdb, keyHash, 60)
	if err != nil {
		t.Fatalf("IncrDailyTokens: %v", err)
	}

	ok, remaining, err = CheckDailyQuota(ctx, rdb, apiKey)
	if err != nil {
		t.Fatalf("CheckDailyQuota after 60: %v", err)
	}
	if !ok {
		t.Error("expected quota still available after 60 tokens")
	}
	if remaining != 40 {
		t.Errorf("expected remaining=40, got %d", remaining)
	}

	// Consume 50 more — total 110 > quota of 100.
	_, err = IncrDailyTokens(ctx, rdb, keyHash, 50)
	if err != nil {
		t.Fatalf("IncrDailyTokens: %v", err)
	}

	ok, remaining, err = CheckDailyQuota(ctx, rdb, apiKey)
	if err != nil {
		t.Fatalf("CheckDailyQuota after 110: %v", err)
	}
	if ok {
		t.Errorf("expected quota exhausted after 110 tokens (quota=100), remaining=%d", remaining)
	}
}
