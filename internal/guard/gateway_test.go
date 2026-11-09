package guard

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeAuth struct {
	mu  sync.Mutex
	ids map[string]*Identity
}

func (f *fakeAuth) Authorize(_ context.Context, token string) (*Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.ids[token]; ok {
		cp := *id
		return &cp, nil
	}
	return nil, ErrUnauthorized
}

func (f *fakeAuth) set(token string, id *Identity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids[token] = id
}

type memSink struct {
	mu  sync.Mutex
	evs []Event
}

func (m *memSink) Record(e Event) { m.mu.Lock(); m.evs = append(m.evs, e); m.mu.Unlock() }

// wait returns the most recent event of the given kind, waiting for it to appear.
func (m *memSink) wait(t *testing.T, kind string) Event {
	t.Helper()
	for i := 0; i < 300; i++ {
		m.mu.Lock()
		for j := len(m.evs) - 1; j >= 0; j-- {
			if m.evs[j].Kind == kind {
				e := m.evs[j]
				m.mu.Unlock()
				return e
			}
		}
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %q event recorded", kind)
	return Event{}
}

func (m *memSink) text() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var parts []string
	for _, e := range m.evs {
		parts = append(parts, e.Detail, e.Path, e.Host, e.Rule, e.Method)
	}
	return strings.Join(parts, " ")
}

type rig struct {
	gw    *Gateway
	srv   *httptest.Server
	auth  *fakeAuth
	sink  *memSink
	token string
}

func newRig(t *testing.T, p Policy, secrets map[string]string) *rig {
	r := &rig{auth: &fakeAuth{ids: map[string]*Identity{}}, sink: &memSink{}, token: "tok-ws1"}
	r.auth.set(r.token, &Identity{WorkspaceID: "ws1", Policy: p})
	r.gw = &Gateway{Auth: r.auth, Sink: r.sink, DialTimeout: 2 * time.Second,
		Secrets: func(n string) (string, bool) { v, ok := secrets[n]; return v, ok }}
	r.srv = httptest.NewServer(r.gw)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) basic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("ws1:"+token))
}

// connect performs a CONNECT through the gateway and returns the status line and, on
// success, the open tunnel.
func (r *rig) connect(t *testing.T, target, auth string) (status string, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", strings.TrimPrefix(r.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if auth != "" {
		req += "Proxy-Authorization: " + auth + "\r\n"
	}
	c.Write([]byte(req + "\r\n"))
	br = bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading CONNECT reply: %v", err)
	}
	for { // drain headers
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	return strings.TrimSpace(line), c, br
}

// echoServer accepts TCP connections and echoes; accepted counts how many arrived.
func echoServer(t *testing.T) (addr string, accepted *atomic.Int32) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().String(), accepted
}

func port(addr string) string { _, p, _ := net.SplitHostPort(addr); return p }

func TestConnectTunnelsToAnAllowedHost(t *testing.T) {
	addr, _ := echoServer(t)
	r := newRig(t, Policy{Allow: []string{"localhost"}, AllowPrivate: true}, nil)
	status, c, br := r.connect(t, "localhost:"+port(addr), r.basic(r.token))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q, want 200", status)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("tunnel did not echo: %q, %v", buf, err)
	}
	c.Close()
	e := r.sink.wait(t, "connect")
	if e.Decision != "allow" || e.WorkspaceID != "ws1" || e.Rule != "localhost" || e.Bytes == 0 {
		t.Errorf("event = %+v", e)
	}
}

func TestConnectToAHostOutsideTheAllowListNeverReachesIt(t *testing.T) {
	addr, accepted := echoServer(t)
	r := newRig(t, Policy{Allow: []string{"somewhere-else.example"}, AllowPrivate: true}, nil)
	status, _, _ := r.connect(t, "localhost:"+port(addr), r.basic(r.token))
	if !strings.Contains(status, "403") {
		t.Fatalf("status = %q, want 403", status)
	}
	time.Sleep(100 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Error("a refused request still opened a connection to the destination")
	}
	if e := r.sink.wait(t, "connect"); e.Decision != "deny" || e.Rule != "not-in-allow-list" {
		t.Errorf("event = %+v", e)
	}
	if r.gw.Denied() != 1 {
		t.Errorf("denied counter = %d, want 1", r.gw.Denied())
	}
}

// An allowed name must not become a way into the cluster network or a metadata service.
func TestAnAllowedNameThatResolvesToAnInternalAddressIsRefused(t *testing.T) {
	addr, accepted := echoServer(t)
	r := newRig(t, Policy{Allow: []string{"localhost", "169.254.169.254"}}, nil) // AllowPrivate is off
	status, _, _ := r.connect(t, "localhost:"+port(addr), r.basic(r.token))
	if !strings.Contains(status, "403") {
		t.Fatalf("status = %q, want 403", status)
	}
	time.Sleep(100 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Error("connected to a loopback address although private addresses are not allowed")
	}
	if e := r.sink.wait(t, "connect"); e.Decision != "deny" || e.Rule != "private-address" {
		t.Errorf("event = %+v, want a private-address denial", e)
	}
	status, _, _ = r.connect(t, "169.254.169.254:80", r.basic(r.token))
	if !strings.Contains(status, "403") {
		t.Errorf("cloud metadata address: status = %q, want 403", status)
	}
}

