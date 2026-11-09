package provisiond

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/provisioner"
)

// fakeServer speaks the server's side of the provisioner protocol.
type fakeServer struct {
	srv    *httptest.Server
	accept atomic.Bool
	mu     sync.Mutex
	conns  []*websocket.Conn
	msgs   chan tagged
	hdrs   []http.Header // request headers of each connection attempt
	urls   []string      // request URIs of each connection attempt
}

type tagged struct {
	conn int // 1-based connection number
	msg  wsMsg
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{msgs: make(chan tagged, 256)}
	f.accept.Store(true)
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hdrs, f.urls = append(f.hdrs, r.Header.Clone()), append(f.urls, r.RequestURI)
		f.mu.Unlock()
		if !f.accept.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		n := len(f.conns)
		f.mu.Unlock()
		for {
			var m wsMsg
			if err := c.ReadJSON(&m); err != nil {
				return
			}
			f.msgs <- tagged{n, m}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) conn(n int) *websocket.Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > len(f.conns) {
		return nil
	}
	return f.conns[n-1]
}

// next returns the next message matching typ (and conn, if non-zero); other messages are discarded.
func (f *fakeServer) next(t *testing.T, typ string, conn int) tagged {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-f.msgs:
			if m.msg.Type == typ && (conn == 0 || m.conn == conn) {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q on conn %d", typ, conn)
		}
	}
}

func (f *fakeServer) waitConn(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if f.conn(n) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection %d never arrived", n)
}

func newDaemon(f *fakeServer, run runner) *Daemon {
	d := New(f.srv.URL, "key")
	d.initBackoff = 20 * time.Millisecond
	d.run = run
	return d
}

func sendJob(t *testing.T, c *websocket.Conn, id, ws string) {
	t.Helper()
	err := c.WriteJSON(wsMsg{Type: "job", Job: &claimResponse{
		JobID: id, Action: "start", WorkspaceID: ws, Files: map[string]string{"main.tf": "x"}, StateBucket: "local",
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobIsReportedAndSlotReturned(t *testing.T) {
	f := newFakeServer(t)
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		return map[string]string{"service_name": "svc-" + in.WorkspaceID}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 2)
	f.waitConn(t, 1)
	f.next(t, "acquire", 1)
	f.next(t, "acquire", 1)

	sendJob(t, f.conn(1), "j1", "w1")
	done := f.next(t, "done", 1)
	if done.msg.JobID != "j1" || done.msg.Outputs["service_name"] != "svc-w1" {
		t.Errorf("done = %+v", done.msg)
	}
	f.next(t, "acquire", 1) // the freed slot is offered again
}

func TestFailureIsReported(t *testing.T) {
	f := newFakeServer(t)
	d := newDaemon(f, func(context.Context, provisioner.Input, provisioner.Action) (map[string]string, error) {
		return nil, errors.New("terraform apply: quota exceeded")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 1)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	if m := f.next(t, "fail", 1); m.msg.JobID != "j1" || m.msg.Error != "terraform apply: quota exceeded" {
		t.Errorf("fail = %+v", m.msg)
	}
}

// The regression this daemon was restructured for: a dropped WebSocket used to
// cancel the context Terraform ran on, killing `terraform apply` mid-flight.
func TestJobSurvivesDisconnectAndReportsOnNewConnection(t *testing.T) {
	f := newFakeServer(t)
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- ctx
		select {
		case <-release:
			return map[string]string{"ok": "1"}, nil
		case <-ctx.Done():
			return nil, errors.New("terraform: signal: killed")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 2)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	runCtx := <-started

	f.conn(1).Close() // network blip / server rollout
	f.waitConn(t, 2)

	// The job still holds one of the two slots, so only one is requested on the new connection.
	f.next(t, "acquire", 2)
	time.Sleep(100 * time.Millisecond)
	select {
	case m := <-f.msgs:
		if m.conn == 2 && m.msg.Type == "acquire" {
			t.Errorf("reconnect asked for a second slot while a job is still running")
		}
	default:
	}

	if runCtx.Err() != nil {
		t.Fatal("terraform's context was cancelled when the connection dropped")
	}
	close(release)
	if m := f.next(t, "done", 2); m.msg.JobID != "j1" {
		t.Errorf("done = %+v", m.msg)
	}
	f.next(t, "acquire", 2) // slot handed back on the connection that is now current
}

func TestResultFinishedWhileDisconnectedIsDeliveredAfterReconnect(t *testing.T) {
	f := newFakeServer(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 1)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	<-started

	f.accept.Store(false) // server is down
	f.conn(1).Close()
	time.Sleep(150 * time.Millisecond)
	close(release) // job finishes with nobody to tell
	time.Sleep(100 * time.Millisecond)

	f.accept.Store(true)
	f.waitConn(t, 2)
	if m := f.next(t, "done", 2); m.msg.JobID != "j1" {
		t.Errorf("held result not delivered: %+v", m.msg)
	}
}

func TestStoppingTheDaemonCancelsTerraform(t *testing.T) {
	f := newFakeServer(t)
	started := make(chan context.Context, 1)
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx, 1)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	runCtx := <-started
	cancel()
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("shutting the daemon down did not cancel the running job")
	}
}

func TestSecondJobForSameWorkspaceIsDeferredNotFailed(t *testing.T) {
	f := newFakeServer(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	})
	d.deferDelay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 2)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	<-started
	sendJob(t, f.conn(1), "j2", "w1")
	// Busy with w1 already: j2 goes back to the queue. It must not be reported as a
	// failure, which would burn one of the job's retries.
	if m := f.next(t, "defer", 1); m.msg.JobID != "j2" {
		t.Errorf("defer = %+v, want j2 deferred", m.msg)
	}
	close(release)
	if m := f.next(t, "done", 1); m.msg.JobID != "j1" {
		t.Errorf("done = %+v", m.msg)
	}
}

