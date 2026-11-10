package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperProcess is not a test: the supervisor tests re-run this binary as the
// supervised command. HELPER_MODE=exit exits at once; HELPER_MODE=listen opens
// HELPER_ADDR and stays up.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("HELPER_MODE") {
	case "exit":
		os.Exit(3)
	case "listen":
		l, err := net.Listen("tcp", os.Getenv("HELPER_ADDR"))
		if err != nil {
			os.Exit(4)
		}
		defer l.Close()
		time.Sleep(time.Minute)
	}
}

func helper(ctx context.Context, mode, addr string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "HELPER_MODE="+mode, "HELPER_ADDR="+addr)
	return cmd
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestRestartDelayGrowsThenResetsAfterAHealthyRun(t *testing.T) {
	initial, max := time.Second, 8*time.Second
	delay := initial
	var waits []time.Duration
	for i := 0; i < 6; i++ { // keeps failing fast
		var w time.Duration
		w, delay = restartDelay(delay, time.Second, initial, max)
		waits = append(waits, w)
	}
	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for i, w := range waits {
		if w != want[i]*time.Second {
			t.Errorf("wait %d = %v, want %v", i, w, want[i]*time.Second)
		}
	}
	// One long healthy run forgets the failures.
	if w, _ := restartDelay(delay, 5*time.Minute, initial, max); w != initial {
		t.Errorf("after a healthy run the wait is %v, want %v", w, initial)
	}
	// Never below the initial delay, even from a zero value.
	if w, _ := restartDelay(0, 0, initial, max); w != initial {
		t.Errorf("zero prev gives %v, want %v", w, initial)
	}
}

func TestWaitForPort(t *testing.T) {
	addr := freeAddr(t)
	go func() {
		time.Sleep(400 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			time.Sleep(3 * time.Second)
			l.Close()
		}
	}()
	if !waitForPort(addr, 3*time.Second) {
		t.Error("never saw the port open")
	}
	if waitForPort(freeAddr(t), 300*time.Millisecond) {
		t.Error("reported a closed port as open")
	}
}

func TestSuperviseRestartsACommandThatKeepsExiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var starts atomic.Int32
	var ready atomic.Bool
	superviseCmd(ctx, "flaky", func() *exec.Cmd {
		starts.Add(1)
		return helper(ctx, "exit", "")
	}, freeAddr(t), func() { ready.Store(true) }, 20*time.Millisecond, 100*time.Millisecond)
	if n := starts.Load(); n < 3 {
		t.Errorf("command started %d times, want it restarted repeatedly", n)
	}
	if ready.Load() {
		t.Error("reported ready although nothing ever listened")
	}
}

func TestSuperviseReportsReadyOnceThePortIsOpenAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	addr := freeAddr(t)
	ready := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		superviseCmd(ctx, "svc", func() *exec.Cmd { return helper(ctx, "listen", addr) }, addr,
			func() { ready <- struct{}{} }, 20*time.Millisecond, 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("never reported ready")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not stop after cancel")
	}
}

func TestReadCappedStopsAtTheLimit(t *testing.T) {
	for _, c := range []struct {
		name   string
		size   int
		tooBig bool
	}{{"empty", 0, false}, {"under", 9, false}, {"exactly at the limit", 10, false}, {"one byte over", 11, true}, {"far over", 1000, true}} {
		b, tooBig := readCapped(strings.NewReader(strings.Repeat("x", c.size)), 10)
		if tooBig != c.tooBig {
			t.Errorf("%s: tooBig = %v, want %v", c.name, tooBig, c.tooBig)
		}
		if !tooBig && len(b) != c.size {
			t.Errorf("%s: got %d bytes, want %d", c.name, len(b), c.size)
		}
		if tooBig && b != nil {
			t.Errorf("%s: an oversized body must not be returned partially", c.name)
		}
	}
}
