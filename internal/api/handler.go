// Package api provides HTTP routing and handlers for OpenAI-compatible endpoints.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/TECHMINEs/Relay/internal/auth"
	"github.com/TECHMINEs/Relay/internal/metrics"
	"github.com/TECHMINEs/Relay/internal/provider"
	"github.com/TECHMINEs/Relay/internal/ratelimit"
	"github.com/TECHMINEs/Relay/internal/router"
	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ErrorResponse represents standard OpenAI error format.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains specific error fields.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// Handler handles API requests.
type Handler struct {
	provider provider.Provider
	router   *router.Router       // may be nil; if set, used for Chat/Embeddings with reliability features
	metrics  *metrics.Metrics     // may be nil; if set, used for Prometheus instrumentation
	auth     auth.Lookup          // may be nil; if set, validates API keys
	limiter  *ratelimit.Limiter   // may be nil; if set, enforces token bucket rate limits
	logger   *metrics.AsyncLogger // may be nil; if set, logs requests asynchronously
}

// HandlerOption configures optional Handler components.
type HandlerOption func(*Handler)

// WithAuth configures API key authentication lookup for the Handler.
func WithAuth(lookup auth.Lookup) HandlerOption {
	return func(h *Handler) {
		h.auth = lookup
	}
}

// WithRateLimiter configures token-bucket rate limiting for the Handler.
func WithRateLimiter(limiter *ratelimit.Limiter) HandlerOption {
	return func(h *Handler) {
		h.limiter = limiter
	}
}

// WithAsyncLogger configures async request persistence for the Handler.
func WithAsyncLogger(logger *metrics.AsyncLogger) HandlerOption {
	return func(h *Handler) {
		h.logger = logger
	}
}

