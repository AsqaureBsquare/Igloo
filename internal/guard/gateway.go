package guard

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Event is one decision the gateway made, kept so an owner can see what a workspace
// did on the network.
type Event struct {
	Time        time.Time `json:"time"`
	WorkspaceID string    `json:"workspace_id"`
	Kind        string    `json:"kind"`     // connect | http | route
	Decision    string    `json:"decision"` // allow | deny
	Host        string    `json:"host"`
	Method      string    `json:"method,omitempty"`
	Path        string    `json:"path,omitempty"`
	Rule        string    `json:"rule,omitempty"`   // the allow entry or route that applied, or why it was denied
	Status      int       `json:"status,omitempty"` // HTTP status returned or seen
	Bytes       int64     `json:"bytes,omitempty"`  // bytes moved for tunnels and bodies
	Detail      string    `json:"detail,omitempty"`
}

// Sink receives events. Record must not block the request path.
type Sink interface{ Record(Event) }

// Identity is who a token belongs to and what they may do.
type Identity struct {
	WorkspaceID string `json:"workspace_id"`
	Policy      Policy `json:"policy"`
}

// ErrUnauthorized means the token is not valid for any guarded workspace.
var ErrUnauthorized = errors.New("unauthorized")

// Authorizer turns a presented token into an Identity.
type Authorizer interface {
	Authorize(ctx context.Context, token string) (*Identity, error)
}

// Gateway is the egress proxy. It serves three kinds of request:
//
//	CONNECT host:port          an HTTPS tunnel, checked by host name, never decrypted
//	GET http://host/path       a plain-HTTP proxy request, checked by host name
//	/route/<name>/<path>       a request to the gateway itself that it forwards to a
//	                           configured upstream with a credential added
type Gateway struct {
	Auth    Authorizer
	Secrets func(name string) (string, bool) // values the gateway holds, by name
	Sink    Sink

	// DialTimeout bounds connecting to a destination (default 10s).
	DialTimeout time.Duration
	// Resolver lets tests control name resolution; nil uses the system resolver.
	Resolver *net.Resolver

	denied atomic.Int64
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet && !r.URL.IsAbs():
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodConnect:
		g.connect(w, r)
	case !r.URL.IsAbs() && strings.HasPrefix(r.URL.Path, "/route/"):
		g.route(w, r)
	case r.URL.IsAbs():
		g.forward(w, r)
	default:
		http.Error(w, "igloo-guard: not a proxy request", http.StatusBadRequest)
	}
}

// ─── identity ─────────────────────────────────────────────────────────────────

func (g *Gateway) proxyIdentity(r *http.Request) (*Identity, error) {
	h := r.Header.Get("Proxy-Authorization")
	enc, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return nil, ErrUnauthorized
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
	if err != nil {
		return nil, ErrUnauthorized
	}
	_, token, _ := strings.Cut(string(raw), ":") // the user name is only a label
	return g.Auth.Authorize(r.Context(), token)
}

// routeToken reads the placeholder credential a workspace presents on a route request.
func routeToken(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && t != "" {
		return t
	}
	if t := r.Header.Get("X-Api-Key"); t != "" {
		return t
	}
	return r.Header.Get("X-Igloo-Token")
}

// authFailed answers a failed identity check: 407 for bad credentials, 503 when the Igloo
// server could not be asked (traffic is refused either way).
func authFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnavailable) {
		http.Error(w, "igloo-guard: cannot verify credentials right now, try again shortly", http.StatusServiceUnavailable)
		return
	}
	proxyAuthRequired(w)
}

func proxyAuthRequired(w http.ResponseWriter) {
	w.Header().Set("Proxy-Authenticate", `Basic realm="igloo-guard"`)
	http.Error(w, "igloo-guard: proxy credentials required", http.StatusProxyAuthRequired)
}

func (g *Gateway) record(e Event) {
	e.Time = time.Now().UTC()
	if e.Decision == "deny" {
		g.denied.Add(1)
	}
	if g.Sink != nil {
		g.Sink.Record(e)
	}
}

