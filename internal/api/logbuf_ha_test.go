package api

// Two LogBuffers sharing one store stand in for two replicas: the provisioner streams
// Terraform output to one, the browser watches the SSE log on the other.

import (
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/store"
)

func TestLogLinesCrossReplicas(t *testing.T) {
	st := store.NewMemory()
	a, b := NewLogBuffer(), NewLogBuffer()
	a.Back(st)
	b.Back(st)

	a.Append("w1", "terraform init")
	a.Append("w1", "terraform apply")

	snapshot, ch, cancel := b.Subscribe("w1")
	defer cancel()
	if len(snapshot) != 2 || snapshot[0] != "terraform init" || snapshot[1] != "terraform apply" {
		t.Fatalf("snapshot on B = %q, want the two lines appended on A", snapshot)
	}

	a.Append("w1", "✓ Provisioning complete")
	select {
	case line := <-ch:
		if line != "✓ Provisioning complete" {
			t.Fatalf("streamed line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a line appended on A never reached B's subscriber")
	}

	// No duplicates: the snapshot lines must not stream again.
	select {
	case line := <-ch:
		t.Fatalf("unexpected extra line %q (duplicate of the snapshot?)", line)
	case <-time.After(2 * logPollInterval):
	}

	if lines := b.Snapshot("w1"); len(lines) != 3 {
		t.Fatalf("snapshot after streaming = %d lines, want 3", len(lines))
	}
}

func TestLogSubscriberStopsCleanly(t *testing.T) {
	st := store.NewMemory()
	b := NewLogBuffer()
	b.Back(st)
	_, ch, cancel := b.Subscribe("w1")
	cancel()
	cancel() // idempotent
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("got a line after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel never closed after cancel")
	}
}
