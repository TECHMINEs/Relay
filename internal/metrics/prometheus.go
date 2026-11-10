// Package metrics provides Prometheus instrumentation for the Relay gateway.
//
// Design: all metrics live in an explicit prometheus.Registry (not the default
// global registry) so tests can construct isolated registries without
// cross-test pollution.  The package exposes a Metrics struct that holds every
// collector; callers inject it where needed rather than reading package-level
// vars.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds every Prometheus collector used by the gateway.
type Metrics struct {
	// RequestsTotal counts completed requests by provider, model, and HTTP
	// status code.
	RequestsTotal *prometheus.CounterVec

	// RequestDuration tracks end-to-end latency (seconds) per provider and model.
	RequestDuration *prometheus.HistogramVec

	// TokensTotal counts prompt and completion tokens by provider and model.
	// The "kind" label is either "prompt" or "completion".
	TokensTotal *prometheus.CounterVec

	// RateLimitHits counts requests rejected by the token-bucket rate limiter.
	RateLimitHits *prometheus.CounterVec

	// CircuitBreakerState is a gauge per named provider:
	//   0 = Closed, 1 = HalfOpen, 2 = Open.
	CircuitBreakerState *prometheus.GaugeVec

	// UpstreamRetries counts retry attempts per provider.
	UpstreamRetries *prometheus.CounterVec

	// DroppedLogEntries counts async-logger entries discarded when the
	// buffer channel was full.
	DroppedLogEntries prometheus.Counter

	// ActiveStreams is the number of currently open SSE streams.
	ActiveStreams prometheus.Gauge
}

// New creates and registers all collectors in the supplied registry.
// Returns an error if any registration fails.
func New(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_requests_total",
			Help: "Total number of requests handled, by provider, model, and HTTP status.",
		}, []string{"provider", "model", "status"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "relay_request_duration_seconds",
			Help:    "End-to-end request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider", "model"}),

		TokensTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_tokens_total",
			Help: "Cumulative token usage by provider, model, and kind (prompt|completion).",
		}, []string{"provider", "model", "kind"}),

		RateLimitHits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_rate_limit_hits_total",
			Help: "Number of requests rejected by the rate limiter, by key hash.",
		}, []string{"key_hash"}),

		CircuitBreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "relay_circuit_breaker_state",
			Help: "Circuit breaker state per provider: 0=Closed, 1=HalfOpen, 2=Open.",
		}, []string{"provider"}),

		UpstreamRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_upstream_retries_total",
			Help: "Number of upstream retry attempts, by provider.",
		}, []string{"provider"}),

		DroppedLogEntries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_dropped_log_entries_total",
			Help: "Async request-log entries discarded because the buffer was full.",
		}),

		ActiveStreams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "relay_active_streams",
			Help: "Number of SSE streaming connections currently open.",
		}),
	}

	collectors := []prometheus.Collector{
		m.RequestsTotal,
		m.RequestDuration,
		m.TokensTotal,
		m.RateLimitHits,
		m.CircuitBreakerState,
		m.UpstreamRetries,
		m.DroppedLogEntries,
		m.ActiveStreams,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}
