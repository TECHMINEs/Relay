// Package router implements a weighted, circuit-breaker-aware provider router
// with retry logic using exponential backoff and full jitter.
//
// Request flow:
//
//	Router.Chat / Router.ChatStream
//	  └─► pickProvider()          – weighted random, skips open breakers
//	        └─► RetryPolicy.Do()  – per-attempt timeout + overall deadline
//	              └─► provider.Chat / provider.ChatStream
//	                    └─► breaker.RecordSuccess / RecordFailure
//
// Streaming calls are never retried after the first chunk has been received;
// the retry gate is the error return of ChatStream itself.
package router

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"time"

	"github.com/TECHMINEs/Relay/internal/breaker"
	"github.com/TECHMINEs/Relay/internal/provider"
)

// ErrAllBreakerOpen is returned when every registered provider's circuit
// breaker is in the Open state.
var ErrAllBreakerOpen = errors.New("all provider circuit breakers are open")

// RetryableError wraps an upstream error that is eligible for retry.
type RetryableError struct {
	Cause error
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v", e.Cause)
}

func (e *RetryableError) Unwrap() error { return e.Cause }

// isRetryable returns true for transient errors that warrant a retry:
// connection failures, simulated provider errors, per-attempt deadline
// exceeded (but NOT the overall parent context deadline or cancellation).
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Never retry client-side cancellations.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Retryable: provider connection errors, 5xx-class simulation, or breaker
	// open (we'll pick a different provider on next attempt).
	if errors.Is(err, provider.ErrProviderConnection) ||
		errors.Is(err, provider.ErrSimulatedFailure) ||
		errors.Is(err, breaker.ErrOpen) {
		return true
	}
	// Retryable: per-attempt deadline exceeded (wrapped as DeadlineExceeded
	// when only the attempt-level context has timed out).
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// RetryConfig holds retry and backoff tuning parameters.
type RetryConfig struct {
	// MaxAttempts is the total number of attempts (1 = no retries).
	MaxAttempts int
	// AttemptTimeout is the per-attempt deadline.  Set to 0 to inherit the
	// parent context deadline without adding an extra per-attempt limit.
	AttemptTimeout time.Duration
	// BaseDelay is the starting backoff interval.
	BaseDelay time.Duration
	// MaxDelay caps the computed exponential backoff interval.
	MaxDelay time.Duration
}

// DefaultRetryConfig returns a RetryConfig suitable for production use.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts:    3,
		AttemptTimeout: 30 * time.Second,
		BaseDelay:      100 * time.Millisecond,
		MaxDelay:       30 * time.Second,
	}
}

// jitteredBackoff returns a random duration in [0, min(maxDelay, base*2^attempt)].
// This is the "full jitter" strategy from the AWS blog on exponential backoff.
func jitteredBackoff(attempt int, base, maxDelay time.Duration) time.Duration {
	exp := math.Pow(2, float64(attempt))
	cap := time.Duration(float64(base) * exp)
	if cap > maxDelay {
		cap = maxDelay
	}
	if cap <= 0 {
		return 0
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(cap)))
	return time.Duration(n.Int64())
}

// ProviderEntry bundles a Provider with its routing weight and circuit breaker.
type ProviderEntry struct {
	// Provider is the upstream LLM backend.
	Provider provider.Provider
	// Weight controls the relative probability of selection (higher = more
	// likely).  Must be > 0.
	Weight int
	// Breaker guards the provider against cascading failure.
	Breaker *breaker.CircuitBreaker
}

// Router is a circuit-breaker-aware, weighted-random provider selector with
// integrated retry-with-backoff.
type Router struct {
	entries []ProviderEntry
	retry   RetryConfig
}

// New creates a Router from the supplied entries and retry config.  At least
// one entry must be provided and all weights must be positive.
func New(entries []ProviderEntry, retry RetryConfig) (*Router, error) {
	if len(entries) == 0 {
		return nil, errors.New("router: at least one provider entry is required")
	}
	for i, e := range entries {
		if e.Weight <= 0 {
			return nil, fmt.Errorf("router: entry[%d] has non-positive weight %d", i, e.Weight)
		}
		if e.Breaker == nil {
			return nil, fmt.Errorf("router: entry[%d] has nil circuit breaker", i)
		}
		if e.Provider == nil {
			return nil, fmt.Errorf("router: entry[%d] has nil provider", i)
		}
	}
	return &Router{entries: entries, retry: retry}, nil
}