// NewHandler creates a new Handler with the given provider and optional components.
func NewHandler(p provider.Provider, opts ...HandlerOption) *Handler {
	h := &Handler{
		provider: p,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewHandlerWithRouter creates a Handler backed by the reliability router,
// which itself implements provider.Provider.
func NewHandlerWithRouter(rt *router.Router, opts ...HandlerOption) *Handler {
	h := &Handler{
		provider: rt,
		router:   rt,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// NewHandlerWithMetrics creates a router-backed Handler with Prometheus metrics.
func NewHandlerWithMetrics(rt *router.Router, m *metrics.Metrics, opts ...HandlerOption) *Handler {
	h := &Handler{
		provider: rt,
		router:   rt,
		metrics:  m,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterRoutes mounts the OpenAI-compatible endpoints, plus /metrics when a
// registry is given.
func RegisterRoutes(r chi.Router, h *Handler, reg prometheus.Gatherer) {
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	if h.metrics != nil {
		r.Use(metrics.Middleware(h.metrics))
	}

	r.Get("/healthz", h.Healthz)
	r.Post("/__chaos", h.Chaos)

	if reg != nil {
		r.Get("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
			EnableOpenMetrics: false,
		}).ServeHTTP)
	}

	r.Group(func(apiRouter chi.Router) {
		if h.auth != nil {
			apiRouter.Use(auth.Middleware(h.auth))
		}
		if h.limiter != nil {
			apiRouter.Use(ratelimit.Middleware(h.limiter, ratelimit.DefaultKeyFunc))
		}

		apiRouter.Post("/v1/chat/completions", h.ChatCompletions)
		apiRouter.Post("/v1/embeddings", h.Embeddings)
	})
}

// Healthz responds with a 200 OK for readiness/liveness checks.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// Chaos handles POST /__chaos to adjust fault injection dynamically during chaos tests.
func (h *Handler) Chaos(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ErrorRate *float64 `json:"error_rate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if body.ErrorRate != nil {
		type errorRater interface {
			SetErrorRate(rate float64)
		}
		if er, ok := h.provider.(errorRater); ok {
			er.SetErrorRate(*body.ErrorRate)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ChatCompletions handles POST /v1/chat/completions with JSON response or SSE streaming.
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("Failed to decode JSON request: %v", err))
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Missing required field: model")
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Missing required field: messages must not be empty")
		return
	}

	// Inject model into context so the Prometheus middleware can label metrics.
	ctx = metrics.ContextWithModel(ctx, req.Model)
	r = r.WithContext(ctx)

	if req.Stream {
		h.handleChatStream(w, r, &req)
		return
	}

	startTime := time.Now()

	resp, err := h.provider.Chat(ctx, &req)
	latency := int(time.Since(startTime).Milliseconds())

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			// Client disconnected, nothing left to write
			return
		}
		status, errType := router.HTTPStatusFor(err)
		if errors.Is(err, router.ErrAllBreakerOpen) && h.router != nil {
			retryAfter := h.router.RetryAfter()
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		writeError(w, status, errType, err.Error())
		return
	}

	providerName := metrics.ProviderFromContext(ctx)
	if h.metrics != nil && resp != nil {
		h.metrics.TokensTotal.WithLabelValues(providerName, req.Model, "prompt").Add(float64(resp.Usage.PromptTokens))
		h.metrics.TokensTotal.WithLabelValues(providerName, req.Model, "completion").Add(float64(resp.Usage.CompletionTokens))
	}

	if h.logger != nil && resp != nil {
		var apiKeyID int64
		if k := store.APIKeyFromContext(ctx); k != nil {
			apiKeyID = k.ID
		}
		h.logger.Log(store.RequestLogEntry{
			APIKeyID:         apiKeyID,
			Model:            req.Model,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			LatencyMS:        latency,
			StatusCode:       http.StatusOK,
			Provider:         providerName,
			CreatedAt:        time.Now(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleChatStream(w http.ResponseWriter, r *http.Request, req *provider.ChatRequest) {
	ctx := r.Context()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "server_error", "Streaming is not supported by server connection")
		return
	}

	chunkChan, err := h.provider.ChatStream(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected, exit loop immediately
			return
		case chunk, ok := <-chunkChan:
			if !ok {
				// Stream ended, send [DONE]
				_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}

			if chunk.Err != nil {
				errResp := ErrorResponse{
					Error: ErrorDetail{
						Message: chunk.Err.Error(),
						Type:    "stream_error",
					},
				}
				errBytes, _ := json.Marshal(errResp)
				_, _ = fmt.Fprintf(w, "data: %s\n\n", string(errBytes))
				flusher.Flush()
				return
			}

			chunkBytes, err := json.Marshal(chunk)
			if err != nil {
				return
			}

			_, writeErr := fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
			if writeErr != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Embeddings handles POST /v1/embeddings.
func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req provider.EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("Failed to decode JSON request: %v", err))
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Missing required field: model")
		return
	}

	if len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "Missing required field: input must not be empty")
		return
	}

	// Inject model into context so the Prometheus middleware can label metrics.
	ctx = metrics.ContextWithModel(ctx, req.Model)

	startTime := time.Now()
	resp, err := h.provider.Embeddings(ctx, &req)
	latency := int(time.Since(startTime).Milliseconds())

	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		status, errType := router.HTTPStatusFor(err)
		if errors.Is(err, router.ErrAllBreakerOpen) && h.router != nil {
			retryAfter := h.router.RetryAfter()
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		writeError(w, status, errType, err.Error())
		return
	}

	providerName := metrics.ProviderFromContext(ctx)
	if h.metrics != nil && resp != nil {
		h.metrics.TokensTotal.WithLabelValues(providerName, req.Model, "prompt").Add(float64(resp.Usage.PromptTokens))
		h.metrics.TokensTotal.WithLabelValues(providerName, req.Model, "total").Add(float64(resp.Usage.TotalTokens))
	}
	if h.logger != nil && resp != nil {
		var apiKeyID int64
		if k := store.APIKeyFromContext(ctx); k != nil {
			apiKeyID = k.ID
		}
		h.logger.Log(store.RequestLogEntry{
			APIKeyID:         apiKeyID,
			Model:            req.Model,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			LatencyMS:        latency,
			StatusCode:       http.StatusOK,
			Provider:         providerName,
			CreatedAt:        time.Now(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Message: msg,
			Type:    errType,
		},
	})
}