func TestProxyCredentialsAreRequired(t *testing.T) {
	addr, accepted := echoServer(t)
	r := newRig(t, Policy{Allow: []string{"localhost"}, AllowPrivate: true}, nil)
	for name, auth := range map[string]string{"none": "", "wrong token": r.basic("nope"), "not basic": "Bearer tok-ws1"} {
		status, _, _ := r.connect(t, "localhost:"+port(addr), auth)
		if !strings.Contains(status, "407") {
			t.Errorf("%s: status = %q, want 407", name, status)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Error("an unauthenticated request reached the destination")
	}
}

func TestPlainHTTPIsForwardedOnlyToAllowedHosts(t *testing.T) {
	var gotProxyAuth, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotProxyAuth, gotPath = req.Header.Get("Proxy-Authorization"), req.URL.Path
		w.Write([]byte("hello from upstream"))
	}))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")

	r := newRig(t, Policy{Allow: []string{"127.0.0.1"}, AllowPrivate: true}, nil)
	proxy, _ := url.Parse(r.srv.URL)
	proxy.User = url.UserPassword("ws1", r.token)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + upHost + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello from upstream" || gotPath != "/hello" {
		t.Errorf("forward = %d %q path %q", resp.StatusCode, body, gotPath)
	}
	if gotProxyAuth != "" {
		t.Error("the workspace's proxy credential leaked to the destination")
	}
	if e := r.sink.wait(t, "http"); e.Decision != "allow" || e.Method != "GET" || e.Path != "/hello" || e.Status != 200 {
		t.Errorf("event = %+v", e)
	}

	// Same request, host no longer allowed.
	r.auth.set(r.token, &Identity{WorkspaceID: "ws1", Policy: Policy{Allow: []string{"other.example"}, AllowPrivate: true}})
	resp, err = client.Get("http://" + upHost + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("after the policy changed: status %d, want 403 (changes must apply to the next request)", resp.StatusCode)
	}
}

func TestRouteInjectsTheSecretAndNeverForwardsThePlaceholder(t *testing.T) {
	var hdr http.Header
	var gotPath, gotQuery, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hdr, gotPath, gotQuery = req.Header.Clone(), req.URL.Path, req.URL.RawQuery
		b, _ := io.ReadAll(req.Body)
		gotBody = string(b)
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(201)
		w.Write([]byte("created"))
	}))
	defer up.Close()

	pol := Policy{AllowPrivate: true, Routes: []Route{
		{Name: "llm", Upstream: up.URL + "/v1", Prefix: "Bearer ", Secret: "LLM_KEY"},
		{Name: "tool", Upstream: up.URL, Header: "X-Api-Key", Secret: "TOOL_KEY"},
	}}
	r := newRig(t, pol, map[string]string{"LLM_KEY": "sk-real-secret", "TOOL_KEY": "tool-real"})

	req, _ := http.NewRequest("POST", r.srv.URL+"/route/llm/chat/completions?stream=false", strings.NewReader(`{"q":1}`))
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 201 || string(body) != "created" || resp.Header.Get("X-Upstream") != "yes" {
		t.Errorf("response = %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if gotPath != "/v1/chat/completions" || gotQuery != "stream=false" || gotBody != `{"q":1}` {
		t.Errorf("upstream saw path %q query %q body %q", gotPath, gotQuery, gotBody)
	}
	if hdr.Get("Authorization") != "Bearer sk-real-secret" {
		t.Errorf("upstream Authorization = %q, want the real secret", hdr.Get("Authorization"))
	}
	if strings.Contains(strings.Join(hdr.Values("Authorization"), ","), r.token) || hdr.Get("Cookie") != "" || hdr.Get("Content-Type") != "application/json" {
		t.Errorf("upstream headers = %v (placeholder and cookie must be dropped, other headers kept)", hdr)
	}
	if e := r.sink.wait(t, "route"); e.Decision != "allow" || e.Rule != "route:llm" || e.Status != 201 || e.WorkspaceID != "ws1" {
		t.Errorf("event = %+v", e)
	}
	if strings.Contains(r.sink.text(), "sk-real-secret") {
		t.Error("the secret value leaked into the audit trail")
	}

	// A different header name, no prefix, token presented as x-api-key.
	req, _ = http.NewRequest("GET", r.srv.URL+"/route/tool/ping", nil)
	req.Header.Set("X-Api-Key", r.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hdr.Get("X-Api-Key") != "tool-real" || hdr.Get("Authorization") != "" {
		t.Errorf("custom header route: X-Api-Key=%q Authorization=%q", hdr.Get("X-Api-Key"), hdr.Get("Authorization"))
	}
}

func TestRouteFailuresAreExplainedAndSafe(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer up.Close()
	pol := Policy{Routes: []Route{{Name: "ok", Upstream: up.URL, Secret: "K"}, {Name: "nokey", Upstream: up.URL, Secret: "MISSING"}}} // AllowPrivate off
	r := newRig(t, pol, map[string]string{"K": "v"})

	call := func(path, token string) int {
		req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := call("/route/ok/x", ""); got != 401 {
		t.Errorf("no token: %d, want 401", got)
	}
	if got := call("/route/ok/x", "bad"); got != 401 {
		t.Errorf("bad token: %d, want 401", got)
	}
	if got := call("/route/nope/x", r.token); got != 404 {
		t.Errorf("unknown route: %d, want 404", got)
	}
	if got := call("/route/nokey/x", r.token); got != 502 {
		t.Errorf("missing secret: %d, want 502", got)
	}
	// The route's upstream is on loopback and the policy does not allow private addresses.
	if got := call("/route/ok/x", r.token); got != 403 {
		t.Errorf("route to an internal address: %d, want 403", got)
	}
	if hits.Load() != 0 {
		t.Errorf("%d request(s) reached the upstream although every call should have been stopped", hits.Load())
	}
}

func TestHealthz(t *testing.T) {
	r := newRig(t, Policy{}, nil)
	resp, err := http.Get(r.srv.URL + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", resp, err)
	}
	resp.Body.Close()
}
