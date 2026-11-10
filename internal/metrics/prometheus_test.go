// Package metrics — table-driven tests for Prometheus collectors, middleware,
// and the AsyncLogger drop-counter integration.
package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newRegistry(t *testing.T) (*prometheus.Registry, *Metrics) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	return reg, m
}

// counterValue reads the current value of a *prometheus.Counter.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	pb := &dto.Metric{}
	if err := c.Write(pb); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return pb.GetCounter().GetValue()
}

// counterVecValue reads the value for a specific label set from a CounterVec.
func counterVecValue(t *testing.T, cv *prometheus.CounterVec, labels prometheus.Labels) float64 {
	t.Helper()
	c, err := cv.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("CounterVec.GetMetricWith(%v): %v", labels, err)
	}
	return counterValue(t, c)
}

// histogramCount reads the observation count from a HistogramVec label set.
func histogramCount(t *testing.T, hv *prometheus.HistogramVec, labels prometheus.Labels) uint64 {
	t.Helper()
	obs, err := hv.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("HistogramVec.GetMetricWith(%v): %v", labels, err)
	}
	pb := &dto.Metric{}
	if err := obs.(prometheus.Metric).Write(pb); err != nil {
		t.Fatalf("histogram.Write: %v", err)
	}
	return pb.GetHistogram().GetSampleCount()
}

// gaugeValue reads the current value of a GaugeVec label set.
func gaugeVecValue(t *testing.T, gv *prometheus.GaugeVec, labels prometheus.Labels) float64 {
	t.Helper()
	g, err := gv.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GaugeVec.GetMetricWith(%v): %v", labels, err)
	}
	pb := &dto.Metric{}
	if err := g.Write(pb); err != nil {
		t.Fatalf("gauge.Write: %v", err)
	}
	return pb.GetGauge().GetValue()
}

// ─── New() registration ───────────────────────────────────────────────────────

func TestNewRegistersAllCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	if m == nil {
		t.Fatal("New returned nil Metrics")
	}

	// Verify we can gather without error and see our metric families.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	want := map[string]bool{
		"relay_requests_total":            false,
		"relay_request_duration_seconds":  false,
		"relay_tokens_total":              false,
		"relay_rate_limit_hits_total":     false,
		"relay_circuit_breaker_state":     false,
		"relay_upstream_retries_total":    false,
		"relay_dropped_log_entries_total": false,
		"relay_active_streams":            false,
	}
	for _, mf := range mfs {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	// Counters/histograms with no observations may not appear in Gather() until
	// touched; touch them all to ensure they register without error.
	m.RequestsTotal.WithLabelValues("p", "m", "200").Add(0)
	m.RequestDuration.WithLabelValues("p", "m").Observe(0)
	m.TokensTotal.WithLabelValues("p", "m", "prompt").Add(0)
	m.RateLimitHits.WithLabelValues("key").Add(0)
	m.CircuitBreakerState.WithLabelValues("p").Set(0)
	m.UpstreamRetries.WithLabelValues("p").Add(0)
	m.DroppedLogEntries.Add(0)
	m.ActiveStreams.Set(0)

	mfs, _ = reg.Gather()
	for _, mf := range mfs {
		want[mf.GetName()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("metric %q not found in registry after touch", name)
		}
	}
}

func TestNewErrorOnDoubleRegister(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(reg); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := New(reg); err == nil {
		t.Fatal("second New with same registry should return error, got nil")
	}
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func TestMiddlewareRecordsRequestsAndDuration(t *testing.T) {
	tests := []struct {
		name          string
		handlerStatus int
		wantStatus    string
		wantCount     float64
		wantDurObs    uint64
	}{
		{name: "200 OK", handlerStatus: http.StatusOK, wantStatus: "200", wantCount: 1, wantDurObs: 1},
		{name: "429 TooMany", handlerStatus: http.StatusTooManyRequests, wantStatus: "429", wantCount: 1, wantDurObs: 1},
		{name: "503 Unavailable", handlerStatus: http.StatusServiceUnavailable, wantStatus: "503", wantCount: 1, wantDurObs: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, m := newRegistry(t)

			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.handlerStatus)
			})

			// The handler calls ContextWithProvider / ContextWithModel on the
			// bag-injected context that Middleware provides.
			handler := Middleware(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ContextWithProvider(r.Context(), "mock")
				ContextWithModel(r.Context(), "test-model")
				inner.ServeHTTP(w, r)
			}))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			handler.ServeHTTP(rec, req)

			labels := prometheus.Labels{"provider": "mock", "model": "test-model", "status": tc.wantStatus}
			got := counterVecValue(t, m.RequestsTotal, labels)
			if got != tc.wantCount {
				t.Errorf("RequestsTotal = %v, want %v", got, tc.wantCount)
			}

			durLabels := prometheus.Labels{"provider": "mock", "model": "test-model"}
			durCount := histogramCount(t, m.RequestDuration, durLabels)
			if durCount != tc.wantDurObs {
				t.Errorf("RequestDuration observations = %v, want %v", durCount, tc.wantDurObs)
			}
		})
	}
}

