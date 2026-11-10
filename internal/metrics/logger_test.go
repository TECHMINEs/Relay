package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/store"
)

// ─── Stub inserter ────────────────────────────────────────────────────────────

type stubInserter struct {
	mu      sync.Mutex
	batches [][]store.RequestLogEntry
}

func (s *stubInserter) InsertRequestLogBatch(_ context.Context, entries []store.RequestLogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]store.RequestLogEntry, len(entries))
	copy(cp, entries)
	s.batches = append(s.batches, cp)
	return nil
}

func (s *stubInserter) totalInserted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		n += len(b)
	}
	return n
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestAsyncLoggerBatchInsert(t *testing.T) {
	ins := &stubInserter{}
	al := NewAsyncLogger(ins, 100, 5, 50*time.Millisecond)
	ctx := context.Background()
	al.Start(ctx)

	for i := 0; i < 12; i++ {
		al.Log(store.RequestLogEntry{Model: "test", TotalTokens: i})
	}

	// Wait for at least 2 batches (10 entries) to flush.
	time.Sleep(200 * time.Millisecond)
	al.Stop()
	// Give the drain goroutine a moment to finish.
	time.Sleep(50 * time.Millisecond)

	total := ins.totalInserted()
	if total != 12 {
		t.Errorf("expected 12 entries inserted, got %d", total)
	}
}

func TestAsyncLoggerDropsWhenFull(t *testing.T) {
	ins := &stubInserter{}
	// Buffer of 2, very slow flush to force drops.
	al := NewAsyncLogger(ins, 2, 10, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	al.Start(ctx)

	// Reset global counter.
	Global.DroppedLogEntries.Store(0)

	// Send 10 entries into a buffer of size 2 without the logger running its loop.
	for i := 0; i < 10; i++ {
		al.Log(store.RequestLogEntry{Model: "overflow"})
	}

	dropped := Global.DroppedLogEntries.Load()
	if dropped == 0 {
		t.Error("expected at least one dropped log entry")
	}
	t.Logf("dropped %d entries (buffer size 2, sent 10)", dropped)
}

func TestAsyncLoggerFlushInterval(t *testing.T) {
	ins := &stubInserter{}
	al := NewAsyncLogger(ins, 100, 1000, 30*time.Millisecond) // batch=1000 but flush=30ms
	ctx := context.Background()
	al.Start(ctx)
	defer al.Stop()

	al.Log(store.RequestLogEntry{Model: "flush-test"})
	// Flush interval should trigger within 60ms.
	time.Sleep(80 * time.Millisecond)

	if ins.totalInserted() != 1 {
		t.Errorf("expected 1 entry flushed by interval, got %d", ins.totalInserted())
	}
}

func TestAsyncLoggerConcurrentSafety(t *testing.T) {
	ins := &stubInserter{}
	al := NewAsyncLogger(ins, 1000, 10, 5*time.Millisecond)
	ctx := context.Background()
	al.Start(ctx)

	var wg sync.WaitGroup
	var sent atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			al.Log(store.RequestLogEntry{Model: "concurrent"})
			sent.Add(1)
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)
	al.Stop()
	time.Sleep(30 * time.Millisecond)

	total := ins.totalInserted() + int(Global.DroppedLogEntries.Load())
	if total < int(sent.Load()) {
		t.Errorf("some entries neither inserted nor counted as dropped: sent=%d accounted=%d",
			sent.Load(), total)
	}
}
