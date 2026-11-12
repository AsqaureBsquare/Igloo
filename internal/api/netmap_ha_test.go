package api

// Two NetMapStores sharing one store stand in for two replicas: the agent reports its
// connections to one, the browser opens the Network tab on the other.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/store"
)

func TestNetworkSnapshotsCrossReplicas(t *testing.T) {
	st := store.NewMemory()
	a, b := NewNetMapStore(), NewNetMapStore()
	a.Back(st)
	b.Back(st)

	msg, _ := json.Marshal(map[string]any{
		"type": "net_snapshot",
		"connections": []NetConn{
			{LocalAddr: "10.0.0.5", LocalPort: "40000", RemoteAddr: "1.2.3.4", RemotePort: "443", State: "ESTABLISHED"},
		},
	})
	a.HandleAgentMessage("w1", msg)

	if snap := a.Get("w1"); snap == nil || len(snap.Connections) != 1 {
		t.Fatalf("the reporting replica itself has no snapshot: %+v", snap)
	}

	// The flusher persists within a tick; then the other replica can answer.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap := b.Get("w1"); snap != nil {
			if len(snap.Connections) != 1 || snap.Connections[0].RemoteAddr != "1.2.3.4" {
				t.Fatalf("snapshot crossed replicas mangled: %+v", snap)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the other replica never saw the snapshot")
		}
		time.Sleep(100 * time.Millisecond)
	}

	a.Delete("w1")
	if snap := b.Get("w1"); snap != nil {
		t.Fatalf("snapshot survived delete on the other replica: %+v", snap)
	}
}
