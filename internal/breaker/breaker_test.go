package breaker

import (
	"sync"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

func newTestCB(windowSize, minReq int, threshold float64, cooldown time.Duration, maxProbes int) *CircuitBreaker {
	return New(Config{
		WindowSize:        windowSize,
		FailureThreshold:  threshold,
		MinRequests:       minReq,
		Cooldown:          cooldown,
		MaxHalfOpenProbes: maxProbes,
	})
}

// recordN records n successes or failures on cb.
func recordN(cb *CircuitBreaker, n int, fail bool) {
	for i := 0; i < n; i++ {
		if fail {
			cb.RecordFailure()
		} else {
			cb.RecordSuccess()
		}
	}
}

// ----------------------------------------------------------------------------
// State-machine transitions
// ----------------------------------------------------------------------------

func TestInitialStateClosed(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, time.Second, 1)
	if s := cb.State(); s != StateClosed {
		t.Fatalf("expected Closed, got %s", s)
	}
}

func TestDoesNotTripBelowMinRequests(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, time.Second, 1)
	// 4 failures — below MinRequests of 5
	recordN(cb, 4, true)
	if s := cb.State(); s != StateClosed {
		t.Fatalf("expected Closed, got %s (should not trip below MinRequests)", s)
	}
}

func TestTripsWhenFailureRateExceedsThreshold(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, time.Second, 1)
	// 3 successes + 5 failures = 5/8 ≈ 62.5% ≥ 50%
	recordN(cb, 3, false)
	recordN(cb, 5, true)
	if s := cb.State(); s != StateOpen {
		t.Fatalf("expected Open, got %s", s)
	}
}

func TestRejectsRequestsWhenOpen(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, time.Second, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true)

	if err := cb.Allow(); err != ErrOpen {
		t.Fatalf("expected ErrOpen, got %v", err)
	}
}

func TestTransitionsToHalfOpenAfterCooldown(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, 20*time.Millisecond, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true) // trips to Open

	time.Sleep(30 * time.Millisecond) // cooldown expires
	// currentState() is called inside Allow / State
	if s := cb.State(); s != StateHalfOpen {
		t.Fatalf("expected HalfOpen after cooldown, got %s", s)
	}
}

func TestHalfOpenProbeLimitEnforced(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, 20*time.Millisecond, 2)
	recordN(cb, 3, false)
	recordN(cb, 5, true)
	time.Sleep(30 * time.Millisecond)

	// Two probes allowed
	if err := cb.Allow(); err != nil {
		t.Fatalf("first probe should be allowed, got %v", err)
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("second probe should be allowed, got %v", err)
	}
	// Third should be rejected
	if err := cb.Allow(); err != ErrOpen {
		t.Fatalf("third probe should get ErrOpen, got %v", err)
	}
}

func TestHalfOpenSuccessTransitionsToClosed(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, 20*time.Millisecond, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true)
	time.Sleep(30 * time.Millisecond)

	if err := cb.Allow(); err != nil {
		t.Fatalf("probe should be allowed: %v", err)
	}
	cb.RecordSuccess()

	if s := cb.State(); s != StateClosed {
		t.Fatalf("expected Closed after probe success, got %s", s)
	}
}

func TestHalfOpenFailureTransitionsToOpen(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, 20*time.Millisecond, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true)
	time.Sleep(30 * time.Millisecond)

	if err := cb.Allow(); err != nil {
		t.Fatalf("probe should be allowed: %v", err)
	}
	cb.RecordFailure()

	if s := cb.State(); s != StateOpen {
		t.Fatalf("expected Open after probe failure, got %s", s)
	}
}

func TestClosedAfterRecovery(t *testing.T) {
	cb := newTestCB(10, 5, 0.5, 20*time.Millisecond, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true) // Open
	time.Sleep(30 * time.Millisecond)
	_ = cb.Allow()
	cb.RecordSuccess() // Closed

	// Should now allow requests normally again
	for i := 0; i < 10; i++ {
		if err := cb.Allow(); err != nil {
			t.Fatalf("expected Allow after recovery, got %v (iter %d)", err, i)
		}
		cb.RecordSuccess()
	}
	if s := cb.State(); s != StateClosed {
		t.Fatalf("expected Closed, got %s", s)
	}
}

func TestWindowRingBufferEviction(t *testing.T) {
	// Window = 5, threshold = 0.5, minReq = 5
	cb := newTestCB(5, 5, 0.5, time.Second, 1)
	// Fill with 5 failures — trips open
	recordN(cb, 5, true)
	if s := cb.State(); s != StateOpen {
		t.Fatalf("expected Open, got %s", s)
	}

	// After recovery, old failures should be evicted
	cb2 := newTestCB(5, 5, 0.5, 20*time.Millisecond, 1)
	recordN(cb2, 3, false)
	recordN(cb2, 3, true) // ring now: [F,F,F,S,S] - 3/5 = 60% trips
	if s := cb2.State(); s != StateOpen {
		t.Fatalf("expected Open, got %s", s)
	}
	time.Sleep(30 * time.Millisecond)
	_ = cb2.Allow()
	cb2.RecordSuccess() // back to Closed, window reset

	// Now 4 successes — should not trip (only 4 results, below MinRequests?)
	recordN(cb2, 4, false)
	if s := cb2.State(); s != StateClosed {
		t.Fatalf("expected Closed after recovery with clean window, got %s", s)
	}
}

func TestCooldownRemaining(t *testing.T) {
	cooldown := 200 * time.Millisecond
	cb := newTestCB(10, 5, 0.5, cooldown, 1)
	recordN(cb, 3, false)
	recordN(cb, 5, true)

	rem := cb.CooldownRemaining()
	if rem <= 0 || rem > cooldown {
		t.Fatalf("expected positive cooldown remainder, got %v", rem)
	}
}

// ----------------------------------------------------------------------------
// Concurrency safety
// ----------------------------------------------------------------------------

func TestConcurrentAllowAndRecord(t *testing.T) {
	cb := newTestCB(50, 10, 0.6, 10*time.Millisecond, 2)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if cb.Allow() == nil {
				if i%3 == 0 {
					cb.RecordFailure()
				} else {
					cb.RecordSuccess()
				}
			}
		}(i)
	}
	wg.Wait()
	// No panic, no deadlock — state is valid
	s := cb.State()
	if s != StateClosed && s != StateOpen && s != StateHalfOpen {
		t.Fatalf("unexpected state: %v", s)
	}
}

func TestConcurrentHalfOpenProbeLimit(t *testing.T) {
	// Ensure at most maxProbes goroutines pass Allow() during HalfOpen.
	cb := newTestCB(10, 5, 0.5, 10*time.Millisecond, 3)
	recordN(cb, 3, false)
	recordN(cb, 5, true)
	time.Sleep(20 * time.Millisecond)

	var (
		wg      sync.WaitGroup
		allowed int64
		mu      sync.Mutex
	)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.Allow() == nil {
				mu.Lock()
				allowed++
				mu.Unlock()
				// Simulate slow probe without recording result
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	if allowed > 3 {
		t.Fatalf("expected at most 3 probes, %d were allowed", allowed)
	}
}
