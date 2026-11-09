package guard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// BatchSink collects events and hands them to flush in batches. Record never blocks the
// request path: if events arrive faster than they can be delivered, the oldest are
// dropped (and counted) rather than slowing the proxy.
type BatchSink struct {
	flush func(context.Context, []Event) error
	max   int
	keep  int

	mu      sync.Mutex
	pending []Event
	kick    chan struct{}
	done    chan struct{}
	stopped chan struct{}
	dropped atomic.Int64
}

// NewBatchSink starts a sink that delivers whenever max events are waiting or every
// interval, retaining up to keep undelivered events across failures.
func NewBatchSink(flush func(context.Context, []Event) error, interval time.Duration, max, keep int) *BatchSink {
	b := &BatchSink{flush: flush, max: max, keep: keep, kick: make(chan struct{}, 1),
		done: make(chan struct{}), stopped: make(chan struct{})}
	go b.run(interval)
	return b
}

func (b *BatchSink) Record(e Event) {
	b.mu.Lock()
	b.pending = append(b.pending, e)
	if over := len(b.pending) - b.keep; over > 0 {
		b.pending = b.pending[over:]
		b.dropped.Add(int64(over))
	}
	full := len(b.pending) >= b.max
	b.mu.Unlock()
	if full {
		select {
		case b.kick <- struct{}{}:
		default:
		}
	}
}

// Dropped is how many events were discarded because delivery fell too far behind.
func (b *BatchSink) Dropped() int64 { return b.dropped.Load() }

func (b *BatchSink) run(interval time.Duration) {
	defer close(b.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			b.deliver()
		case <-b.kick:
			b.deliver()
		case <-b.done:
			b.deliver()
			return
		}
	}
}

// deliver sends everything pending, a batch at a time. A failed batch stays queued for the
// next attempt.
func (b *BatchSink) deliver() {
	for {
		b.mu.Lock()
		n := len(b.pending)
		if n > b.max {
			n = b.max
		}
		if n == 0 {
			b.mu.Unlock()
			return
		}
		batch := append([]Event(nil), b.pending[:n]...)
		b.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := b.flush(ctx, batch)
		cancel()
		if err != nil {
			return
		}
		b.mu.Lock()
		if len(b.pending) >= n {
			b.pending = b.pending[n:]
		} else {
			b.pending = nil
		}
		b.mu.Unlock()
	}
}

// Close delivers what is left (once) and stops the sink.
func (b *BatchSink) Close() {
	select {
	case <-b.done:
	default:
		close(b.done)
	}
	<-b.stopped
}
