// Package auth provides API key authentication via hashed key lookup.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/TECHMINEs/Relay/internal/store"
)

// APIKeyFromContext extracts the authenticated *store.APIKey from the context.
// Returns nil if not present.
func APIKeyFromContext(ctx context.Context) *store.APIKey {
	return store.APIKeyFromContext(ctx)
}

// HashKey returns the hex-encoded SHA-256 digest of rawKey.
// This is the value stored in api_keys.key_hash.
func HashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// Lookup is the persistence interface required by the middleware.
// It is a subset of *store.Store so tests can use a lightweight stub.
type Lookup interface {
	// GetAPIKeyByHash returns the APIKey row for the given SHA-256 hex hash,
	// or (nil, nil) if not found.
	GetAPIKeyByHash(ctx context.Context, hash string) (*store.APIKey, error)
}

// ExtractBearer pulls the raw API key from "Authorization: Bearer <key>".
func ExtractBearer(r *http.Request) (string, error) {
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(hdr, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", fmt.Errorf("authorization header must be 'Bearer <key>'")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", fmt.Errorf("empty bearer token")
	}
	return token, nil
}

// DefaultKeyFunc extracts the raw API key from the Authorization: Bearer <key> header.
// It returns an empty string if the header is missing, malformed, or empty.
func DefaultKeyFunc(r *http.Request) string {
	token, err := ExtractBearer(r)
	if err != nil {
		return ""
	}
	return token
}

// Middleware returns an HTTP middleware that validates the Bearer token in the
// Authorization header against the api_keys table.
//
// Valid, active keys are stored in the request context via [APIKeyFromContext].
// Invalid or missing keys respond with 401. Inactive keys respond with 403.
func Middleware(lookup Lookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := ExtractBearer(r)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, err.Error())
				return
			}

			hash := HashKey(raw)
			key, err := lookup.GetAPIKeyByHash(r.Context(), hash)
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "auth lookup failed")
				return
			}
			if key == nil {
				writeAuthError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			if !key.IsActive {
				writeAuthError(w, http.StatusForbidden, "API key is inactive")
				return
			}

			ctx := store.ContextWithAPIKey(r.Context(), key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthError writes a JSON error consistent with OpenAI's error envelope.
func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q,"type":"auth_error"}}`, msg)
}
