package guard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer plays the Igloo server's guard API.
type fakeServer struct {
	srv      *httptest.Server
	hits     atomic.Int32
	down     atomic.Bool
	mu       sync.Mutex
	keyUsed  string
	events   []Event
	policies map[string]Identity
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{policies: map[string]Identity{"good": {WorkspaceID: "ws1", Policy: Policy{Allow: []string{"a.example"}}}}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		f.mu.Lock()
		f.keyUsed = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/guard/authorize":
			f.hits.Add(1)
			var body struct{ Token string }
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			id, ok := f.policies[body.Token]
			f.mu.Unlock()
			if !ok {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(id)
		case "/api/guard/events":
			var body struct{ Events []Event }
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.events = append(f.events, body.Events...)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestAuthorizeAsksTheServerWithTheGuardKeyAndCaches(t *testing.T) {
	f := newFakeServer(t)
	c := &ServerClient{BaseURL: f.srv.URL, Key: "guard-key", TTL: time.Minute}

	id, err := c.Authorize(context.Background(), "good")
	if err != nil || id.WorkspaceID != "ws1" || len(id.Policy.Allow) != 1 {
		t.Fatalf("Authorize = %+v, %v", id, err)
	}
	if f.keyUsed != "guard-key" {
		t.Errorf("server saw key %q", f.keyUsed)
	}
	for i := 0; i < 5; i++ {
		if _, err := c.Authorize(context.Background(), "good"); err != nil {
			t.Fatal(err)
		}
	}
	if f.hits.Load() != 1 {
		t.Errorf("server was asked %d times for the same token, want 1 (cached)", f.hits.Load())
	}
}

func TestAnUnknownTokenIsRefusedAndRememberedBriefly(t *testing.T) {
	f := newFakeServer(t)
	c := &ServerClient{BaseURL: f.srv.URL, Key: "k", TTL: time.Minute}
	for i := 0; i < 3; i++ {
		if _, err := c.Authorize(context.Background(), "nope"); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("attempt %d: %v, want ErrUnauthorized", i, err)
		}
	}
	if f.hits.Load() != 1 {
		t.Errorf("a bad token hit the server %d times, want 1", f.hits.Load())
	}
}

func TestPolicyChangesAreSeenAfterTheTTL(t *testing.T) {
	f := newFakeServer(t)
	c := &ServerClient{BaseURL: f.srv.URL, Key: "k", TTL: 80 * time.Millisecond}
	if id, _ := c.Authorize(context.Background(), "good"); len(id.Policy.Allow) != 1 {
		t.Fatal("setup")
	}
	f.mu.Lock()
	f.policies["good"] = Identity{WorkspaceID: "ws1", Policy: Policy{Allow: []string{"a.example", "b.example"}}}
	f.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	id, _ := c.Authorize(context.Background(), "good")
	if len(id.Policy.Allow) != 2 {
		t.Errorf("policy after TTL = %v, want the updated one", id.Policy.Allow)
	}
}

func TestServerOutageFailsClosedButKeepsRecentlyVerifiedWorkspacesWorking(t *testing.T) {
	f := newFakeServer(t)
	c := &ServerClient{BaseURL: f.srv.URL, Key: "k", TTL: 50 * time.Millisecond}
	if _, err := c.Authorize(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	f.down.Store(true)
	time.Sleep(80 * time.Millisecond) // the cached answer is now past its TTL
	// Seen recently, so a restart of the Igloo server doesn't cut it off.
	if id, err := c.Authorize(context.Background(), "good"); err != nil || id.WorkspaceID != "ws1" {
		t.Errorf("recently verified token during an outage: %+v, %v", id, err)
	}
	// Never verified: refused, and reported as unavailable (not as a bad token).
	if _, err := c.Authorize(context.Background(), "never-seen"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("unverifiable token during an outage: %v, want ErrUnavailable", err)
	}
	// Long enough ago that the stale answer no longer counts.
	time.Sleep(300 * time.Millisecond)
	if _, err := c.Authorize(context.Background(), "good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("long outage: %v, want ErrUnavailable (fail closed)", err)
	}
}

func TestTheGatewayAnswers503WhenItCannotVerifyAndNeverForwards(t *testing.T) {
	var reached atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer up.Close()
	f := newFakeServer(t)
	f.down.Store(true)
	g := &Gateway{Auth: &ServerClient{BaseURL: f.srv.URL, Key: "k"}, Sink: &memSink{}}
	srv := httptest.NewServer(g)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/route/x/y", nil)
	req.Header.Set("Authorization", "Bearer good")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("route during an outage: %d, want 503", resp.StatusCode)
	}
	if reached.Load() != 0 {
		t.Error("traffic was forwarded although credentials could not be verified")
	}
}

func TestSendEventsPostsABatch(t *testing.T) {
	f := newFakeServer(t)
	c := &ServerClient{BaseURL: f.srv.URL, Key: "k"}
	err := c.SendEvents(context.Background(), []Event{{WorkspaceID: "ws1", Kind: "connect", Decision: "deny", Host: "x:443"}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) != 1 || f.events[0].Host != "x:443" || f.events[0].Decision != "deny" {
		t.Errorf("server received %+v", f.events)
	}
	f.down.Store(true)
	if err := c.SendEvents(context.Background(), []Event{{}}); err == nil {
		t.Error("a failed delivery must be reported so the sink keeps the events")
	}
}

// ─── BatchSink ────────────────────────────────────────────────────────────────

type collector struct {
	mu     sync.Mutex
	got    []Event
	failed atomic.Int32
	fail   atomic.Bool
}

func (c *collector) flush(_ context.Context, evs []Event) error {
	if c.fail.Load() {
		c.failed.Add(1)
		return errors.New("down")
	}
	c.mu.Lock()
	c.got = append(c.got, evs...)
	c.mu.Unlock()
	return nil
}

func (c *collector) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.got) }

func TestBatchSinkDeliversInOrderAndOnClose(t *testing.T) {
	c := &collector{}
	s := NewBatchSink(c.flush, time.Hour, 3, 100) // only the size trigger and Close can fire
	for i := 0; i < 7; i++ {
		s.Record(Event{Host: string(rune('a' + i))})
	}
	s.Close()
	if c.count() != 7 {
		t.Fatalf("delivered %d events, want 7", c.count())
	}
	var order string
	for _, e := range c.got {
		order += e.Host
	}
	if order != "abcdefg" {
		t.Errorf("order = %q, want abcdefg", order)
	}
}

func TestBatchSinkKeepsEventsAcrossFailuresAndDropsOldestWhenFull(t *testing.T) {
	c := &collector{}
	c.fail.Store(true)
	s := NewBatchSink(c.flush, 20*time.Millisecond, 100, 5)
	for i := 0; i < 8; i++ {
		s.Record(Event{Host: string(rune('a' + i))})
	}
	time.Sleep(100 * time.Millisecond)
	if c.failed.Load() == 0 {
		t.Fatal("never tried to deliver")
	}
	if s.Dropped() != 3 {
		t.Errorf("dropped = %d, want 3 (8 events, room for 5)", s.Dropped())
	}
	c.fail.Store(false) // the server comes back
	s.Close()
	var got string
	for _, e := range c.got {
		got += e.Host
	}
	if got != "defgh" {
		t.Errorf("delivered %q after recovery, want the newest five: defgh", got)
	}
}

func TestRecordNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	s := NewBatchSink(func(context.Context, []Event) error { <-block; return nil }, time.Millisecond, 10, 50)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			s.Record(Event{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Record blocked while delivery was stuck")
	}
	close(block)
	s.Close()
}