// Denied is how many requests the gateway has refused since it started.
func (g *Gateway) Denied() int64 { return g.denied.Load() }

// ─── dialing ──────────────────────────────────────────────────────────────────

var errPrivate = errors.New("destination is a private or internal address")

// dialer connects to addr, refusing private/internal addresses unless allowed. The check
// runs on the address actually being connected to, after name resolution, so an allowed
// name that resolves (or is later re-pointed) to an internal address is still refused.
func (g *Gateway) dialer(allowPrivate bool) *net.Dialer {
	to := g.DialTimeout
	if to == 0 {
		to = 10 * time.Second
	}
	d := &net.Dialer{Timeout: to, Resolver: g.Resolver}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil || blockedAddr(net.ParseIP(host)) {
				return errPrivate
			}
			return nil
		}
	}
	return d
}

func (g *Gateway) transport(allowPrivate bool) *http.Transport {
	d := g.dialer(allowPrivate)
	return &http.Transport{
		DialContext:           d.DialContext,
		Proxy:                 nil, // never chain to another proxy
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
		MaxIdleConns:          0,
		DisableKeepAlives:     true,
	}
}

func dialDecision(err error) (decision, rule string, status int) {
	if errors.Is(err, errPrivate) {
		return "deny", "private-address", http.StatusForbidden
	}
	return "allow", "", http.StatusBadGateway
}

// ─── CONNECT ──────────────────────────────────────────────────────────────────

func (g *Gateway) connect(w http.ResponseWriter, r *http.Request) {
	id, err := g.proxyIdentity(r)
	if err != nil {
		authFailed(w, err)
		return
	}
	host, _, splitErr := net.SplitHostPort(r.Host)
	if splitErr != nil {
		http.Error(w, "igloo-guard: CONNECT needs host:port", http.StatusBadRequest)
		return
	}
	ev := Event{WorkspaceID: id.WorkspaceID, Kind: "connect", Host: r.Host}

	rule, ok := id.Policy.Allows(host)
	if !ok {
		ev.Decision, ev.Rule, ev.Status = "deny", "not-in-allow-list", http.StatusForbidden
		g.record(ev)
		http.Error(w, "igloo-guard: "+host+" is not in this workspace's allow-list", http.StatusForbidden)
		return
	}
	ev.Rule = rule

	upstream, err := g.dialer(id.Policy.AllowPrivate).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		dec, why, status := dialDecision(err)
		ev.Decision, ev.Status, ev.Detail = dec, status, err.Error()
		if why != "" {
			ev.Rule = why
		}
		g.record(ev)
		http.Error(w, "igloo-guard: cannot reach "+host+": "+err.Error(), status)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "igloo-guard: connection cannot be taken over", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Anything the client already sent after the CONNECT line belongs to the tunnel.
	done := make(chan int64, 2)
	go func() {
		n, _ := io.Copy(upstream, buf)
		m, _ := io.Copy(upstream, client)
		if tc, ok := upstream.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- n + m
	}()
	go func() {
		n, _ := io.Copy(client, upstream)
		if tc, ok := client.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- n
	}()
	total := <-done
	total += <-done
	ev.Decision, ev.Status, ev.Bytes = "allow", http.StatusOK, total
	g.record(ev)
}

// ─── plain HTTP proxying ──────────────────────────────────────────────────────

