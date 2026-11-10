package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TECHMINEs/Relay/internal/store"
)

// stubLookup implements Lookup for tests.
type stubLookup struct {
	keys map[string]*store.APIKey
}

func (s *stubLookup) GetAPIKeyByHash(_ context.Context, hash string) (*store.APIKey, error) {
	return s.keys[hash], nil
}

func makeStub(raw string, active bool) *stubLookup {
	return &stubLookup{
		keys: map[string]*store.APIKey{
			HashKey(raw): {
				ID:              1,
				KeyHash:         HashKey(raw),
				Name:            "test",
				RequestsPerSec:  10,
				Burst:           20,
				DailyTokenQuota: 100000,
				IsActive:        active,
			},
		},
	}
}

func TestHashKey(t *testing.T) {
	h1 := HashKey("my-secret-key")
	h2 := HashKey("my-secret-key")
	if h1 != h2 {
		t.Error("HashKey must be deterministic")
	}
	if HashKey("a") == HashKey("b") {
		t.Error("HashKey must distinguish different inputs")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex SHA-256, got %d", len(h1))
	}
}

func TestMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		authHeader     string
		lookup         Lookup
		expectedStatus int
	}{
		{
			name:           "missing authorization header",
			authHeader:     "",
			lookup:         makeStub("good-key", true),
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "malformed authorization header",
			authHeader:     "Token abc123",
			lookup:         makeStub("good-key", true),
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "unknown key",
			authHeader:     "Bearer wrong-key",
			lookup:         makeStub("good-key", true),
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "inactive key",
			authHeader:     "Bearer good-key",
			lookup:         makeStub("good-key", false),
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "valid active key",
			authHeader:     "Bearer good-key",
			lookup:         makeStub("good-key", true),
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := Middleware(tt.lookup)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := APIKeyFromContext(r.Context())
				if key == nil {
					t.Error("expected APIKey in context for successful auth")
				}
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestDefaultKeyFunc(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if k := DefaultKeyFunc(req); k != "" {
		t.Errorf("expected empty string for missing Authorization header, got %q", k)
	}

	req.Header.Set("Authorization", "Bearer my-secret-api-key")
	if k := DefaultKeyFunc(req); k != "my-secret-api-key" {
		t.Errorf("expected extracted key 'my-secret-api-key', got %q", k)
	}

	req.Header.Set("Authorization", "InvalidFormat foo")
	if k := DefaultKeyFunc(req); k != "" {
		t.Errorf("expected empty string for invalid Authorization header, got %q", k)
	}
}
