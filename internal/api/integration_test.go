package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/api"
	"github.com/TECHMINEs/Relay/internal/auth"
	"github.com/TECHMINEs/Relay/internal/config"
	"github.com/TECHMINEs/Relay/internal/metrics"
	"github.com/TECHMINEs/Relay/internal/provider"
	"github.com/TECHMINEs/Relay/internal/ratelimit"
	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// memAuthStore is an in-memory lookup table for API keys.
type memAuthStore struct {
	keys map[string]*store.APIKey
}

func (s *memAuthStore) GetAPIKeyByHash(_ context.Context, hash string) (*store.APIKey, error) {
	return s.keys[hash], nil
}

// memLogInserter captures batched request logs in memory for assertions.
type memLogInserter struct {
	mu      sync.Mutex
	entries []store.RequestLogEntry
}

func (m *memLogInserter) InsertRequestLogBatch(_ context.Context, entries []store.RequestLogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	return nil
}

func (m *memLogInserter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// TestEndToEndAuthRateLimitAndLogging proves the three critical requirements:
// 1. An unauthenticated request returns 401.
// 2. A request with an exhausted rate limit bucket returns 429.
// 3. A fully authorized request succeeds with 200 and is recorded by the async logger.
func TestEndToEndAuthRateLimitAndLogging(t *testing.T) {
	// Connect to local Redis for true token bucket evaluation
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379", DB: 14})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available on localhost:6379: %v", err)
	}
	defer rdb.Close()

	rawValidKey := "sk-relay-integration-test-key"
	keyHash := auth.HashKey(rawValidKey)

	bucketKey := ratelimit.BucketKey(keyHash)
	rdb.Del(context.Background(), bucketKey)
	defer rdb.Del(context.Background(), bucketKey)

	// API key with burst of 1 and slow refill (0.01 tokens/sec)
	activeKey := &store.APIKey{
		ID:             42,
		KeyHash:        keyHash,
		Name:           "integration-key",
		RequestsPerSec: 0.01,
		Burst:          1,
		IsActive:       true,
	}

	authStore := &memAuthStore{
		keys: map[string]*store.APIKey{
			keyHash: activeKey,
		},
	}

	limiter, err := ratelimit.New(context.Background(), rdb)
	if err != nil {
		t.Fatalf("failed to initialize ratelimit: %v", err)
	}

	memInserter := &memLogInserter{}
	asyncLogger := metrics.NewAsyncLogger(memInserter, 100, 10, 10*time.Millisecond)
	asyncLogger.Start(context.Background())
	defer asyncLogger.Stop()

	mockP := provider.NewMockProvider(&config.Config{})
	handler := api.NewHandler(
		mockP,
		api.WithAuth(authStore),
		api.WithRateLimiter(limiter),
		api.WithAsyncLogger(asyncLogger),
	)

	r := chi.NewRouter()
	api.RegisterRoutes(r, handler, nil)

	chatPayload := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`

	// ------------------------------------------------------------------------
	// Test 1: Unauthenticated request returns 401 Unauthorized
	// ------------------------------------------------------------------------
	t.Run("unauthenticated request returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(chatPayload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d. Body: %s", rec.Code, rec.Body.String())
		}

		var errResp api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to parse error response: %v", err)
		}
		if errResp.Error.Type != "auth_error" {
			t.Errorf("expected error type auth_error, got %s", errResp.Error.Type)
		}
	})

	// ------------------------------------------------------------------------
	// Test 2: Fully authorized request succeeds (HTTP 200) and is logged
	// ------------------------------------------------------------------------
	t.Run("fully authorized request succeeds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(chatPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+rawValidKey)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d. Body: %s", rec.Code, rec.Body.String())
		}

		// Verify rate limit headers
		if rec.Header().Get("X-RateLimit-Limit") != "1" {
			t.Errorf("expected X-RateLimit-Limit: 1, got %s", rec.Header().Get("X-RateLimit-Limit"))
		}
		if rec.Header().Get("X-RateLimit-Remaining") != "0" {
			t.Errorf("expected X-RateLimit-Remaining: 0, got %s", rec.Header().Get("X-RateLimit-Remaining"))
		}

		// Wait briefly for the async logger flush interval
		time.Sleep(50 * time.Millisecond)

		if count := memInserter.count(); count != 1 {
			t.Errorf("expected 1 logged entry, got %d", count)
		}
	})

	// ------------------------------------------------------------------------
	// Test 3: Request with exhausted rate limit bucket returns 429 Too Many Requests
	// ------------------------------------------------------------------------
	t.Run("exhausted rate limit bucket returns 429", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(chatPayload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+rawValidKey)
		rec := httptest.NewRecorder()

		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 Too Many Requests, got %d. Body: %s", rec.Code, rec.Body.String())
		}

		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("expected non-empty Retry-After header on 429 response")
		}

		var errResp api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to parse rate limit error: %v", err)
		}
		if errResp.Error.Type != "rate_limit_error" {
			t.Errorf("expected error type rate_limit_error, got %s", errResp.Error.Type)
		}
	})
}