var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHop(h http.Header) {
	for _, c := range strings.Split(h.Get("Connection"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			h.Del(c)
		}
	}
	for _, k := range hopHeaders {
		h.Del(k)
	}
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request) {
	id, err := g.proxyIdentity(r)
	if err != nil {
		authFailed(w, err)
		return
	}
	ev := Event{WorkspaceID: id.WorkspaceID, Kind: "http", Host: r.URL.Host, Method: r.Method, Path: r.URL.Path}
	if r.URL.Scheme != "http" {
		ev.Decision, ev.Rule, ev.Status = "deny", "unsupported-scheme", http.StatusBadRequest
		g.record(ev)
		http.Error(w, "igloo-guard: HTTPS goes through CONNECT", http.StatusBadRequest)
		return
	}
	rule, ok := id.Policy.Allows(r.URL.Host)
	if !ok {
		ev.Decision, ev.Rule, ev.Status = "deny", "not-in-allow-list", http.StatusForbidden
		g.record(ev)
		http.Error(w, "igloo-guard: "+r.URL.Hostname()+" is not in this workspace's allow-list", http.StatusForbidden)
		return
	}
	ev.Rule = rule

	out := r.Clone(r.Context())
	out.RequestURI = ""
	stripHop(out.Header)
	g.relay(w, out, id.Policy.AllowPrivate, ev)
}

// relay sends out upstream and streams the response back, recording the outcome.
func (g *Gateway) relay(w http.ResponseWriter, out *http.Request, allowPrivate bool, ev Event) {
	resp, err := g.transport(allowPrivate).RoundTrip(out)
	if err != nil {
		dec, why, status := dialDecision(err)
		ev.Decision, ev.Status, ev.Detail = dec, status, err.Error()
		if why != "" {
			ev.Rule = why
		}
		g.record(ev)
		http.Error(w, "igloo-guard: upstream error: "+err.Error(), status)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	ev.Decision, ev.Status, ev.Bytes = "allow", resp.StatusCode, n
	g.record(ev)
}

// ─── credential routes ────────────────────────────────────────────────────────

func (g *Gateway) route(w http.ResponseWriter, r *http.Request) {
	token := routeToken(r)
	if token == "" {
		http.Error(w, "igloo-guard: present your workspace token as a bearer token", http.StatusUnauthorized)
		return
	}
	id, err := g.Auth.Authorize(r.Context(), token)
	if errors.Is(err, ErrUnavailable) {
		http.Error(w, "igloo-guard: cannot verify credentials right now, try again shortly", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "igloo-guard: unknown or expired workspace token", http.StatusUnauthorized)
		return
	}

	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/route/"), "/")
	ev := Event{WorkspaceID: id.WorkspaceID, Kind: "route", Method: r.Method, Path: "/" + rest, Rule: "route:" + name}
	rt, ok := id.Policy.Route(name)
	if !ok {
		ev.Decision, ev.Rule, ev.Status = "deny", "unknown-route", http.StatusNotFound
		ev.Host = name
		g.record(ev)
		http.Error(w, "igloo-guard: no route named "+name+" for this workspace", http.StatusNotFound)
		return
	}
	base, err := url.Parse(rt.Upstream)
	if err != nil {
		http.Error(w, "igloo-guard: bad route configuration", http.StatusInternalServerError)
		return
	}
	ev.Host = base.Host

	secret, have := "", false
	if g.Secrets != nil {
		secret, have = g.Secrets(rt.Secret)
	}
	if !have || secret == "" {
		ev.Decision, ev.Status, ev.Detail = "allow", http.StatusBadGateway, "gateway has no value for secret "+rt.Secret
		g.record(ev)
		http.Error(w, "igloo-guard: the gateway has no credential configured for route "+name, http.StatusBadGateway)
		return
	}

	target := *base
	target.Path = strings.TrimSuffix(base.Path, "/") + "/" + rest
	target.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, "igloo-guard: bad request", http.StatusBadRequest)
		return
	}
	out.Header = r.Header.Clone()
	stripHop(out.Header)
	// The workspace's placeholder never goes upstream, and nor does anything that could
	// carry another credential from the workspace.
	for _, k := range []string{"Authorization", "X-Api-Key", "X-Igloo-Token", "Cookie"} {
		out.Header.Del(k)
	}
	header := rt.Header
	if header == "" {
		header = "Authorization"
	}
	out.Header.Set(header, rt.Prefix+secret)
	out.ContentLength = r.ContentLength
	g.relay(w, out, id.Policy.AllowPrivate, ev)
}
