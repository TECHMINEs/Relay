# Phase 4: Observability / Prometheus Metrics

## What We Built

Phase 4 wires the gateway into the Prometheus/Grafana observability stack. Every interesting event that happens inside Relay — a request arriving, a token being consumed, a circuit breaker tripping, a log entry being dropped — now has a named, labelled counter, histogram, or gauge that an operator can query from Grafana.

### New files

| File | Purpose |
|---|---|
| `internal/metrics/prometheus.go` | Defines all collectors; `New(reg)` registers them in a supplied registry |
| `internal/metrics/context.go` | Mutable per-request label bag injected by middleware; handlers write provider + model into it |
| `internal/metrics/middleware.go` | chi-compatible middleware that records `relay_requests_total` and `relay_request_duration_seconds` |
| `internal/metrics/prometheus_test.go` | Table-driven tests for every collector, middleware, and the `/metrics` endpoint |
| `deploy/prometheus.yml` | Prometheus scrape config targeting `relay:8080/metrics` |
| `deploy/grafana/provisioning/` | Auto-provisioned datasource (Prometheus) and dashboard loader |
| `deploy/grafana/dashboards/relay.json` | Grafana dashboard JSON with 8 panels |

### Updated files

| File | Change |
|---|---|
| `internal/metrics/logger.go` | Added `WithDropCounter(prometheus.Counter)` so the async logger increments a real Prometheus counter on drops |
| `internal/api/handler.go` | Injects model name into context bag; records `relay_tokens_total` after a successful chat completion; exposes `GET /metrics` via `promhttp.HandlerFor` |
| `cmd/relay/main.go` | Creates a `prometheus.Registry`, registers Go + process collectors, creates `metrics.Metrics`, wires everything through; starts `pollBreakerStates` goroutine |
| `deploy/docker-compose.yml` | Added `prom/prometheus:v2.53.0` and `grafana/grafana:11.1.0` services |

---

## Metrics Catalogue

| Metric | Type | Labels | What it tracks |
|---|---|---|---|
| `relay_requests_total` | Counter | `provider`, `model`, `status` | Every completed HTTP request with its status code |
| `relay_request_duration_seconds` | Histogram | `provider`, `model` | End-to-end wall-clock latency (default Prometheus buckets) |
| `relay_tokens_total` | Counter | `provider`, `model`, `kind` | Prompt and completion token consumption |
| `relay_rate_limit_hits_total` | Counter | `key_hash` | Requests rejected by the Lua token bucket |
| `relay_circuit_breaker_state` | Gauge | `provider` | 0 = Closed, 1 = HalfOpen, 2 = Open |
| `relay_upstream_retries_total` | Counter | `provider` | Retry attempts fired by the router |
| `relay_dropped_log_entries_total` | Counter | — | Async-logger entries discarded on a full buffer |
| `relay_active_streams` | Gauge | — | SSE streaming connections in flight |

The `/metrics` endpoint also exposes the standard `go_*` and `process_*` families registered at startup.

---

## Design Decisions

### Non-default registry

Prometheus ships with a `prometheus.DefaultRegisterer` that is a process-wide singleton. Using it causes two problems in a well-tested project:

1. **Test pollution** — metrics registered in one test case bleed into the next because the global registry is never reset between `t.Run` invocations.
2. **Double-registration panics** — if any production code accidentally imports a package that auto-registers on init, and your test also registers the same collector, you get a panic.

We create an explicit `prometheus.NewRegistry()` in `main()` and pass it into `metrics.New(reg)`. Each test creates its own fresh registry. The rule is: **nothing is registered at package init time**.

### Mutable per-request label bag

The Prometheus middleware needs to record `provider` and `model` labels, but those values are only known *inside* the handler (after decoding the request body). Chi middleware runs as a chain around the handler — the middleware calls `next.ServeHTTP`, then reads labels.

A naive approach stores values in context using `context.WithValue`, but that creates a new context on every write. If the handler does `r.WithContext(enrichedCtx)` and then calls the next layer, the middleware's original `r` still points at the old context — the labels are invisible.

