package api

import "sync"

const maxLogLines = 500

// LogBuffer is an in-memory per-workspace log buffer with pub/sub for SSE streaming.
type LogBuffer struct {
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

func (b *LogBuffer) Append(workspaceID, line string) {
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

// Snapshot returns a copy of the buffered lines for a workspace.
func (b *LogBuffer) Snapshot(workspaceID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines[workspaceID]))
	copy(out, b.lines[workspaceID])
	return out
}

func (b *LogBuffer) Clear(workspaceID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.lines, workspaceID)
}
