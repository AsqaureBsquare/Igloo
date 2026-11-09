package main

import (
	"context"
	"io"
	"log"
	"net"
	"os/exec"
	"time"
)

// healthyAfter is how long something must stay up before earlier failures are
// forgotten and the retry delay starts over.
const healthyAfter = 30 * time.Second

// restartDelay returns how long to wait before the next attempt and the delay to
// use after that. The delay doubles up to max while attempts keep failing quickly,
// and resets to initial once an attempt ran for healthyAfter, so a connection that
// was fine for hours isn't punished for old failures.
func restartDelay(prev, ranFor, initial, max time.Duration) (wait, next time.Duration) {
	if prev < initial || ranFor >= healthyAfter {
		prev = initial
	}
	next = prev * 2
	if next > max {
		next = max
	}
	return prev, next
}

// waitForPort reports whether something is listening on addr within timeout.
func waitForPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// superviseCmd keeps a command running: it starts it, calls onReady (once per run)
// when addr accepts connections, and restarts it with a growing delay when it exits,
// until ctx is cancelled.
func superviseCmd(ctx context.Context, name string, mk func() *exec.Cmd, addr string, onReady func(), initial, max time.Duration) {
	delay := initial
	for ctx.Err() == nil {
		started := time.Now()
		cmd := mk()
		if err := cmd.Start(); err != nil {
			log.Printf("[igloo-agent] failed to start %s: %v", name, err)
		} else {
			log.Printf("[igloo-agent] %s started (pid %d)", name, cmd.Process.Pid)
			go func() {
				if waitForPort(addr, 60*time.Second) && onReady != nil {
					onReady()
				}
			}()
			err := cmd.Wait()
			log.Printf("[igloo-agent] %s exited: %v", name, err)
		}
		var wait time.Duration
		wait, delay = restartDelay(delay, time.Since(started), initial, max)
		log.Printf("[igloo-agent] restarting %s in %v", name, wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}

// maxProxyBody caps one proxied HTTP response. The agent sends it to the server as a
// single base64 message, which must stay under the server's per-message limit.
const maxProxyBody = 32 << 20

// readCapped reads at most max bytes from r. The second result is true when r held
// more than that, in which case the returned bytes are not the whole body.
func readCapped(r io.Reader, max int64) ([]byte, bool) {
	b, _ := io.ReadAll(io.LimitReader(r, max+1))
	if int64(len(b)) > max {
		return nil, true
	}
	return b, false
}
