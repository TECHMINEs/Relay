# Phase 3: Auth, Rate Limiting, and Quotas

## Components implemented

| Package | Files | Responsibility |
|:--------|:------|:--------------|
| `internal/auth` | `auth.go` | Bearer token extraction, SHA-256 key hashing, lookup middleware |
| `internal/store` | `store.go` | Postgres pool, `api_keys` lookup, batch request-log insert, daily token upsert |
| `internal/ratelimit` | `ratelimit.go`, `token_bucket.lua` | Redis Lua token bucket, EVALSHA + fallback, daily quota helpers, HTTP middleware |
| `internal/metrics` | `logger.go` | Async buffered channel logger, batch Postgres insert, drop-and-count on overflow |
| `migrations/` | `001_*.sql`, `002_*.sql` | Schema for `api_keys`, `request_log`, `daily_token_usage` |

---

## The INCR+EXPIRE race condition — step by step

### The naïve approach

A common first attempt at Redis rate limiting uses two separate commands:

```
count = INCR  key          → returns new integer value
        EXPIRE key  1      → sets 1-second TTL
allow = (count <= limit)
```

This looks safe because `INCR` itself is atomic on Redis. The problem is that
**the three steps are not one transaction** — any number of other clients can
execute commands between them.

### Timeline of the race

```
Time →
──────────────────────────────────────────────────────────────────────
Instance A │  GET key → 0        │  INCR → 1  │  check 1 ≤ 5 → ADMIT
Instance B │  GET key → 0        │  INCR → 2  │  check 2 ≤ 5 → ADMIT
Instance C │  GET key → 0        │  INCR → 3  │  check 3 ≤ 5 → ADMIT
 ⋮                   ← all see stale "0" before any INCR is visible →
Instance E │  GET key → 0        │  INCR → 5  │  check 5 ≤ 5 → ADMIT
                                                  ↑
                             limit=5 but 50 goroutines were all admitted
```

In the `TestNaiveIncrExpireOverAdmits` test, 50 goroutines all **read the same
stale `0`** before any of them has written their INCR result back. All 50 pass
the `val < limit` check and all 50 are admitted — 10× over the limit.

A second, subtler hazard is TTL drift: if Instance A runs `EXPIRE` after
Instance B already ran `EXPIRE`, A resets the window's expiry forward in time.
A window that should last 1 second can silently last up to 2 seconds, doubling
effective admission.

### Why EVALSHA fixes it

Redis guarantees that **a Lua script runs atomically**: no other command from
any client executes between the first and last Redis call inside the script. The
Redis event loop processes one script at a time.

The token-bucket script performs this entire sequence as **one indivisible
unit**:

```
HMGET  key  tokens  last_ms    ← read current bucket state
compute refill (elapsed time × rate)
cap tokens at burst
if tokens >= cost → deduct, allow=1
else              → compute retry_after, allow=0
HMSET  key  tokens  last_ms    ← write updated state atomically
EXPIRE key  ttl_s              ← refresh TTL, also inside the script
return {allowed, remaining, retry_after_ms, reset_ms}
```

Because the HMGET and HMSET are fused in one script call, a second instance
that arrives while the first is running will block at the Redis command level
until the first script has finished. There is no window for a stale read.

### EVALSHA vs EVAL

`EVAL` sends the full Lua source on every call. `EVALSHA` sends only the 40-byte
SHA-1 of the loaded script, reducing per-request network overhead:

```
First call:    ScriptLoad(lua) → sha
Every call:    EVALSHA sha keys args  (fast path)
On NOSCRIPT:   ScriptLoad(lua) → new sha → EVALSHA (e.g. after Redis restart)
```

The SHA is cached in a `sync.RWMutex`-protected field on `Limiter` so it is
safe across concurrent goroutines and updated atomically on cache miss.

---

## Token bucket math

### State stored per API key

```
HMSET rl:bucket:<hash>
    tokens   <float>    ← current token count
    last_ms  <int>      ← Unix time in ms when tokens were last computed
```

### Refill formula (executed inside Lua, on every request)

```
elapsed_ms = max(0, now_ms − last_ms)          # wall clock since last update
refill     = elapsed_ms / 1000 × rate          # tokens earned
tokens     = min(burst, tokens + refill)        # cap at bucket capacity
```

Using `max(0, …)` prevents negative refill if clocks skew across Redis cluster
nodes (extremely rare but safe to guard).

### Decision and deduction

```
if tokens >= cost:
    tokens  -= cost      # atomic deduction
    allowed  = 1
    headers  = X-RateLimit-Remaining, X-RateLimit-Reset
else:
    retry_after_ms = ceil((cost − tokens) / rate × 1000)
    HTTP 429, Retry-After = ceil(retry_after_ms / 1000)
```

---

