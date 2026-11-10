package router

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/breaker"
	"github.com/TECHMINEs/Relay/internal/config"
	"github.com/TECHMINEs/Relay/internal/provider"
)

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

func newEntry(p provider.Provider, weight int, bcfg breaker.Config) ProviderEntry {
	return ProviderEntry{
		Provider: p,
		Weight:   weight,
		Breaker:  breaker.New(bcfg),
	}
}

// fastBreakerConfig returns a breaker that trips after 1 failure with a very
// short cooldown — useful for unit tests.
func fastBreakerConfig() breaker.Config {
	return breaker.Config{
		WindowSize:        5,
		FailureThreshold:  0.4,
		MinRequests:       1,
		Cooldown:          30 * time.Millisecond,
		MaxHalfOpenProbes: 1,
	}
}

// alwaysSucceedMock returns a mock provider that never fails or stalls.
func alwaysSucceedMock() *provider.MockProvider {
	return provider.NewMockProvider(&config.Config{})
}

// alwaysFailMock returns a mock provider with 100% error rate.
func alwaysFailMock() *provider.MockProvider {
	return provider.NewMockProvider(&config.Config{
		MockErrorRate: 1.0,
	})
}

func defaultRetry() RetryConfig {
	return RetryConfig{
		MaxAttempts:    3,
		AttemptTimeout: 5 * time.Second,
		BaseDelay:      5 * time.Millisecond,
		MaxDelay:       20 * time.Millisecond,
	}
}

// ----------------------------------------------------------------------------
// Constructor validation
// ----------------------------------------------------------------------------

func TestNewRouterRejectsEmpty(t *testing.T) {
	_, err := New(nil, DefaultRetryConfig())
	if err == nil {
		t.Fatal("expected error for empty entries")
	}
}

func TestNewRouterRejectsZeroWeight(t *testing.T) {
	p := alwaysSucceedMock()
	e := newEntry(p, 0, fastBreakerConfig())
	_, err := New([]ProviderEntry{e}, DefaultRetryConfig())
	if err == nil {
		t.Fatal("expected error for zero weight")
	}
}

func TestNewRouterRejectsNilBreaker(t *testing.T) {
	p := alwaysSucceedMock()
	e := ProviderEntry{Provider: p, Weight: 1, Breaker: nil}
	_, err := New([]ProviderEntry{e}, DefaultRetryConfig())
	if err == nil {
		t.Fatal("expected error for nil breaker")
	}
}

// ----------------------------------------------------------------------------
// Basic routing
// ----------------------------------------------------------------------------

func TestRouterSuccessfulChat(t *testing.T) {
	p := alwaysSucceedMock()
	rt, err := New([]ProviderEntry{newEntry(p, 10, fastBreakerConfig())}, defaultRetry())
	if err != nil {
		t.Fatal(err)
	}

	resp, err := rt.Chat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("unexpected object: %s", resp.Object)
	}
}

// ----------------------------------------------------------------------------
// Circuit breaker + failover
// ----------------------------------------------------------------------------

