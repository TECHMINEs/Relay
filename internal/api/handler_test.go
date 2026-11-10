package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/auth"
	"github.com/TECHMINEs/Relay/internal/config"
	"github.com/TECHMINEs/Relay/internal/provider"
	"github.com/TECHMINEs/Relay/internal/ratelimit"
	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

func setupTestRouter(p provider.Provider) http.Handler {
	r := chi.NewRouter()
	h := NewHandler(p)
	RegisterRoutes(r, h, nil)
	return r
}

func TestChatCompletions(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		cfg            *config.Config
		expectedStatus int
		validate       func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name:           "invalid json body",
			body:           `{invalid-json`,
			cfg:            &config.Config{},
			expectedStatus: http.StatusBadRequest,
			validate: func(t *testing.T, w *httptest.ResponseRecorder) {
				var errResp ErrorResponse
				if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
					t.Fatalf("failed to decode error json: %v", err)
				}
				if errResp.Error.Type != "invalid_request_error" {
					t.Errorf("expected error type invalid_request_error, got %s", errResp.Error.Type)
				}
			},
		},
		{
			name:           "missing model",
			body:           `{"messages":[{"role":"user","content":"hi"}]}`,
			cfg:            &config.Config{},
			expectedStatus: http.StatusBadRequest,
			validate: func(t *testing.T, w *httptest.ResponseRecorder) {
				var errResp ErrorResponse
				_ = json.NewDecoder(w.Body).Decode(&errResp)
				if !strings.Contains(errResp.Error.Message, "model") {
					t.Errorf("expected error message to mention model, got %s", errResp.Error.Message)
				}
			},
		},
		{
			name:           "missing messages",
			body:           `{"model":"gpt-4"}`,
			cfg:            &config.Config{},
			expectedStatus: http.StatusBadRequest,
			validate: func(t *testing.T, w *httptest.ResponseRecorder) {
				var errResp ErrorResponse
				_ = json.NewDecoder(w.Body).Decode(&errResp)
				if !strings.Contains(errResp.Error.Message, "messages") {
					t.Errorf("expected error message to mention messages, got %s", errResp.Error.Message)
				}
			},
		},
		{
			name:           "successful non-streaming chat request",
			body:           `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`,
			cfg:            &config.Config{},
			expectedStatus: http.StatusOK,
			validate: func(t *testing.T, w *httptest.ResponseRecorder) {
				var resp provider.Response
				if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
					t.Fatalf("failed to decode response: %v", err)
				}
				if resp.Object != "chat.completion" {
					t.Errorf("expected object chat.completion, got %s", resp.Object)
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Choices[0].Message.Role != "assistant" {
					t.Errorf("expected assistant, got %s", resp.Choices[0].Message.Role)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := provider.NewMockProvider(tt.cfg)
			router := setupTestRouter(p)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d. Body: %s", tt.expectedStatus, rec.Code, rec.Body.String())
			}
			if tt.validate != nil {
				tt.validate(t, rec)
			}
		})
	}
}

