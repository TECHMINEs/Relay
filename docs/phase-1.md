# Phase 1: Core Proxy and Providers

## Architecture Overview

Phase 1 establishes the foundational layer of Relay: a high-throughput, OpenAI-compatible reverse proxy with swappable upstream provider engines, Server-Sent Events (SSE) streaming, context cancellation propagation, and a deterministic mock provider for testing and latency/failure simulation.

```
                      +-----------------------------+
                      |       Client Request        |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      |        Chi HTTP Router      |
                      |  POST /v1/chat/completions  |
                      |     POST /v1/embeddings     |
                      +--------------+--------------+
                                     |
                         +-----------+-----------+
                         |                       |
                         v                       v
               +-------------------+   +--------------------+
               |   OllamaProvider  |   |    MockProvider    |
               | (local LLM proxy) |   | (faults & latency) |
               +-------------------+   +--------------------+
```

## Implemented Components

### 1. Provider Abstraction (`internal/provider/provider.go`)
- **`Provider` Interface**:
  - `Chat(ctx context.Context, req *ChatRequest) (*Response, error)`: Unary chat completion handling standard OpenAI request and response structs.
  - `ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error)`: SSE streaming channel producing OpenAI-formatted chunk deltas.
  - `Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)`: Vector embeddings interface.
- Standardized typed models matching the OpenAI schema (`ChatMessage`, `ChatChoice`, `Usage`, `Chunk`, `ChunkChoice`, `ChunkDelta`).

### 2. Upstream Providers
- **`OllamaProvider` (`internal/provider/ollama.go`)**:
  - Translates OpenAI chat and embedding requests to Ollama's native `/api/chat` and `/api/embed` endpoints.
  - Parses streaming NDJSON lines and normalizes them into OpenAI-compatible SSE events (`chat.completion.chunk`).
  - Supports configurable timeouts and custom upstream base URLs via environment configuration.
- **`MockProvider` (`internal/provider/mock.go`)**:
  - Controlled fault injection without external dependencies.
  - Configurable random uniform latency range (`MOCK_LATENCY_MIN` to `MOCK_LATENCY_MAX`).
  - Configurable error rate (`MOCK_ERROR_RATE`, 0.0 to 1.0) simulating transient upstream failures.
  - Stall forever mode (`MOCK_STALL_FOREVER=true`) for testing timeouts, deadlocks, and client-abort handling.
  - Thread-safe tracking of total requests and context cancellations via atomic counters (`CallCount`, `CancelCount`).

### 3. HTTP Gateway & SSE Streaming Engine (`internal/api/handler.go`)
- Built on `go-chi/chi/v5` with request ID tracking and panic recovery.
- Full support for `POST /v1/chat/completions` (unary JSON & SSE stream when `stream: true`).
- **Context & Client Disconnect Cancellation**:
  - The HTTP request context (`r.Context()`) is propagated down into the Provider methods and upstream HTTP/goroutine loops.
  - If a client disconnects or aborts the HTTP connection midway through a unary call or during SSE streaming, `ctx.Done()` fires. The mock and Ollama providers cleanly terminate their network calls and streaming loops without leaking goroutines or dangling connections.
- Formats streaming output with standard SSE conventions: `data: {JSON}\n\n` followed by `data: [DONE]\n\n`.

### 4. Centralized Configuration (`internal/config/config.go`)
- Single source of truth reading all settings from environment variables.
- Defaults to sensible values for local development and container deployments.
- Validates constraints (e.g., error rate bounds, min/max latency invariants) at startup.

### 5. Deployment Setup (`deploy/docker-compose.yml`, `deploy/Dockerfile`)
- Multi-stage Docker build producing an alpine-based scratch binary (`relay`).
- Docker Compose configuration orchestrating:
  - `relay`: Port 8080 (the gateway)
  - `ollama`: Port 11434 (local LLM engine)
  - `postgres`: Port 5432 (`pgvector/pgvector:pg16` for vector storage in upcoming phases)
  - `redis`: Port 6379 (Redis 7 Alpine for upcoming rate-limiting and response caching)

## Design Decisions & Trade-Offs

| Decision | Alternative Considered | Rationale & Trade-Off |
| :--- | :--- | :--- |
| **Channel-based stream (`<-chan Chunk`)** | `io.Reader` / callback functions | A read-only channel decoupling decouples stream consumption from provider-specific networking. It simplifies concurrency in Go, allowing handlers to `select` across channel reads and `ctx.Done()` client disconnects. |
| **Buffered channels in streaming** | Unbuffered channels | Small buffer (10–16 items) absorbs transient network flushes to avoid stalling upstream token generation while the client socket flushes. |
| **Atomic cancel counters on Mock** | Channels / mutexes | `sync/atomic` provides zero-contention, lock-free verification during high-concurrency race tests. |
| **Dynamic provider selection at startup** | Dynamic per-request routing | Keeps Phase 1 simple and modular; prepares clean interfaces for Phase 4 (smart router and load balancing). |

## Verification & Testing

Table-driven unit tests implemented in `internal/config`, `internal/provider`, and `internal/api`:
1. Request & response schema validation (OpenAI-compatible payload structure).
2. Streaming token chunk order and terminal `[DONE]` validation.
3. Client disconnect / cancellation propagation stopping upstream execution.
4. Error rate and latency range simulations.
5. All tests run cleanly with the race detector: `go test -race -count=1 ./...`.
