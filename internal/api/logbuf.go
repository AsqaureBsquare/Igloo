package api

import (
	"context"
	"sync"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

const maxLogLines = 500

// logPollInterval is how often a backed subscriber asks the store for new lines. Build
// logs are read by humans; well under a second is indistinguishable from push.
const logPollInterval = 700 * time.Millisecond

// LogStore is the slice of the store the log buffer needs; store.ProvisionLogStore
// satisfies it.
type LogStore interface {
	AppendProvisionLog(ctx context.Context, workspaceID, line string) (int64, error)
	ListProvisionLogs(ctx context.Context, workspaceID string, afterSeq int64) ([]models.ProvisionLogLine, error)
	DeleteProvisionLogs(ctx context.Context, workspaceID string) error
}

// LogBuffer delivers per-workspace build logs to SSE streams. Backed by a store it
// persists every line and subscribers tail the store by sequence number, so a browser
// on one replica sees lines the provisioner reports to another; without a store it is
// a per-process buffer with local pub/sub (single replica only).
type LogBuffer struct {
	store LogStore

	mu    sync.Mutex
	lines map[string][]string
	subs  map[string][]chan string
}

func NewLogBuffer() *LogBuffer {
	return &LogBuffer{
		lines: make(map[string][]string),
		subs:  make(map[string][]chan string),
	}
}

// Back makes the store the source of truth. Call once, before the buffer serves.
func (b *LogBuffer) Back(s LogStore) { b.store = s }

func (b *LogBuffer) Append(workspaceID, line string) {
	if b.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := b.store.AppendProvisionLog(ctx, workspaceID, line); err == nil {
			return
		}
		// The store refused the line; fall through so at least this replica shows it.
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines[workspaceID] = append(b.lines[workspaceID], line)
	if len(b.lines[workspaceID]) > maxLogLines {
		b.lines[workspaceID] = b.lines[workspaceID][len(b.lines[workspaceID])-maxLogLines:]
	}
	for _, ch := range b.subs[workspaceID] {
		select {
		case ch <- line:
		default:
		}
	}
}

// Subscribe returns a snapshot of existing lines plus a channel for new lines.
// Call cancel when done to unsubscribe.
func (b *LogBuffer) Subscribe(workspaceID string) ([]string, <-chan string, func()) {
	if b.store != nil {
		return b.subscribeStore(workspaceID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := make([]string, len(b.lines[workspaceID]))
	copy(snapshot, b.lines[workspaceID])
	ch := make(chan string, 64)
	b.subs[workspaceID] = append(b.subs[workspaceID], ch)
	return snapshot, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.subs[workspaceID]
		for i, s := range subs {
			if s == ch {
				b.subs[workspaceID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
}

// subscribeStore tails the store: everything after the snapshot's last sequence number,
// in order, whichever replica appended it. The poller owns the channel and closes it.
func (b *LogBuffer) subscribeStore(workspaceID string) ([]string, <-chan string, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	existing, _ := b.store.ListProvisionLogs(ctx, workspaceID, 0)
	cancel()
	var lastSeq int64
	snapshot := make([]string, 0, len(existing))
	for _, l := range existing {
		snapshot = append(snapshot, l.Line)
		lastSeq = l.Seq
	}

	ch := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(ch)
		t := time.NewTicker(logPollInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				lines, err := b.store.ListProvisionLogs(ctx, workspaceID, lastSeq)
				cancel()
				if err != nil {
					continue
				}
				for _, l := range lines {
					select {
					case ch <- l.Line:
						lastSeq = l.Seq
					case <-done:
						return
					}
				}
			}
		}
	}()
	var once sync.Once
	return snapshot, ch, func() { once.Do(func() { close(done) }) }
}

// Snapshot returns a copy of the buffered lines for a workspace.
func (b *LogBuffer) Snapshot(workspaceID string) []string {
	if b.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		lines, err := b.store.ListProvisionLogs(ctx, workspaceID, 0)
		if err == nil {
			out := make([]string, 0, len(lines))
			for _, l := range lines {
				out = append(out, l.Line)
			}
			return out
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines[workspaceID]))
	copy(out, b.lines[workspaceID])
	return out
}

func (b *LogBuffer) Clear(workspaceID string) {
	if b.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = b.store.DeleteProvisionLogs(ctx, workspaceID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.lines, workspaceID)
}
