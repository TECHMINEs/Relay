// Package breaker implements a mutex-safe circuit breaker state machine.
//
// The breaker transitions through three states:
//
//	Closed  ──(failure rate ≥ threshold)──►  Open
//	Open    ──(cooldown elapsed)──────────►  HalfOpen
//	HalfOpen──(probe succeeds)────────────►  Closed
//	HalfOpen──(probe fails)───────────────►  Open
//
// Failure rate is measured over a sliding ring-buffer window.  All state reads
// and writes are protected by a single mutex, so concurrent requests during the
// HalfOpen transition cannot cause races.
package breaker

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrOpen is returned by Allow when the breaker is open and no cooldown has
// elapsed, or when too many half-open probes are already in flight.
var ErrOpen = errors.New("circuit breaker open")

// State represents the current state of the circuit breaker.
type State int8

const (
	// StateClosed is the normal operating state; all requests pass through.
	StateClosed State = iota
	// StateOpen rejects all requests until the cooldown has elapsed.
	StateOpen
	// StateHalfOpen allows a limited number of probe requests to test recovery.
	StateHalfOpen
)

// String returns a human-readable state name.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Config holds the tuning parameters for a CircuitBreaker.
type Config struct {
	// WindowSize is the number of outcomes tracked by the sliding ring buffer.
	WindowSize int
	// FailureThreshold is the minimum fraction of failures [0,1) that trips
	// the breaker from Closed to Open.
	FailureThreshold float64
	// MinRequests is the minimum number of recorded outcomes before the failure
	// rate is evaluated.  Prevents tripping on a single early failure.
	MinRequests int
	// Cooldown is how long the breaker stays Open before moving to HalfOpen.
	Cooldown time.Duration
	// MaxHalfOpenProbes is the maximum number of concurrent probe requests
	// allowed while in the HalfOpen state.
	MaxHalfOpenProbes int
}

// DefaultConfig returns a Config with sensible production defaults.
func DefaultConfig() Config {
	return Config{
		WindowSize:        20,
		FailureThreshold:  0.5,
		MinRequests:       5,
		Cooldown:          10 * time.Second,
		MaxHalfOpenProbes: 1,
	}
}

// outcome represents a single recorded call result.
type outcome bool

const (
	outcomeSuccess outcome = false
	outcomeFailure outcome = true
)

// CircuitBreaker is a thread-safe, three-state circuit breaker.
type CircuitBreaker struct {
	mu       sync.Mutex
	cfg      Config
	state    State
	openedAt time.Time // when we entered Open state

	// ring buffer tracking recent call outcomes
	ring []outcome
	pos  int // next write position
	fill int // how many slots are filled (≤ WindowSize)

	// half-open probe tracking
	probesInFlight int
}

// New creates a new CircuitBreaker with the supplied configuration.
func New(cfg Config) *CircuitBreaker {
	if cfg.WindowSize <= 0 {
		cfg.WindowSize = 20
	}
	if cfg.MaxHalfOpenProbes <= 0 {
		cfg.MaxHalfOpenProbes = 1
	}
	return &CircuitBreaker{
		cfg:  cfg,
		ring: make([]outcome, cfg.WindowSize),
	}
}

// State returns the current state of the breaker.  Safe for concurrent use.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentState()
}

// currentState evaluates the actual state, including automatic Open→HalfOpen
// transition once the cooldown has elapsed.  Must be called with cb.mu held.
func (cb *CircuitBreaker) currentState() State {
	if cb.state == StateOpen && time.Since(cb.openedAt) >= cb.cfg.Cooldown {
		cb.state = StateHalfOpen
		cb.probesInFlight = 0
	}
	return cb.state
}

// CooldownRemaining returns the approximate duration until the breaker leaves
// the Open state.  Returns 0 if not open or if cooldown has already elapsed.
func (cb *CircuitBreaker) CooldownRemaining() time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state != StateOpen {
		return 0
	}
	remaining := cb.cfg.Cooldown - time.Since(cb.openedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Allow returns nil if a request should be allowed through, or ErrOpen if the
// breaker is open (or HalfOpen but the probe limit is already reached).
//
// If nil is returned the caller MUST call either RecordSuccess or
// RecordFailure to maintain the ring-buffer invariants.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.currentState() {
	case StateClosed:
		return nil

	case StateOpen:
		return ErrOpen

	case StateHalfOpen:
		if cb.probesInFlight >= cb.cfg.MaxHalfOpenProbes {
			return ErrOpen
		}
		cb.probesInFlight++
		return nil

	default:
		return fmt.Errorf("unknown breaker state: %v", cb.state)
	}
}

// RecordSuccess records a successful call outcome.  If in HalfOpen state, a
// success immediately transitions the breaker back to Closed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.transitionClosed()
		return
	}

	cb.record(outcomeSuccess)
}

// RecordFailure records a failed call outcome.  If in HalfOpen state, the
// breaker immediately transitions back to Open.  In Closed state the failure
// rate is re-evaluated against the threshold.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.transitionOpen()
		return
	}

	cb.record(outcomeFailure)
	cb.evaluateThreshold()
}

// record writes an outcome into the ring buffer.  Must be called with mu held.
func (cb *CircuitBreaker) record(o outcome) {
	cb.ring[cb.pos%cb.cfg.WindowSize] = o
	cb.pos++
	if cb.fill < cb.cfg.WindowSize {
		cb.fill++
	}
}

// evaluateThreshold checks whether the failure rate has exceeded the
// configured threshold and, if so, trips the breaker.  Must be called with
// mu held.
func (cb *CircuitBreaker) evaluateThreshold() {
	if cb.fill < cb.cfg.MinRequests {
		return
	}

	var failures int
	for i := 0; i < cb.fill; i++ {
		if cb.ring[i] == outcomeFailure {
			failures++
		}
	}

	rate := float64(failures) / float64(cb.fill)
	if rate >= cb.cfg.FailureThreshold {
		cb.transitionOpen()
	}
}

// transitionOpen moves to the Open state and stamps the open timestamp.
// Must be called with mu held.
func (cb *CircuitBreaker) transitionOpen() {
	cb.state = StateOpen
	cb.openedAt = time.Now()
	cb.probesInFlight = 0
	cb.resetWindow()
}

// transitionClosed moves to the Closed state and resets all tracking.
// Must be called with mu held.
func (cb *CircuitBreaker) transitionClosed() {
	cb.state = StateClosed
	cb.probesInFlight = 0
	cb.resetWindow()
}

// resetWindow clears the ring buffer.  Must be called with mu held.
func (cb *CircuitBreaker) resetWindow() {
	cb.ring = make([]outcome, cb.cfg.WindowSize)
	cb.pos = 0
	cb.fill = 0
}
