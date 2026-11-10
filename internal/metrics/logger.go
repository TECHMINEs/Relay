// Package metrics provides lightweight counters, Prometheus instrumentation,
// and an async request logger for the Relay gateway.
package metrics

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Counters holds simple atomic metrics used as a lightweight fallback when
// a Prometheus registry is not wired in (e.g. in some unit tests).
type Counters struct {
	DroppedLogEntries atomic.Int64 // log entries dropped because the buffer is full
	TotalRequests     atomic.Int64
	RateLimited       atomic.Int64
}

// Global is the process-wide atomic metrics store (used by logger when no
// Prometheus counter is configured).
var Global = &Counters{}

// Inserter is the persistence interface required by the async logger.
type Inserter interface {
	InsertRequestLogBatch(ctx context.Context, entries []store.RequestLogEntry) error
}

// AsyncLogger drains a buffered channel and batch-inserts into Postgres.
// Entries are dropped (not blocked on) when the channel is full; the drop
// event is recorded both in Global.DroppedLogEntries and, if provided, in a
// Prometheus counter.
type AsyncLogger struct {
	ch          chan store.RequestLogEntry
	inserter    Inserter
	batch       int
	flush       time.Duration
	done        chan struct{}
	dropCounter prometheus.Counter // may be nil
}

// NewAsyncLogger creates an AsyncLogger with the given channel buffer size,
// batch size, and flush interval.
//
//   - bufSize: maximum queued entries before new ones are dropped.
//   - batchSize: how many entries to insert per Postgres round-trip.
//   - flushInterval: maximum time between flushes even if batch is not full.
func NewAsyncLogger(inserter Inserter, bufSize, batchSize int, flushInterval time.Duration) *AsyncLogger {
	return &AsyncLogger{
		ch:       make(chan store.RequestLogEntry, bufSize),
		inserter: inserter,
		batch:    batchSize,
		flush:    flushInterval,
		done:     make(chan struct{}),
	}
}

// WithDropCounter attaches a Prometheus counter that is incremented whenever
// an entry is dropped due to a full buffer.  Returns the same *AsyncLogger for
// chaining.
func (al *AsyncLogger) WithDropCounter(c prometheus.Counter) *AsyncLogger {
	al.dropCounter = c
	return al
}

// Log enqueues an entry for async persistence.  If the buffer is full the
// entry is dropped, Global.DroppedLogEntries is incremented, and the
// Prometheus drop counter (if set) is also incremented.
func (al *AsyncLogger) Log(e store.RequestLogEntry) {
	select {
	case al.ch <- e:
	default:
		Global.DroppedLogEntries.Add(1)
		if al.dropCounter != nil {
			al.dropCounter.Inc()
		}
	}
}

// Start launches the background drainer goroutine.  Call Stop to shut it down
// gracefully.
func (al *AsyncLogger) Start(ctx context.Context) {
	go al.run(ctx)
}

// Stop signals the drainer to flush remaining entries and exit, then blocks
// until it has done so.
func (al *AsyncLogger) Stop() {
	close(al.done)
}

func (al *AsyncLogger) run(ctx context.Context) {
	ticker := time.NewTicker(al.flush)
	defer ticker.Stop()

	buf := make([]store.RequestLogEntry, 0, al.batch)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := al.inserter.InsertRequestLogBatch(ctx, buf); err != nil {
			log.Printf("async logger: batch insert failed: %v", err)
		}
		buf = buf[:0]
	}

	for {
		select {
		case entry := <-al.ch:
			buf = append(buf, entry)
			if len(buf) >= al.batch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-al.done:
			// Drain remaining entries with a short deadline.
			drainCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for {
				select {
				case entry := <-al.ch:
					buf = append(buf, entry)
				default:
					goto drained
				}
			}
		drained:
			_ = al.inserter.InsertRequestLogBatch(drainCtx, buf)
			return
		case <-ctx.Done():
			return
		}
	}
}
