// Package metrics — HTTP middleware for Prometheus instrumentation.
package metrics

import (
	"net/http"
	"strconv"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher to forward flush calls to the underlying ResponseWriter.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware returns a chi-compatible HTTP middleware that records
// relay_requests_total and relay_request_duration_seconds for every request.
//
// It injects a mutable *RequestLabels bag into the request context before
// calling the next handler.  Downstream handlers (e.g. ChatCompletions) write
// provider and model names into the bag via ContextWithProvider /
// ContextWithModel.  The middleware reads those values back after the handler
// returns for accurate Prometheus label attribution.
func Middleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Inject a mutable label bag so handlers can write provider/model.
			ctx, lb := contextWithLabels(r.Context())
			r = r.WithContext(ctx)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			elapsed := time.Since(start).Seconds()
			status := strconv.Itoa(rec.status)

			// Read labels written by the handler (or "unknown" if unset).
			provider := lb.Provider
			if provider == "" {
				provider = "unknown"
			}
			model := lb.Model
			if model == "" {
				model = "unknown"
			}

			m.RequestsTotal.WithLabelValues(provider, model, status).Inc()
			m.RequestDuration.WithLabelValues(provider, model).Observe(elapsed)
		})
	}
}