func TestRouterFailsoverToSecondProvider(t *testing.T) {
	fail := alwaysFailMock()
	succeed := alwaysSucceedMock()

	bCfg := breaker.Config{
		WindowSize:        5,
		FailureThreshold:  0.5,
		MinRequests:       1,
		Cooldown:          5 * time.Second,
		MaxHalfOpenProbes: 1,
	}

	// Give fail weight 1 and succeed weight 100 so retries land on succeed.
	rt, err := New([]ProviderEntry{
		newEntry(fail, 1, bCfg),
		newEntry(succeed, 100, bCfg),
	}, defaultRetry())
	if err != nil {
		t.Fatal(err)
	}

	// With retries the router should ultimately succeed via the second provider.
	resp, err := rt.Chat(context.Background(), &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected success via failover, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestRouterReturns503WhenAllBreakersOpen(t *testing.T) {
	fail := alwaysFailMock()

	bCfg := breaker.Config{
		WindowSize:        3,
		FailureThreshold:  0.4,
		MinRequests:       1,
		Cooldown:          5 * time.Second,
		MaxHalfOpenProbes: 1,
	}

	rt, err := New([]ProviderEntry{newEntry(fail, 1, bCfg)}, RetryConfig{
		MaxAttempts:    1, // no retries so breaker trips quickly
		AttemptTimeout: time.Second,
		BaseDelay:      5 * time.Millisecond,
		MaxDelay:       10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	}

	// Drive the breaker open with a series of failing calls.
	for i := 0; i < 5; i++ {
		_, _ = rt.Chat(context.Background(), req)
	}

	// Now all breakers should be open.
	_, err = rt.Chat(context.Background(), req)
	if !errors.Is(err, ErrAllBreakerOpen) {
		t.Fatalf("expected ErrAllBreakerOpen, got %v", err)
	}

	// HTTPStatusFor should map this to 503.
	status, errType := HTTPStatusFor(err)
	if status != 503 {
		t.Errorf("expected HTTP 503, got %d", status)
	}
	if errType != "all_providers_unavailable" {
		t.Errorf("expected errType all_providers_unavailable, got %s", errType)
	}
}

// ----------------------------------------------------------------------------
// Retry behaviour
// ----------------------------------------------------------------------------

func TestRetryOnTransientError(t *testing.T) {
	// A provider that fails exactly twice then succeeds.
	var callCount atomic.Int32

	stub := &callCountProvider{
		fn: func() error {
			n := callCount.Add(1)
			if n <= 2 {
				return provider.ErrSimulatedFailure
			}
			return nil
		},
	}

	bCfg := breaker.Config{
		WindowSize:        20,
		FailureThreshold:  0.9, // high threshold so breaker won't trip
		MinRequests:       10,
		Cooldown:          time.Second,
		MaxHalfOpenProbes: 1,
	}

	rt, err := New([]ProviderEntry{newEntry(stub, 1, bCfg)}, RetryConfig{
		MaxAttempts:    5,
		AttemptTimeout: time.Second,
		BaseDelay:      1 * time.Millisecond,
		MaxDelay:       5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := rt.Chat(context.Background(), &provider.ChatRequest{
		Model:    "test",
		Messages: []provider.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 calls (2 fail + 1 success), got %d", callCount.Load())
	}
}

func TestNonRetryableErrorNotRetried(t *testing.T) {
	var callCount atomic.Int32
	stub := &callCountProvider{
		fn: func() error {
			callCount.Add(1)
			return provider.ErrInvalidRequest // not retryable
		},
	}

	bCfg := breaker.Config{
		WindowSize: 20, FailureThreshold: 0.9, MinRequests: 10,
		Cooldown: time.Second, MaxHalfOpenProbes: 1,
	}
	rt, _ := New([]ProviderEntry{newEntry(stub, 1, bCfg)}, RetryConfig{
		MaxAttempts: 3, AttemptTimeout: time.Second,
		BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond,
	})

	_, err := rt.Chat(context.Background(), &provider.ChatRequest{
		Model: "test", Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if callCount.Load() != 1 {
		t.Errorf("expected exactly 1 call (no retry for non-retryable), got %d", callCount.Load())
	}
}

func TestOverallDeadlineRespectsContext(t *testing.T) {
	stall := provider.NewMockProvider(&config.Config{MockStallForever: true})

	bCfg := breaker.Config{
		WindowSize: 5, FailureThreshold: 0.9, MinRequests: 10,
		Cooldown: time.Second, MaxHalfOpenProbes: 1,
	}
	rt, _ := New([]ProviderEntry{newEntry(stall, 1, bCfg)}, RetryConfig{
		MaxAttempts:    3,
		AttemptTimeout: 5 * time.Second,
		BaseDelay:      1 * time.Millisecond,
		MaxDelay:       5 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := rt.Chat(ctx, &provider.ChatRequest{
		Model: "test", Messages: []provider.ChatMessage{{Role: "user", Content: "stall"}},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from stalled provider")
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("expected fast failure from context deadline, took %v", elapsed)
	}
}

// ----------------------------------------------------------------------------
// Chaos integration test: 50% error rate
// ----------------------------------------------------------------------------

func TestChaos50PercentErrorRate(t *testing.T) {
	// Use a mock with 50% error rate plus a reliable fallback.
	chaotic := provider.NewMockProvider(&config.Config{MockErrorRate: 0.5})
	reliable := provider.NewMockProvider(&config.Config{})

	bCfg := breaker.Config{
		WindowSize:        30,
		FailureThreshold:  0.8, // high threshold — we want it to survive
		MinRequests:       5,
		Cooldown:          50 * time.Millisecond,
		MaxHalfOpenProbes: 1,
	}

	rt, err := New([]ProviderEntry{
		{Provider: chaotic, Weight: 1, Breaker: breaker.New(bCfg)},
		{Provider: reliable, Weight: 10, Breaker: breaker.New(bCfg)},
	}, RetryConfig{
		MaxAttempts:    3,
		AttemptTimeout: 2 * time.Second,
		BaseDelay:      1 * time.Millisecond,
		MaxDelay:       10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := &provider.ChatRequest{
		Model:    "gpt-4",
		Messages: []provider.ChatMessage{{Role: "user", Content: "chaos"}},
	}

	successCount := 0
	const total = 50
	for i := 0; i < total; i++ {
		resp, err := rt.Chat(context.Background(), req)
		if err == nil && resp != nil {
			successCount++
		}
	}

	// With a reliable fallback and 3 retries, success rate should be very high.
	if successCount < total-3 {
		t.Errorf("expected at least %d/%d successes under 50%% chaos, got %d", total-3, total, successCount)
	}
}

// ----------------------------------------------------------------------------
// Mid-stream failure must NOT be retried
// ----------------------------------------------------------------------------

func TestMidStreamFailureNotRetried(t *testing.T) {
	// A provider whose stream returns successfully (channel is created) but
	// sends an error chunk mid-way through.
	var streamCalls atomic.Int32
	stub := &midStreamErrorProvider{
		callCount: &streamCalls,
	}

	bCfg := breaker.Config{
		WindowSize: 20, FailureThreshold: 0.9, MinRequests: 10,
		Cooldown: time.Second, MaxHalfOpenProbes: 1,
	}
	rt, _ := New([]ProviderEntry{newEntry(stub, 1, bCfg)}, RetryConfig{
		MaxAttempts:    3,
		AttemptTimeout: time.Second,
		BaseDelay:      1 * time.Millisecond,
		MaxDelay:       5 * time.Millisecond,
	})

	ch, err := rt.ChatStream(context.Background(), &provider.ChatRequest{
		Model: "test", Messages: []provider.ChatMessage{{Role: "user", Content: "stream"}},
	})

	// ChatStream itself must return nil error (channel established)
	if err != nil {
		t.Fatalf("expected nil error from ChatStream, got: %v", err)
	}

	// Drain the channel
	var errorChunks int
	for chunk := range ch {
		if chunk.Err != nil {
			errorChunks++
		}
	}

	// Should have received one error chunk
	if errorChunks == 0 {
		t.Error("expected at least one error chunk from mid-stream failure")
	}

	// CRITICAL: ChatStream should have been called exactly ONCE — no retry
	// because the channel was successfully established.
	if streamCalls.Load() != 1 {
		t.Errorf("expected exactly 1 stream call (no retry after channel established), got %d", streamCalls.Load())
	}
}

// ----------------------------------------------------------------------------
// Stub providers for unit-testing the router
// ----------------------------------------------------------------------------

// callCountProvider is a provider whose Chat behaviour is controlled by fn.
type callCountProvider struct {
	fn func() error
}

func (c *callCountProvider) Name() string { return "call-count-stub" }

func (c *callCountProvider) Chat(_ context.Context, req *provider.ChatRequest) (*provider.Response, error) {
	if err := c.fn(); err != nil {
		return nil, err
	}
	return &provider.Response{
		Object:  "chat.completion",
		Model:   req.Model,
		Choices: []provider.ChatChoice{{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
	}, nil
}

func (c *callCountProvider) ChatStream(_ context.Context, req *provider.ChatRequest) (<-chan provider.Chunk, error) {
	if err := c.fn(); err != nil {
		return nil, err
	}
	ch := make(chan provider.Chunk, 1)
	stop := "stop"
	ch <- provider.Chunk{
		Object: "chat.completion.chunk",
		Model:  req.Model,
		Choices: []provider.ChunkChoice{{
			Delta:        provider.ChunkDelta{Content: "ok"},
			FinishReason: &stop,
		}},
	}
	close(ch)
	return ch, nil
}

func (c *callCountProvider) Embeddings(_ context.Context, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	if err := c.fn(); err != nil {
		return nil, err
	}
	return &provider.EmbeddingResponse{Object: "list", Model: req.Model}, nil
}

// midStreamErrorProvider returns a channel successfully but sends an error chunk.
type midStreamErrorProvider struct {
	callCount *atomic.Int32
}

func (m *midStreamErrorProvider) Name() string { return "mid-stream-error-stub" }

func (m *midStreamErrorProvider) Chat(_ context.Context, req *provider.ChatRequest) (*provider.Response, error) {
	return &provider.Response{Object: "chat.completion", Model: req.Model}, nil
}

func (m *midStreamErrorProvider) ChatStream(_ context.Context, req *provider.ChatRequest) (<-chan provider.Chunk, error) {
	m.callCount.Add(1)
	ch := make(chan provider.Chunk, 3)
	stop := "stop"
	ch <- provider.Chunk{Object: "chat.completion.chunk", Model: req.Model,
		Choices: []provider.ChunkChoice{{Delta: provider.ChunkDelta{Role: "assistant"}}}}
	ch <- provider.Chunk{Object: "chat.completion.chunk", Model: req.Model,
		Choices: []provider.ChunkChoice{{Delta: provider.ChunkDelta{Content: "hello "},
			FinishReason: &stop}}}
	ch <- provider.Chunk{Err: errors.New("mid-stream upstream disconnected")}
	close(ch)
	return ch, nil
}

func (m *midStreamErrorProvider) Embeddings(_ context.Context, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	return &provider.EmbeddingResponse{Object: "list", Model: req.Model}, nil
}