The solution: the middleware injects a pointer to a `RequestLabels` struct into the context **before** calling `next`. The handler reads the same pointer from context and writes into it. After `next.ServeHTTP` returns the middleware reads the struct. Because both sides share the pointer, no `r.WithContext` gymnastics are needed.

```
Middleware                Handler
    |                        |
    +-- inject *labels ----→ |
    |   into ctx             |
    |                        |
    +-- next(w, r) --------→ writes lb.Provider = "ollama"
    |                              lb.Model     = "llama3"
    |   ← returns ─────────────────
    |
    reads lb.Provider, lb.Model
    records counter/histogram
```

This is safe because Go's HTTP server processes a single request on a single goroutine — there is no concurrent access to the struct within one request lifetime.

### Circuit-breaker state as a gauge

Circuit-breaker state changes are infrequent and the interesting question for operators is "what is the state *right now*?", not "how many state transitions happened?". A gauge answers that question directly and enables Grafana threshold colouring (green = closed, yellow = half-open, red = open).

A background goroutine (`pollBreakerStates`) updates the gauge every five seconds. Alternatives considered:

- **Recording inside RecordSuccess/Failure** — would require passing the `*Metrics` into the breaker package, creating an import cycle (breaker → metrics → store → ...).
- **Prometheus `StateSet`** — not yet in the Go client; the three-value numeric gauge with label mappings is equally readable.

### `promhttp.HandlerFor` over the default mux handler

Using `prometheus.DefaultRegisterer` together with `promhttp.Handler()` would scrape the global registry (which we don't use). `promhttp.HandlerFor(reg, ...)` scrapes only our explicit registry. This also lets us start the server without pulling in the default registry's Go runtime metrics unless we opt in.

---

## Grafana Dashboard

The provisioned dashboard (`deploy/grafana/dashboards/relay.json`) has four row sections:

| Row | Panels |
|---|---|
| Traffic | RPS by provider/model/status; p50 + p99 latency |
| Tokens | Token throughput (prompt vs completion) |
| Reliability | Circuit breaker state (stat panel with colour); retry rate; active SSE streams |
| Rate limiting | Rate-limit hits/s; dropped log entries/s |

Grafana auto-provisions the Prometheus datasource and the dashboard on first boot via the `provisioning/` directory — no manual click-through required.

---

## Trade-offs

| Decision | Pro | Con |
|---|---|---|
| Default histogram buckets | No config needed | Not tuned for LLM latency (buckets up to 10 s; LLM calls can be 60 s+) |
| 5 s breaker-state poll | Simple; no coupling | Up to 5 s lag between a state transition and the gauge update |
| Label `key_hash` on rate-limit counter | Granular per-key visibility | High cardinality if there are thousands of API keys; add a hash prefix truncation if needed |
| Token counts from non-streaming responses only | Accurate; tokens come from the JSON response body | Streaming responses don't have a token count until `[DONE]`; a follow-on phase can accumulate chunk counts |

---

## Test Coverage

```
ok  github.com/TECHMINEs/Relay/internal/metrics  (race detector clean)
```

Tests:

- `TestNewRegistersAllCollectors` — every collector name appears in `Gather()` after a touch
- `TestNewErrorOnDoubleRegister` — registering twice on the same registry returns an error
- `TestMiddlewareRecordsRequestsAndDuration` — table-driven: 200, 429, 503 all produce correct counter + histogram values
- `TestMiddlewareDefaultLabelsWhenContextMissing` — requests without label injection use "unknown"
- `TestMiddlewareMultipleRequestsAccumulate` — five requests accumulate to five in the counter
- `TestMetricsEndpointExposesCollectors` — live HTTP server, GET /metrics, all metric names present in body
- `TestContextHelpers` — mutable bag: no-op without bag, readable after injection
- `TestCircuitBreakerStateGauge` — all three numeric state values round-trip correctly
- `TestRateLimitHitsCounter` — seven hits → counter = 7
- `TestAsyncLoggerDropCounterPrometheus` — overfill buffer, Prometheus counter increments
- `TestTokensTotalCounter` — prompt and completion labels tracked independently
- `TestActiveStreamsGauge` — Inc × 2, Dec × 1 → gauge = 1
- `TestUpstreamRetriesCounter` — two providers tracked independently
