package guard

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Baseline is the organisation-wide floor under every workspace policy. An administrator sets
// it; a workspace owner can tighten their own policy but never loosen it past the baseline.
//
// It is applied twice: when a policy is saved (Check, so the owner gets a clear refusal) and
// again whenever the gateway asks what a workspace may do (Apply), so tightening the baseline
// takes effect for existing workspaces without anyone editing them.
type Baseline struct {
	// Mode is the default network access: "restricted" (or empty) lets a workspace reach only
	// what its policy allows; "open" lets every guarded workspace reach anything except Deny
	// (and private addresses), whatever its own policy says.
	Mode string `json:"mode,omitempty"`

	// Deny lists hosts no workspace may reach, in the same form as Policy.Allow. It always
	// wins over an allow entry, including a broader wildcard.
	Deny []string `json:"deny,omitempty"`

	// Ceiling, when not empty, is the most any policy may allow: every allow entry and
	// route upstream has to fall inside one of these hosts or wildcards.
	Ceiling []string `json:"ceiling,omitempty"`

	// ForbidPrivate stops policies from reaching private, loopback and link-local addresses
	// (the AllowPrivate switch).
	ForbidPrivate bool `json:"forbid_private,omitempty"`

	// RequireGuard refuses new workspaces and sandboxes created without the egress gateway.
	RequireGuard bool `json:"require_guard,omitempty"`

	// MaxSandboxTTLSeconds caps how long a sandbox may live. Zero means no extra cap.
	MaxSandboxTTLSeconds int `json:"max_sandbox_ttl_seconds,omitempty"`
}

// IsZero reports whether the baseline restricts nothing.
func (b Baseline) IsZero() bool {
	return !b.Open() && len(b.Deny) == 0 && len(b.Ceiling) == 0 && !b.ForbidPrivate && !b.RequireGuard && b.MaxSandboxTTLSeconds == 0
}

// Open reports whether workspaces may reach everything that is not denied.
func (b Baseline) Open() bool { return b.Mode == ModeOpen }

// Network access modes for Baseline.Mode.
const (
	ModeRestricted = "restricted"
	ModeOpen       = "open"
)

// Validate rejects a baseline that could not be enforced as written.
func (b Baseline) Validate() error {
	if b.Mode != "" && b.Mode != ModeRestricted && b.Mode != ModeOpen {
		return fmt.Errorf("mode must be %q or %q", ModeRestricted, ModeOpen)
	}
	if b.Open() && len(b.Ceiling) > 0 {
		return fmt.Errorf("a ceiling limits what may be allowed, so it cannot be combined with open mode")
	}
	if err := (Policy{Allow: b.Deny}).Validate(); err != nil {
		return fmt.Errorf("deny: %w", err)
	}
	if err := (Policy{Allow: b.Ceiling}).Validate(); err != nil {
		return fmt.Errorf("ceiling: %w", err)
	}
	if b.MaxSandboxTTLSeconds < 0 {
		return fmt.Errorf("max_sandbox_ttl_seconds cannot be negative")
	}
	return nil
}

// MaxSandboxTTL returns the cap as a duration, or 0 for none.
func (b Baseline) MaxSandboxTTL() time.Duration {
	return time.Duration(b.MaxSandboxTTLSeconds) * time.Second
}

// Check reports why p is not allowed under the baseline, or nil if it is.
func (b Baseline) Check(p Policy) error {
	for _, a := range p.Allow {
		if b.denies(a) {
			return fmt.Errorf("allow entry %q is blocked by your organisation's network baseline", a)
		}
		if !b.withinCeiling(a) {
			return fmt.Errorf("allow entry %q is outside what your organisation's network baseline permits", a)
		}
	}
	for _, r := range p.Routes {
		host := upstreamHost(r.Upstream)
		if host == "" {
			continue // Validate reports a bad upstream
		}
		if b.denies(host) {
			return fmt.Errorf("route %q: %s is blocked by your organisation's network baseline", r.Name, host)
		}
		if !b.withinCeiling(host) {
			return fmt.Errorf("route %q: %s is outside what your organisation's network baseline permits", r.Name, host)
		}
	}
	if b.ForbidPrivate && p.AllowPrivate {
		return fmt.Errorf("allow_private is not permitted by your organisation's network baseline")
	}
	return nil
}

// Apply returns the policy a workspace is actually held to: p with everything the baseline
// forbids removed, and the baseline's deny list added so the gateway checks it first. The
// stored policy is not changed.
func (b Baseline) Apply(p Policy) Policy {
	p.AllowAll = false // only the baseline can open a workspace up, never a stored policy
	if b.IsZero() {
		return p
	}
	out := Policy{AllowPrivate: p.AllowPrivate && !b.ForbidPrivate, AllowAll: b.Open(), Deny: append([]string(nil), b.Deny...)}
	for _, a := range p.Allow {
		if !b.denies(a) && b.withinCeiling(a) {
			out.Allow = append(out.Allow, a)
		}
	}
	for _, r := range p.Routes {
		host := upstreamHost(r.Upstream)
		if host == "" || b.denies(host) || !b.withinCeiling(host) {
			continue
		}
		out.Routes = append(out.Routes, r)
	}
	return out
}

// denies reports whether every host the entry could match is denied.
func (b Baseline) denies(entry string) bool {
	for _, d := range b.Deny {
		if covers(d, entry) {
			return true
		}
	}
	return false
}

func (b Baseline) withinCeiling(entry string) bool {
	if len(b.Ceiling) == 0 {
		return true
	}
	for _, c := range b.Ceiling {
		if covers(c, entry) {
			return true
		}
	}
	return false
}

// covers reports whether pattern matches every host that entry (an exact name or a
// *.wildcard) could match.
func covers(pattern, entry string) bool {
	pattern, entry = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(entry))
	if pattern == "" || entry == "" {
		return false
	}
	if !strings.HasPrefix(entry, "*.") {
		_, ok := Policy{Allow: []string{pattern}}.Allows(entry)
		return ok
	}
	// A wildcard entry matches every subdomain of its suffix, so only a wildcard that
	// reaches that suffix covers it.
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	p, e := pattern[2:], entry[2:]
	return e == p || strings.HasSuffix(e, "."+p)
}

func upstreamHost(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil {
		return ""
	}
	return normalizeHost(u.Host)
}