func TestStreamingChunkOrder(t *testing.T) {
	cfg := &config.Config{
		MockChunkDelay: 2 * time.Millisecond,
	}
	p := provider.NewMockProvider(cfg)
	router := setupTestRouter(p)

	reqBody := `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"stream test"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %s", contentType)
	}

	scanner := bufio.NewScanner(rec.Body)
	var chunks []provider.Chunk
	var receivedDone bool

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				receivedDone = true
				continue
			}
			var c provider.Chunk
			if err := json.Unmarshal([]byte(payload), &c); err != nil {
				t.Fatalf("failed to decode chunk JSON: %v, raw: %s", err, payload)
			}
			chunks = append(chunks, c)
		}
	}

	if !receivedDone {
		t.Error("expected [DONE] event at the end of SSE stream")
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Verify chunk order:
	// 1. First chunk has role "assistant"
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected first chunk delta role to be 'assistant', got %s", chunks[0].Choices[0].Delta.Role)
	}

	// 2. Intermediate chunks carry content
	var fullContent strings.Builder
	for _, chunk := range chunks {
		fullContent.WriteString(chunk.Choices[0].Delta.Content)
	}
	expectedContent := "Hello from Relay mock provider streaming!"
	if fullContent.String() != expectedContent {
		t.Errorf("expected concatenated content %q, got %q", expectedContent, fullContent.String())
	}

	// 3. Final chunk has finish_reason "stop"
	lastChunk := chunks[len(chunks)-1]
	if lastChunk.Choices[0].FinishReason == nil || *lastChunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected last chunk finish_reason to be 'stop'")
	}
}

func TestClientCancellationStopsUpstreamCall(t *testing.T) {
	t.Run("Non-streaming cancellation", func(t *testing.T) {
		cfg := &config.Config{
			MockStallForever: true,
		}
		p := provider.NewMockProvider(cfg)
		router := setupTestRouter(p)

		ctx, cancel := context.WithCancel(context.Background())
		reqBody := `{"model":"gpt-4","messages":[{"role":"user","content":"stall me"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody)).WithContext(ctx)

		rec := httptest.NewRecorder()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			router.ServeHTTP(rec, req)
		}()

		// Allow request to enter provider stall
		time.Sleep(30 * time.Millisecond)

		// Cancel client request context
		cancel()
		wg.Wait()

		if p.CancelCount() != 1 {
			t.Errorf("expected mock provider to observe 1 cancellation, got %d", p.CancelCount())
		}
	})

	t.Run("Streaming cancellation stops upstream", func(t *testing.T) {
		cfg := &config.Config{
			MockStallForever: true,
		}
		p := provider.NewMockProvider(cfg)
		router := setupTestRouter(p)

		ctx, cancel := context.WithCancel(context.Background())
		reqBody := `{"model":"gpt-4","stream":true,"messages":[{"role":"user","content":"stall stream"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody)).WithContext(ctx)

		rec := httptest.NewRecorder()

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			router.ServeHTTP(rec, req)
		}()

		// Wait briefly then cancel
		time.Sleep(30 * time.Millisecond)
		cancel()
		wg.Wait()

		// The streaming provider observes cancellation in its own goroutine, which can
		// finish after the handler has already returned, so wait for it rather than
		// asserting immediately.
		deadline := time.Now().Add(time.Second)
		for p.CancelCount() < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if p.CancelCount() != 1 {
			t.Errorf("expected mock provider cancel count 1, got %d", p.CancelCount())
		}
	})
}

// stubAuthStore implements auth.Lookup for integration testing.
type stubAuthStore struct {
	keys map[string]*store.APIKey
}

func (s *stubAuthStore) GetAPIKeyByHash(_ context.Context, hash string) (*store.APIKey, error) {
	return s.keys[hash], nil
}

func TestAuthAndRateLimiterIntegration(t *testing.T) {
	tests := []struct {
		name           string
		authHeader     string
		burst          int
		rps            float64
		setupRedis     bool
		exhaustLimit   bool
		expectedStatus int
	}{
		{
			name:           "unauthenticated request returns 401",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid bearer token returns 401",
			authHeader:     "Bearer invalid-key",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "authenticated request within limit returns 200",
			authHeader:     "Bearer test-valid-key",
			burst:          5,
			rps:            10.0,
			setupRedis:     true,
			exhaustLimit:   false,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "exhausted rate limit returns 429",
			authHeader:     "Bearer test-valid-key",
			burst:          1,
			rps:            0.001,
			setupRedis:     true,
			exhaustLimit:   true,
			expectedStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawKey := "test-valid-key"
			keyHash := auth.HashKey(rawKey)

			apiKey := &store.APIKey{
				ID:             1,
				KeyHash:        keyHash,
				Name:           "test-key",
				RequestsPerSec: tt.rps,
				Burst:          tt.burst,
				IsActive:       true,
			}

			authStore := &stubAuthStore{
				keys: map[string]*store.APIKey{
					keyHash: apiKey,
				},
			}

			var (
				limiter *ratelimit.Limiter
				rdb     *redis.Client
			)
			if tt.setupRedis {
				rdb = redis.NewClient(&redis.Options{Addr: "localhost:6379", DB: 15})
				if err := rdb.Ping(context.Background()).Err(); err != nil {
					t.Skipf("Redis not available on localhost:6379: %v", err)
				}
				defer rdb.Close()
				bucketKey := ratelimit.BucketKey(keyHash)
				rdb.Del(context.Background(), bucketKey)
				defer rdb.Del(context.Background(), bucketKey)

				l, err := ratelimit.New(context.Background(), rdb)
				if err != nil {
					t.Fatalf("ratelimit.New: %v", err)
				}
				limiter = l

				if tt.exhaustLimit {
					// Consume all tokens from the bucket prior to the request
					res, err := limiter.Allow(context.Background(), bucketKey, tt.rps, tt.burst, tt.burst)
					if err != nil {
						t.Fatalf("limiter.Allow prep: %v", err)
					}
					if !res.Allowed {
						t.Fatalf("expected initial consume to be allowed")
					}
				}
			}

			p := provider.NewMockProvider(&config.Config{})
			r := chi.NewRouter()
			h := NewHandler(p, WithAuth(authStore), WithRateLimiter(limiter))
			RegisterRoutes(r, h, nil)

			body := `{"model":"gpt-4","messages":[{"role":"user","content":"ping"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("status = %d, want %d. Body: %s", rec.Code, tt.expectedStatus, rec.Body.String())
			}
		})
	}
}