// Name satisfies the provider.Provider interface so Router can itself be
// used wherever a Provider is expected.
func (rt *Router) Name() string { return "router" }

// SetErrorRate forwards to any underlying providers implementing SetErrorRate.
func (rt *Router) SetErrorRate(rate float64) {
	type errorRater interface {
		SetErrorRate(rate float64)
	}
	for _, e := range rt.entries {
		if er, ok := e.Provider.(errorRater); ok {
			er.SetErrorRate(rate)
		}
	}
}

// pickEntry selects an entry from those whose circuit breaker is not open.
// Selection is weighted-random.
//
// The algorithm is intentionally two-phase to avoid leaking half-open probe
// permits to providers that are never selected:
//
//  1. Eligibility pass — use Breaker.State() (no permit consumed) to build the
//     weighted candidate list.  Entries whose breaker is StateOpen are skipped.
//  2. Weighted random pick — choose one entry from the list.
//  3. Permit acquisition — call Allow() on the selected entry only.  If the
//     breaker state changed between steps 1 and 3 (e.g. another goroutine just
//     consumed the last half-open probe), Allow() returns ErrOpen, which the
//     caller can treat as a transient failure and retry.
//
// Returns ErrAllBreakerOpen if no eligible entry exists.
func (rt *Router) pickEntry() (*ProviderEntry, error) {
	type candidate struct {
		idx  int
		cumW int
	}

	// Phase 1: build candidate list using State() — no permits consumed.
	var candidates []candidate
	total := 0
	for i := range rt.entries {
		e := &rt.entries[i]
		if e.Breaker.State() != breaker.StateOpen {
			total += e.Weight
			candidates = append(candidates, candidate{idx: i, cumW: total})
		}
	}

	if len(candidates) == 0 {
		return nil, ErrAllBreakerOpen
	}

	// Phase 2: weighted random pick.
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(total)))
	pick := int(n.Int64()) + 1 // [1, total]

	var selected *ProviderEntry
	for _, c := range candidates {
		if pick <= c.cumW {
			selected = &rt.entries[c.idx]
			break
		}
	}
	if selected == nil {
		// Guards against floating-point edge cases in the random draw.
		selected = &rt.entries[candidates[len(candidates)-1].idx]
	}

	// Phase 3: acquire permit from the selected entry only.
	// If the breaker transitioned to Open between phases 1 and 3 (rare race),
	// Allow() returns ErrOpen, which isRetryable() treats as retryable so the
	// caller will attempt pickEntry() again on the next iteration.
	if err := selected.Breaker.Allow(); err != nil {
		return nil, err
	}

	return selected, nil
}

// RetryAfter returns the number of seconds until at least one breaker reopens,
// or 0 if any breaker is already available.
func (rt *Router) RetryAfter() int {
	for i := range rt.entries {
		if rt.entries[i].Breaker.State() != breaker.StateOpen {
			return 0
		}
	}
	var maxSec int
	for i := range rt.entries {
		rem := rt.entries[i].Breaker.CooldownRemaining()
		secs := int(math.Ceil(rem.Seconds()))
		if secs > maxSec {
			maxSec = secs
		}
	}
	return maxSec
}

