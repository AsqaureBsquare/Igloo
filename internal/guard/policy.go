// Package guard is Igloo's egress gateway: a proxy that decides, per workspace, which
// hosts a workspace may reach, injects credentials the workspace never holds, and
// records every decision.
package guard

import (
	"fmt"
	"net"
	"strings"
)

// Policy is what one workspace is allowed to do on the network. It is stored with the
// workspace and served to the gateway by the Igloo server. It never contains secret
// values, only the names of secrets the gateway holds.
type Policy struct {
	// Allow lists the hosts the workspace may reach: an exact name ("api.example.com") or a
	// wildcard that matches subdomains only ("*.example.com", not example.com itself).
	// A bare "*" is rejected.
	Allow []string `json:"allow,omitempty"`

	// AllowPrivate lets the workspace reach hosts that resolve to private, loopback or
	// link-local addresses (internal services). Off by default so an allowed name cannot
	// be used to reach the cluster network or a cloud metadata endpoint.
	AllowPrivate bool `json:"allow_private,omitempty"`

	// Deny lists hosts that are refused even when Allow would permit them. Users do not set
	// it: the server fills it from the organisation baseline when it serves the policy.
	Deny []string `json:"deny,omitempty"`

	// AllowAll lets the workspace reach any host not in Deny (private addresses still need
	// AllowPrivate). Users cannot set it: the server fills it when the organisation baseline
	// is in open mode.
	AllowAll bool `json:"allow_all,omitempty"`

	// Routes expose upstream APIs at http://<gateway>/route/<name>/..., with a credential
	// added by the gateway.
	Routes []Route `json:"routes,omitempty"`
}

// Route forwards /route/<Name>/rest to Upstream/rest and sets one header from a secret
// held by the gateway.
type Route struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`         // base URL, e.g. https://api.example.com
	Header   string `json:"header,omitempty"` // header to set, default "Authorization"
	Prefix   string `json:"prefix,omitempty"` // text before the secret, e.g. "Bearer "
	Secret   string `json:"secret"`           // name of a secret held by the gateway
}

// Validate rejects policies that could not be enforced as written.
func (p Policy) Validate() error {
	if p.AllowAll {
		return fmt.Errorf("allow_all is set by your administrator's network baseline, not by a policy")
	}
	for _, a := range p.Allow {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "" || a == "*":
			return fmt.Errorf("allow entry %q: name a host; a bare wildcard is not allowed", a)
		case strings.Contains(a, "/") || strings.Contains(a, " "):
			return fmt.Errorf("allow entry %q: give a host name only, no scheme, path or spaces", a)
		case strings.HasPrefix(a, "*") && !strings.HasPrefix(a, "*."):
			return fmt.Errorf("allow entry %q: a wildcard must look like *.example.com", a)
		case strings.Count(a, "*") > 1 || (strings.Contains(a, "*") && !strings.HasPrefix(a, "*.")):
			return fmt.Errorf("allow entry %q: only a leading *. wildcard is supported", a)
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Routes {
		if r.Name == "" || strings.ContainsAny(r.Name, "/ ?#") {
			return fmt.Errorf("route name %q: use letters, digits and dashes", r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("route %q is defined twice", r.Name)
		}
		seen[r.Name] = true
		if !strings.HasPrefix(r.Upstream, "https://") && !strings.HasPrefix(r.Upstream, "http://") {
			return fmt.Errorf("route %q: upstream must start with https:// or http://", r.Name)
		}
		if r.Secret == "" {
			return fmt.Errorf("route %q: name the secret the gateway should inject", r.Name)
		}
	}
	return nil
}

// Allows reports whether host (a name or address, with or without a port) is permitted
// and which rule matched.
func (p Policy) Allows(host string) (rule string, ok bool) {
	h := normalizeHost(host)
	if h == "" {
		return "", false
	}
	for _, d := range p.Deny {
		if matchHost(d, h) {
			return "", false
		}
	}
	if p.AllowAll {
		return "*", true
	}
	for _, a := range p.Allow {
		if matchHost(a, h) {
			return strings.ToLower(strings.TrimSpace(a)), true
		}
	}
	return "", false
}

// matchHost reports whether the allow/deny pattern matches the normalised host h.
func matchHost(pattern, h string) bool {
	a := strings.ToLower(strings.TrimSpace(pattern))
	return a == h || (strings.HasPrefix(a, "*.") && strings.HasSuffix(h, a[1:]) && len(h) > len(a)-1)
}

// Route returns the named route.
func (p Policy) Route(name string) (Route, bool) {
	for _, r := range p.Routes {
		if r.Name == name {
			return r, true
		}
	}
	return Route{}, false
}

func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.Trim(h, "[]"), ".")
}

// blockedAddr reports whether ip is somewhere a workspace must not reach unless the
// policy says AllowPrivate: loopback, private ranges, link-local (which includes the
// cloud metadata address 169.254.169.254), carrier-grade NAT, multicast, unspecified.
func blockedAddr(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 { // 100.64.0.0/10
		return true
	}
	return false
}
