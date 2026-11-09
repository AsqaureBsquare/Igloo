package guard

import (
	"net"
	"testing"
)

func TestAllowsMatchesExactAndSubdomainWildcards(t *testing.T) {
	p := Policy{Allow: []string{"api.example.com", "*.pypi.org", "Files.Example.NET"}}
	for host, want := range map[string]bool{
		"api.example.com":     true,
		"API.EXAMPLE.COM":     true,
		"api.example.com:443": true,
		"api.example.com.":    true, // trailing dot
		"files.example.net":   true,
		"pypi.org":            false, // wildcard is subdomains only
		"files.pypi.org":      true,
		"a.b.pypi.org":        true,
		"evilpypi.org":        false,
		"pypi.org.evil.com":   false,
		"xapi.example.com":    false,
		"example.com":         false,
		"":                    false,
		"[2001:db8::1]:443":   false,
	} {
		if _, ok := p.Allows(host); ok != want {
			t.Errorf("Allows(%q) = %v, want %v", host, ok, want)
		}
	}
	if rule, _ := p.Allows("x.pypi.org"); rule != "*.pypi.org" {
		t.Errorf("matched rule = %q, want *.pypi.org", rule)
	}
	if _, ok := (Policy{}).Allows("anything.com"); ok {
		t.Error("an empty policy must allow nothing")
	}
}

func TestValidateRejectsPoliciesThatCannotBeEnforced(t *testing.T) {
	bad := map[string]Policy{
		"bare wildcard":      {Allow: []string{"*"}},
		"empty entry":        {Allow: []string{" "}},
		"scheme":             {Allow: []string{"https://example.com"}},
		"path":               {Allow: []string{"example.com/x"}},
		"wildcard mid-name":  {Allow: []string{"a.*.com"}},
		"wildcard no dot":    {Allow: []string{"*example.com"}},
		"route no name":      {Routes: []Route{{Upstream: "https://x", Secret: "S"}}},
		"route slash name":   {Routes: []Route{{Name: "a/b", Upstream: "https://x", Secret: "S"}}},
		"route bad upstream": {Routes: []Route{{Name: "a", Upstream: "ftp://x", Secret: "S"}}},
		"route no secret":    {Routes: []Route{{Name: "a", Upstream: "https://x"}}},
		"duplicate route":    {Routes: []Route{{Name: "a", Upstream: "https://x", Secret: "S"}, {Name: "a", Upstream: "https://y", Secret: "S"}}},
	}
	for name, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	ok := Policy{Allow: []string{"api.example.com", "*.example.org"}, Routes: []Route{{Name: "llm", Upstream: "https://api.example.com", Secret: "KEY"}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid policy rejected: %v", err)
	}
}

func TestBlockedAddrCoversInternalRanges(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1":            true,
		"::1":                  true,
		"10.1.2.3":             true,
		"172.16.0.1":           true,
		"172.31.255.255":       true,
		"192.168.1.1":          true,
		"169.254.169.254":      true, // cloud metadata
		"100.64.0.1":           true, // carrier-grade NAT
		"100.127.0.1":          true,
		"0.0.0.0":              true,
		"224.0.0.1":            true,
		"fe80::1":              true,
		"fc00::1":              true,
		"8.8.8.8":              false,
		"1.1.1.1":              false,
		"172.32.0.1":           false, // just outside 172.16/12
		"100.128.0.1":          false, // just outside 100.64/10
		"2001:4860:4860::8888": false,
	} {
		if got := blockedAddr(net.ParseIP(ip)); got != want {
			t.Errorf("blockedAddr(%s) = %v, want %v", ip, got, want)
		}
	}
	if !blockedAddr(nil) {
		t.Error("a nil address must be treated as blocked")
	}
}