## Why the concurrent test uses `ratePerSec = 1.0`

The original test used `ratePerSec = 100.0`. At 100 tokens/s, even a **1 ms**
gap between the bucket-seeding call and a later call adds 0.1 tokens. Over 50
rapid goroutines, these sub-millisecond refills can push total admitted requests
to 21–24 instead of exactly 20 — not because the Lua atomicity was violated,
but because **real time elapsed**.

The fix: use `ratePerSec = 1.0`. At 1 token/s, the maximum possible refill
across a 50 ms test run is `1.0 × 0.05 = 0.05 tokens` — far below 1. The
`floor()` in the script's return value rounds this to 0, so the assertion
`admitted <= burst` is now **deterministic** rather than probabilistic.

The start barrier (`close(ready)`) additionally fires all goroutines at the same
instant to maximise concurrent Lua serialisation pressure.

---

## Auth middleware

API keys are **never stored in plaintext**. The `auth` package computes a
`SHA-256(raw_key)` hex digest at registration and on every request, then
performs a single indexed `SELECT` on `api_keys.key_hash`. Inactive keys
return HTTP 403; unknown keys return HTTP 401.

Rate-limit parameters (`requests_per_sec`, `burst`) are loaded from the same
row, so each key can have a different tier.

---

## Async request logger

The `AsyncLogger` decouples request processing from Postgres I/O:

```
Request goroutine  →  Log(entry)  →  chan (buffered, size=bufSize)
                                          ↓   (non-blocking select)
                                     background drainer goroutine
                                          ↓
                                     batch INSERT (pgx CopyFrom)
```

When the buffer is full, `Log` takes the `default` branch of a `select` — the
entry is **dropped** and `Global.DroppedLogEntries` is incremented. This
prevents a slow database from back-pressuring the hot request path. The metric
is visible for alerting.

Batches are flushed when they reach `batchSize` **or** when the `flushInterval`
ticker fires, whichever comes first. On `Stop()`, remaining entries are drained
with a 5-second deadline before the goroutine exits.

---

## Daily token quota reconciliation

```
Redis key: rl:daily:<key_hash>:<YYYY-MM-DD>
TTL: 25 hours (persists past midnight for reconciliation)

IncrDailyTokens  → INCRBY + EXPIRE in a pipeline (two round trips, not one)
CheckDailyQuota  → GET + compare against api_keys.daily_token_quota
```

The Redis counter is the live source of truth. Postgres `daily_token_usage` is
reconciled asynchronously (via the async logger) for durability and monthly
reporting. If Redis is cold (restart) and the counter is lost, the next call
re-creates it at 0 — conservatively giving the key a fresh window for that day.

---

## Test results

```
go test -race -count=1 ./...

ok  github.com/TECHMINEs/Relay/internal/api        1.22s
ok  github.com/TECHMINEs/Relay/internal/auth       1.14s
ok  github.com/TECHMINEs/Relay/internal/breaker    1.34s
ok  github.com/TECHMINEs/Relay/internal/config     1.12s
ok  github.com/TECHMINEs/Relay/internal/metrics    1.59s
ok  github.com/TECHMINEs/Relay/internal/provider   1.18s
ok  github.com/TECHMINEs/Relay/internal/ratelimit  1.63s
ok  github.com/TECHMINEs/Relay/internal/router     1.17s
```

Zero failures, zero race conditions across all 8 packages.

### Key ratelimit test results (5-run stress test)

| Test | Result |
|:-----|:-------|
| `TestAtomicScriptNeverOverAdmits` | 50 goroutines → admitted exactly **20** (= burst) every run |
| `TestNaiveIncrExpireOverAdmits` | 50 goroutines → admitted **50** every run (10× over limit of 5) |
| `TestTwoGatewayInstancesSameRedis` | 40 goroutines across 2 limiters → admitted exactly **10** every run |

---

## Trade-offs

| Decision | Alternative | Reason |
|:---------|:-----------|:-------|
| Token bucket via Lua over fixed-window INCR | Sliding log, leaky bucket | Token bucket gives smooth burst tolerance without per-request log append; Lua keeps it atomic without a distributed lock |
| Float tokens in Redis hash | Integer tokens × 1000 | Simpler Lua; float precision is sufficient since `math.floor` is applied before returning |
| `ratePerSec = 1.0` in concurrent test | High rate with `burst+N` bound | Low rate makes the refill contribution provably negligible, turning a probabilistic assertion into a deterministic one |
| Drop-and-count on logger overflow | Back-pressure / block | Prevents database latency from affecting the hot request path; the metric makes drops observable |
| 25-hour TTL on daily quota key | Exact midnight TTL | Avoids a timezone-sensitive TTL calculation; 25h guarantees the key exists for same-day reconciliation across time zones |
