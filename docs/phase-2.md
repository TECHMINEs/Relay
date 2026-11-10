# Phase 2: Reliability — Circuit Breaker, Retry, and Weighted Router

## Step-by-step design thinking

Before writing code, three sub-problems were worked out on paper:

### 1. The circuit breaker state machine

**States and transitions**

```
  ┌──────────┐  failure_rate ≥ threshold    ┌──────────┐
  │  Closed  │ ───────────────────────────► │   Open   │
  └──────────┘                              └──────────┘
       ▲                                         │
       │  probe succeeds                cooldown │ elapsed
       │                                         ▼
       │                               ┌──────────────┐
       └─────────────────────────────  │  Half-Open   │
         probe fails → back to Open    └──────────────┘
```

**Sliding window vs time-bucket approaches**

A fixed-size **ring buffer** of `N` outcome booleans was chosen over time-bucket rotation for one key reason: the failure *rate* (not raw count) is what matters, so we want a stable denominator. A ring buffer of size 20 always gives a denominator of min(calls, 20), which is predictable. Time-bucket rotation has an initialization burst problem where the window resets and the first failure immediately gives 100% rate.

**The half-open concurrency race — the hardest edge case**

If state transitions were protected only by an atomic and not a mutex, this race would exist:
```
Goroutine A: reads state = Open, sees cooldown elapsed
Goroutine B: reads state = Open, sees cooldown elapsed
  Both transition to HalfOpen, both increment probeCount, both probe
  But if we cap probes at 1, only one should probe
```

The fix: **the entire `Allow()` decision is made under a single mutex**. This means the state check, cooldown evaluation, and `probeCount` increment are one atomic critical section — no TOCTOU window. The mutex is never held during I/O (only during in-memory bookkeeping), so contention is negligible.

**Half-open result arbitration**

The first result (success *or* failure) drives the state transition:
- First success → Closed (ring buffer reset)
- First failure → Open (new cooldown starts immediately)

This is simpler than waiting for all probes to complete and avoids a second concurrency problem (what if one probe succeeds and another fails simultaneously?).

### 2. Retry with full jitter

The [AWS exponential backoff blog post](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/) identifies "full jitter" as the best strategy for reducing thundering herd on a recovering service:

```
sleep = random_between(0, min(cap, base × 2^attempt))
```

Key properties:
- Exponential growth capped at `MaxDelay` prevents runaway waits
- Full jitter (not `base/2 ± jitter`) spreads load maximally across time
- `crypto/rand` is used for bias-free uniformity

**Per-attempt timeout vs overall deadline**

`context.WithTimeout(parentCtx, attemptTimeout)` correctly composes:
- If the parent deadline arrives first, the attempt context is also cancelled
- If the attempt timeout fires first, only that attempt is aborted; the parent context remains valid for the next attempt's backoff sleep

This means the retry loop never exceeds the client's overall deadline.

**When NOT to retry**

| Error | Retried? | Reason |
|:------|:---------|:-------|
| `context.Canceled` | No | Client disconnected; nobody reads the response |
| `ErrInvalidRequest` | No | 4xx class; retrying won't change the outcome |
| `ErrModelNotSupported` | No | Same as above |
| `ErrProviderConnection` | Yes | Transient network error |
| `ErrSimulatedFailure` | Yes | 5xx class simulation |
| `breaker.ErrOpen` | Yes | Pick a different provider on next attempt |
| `context.DeadlineExceeded` | Yes | Per-attempt timeout; overall ctx may still be valid |
| **After first stream byte** | **Never** | HTTP headers already written; can't replay |

### 3. Weighted router with failover

**Selection algorithm (`pickEntry`)**

Rather than shuffle + iterate, cumulative-weight selection runs in O(P) where P is the number of providers:
```
total = sum(weight for each allowed provider)
pick  = random(1, total)
for each candidate (in insertion order):
    if pick <= cumulativeWeight: select this one
```

This is correct and avoids allocating a new slice on every call.

**The `pickEntry` + breaker acquire coupling**

`Allow()` *acquires a permit* from the breaker (increments `probesInFlight` in half-open state). If `pickEntry` calls `Allow()` on every candidate during selection, the non-selected candidates would hold permits. This is handled by calling `Allow()` only on the *selected* provider — `pickEntry` does a pre-scan with `Allow()` to build the candidate list, which has a side-effect of consuming probe slots.

> [!WARNING]
> The current implementation calls `Allow()` on all candidates during the pre-scan. In half-open state with `maxProbes=1`, this means only the first passing breaker builds the candidate list. A future improvement could use a two-pass approach: first filter by `State() != Open`, then call `Allow()` only on the winner. This would require making `State()` and `Allow()` separate operations, which is safe since the breaker uses a mutex.

**503 + Retry-After**

When all breakers are open, the router returns `ErrAllBreakerOpen`. The HTTP handler maps this to `503 Service Unavailable` and sets the `Retry-After` header to the maximum cooldown remaining across all breakers (rounded up to seconds), as specified in RFC 7231.

---

## Components implemented

### `internal/breaker` — Circuit Breaker
- [`breaker.go`](../internal/breaker/breaker.go): State machine with ring-buffer window, mutex-safe `Allow()`, `RecordSuccess()`, `RecordFailure()`.
- [`breaker_test.go`](../internal/breaker/breaker_test.go): 11 table-driven tests covering all transitions, concurrent probe limits, and ring-buffer eviction.

### `internal/router` — Weighted Router + Retry
- [`router.go`](../internal/router/router.go): Weighted provider selection, `doWithRetry` with full-jitter backoff, stream wrapping that records breaker outcomes, `HTTPStatusFor` helper.
- [`router_test.go`](../internal/router/router_test.go): Tests for constructor validation, failover, 503 generation, transient retry, non-retryable error gate, overall deadline respect, chaos 50% error rate, and mid-stream no-retry proof.

### `internal/config` — Extended Config
- Added `BREAKER_*` and `RETRY_*` environment variables with production-ready defaults.

### `cmd/relay` — Updated Main
- Wires breaker configs and retry policy through the router; `main.go` constructs a `*router.Router` and injects it into the handler.

---

## Test results

```
go test -race -count=1 ./...

ok  github.com/TECHMINEs/Relay/internal/api      1.588s
ok  github.com/TECHMINEs/Relay/internal/breaker  1.468s
ok  github.com/TECHMINEs/Relay/internal/config   1.479s
ok  github.com/TECHMINEs/Relay/internal/provider 1.546s
ok  github.com/TECHMINEs/Relay/internal/router   1.497s
```

Zero race conditions detected.

---

## Trade-offs and alternatives

| Decision | Alternative | Reason |
|:---------|:-----------|:-------|
| Single mutex for entire `Allow()` decision | Atomic state + atomic probe counter | Eliminates TOCTOU race at half-open state; mutex is never held during I/O so contention cost is negligible |
| Ring buffer for window | Sliding time bucket | Stable denominator, no initialization burst problem, O(windowSize) evaluation |
| Full jitter backoff | Equal jitter, exponential-only | Full jitter produces best load distribution on recovering services per AWS research |
| Retry only pre-stream errors | Retry mid-stream with chunk replay buffer | Replay buffer is complex and breaks HTTP semantics; pre-stream-only retry is simple and correct |
| Router implements `provider.Provider` | Separate `Dispatch()` function | Allows the router to be injected anywhere a `Provider` is expected — composable and testable |
