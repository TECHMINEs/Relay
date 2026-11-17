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

// Middleware records relay_requests_total and relay_request_duration_seconds,
// labelled with the provider and model the handler sets via ContextWithProvider
// and ContextWithModel.
func Middleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			ctx, lb := contextWithLabels(r.Context())
			r = r.WithContext(ctx)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			elapsed := time.Since(start).Seconds()
			status := strconv.Itoa(rec.status)

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
