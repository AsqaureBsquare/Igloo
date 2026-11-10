package api

import (
	"net/http"
	"testing"
)

func TestLoginLimiterBlocksAfterMaxFails(t *testing.T) {
	var l loginLimiter
	for i := 0; i < loginMaxFails-1; i++ {
		l.fail("k")
		if l.blocked("k") {
			t.Fatalf("blocked after only %d failures", i+1)
		}
	}
	l.fail("k")
	if !l.blocked("k") {
		t.Fatal("not blocked after reaching the limit")
	}
	if l.blocked("other") {
		t.Fatal("limit leaked to an unrelated key")
	}
	l.reset("k")
	if l.blocked("k") {
		t.Fatal("still blocked after reset")
	}
}

func TestClientIPPrefersLastForwardedHop(t *testing.T) {
	r := &http.Request{Header: http.Header{}, RemoteAddr: "10.0.0.9:5555"}
	if got := clientIP(r); got != "10.0.0.9" {
		t.Errorf("socket address: got %q", got)
	}
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	if got := clientIP(r); got != "5.6.7.8" {
		t.Errorf("forwarded: got %q, want the hop our load balancer appended", got)
	}
}