// doWithRetry runs fn with per-attempt timeouts and exponential-backoff retry.
// fn should return (nil, nil) on success; retryable errors are wrapped in
// RetryableError by the caller if needed — this method checks isRetryable.
func (rt *Router) doWithRetry(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < rt.retry.MaxAttempts; attempt++ {
		var (
			attemptCtx    context.Context
			attemptCancel context.CancelFunc
		)
		if rt.retry.AttemptTimeout > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(ctx, rt.retry.AttemptTimeout)
		} else {
			attemptCtx, attemptCancel = context.WithCancel(ctx)
		}

		err := fn(attemptCtx)
		attemptCancel()

		if err == nil {
			return nil
		}

		lastErr = err

		// Never retry once the client has gone or the overall deadline has passed.
		if ctx.Err() != nil {
			return lastErr
		}

		if !isRetryable(err) {
			return lastErr
		}

		if attempt+1 >= rt.retry.MaxAttempts {
			break
		}

		wait := jitteredBackoff(attempt, rt.retry.BaseDelay, rt.retry.MaxDelay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}

// Chat sends a non-streaming chat completion, routing to a weighted random
// available provider, with retries and circuit-breaker integration.
func (rt *Router) Chat(ctx context.Context, req *provider.ChatRequest) (*provider.Response, error) {
	var result *provider.Response

	err := rt.doWithRetry(ctx, func(attemptCtx context.Context) error {
		entry, err := rt.pickEntry()
		if err != nil {
			return err
		}

		resp, callErr := entry.Provider.Chat(attemptCtx, req)
		if callErr != nil {
			entry.Breaker.RecordFailure()
			return callErr
		}

		entry.Breaker.RecordSuccess()
		result = resp
		return nil
	})

	return result, err
}

// ChatStream initiates a streaming chat completion.  The retry policy applies
// ONLY to the initial channel-establishment call (the error return of
// provider.ChatStream).  Once a channel is returned successfully, no retry is
// performed because bytes have been committed to the transport.
//
// Critically, this method does NOT use doWithRetry.  doWithRetry calls
// attemptCancel() immediately after fn returns — which would cancel the
// upstream context while the stream goroutine is still draining chunks from
// the provider.  Instead, ChatStream has its own retry loop that passes the
// parent ctx directly to provider.ChatStream so the stream connection remains
// alive until the client disconnects or the overall deadline fires.
func (rt *Router) ChatStream(ctx context.Context, req *provider.ChatRequest) (<-chan provider.Chunk, error) {
	var lastErr error

	for attempt := 0; attempt < rt.retry.MaxAttempts; attempt++ {
		// Bail out immediately if the parent context is already done.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		entry, pickErr := rt.pickEntry()
		if pickErr != nil {
			lastErr = pickErr
			if !isRetryable(pickErr) || attempt+1 >= rt.retry.MaxAttempts {
				break
			}
			wait := jitteredBackoff(attempt, rt.retry.BaseDelay, rt.retry.MaxDelay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		// Pass the PARENT ctx — not a per-attempt timeout context — so the
		// upstream stream connection is not cancelled when this function
		// returns.  The overall client deadline still bounds the stream.
		ch, callErr := entry.Provider.ChatStream(ctx, req)
		if callErr != nil {
			entry.Breaker.RecordFailure()
			lastErr = callErr
			if !isRetryable(callErr) || ctx.Err() != nil || attempt+1 >= rt.retry.MaxAttempts {
				break
			}
			wait := jitteredBackoff(attempt, rt.retry.BaseDelay, rt.retry.MaxDelay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		// Channel established — once here we never retry.  Wrap the channel
		// to record the circuit-breaker outcome when the stream completes.
		return rt.wrapStreamWithBreaker(ch, entry, ctx), nil
	}

	if lastErr == nil {
		lastErr = ErrAllBreakerOpen
	}
	return nil, lastErr
}

// wrapStreamWithBreaker drains ch from the upstream provider and re-emits
// chunks on a new channel.  It records a circuit-breaker outcome on the first
// error chunk or when the stream closes normally.  ctx should be the overall
// request context, not the per-attempt context.
func (rt *Router) wrapStreamWithBreaker(ch <-chan provider.Chunk, entry *ProviderEntry, ctx context.Context) <-chan provider.Chunk {
	out := make(chan provider.Chunk, 16)
	go func() {
		defer close(out)
		success := true
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				if !ok {
					if success {
						entry.Breaker.RecordSuccess()
					}
					return
				}
				if chunk.Err != nil {
					success = false
					entry.Breaker.RecordFailure()
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// Embeddings sends an embeddings request with routing, circuit-breaker, and
// retry logic.
func (rt *Router) Embeddings(ctx context.Context, req *provider.EmbeddingRequest) (*provider.EmbeddingResponse, error) {
	var result *provider.EmbeddingResponse

	err := rt.doWithRetry(ctx, func(attemptCtx context.Context) error {
		entry, err := rt.pickEntry()
		if err != nil {
			return err
		}

		resp, callErr := entry.Provider.Embeddings(attemptCtx, req)
		if callErr != nil {
			entry.Breaker.RecordFailure()
			return callErr
		}

		entry.Breaker.RecordSuccess()
		result = resp
		return nil
	})

	return result, err
}

// HTTPStatusFor maps a router error to an appropriate HTTP status code.
func HTTPStatusFor(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, ""
	case errors.Is(err, ErrAllBreakerOpen):
		return http.StatusServiceUnavailable, "all_providers_unavailable"
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "request_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "deadline_exceeded"
	default:
		return http.StatusBadGateway, "upstream_error"
	}
}