func TestMiddlewareDefaultLabelsWhenContextMissing(t *testing.T) {
	_, m := newRegistry(t)

	handler := Middleware(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	handler.ServeHTTP(rec, req)

	labels := prometheus.Labels{"provider": "unknown", "model": "unknown", "status": "200"}
	got := counterVecValue(t, m.RequestsTotal, labels)
	if got != 1 {
		t.Errorf("RequestsTotal with unknown labels = %v, want 1", got)
	}
}

func TestMiddlewareMultipleRequestsAccumulate(t *testing.T) {
	_, m := newRegistry(t)

	// Each request sets provider+model via the mutable bag injected by Middleware.
	handler := Middleware(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ContextWithProvider(r.Context(), "ollama")
		ContextWithModel(r.Context(), "llama3")
		w.WriteHeader(http.StatusOK)
	}))

	const n = 5
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		handler.ServeHTTP(rec, req)
	}

	labels := prometheus.Labels{"provider": "ollama", "model": "llama3", "status": "200"}
	got := counterVecValue(t, m.RequestsTotal, labels)
	if got != float64(n) {
		t.Errorf("RequestsTotal after %d requests = %v, want %v", n, got, float64(n))
	}
}

// ─── /metrics endpoint ────────────────────────────────────────────────────────

func TestMetricsEndpointExposesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := New(reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Touch every collector so it has at least one time series; Prometheus
	// only emits metric families that have been observed at least once.
	m.RequestsTotal.WithLabelValues("mock", "test-model", "200").Inc()
	m.RequestDuration.WithLabelValues("mock", "test-model").Observe(0.001)
	m.TokensTotal.WithLabelValues("mock", "test-model", "prompt").Add(10)
	m.RateLimitHits.WithLabelValues("abc123").Inc()
	m.CircuitBreakerState.WithLabelValues("mock").Set(0)
	m.UpstreamRetries.WithLabelValues("mock").Inc()
	m.DroppedLogEntries.Inc()
	m.ActiveStreams.Set(1)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Read the body.
	var buf strings.Builder
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := buf.String()

	wantSnippets := []string{
		"relay_requests_total",
		"relay_request_duration_seconds",
		"relay_tokens_total",
		"relay_rate_limit_hits_total",
		"relay_circuit_breaker_state",
		"relay_upstream_retries_total",
		"relay_dropped_log_entries_total",
		"relay_active_streams",
	}
	for _, s := range wantSnippets {
		if !strings.Contains(body, s) {
			t.Errorf("/metrics body missing %q", s)
		}
	}
}

// ─── Context helpers ──────────────────────────────────────────────────────────

func TestContextHelpers(t *testing.T) {
	// Without a bag in context, helpers return "unknown" and writes are no-ops.
	ctx := context.Background()

	if got := ProviderFromContext(ctx); got != "unknown" {
		t.Errorf("ProviderFromContext on empty ctx = %q, want %q", got, "unknown")
	}
	if got := ModelFromContext(ctx); got != "unknown" {
		t.Errorf("ModelFromContext on empty ctx = %q, want %q", got, "unknown")
	}

	// With a bag injected (as the middleware would do), writes are visible.
	ctx, _ = contextWithLabels(ctx)
	ContextWithProvider(ctx, "ollama")
	ContextWithModel(ctx, "llama3")

	if got := ProviderFromContext(ctx); got != "ollama" {
		t.Errorf("ProviderFromContext = %q, want %q", got, "ollama")
	}
	if got := ModelFromContext(ctx); got != "llama3" {
		t.Errorf("ModelFromContext = %q, want %q", got, "llama3")
	}
}

// ─── CircuitBreakerState gauge ────────────────────────────────────────────────

