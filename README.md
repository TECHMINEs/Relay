<div align="center">

<a href="https://techmines.github.io/"><img src="relay-ui/public/logo-lockup.svg" alt="Relay by TECHMINES" height="84" /></a>

# Relay

**A fault-tolerant, OpenAI-compatible LLM gateway — circuit breakers, retries, rate limits and full observability in front of any model backend.**

[![Relay CI](https://github.com/TECHMINEs/Relay/actions/workflows/go.yml/badge.svg)](https://github.com/TECHMINEs/Relay/actions/workflows/go.yml)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Next.js](https://img.shields.io/badge/Next.js-14-000000?logo=nextdotjs)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-pgvector-4169E1?logo=postgresql&logoColor=white)
![Prometheus](https://img.shields.io/badge/Prometheus-metrics-E6522C?logo=prometheus&logoColor=white)
![Grafana](https://img.shields.io/badge/Grafana-dashboard-F46800?logo=grafana&logoColor=white)

</div>

Relay is a drop-in replacement for the OpenAI `/v1/chat/completions` and `/v1/embeddings` endpoints. It routes each request across weighted providers such as Ollama, with per-provider circuit breakers and jittered retries. Every call is authenticated, rate-limited, metered in Prometheus and logged asynchronously. A Next.js console lets you chat through the gateway and watch its live metrics.

---

## Architecture

```mermaid
flowchart LR
    Client(["OpenAI SDK / curl"])
    UI["relay-ui (Next.js) :3000<br/>Playground · Metrics"]

    subgraph Gateway["Relay gateway (Go, chi) :8080"]
        Auth["auth middleware<br/>Bearer → SHA-256 lookup"]
        RL["rate limiter<br/>Redis Lua token bucket<br/>+ daily token quota"]
        H["API handler<br/>JSON + SSE streaming"]
        R["router<br/>weighted pick · full-jitter retry"]
        CB["circuit breakers<br/>sliding window · half-open probes"]
        Log["async logger<br/>buffered batch inserts"]
        M["/metrics"]
    end

    Redis[("Redis")]
    PG[("Postgres<br/>api_keys · request_log")]
    Ollama["Ollama"]
    Mock["Mock provider<br/>latency / error injection"]
    Prom["Prometheus"] --> Graf["Grafana"]

    Client --> Auth
    UI -->|"/api/relay/v1/* rewrite"| Auth
    Auth --> PG
    Auth --> RL <-->|EVALSHA| Redis
    RL --> H --> R --> CB
    CB --> Ollama
    CB --> Mock
    H --> Log --> PG
    M --> Prom
```

**Request flow:**
1. The API key is hashed with SHA-256 and looked up in `api_keys`.
2. A Redis Lua script atomically refills and debits the key's token bucket and checks its daily token quota.
3. The router picks a healthy provider by weight, skipping those whose breaker is open.
4. Each attempt runs with its own timeout, under exponential backoff with full jitter.
5. Tokens, latency and breaker state are exported to Prometheus, and the request is queued for batched persistence.

## Tech Stack

- **Gateway:** Go 1.26, chi v5, pgx v5 (Postgres), go-redis v9, prometheus/client_golang
- **Console:** Next.js 14, React 18, TypeScript (strict), Tailwind CSS, lucide-react
- **Infrastructure:** Docker Compose (gateway, Postgres with pgvector, Redis, Ollama, Prometheus, Grafana)
- **Quality:** `go vet`, staticcheck and race-enabled tests in GitHub Actions; k6 load and chaos tests

## Local Setup

**Prerequisites:** Go 1.26+, Node.js 20+, and Docker (for the full stack).

**Option A — gateway only, zero dependencies (mock provider):**

```bash
git clone https://github.com/TECHMINEs/Relay.git && cd Relay
go run ./cmd/relay                 # :8080, DEFAULT_PROVIDER=mock (no .env needed)
curl http://localhost:8080/healthz
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"mock-v1","messages":[{"role":"user","content":"Hello!"}]}'
```

If Postgres or Redis are unreachable, the gateway logs a warning and keeps serving, with auth, rate limiting and request logging disabled.

**Option B — full stack:**

```bash
cp .env.example .env               # every value shown is the built-in default
docker compose up --build -d       # gateway :8080 · Ollama :11434 · Prometheus :9090 · Grafana :3010 (admin / relay) · Postgres :5433 · Redis :6380
psql "postgres://postgres:postgres@localhost:5433/relay" -f migrations/001_create_api_keys_and_request_log.sql
psql "postgres://postgres:postgres@localhost:5433/relay" -f migrations/002_daily_token_usage.sql
```

With Postgres connected, `/v1/*` requires an API key. Issue one by storing its SHA-256 hash:

```bash
KEY="relay-$(openssl rand -hex 16)"
psql "postgres://postgres:postgres@localhost:5433/relay" -c \
  "INSERT INTO api_keys (key_hash, name) VALUES ('$(printf %s "$KEY" | shasum -a 256 | cut -d' ' -f1)', 'local');"
curl http://localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama3","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

**Console (http://localhost:3000):**

```bash
cd relay-ui
npm install
npm run dev
```

Point it at a gateway on another port with `RELAY_GATEWAY_URL=http://localhost:8081 npm run dev`. The console sends no API key, so use it with the gateway in Option A mode.

**Tests and load:**

```bash
go test -race ./...
k6 run loadtest/steady.js          # steady-state throughput
k6 run loadtest/chaos.js           # failover under injected faults (POST /__chaos)
```

## Key Features

- **OpenAI-compatible API:** chat completions (JSON and SSE streaming) and embeddings.
- **Circuit breakers:** a sliding-window failure rate, a cooldown, and limited half-open probes for each provider.
- **Retries:** exponential backoff with full jitter, a per-attempt timeout and a capped maximum delay.
- **Weighted provider routing:** automatic failover away from open breakers.
- **API-key auth:** only SHA-256 hashes are stored, never raw keys.
- **Rate limiting and quotas:** a token-bucket limiter implemented as an atomic Redis Lua script, plus per-key daily token quotas.
- **Observability:** Prometheus metrics for requests, latency, tokens, breaker state and dropped log entries, with a pre-provisioned Grafana dashboard.
- **Async request logging:** buffered, batched Postgres inserts with back-pressure accounting.
- **Chaos engineering:** a mock provider with configurable latency, error rate, stalls and streaming chunking, plus a runtime `/__chaos` endpoint.
- **Graceful degradation:** a missing Postgres or Redis disables only the features that depend on it.

## Engineering Highlights

- **Concurrency-safe breaker.** The per-provider state machine (closed, open, half-open) uses a ring-buffer window and probe accounting under contention. It is verified with `-race` tests.
- **Atomic distributed rate limiting.** Refill and debit happen in one Lua script (`EVALSHA`), so concurrent gateway replicas can't over-admit requests.
- **Streaming-aware retries.** Retries are only attempted before the first byte is flushed to an SSE client, so a stream is never duplicated mid-flight.
- **Non-blocking persistence.** Request logs go through a bounded channel flushed in batches by size or interval. When the buffer is full, entries are dropped and counted rather than blocking the hot path.
- **Twelve-factor configuration.** All 24 tunables (timeouts, breaker thresholds, retry policy and provider settings) come from the environment with safe defaults, documented in `.env.example`.

## Known Limitations

- **No console sign-in:** relay-ui has no authentication and calls the gateway without an API key.
- **Two providers:** only Ollama and the mock are implemented. The provider interface is the extension point for OpenAI, Anthropic and others.

---

<div align="center">

Created by **Pranav Jangid** | [**TECHMINES**](https://github.com/TECHMINEs)

</div>