func TestJobValidation(t *testing.T) {
	d := New("http://x", "k")
	d.run = func(context.Context, provisioner.Input, provisioner.Action) (map[string]string, error) {
		t.Fatal("terraform must not run for an invalid job")
		return nil, nil
	}
	ok := func() *claimResponse {
		return &claimResponse{Action: "start", WorkspaceID: "w", Files: map[string]string{"a.tf": "x"}, StateBucket: "b"}
	}
	for name, mutate := range map[string]func(*claimResponse){
		"no files":       func(j *claimResponse) { j.Files = nil },
		"no state":       func(j *claimResponse) { j.StateBucket = "" },
		"unknown action": func(j *claimResponse) { j.Action = "explode" },
	} {
		j := ok()
		mutate(j)
		if _, err := d.terraform(context.Background(), j, func(string) {}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestStopAndDestroyScaleToZero(t *testing.T) {
	var got provisioner.Input
	var action provisioner.Action
	d := New("http://x", "k")
	d.run = func(_ context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		got, action = in, a
		return nil, nil
	}
	for _, c := range []struct {
		act    string
		want   provisioner.Action
		desire string
	}{{"start", provisioner.ActionApply, "1"}, {"stop", provisioner.ActionApply, "0"}, {"destroy", provisioner.ActionDestroy, "0"}} {
		j := &claimResponse{Action: c.act, WorkspaceID: "w", Files: map[string]string{"a.tf": "x"}, StateBucket: "b"}
		if _, err := d.terraform(context.Background(), j, func(string) {}); err != nil {
			t.Fatal(err)
		}
		if action != c.want || got.Vars["desired_count"] != c.desire {
			t.Errorf("%s: action=%s desired_count=%q, want %s / %q", c.act, action, got.Vars["desired_count"], c.want, c.desire)
		}
	}
}

func TestServerInjectedVarsWinOverUserVars(t *testing.T) {
	d := New("http://x", "k")
	j := &claimResponse{WorkspaceID: "real", AgentToken: "tok", ServerURL: "http://igloo",
		Vars: map[string]string{"workspace_id": "spoofed", "agent_token": "spoofed", "server_url": "http://evil", "size": "large"}}
	v := d.buildVars(j, "1")
	if v["workspace_id"] != "real" || v["agent_token"] != "tok" || v["server_url"] != "http://igloo" {
		t.Errorf("user vars overrode injected values: %v", v)
	}
	if v["size"] != "large" {
		t.Error("ordinary variable dropped")
	}
}

// The provisioner key must travel in a header, never in the URL.
func TestProvisionerKeyIsSentInHeaderNotURL(t *testing.T) {
	f := newFakeServer(t)
	d := newDaemon(f, func(context.Context, provisioner.Input, provisioner.Action) (map[string]string, error) {
		return nil, nil
	})
	d.Token = "super-secret-key"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 1)
	f.waitConn(t, 1)

	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.hdrs[0].Get("Authorization"); got != "Bearer super-secret-key" {
		t.Errorf("Authorization header = %q", got)
	}
	if u := f.urls[0]; strings.Contains(u, "super-secret-key") || strings.Contains(u, "token") {
		t.Errorf("credential leaked into the URL: %s", u)
	}
	if !strings.Contains(f.urls[0], "id="+d.ID) {
		t.Errorf("daemon id missing from URL: %s", f.urls[0])
	}
}

// Shutdown must not kill a running `terraform apply`, and must stop the server from
// handing this daemon new jobs (the server keeps every slot the daemon announced).
func TestDrainFinishesRunningJobAndWithdrawsOpenSlots(t *testing.T) {
	f := newFakeServer(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return map[string]string{"ok": "1"}, nil
		case <-ctx.Done():
			return nil, errors.New("killed")
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 2)
	f.waitConn(t, 1)
	f.next(t, "acquire", 1)
	f.next(t, "acquire", 1)
	sendJob(t, f.conn(1), "j1", "w1")
	<-started

	drained := make(chan bool, 1)
	go func() { drained <- d.Drain(5 * time.Second) }()

	// The daemon drops the old connection and reconnects without announcing any slots.
	f.waitConn(t, 2)
	time.Sleep(300 * time.Millisecond)
	for {
		select {
		case m := <-f.msgs:
			if m.conn == 2 && m.msg.Type == "acquire" {
				t.Fatal("a draining daemon announced a free slot")
			}
			continue
		default:
		}
		break
	}

	select {
	case <-drained:
		t.Fatal("Drain returned while a job was still running")
	default:
	}

	close(release)
	if ok := <-drained; !ok {
		t.Error("Drain reported failure although the job finished")
	}
	if m := f.next(t, "done", 2); m.msg.JobID != "j1" {
		t.Errorf("result not delivered during drain: %+v", m.msg)
	}
	// A finished job must not hand its slot back while draining.
	time.Sleep(200 * time.Millisecond)
	select {
	case m := <-f.msgs:
		if m.msg.Type == "acquire" {
			t.Errorf("slot re-announced while draining: %+v", m)
		}
	default:
	}
}

func TestDrainWithNothingRunningReturnsImmediately(t *testing.T) {
	d := New("http://x", "k")
	start := time.Now()
	if !d.Drain(time.Minute) {
		t.Error("idle daemon should drain at once")
	}
	if time.Since(start) > time.Second {
		t.Error("idle drain was not immediate")
	}
}

func TestDrainTimesOutAndLeavesJobRunning(t *testing.T) {
	f := newFakeServer(t)
	started := make(chan context.Context, 1)
	d := newDaemon(f, func(ctx context.Context, in provisioner.Input, a provisioner.Action) (map[string]string, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx, 1)
	f.waitConn(t, 1)
	sendJob(t, f.conn(1), "j1", "w1")
	runCtx := <-started

	if d.Drain(300 * time.Millisecond) {
		t.Error("Drain claimed success although the job is still running")
	}
	if runCtx.Err() != nil {
		t.Error("Drain itself must not cancel running jobs; the caller does that after the timeout")
	}
	cancel()
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling after the timeout did not stop the job")
	}
}