func TestCircuitBreakerStateGauge(t *testing.T) {
	tests := []struct {
		name    string
		state   float64
		wantVal float64
	}{
		{name: "closed", state: 0, wantVal: 0},
		{name: "half-open", state: 1, wantVal: 1},
		{name: "open", state: 2, wantVal: 2},
	}

	_, m := newRegistry(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.CircuitBreakerState.WithLabelValues("mock").Set(tc.state)
			got := gaugeVecValue(t, m.CircuitBreakerState, prometheus.Labels{"provider": "mock"})
			if got != tc.wantVal {
				t.Errorf("CircuitBreakerState = %v, want %v", got, tc.wantVal)
			}
		})
	}
}

// ─── RateLimitHits counter ────────────────────────────────────────────────────

func TestRateLimitHitsCounter(t *testing.T) {
	_, m := newRegistry(t)
	const keyHash = "abc123"
	const hits = 7

	for i := 0; i < hits; i++ {
		m.RateLimitHits.WithLabelValues(keyHash).Inc()
	}

	got := counterVecValue(t, m.RateLimitHits, prometheus.Labels{"key_hash": keyHash})
	if got != float64(hits) {
		t.Errorf("RateLimitHits = %v, want %v", got, float64(hits))
	}
}

// ─── AsyncLogger drop counter integration ────────────────────────────────────

type noopInserter struct{}

func (noopInserter) InsertRequestLogBatch(_ context.Context, _ []store.RequestLogEntry) error {
	return nil
}

func TestAsyncLoggerDropCounterPrometheus(t *testing.T) {
	_, m := newRegistry(t)

	const bufSize = 3
	logger := NewAsyncLogger(noopInserter{}, bufSize, 10, time.Hour).
		WithDropCounter(m.DroppedLogEntries)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger.Start(ctx)

	// Fill the buffer beyond its capacity so drops occur.
	const total = bufSize + 5
	for i := 0; i < total; i++ {
		logger.Log(store.RequestLogEntry{})
	}

	// Give the drainer goroutine a moment to process some entries.
	time.Sleep(20 * time.Millisecond)
	logger.Stop()

	dropped := counterValue(t, m.DroppedLogEntries)
	if dropped == 0 {
		t.Errorf("DroppedLogEntries = 0; expected > 0 after overfilling buffer (total=%d, bufSize=%d)", total, bufSize)
	}
	if dropped > float64(total-bufSize)+1 {
		t.Errorf("DroppedLogEntries = %v; too high (max expected ~%d)", dropped, total-bufSize)
	}
}

// ─── TokensTotal counter ──────────────────────────────────────────────────────

func TestTokensTotalCounter(t *testing.T) {
	_, m := newRegistry(t)

	m.TokensTotal.WithLabelValues("ollama", "llama3", "prompt").Add(100)
	m.TokensTotal.WithLabelValues("ollama", "llama3", "completion").Add(50)

	got := counterVecValue(t, m.TokensTotal, prometheus.Labels{"provider": "ollama", "model": "llama3", "kind": "prompt"})
	if got != 100 {
		t.Errorf("prompt tokens = %v, want 100", got)
	}
	got = counterVecValue(t, m.TokensTotal, prometheus.Labels{"provider": "ollama", "model": "llama3", "kind": "completion"})
	if got != 50 {
		t.Errorf("completion tokens = %v, want 50", got)
	}
}

// ─── ActiveStreams gauge ──────────────────────────────────────────────────────

func TestActiveStreamsGauge(t *testing.T) {
	_, m := newRegistry(t)

	pb := &dto.Metric{}

	m.ActiveStreams.Inc()
	m.ActiveStreams.Inc()
	m.ActiveStreams.Dec()

	if err := m.ActiveStreams.Write(pb); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := pb.GetGauge().GetValue(); got != 1 {
		t.Errorf("ActiveStreams = %v, want 1", got)
	}
}

// ─── UpstreamRetries counter ──────────────────────────────────────────────────

func TestUpstreamRetriesCounter(t *testing.T) {
	_, m := newRegistry(t)

	m.UpstreamRetries.WithLabelValues("ollama").Add(3)
	m.UpstreamRetries.WithLabelValues("mock").Add(1)

	got := counterVecValue(t, m.UpstreamRetries, prometheus.Labels{"provider": "ollama"})
	if got != 3 {
		t.Errorf("UpstreamRetries[ollama] = %v, want 3", got)
	}
	got = counterVecValue(t, m.UpstreamRetries, prometheus.Labels{"provider": "mock"})
	if got != 1 {
		t.Errorf("UpstreamRetries[mock] = %v, want 1", got)
	}
}
