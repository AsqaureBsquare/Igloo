package guard

import (
	"strings"
	"testing"
)

func TestCoversMeansEveryHostTheEntryCouldMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, entry string
		want           bool
	}{
		{"evil.com", "evil.com", true},
		{"evil.com", "a.evil.com", false},
		{"*.evil.com", "a.evil.com", true},
		{"*.evil.com", "a.b.evil.com", true},
		{"*.evil.com", "evil.com", false}, // a wildcard never matches its own apex
		{"*.evil.com", "*.evil.com", true},
		{"*.evil.com", "*.a.evil.com", true},
		{"*.a.evil.com", "*.evil.com", false}, // the entry reaches more than the pattern
		{"evil.com", "*.evil.com", false},     // an exact name can't cover a wildcard
		{"*.com", "*.evil.com", true},
		{"*.evilcom", "x.notevilcom", false}, // suffix must break at a label
		{"*.evil.com", "x.notevil.com", false},
		{"*.evil.com", "*.notevil.com", false}, // a wildcard entry must also break at a label
		{"*.EVIL.com", "A.Evil.COM", true},
		{"", "a.com", false},
	} {
		if got := covers(c.pattern, c.entry); got != c.want {
			t.Errorf("covers(%q, %q) = %v, want %v", c.pattern, c.entry, got, c.want)
		}
	}
}

func TestBaselineRefusesWhatItForbids(t *testing.T) {
	b := Baseline{Deny: []string{"*.evil.com", "pastebin.com"}, Ceiling: []string{"*.corp.example", "api.vendor.io"}, ForbidPrivate: true}
	ok := Policy{Allow: []string{"git.corp.example", "*.build.corp.example", "api.vendor.io"},
		Routes: []Route{{Name: "v", Upstream: "https://api.vendor.io/v1", Secret: "K"}}}
	if err := b.Check(ok); err != nil {
		t.Fatalf("a policy inside the ceiling was refused: %v", err)
	}
	for name, p := range map[string]Policy{
		"denied host":              {Allow: []string{"pastebin.com"}},
		"denied wildcard member":   {Allow: []string{"x.evil.com"}},
		"denied wildcard":          {Allow: []string{"*.evil.com"}},
		"outside the ceiling":      {Allow: []string{"example.org"}},
		"wildcard past ceiling":    {Allow: []string{"*.example"}},
		"apex of ceiling wildcard": {Allow: []string{"corp.example"}},
		"private":                  {Allow: []string{"git.corp.example"}, AllowPrivate: true},
		"route to denied":          {Routes: []Route{{Name: "r", Upstream: "https://x.evil.com", Secret: "K"}}},
		"route outside ceiling":    {Routes: []Route{{Name: "r", Upstream: "https://elsewhere.net/api", Secret: "K"}}},
	} {
		if err := b.Check(p); err == nil || !strings.Contains(err.Error(), "baseline") {
			t.Errorf("%s: Check = %v, want a refusal that names the baseline", name, err)
		}
	}
}

func TestAZeroBaselineChangesNothing(t *testing.T) {
	var b Baseline
	p := Policy{Allow: []string{"a.com"}, AllowPrivate: true}
	if !b.IsZero() || b.Check(p) != nil {
		t.Fatal("an empty baseline must not refuse anything")
	}
	if got := b.Apply(p); got.Allow[0] != "a.com" || !got.AllowPrivate {
		t.Errorf("Apply changed the policy: %+v", got)
	}
}

func TestApplyStripsWhatTheBaselineForbidsFromAnExistingPolicy(t *testing.T) {
	b := Baseline{Deny: []string{"bad.com"}, Ceiling: []string{"*.good.com"}, ForbidPrivate: true}
	stored := Policy{
		Allow:        []string{"a.good.com", "*.good.com", "bad.com", "other.net"},
		AllowPrivate: true,
		Routes: []Route{
			{Name: "ok", Upstream: "https://api.good.com", Secret: "K"},
			{Name: "gone", Upstream: "https://other.net", Secret: "K"},
		},
	}
	got := b.Apply(stored)
	if len(got.Allow) != 2 || got.Allow[0] != "a.good.com" || got.Allow[1] != "*.good.com" {
		t.Errorf("Allow = %v", got.Allow)
	}
	if got.AllowPrivate {
		t.Error("AllowPrivate survived a baseline that forbids it")
	}
	if len(got.Routes) != 1 || got.Routes[0].Name != "ok" {
		t.Errorf("Routes = %+v", got.Routes)
	}
	if len(stored.Allow) != 4 || !stored.AllowPrivate {
		t.Error("Apply modified the stored policy")
	}
}

// The deny list must win over a wildcard that is broader than it, which Check cannot catch.
func TestDenyWinsOverABroaderAllowAtEnforcement(t *testing.T) {
	b := Baseline{Deny: []string{"secret.corp.example"}}
	eff := b.Apply(Policy{Allow: []string{"*.corp.example"}})
	if _, ok := eff.Allows("git.corp.example"); !ok {
		t.Error("the rest of the wildcard should still be allowed")
	}
	if rule, ok := eff.Allows("secret.corp.example:443"); ok {
		t.Errorf("a denied host was allowed by %q", rule)
	}
	// A deny placed in the stored policy by a user can only tighten, and is replaced by the baseline's.
	eff = b.Apply(Policy{Allow: []string{"a.com"}, Deny: []string{"a.com"}})
	if _, ok := eff.Allows("a.com"); !ok {
		t.Error("a user's own deny should not be carried into the effective policy")
	}
}

func TestBaselineValidation(t *testing.T) {
	for name, b := range map[string]Baseline{
		"bare wildcard deny": {Deny: []string{"*"}},
		"scheme in ceiling":  {Ceiling: []string{"https://a.com"}},
		"negative ttl":       {MaxSandboxTTLSeconds: -1},
	} {
		if b.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := (Baseline{Deny: []string{"*.x.com"}, Ceiling: []string{"a.com"}, MaxSandboxTTLSeconds: 600}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestOpenModeAllowsEverythingExceptTheDenyList(t *testing.T) {
	b := Baseline{Mode: ModeOpen, Deny: []string{"pump.co", "*.pump.co"}, ForbidPrivate: true}
	eff := b.Apply(Policy{}) // the workspace's own policy allows nothing
	for _, host := range []string{"google.com", "example.org:443", "a.b.c.example.net"} {
		if _, ok := eff.Allows(host); !ok {
			t.Errorf("%s should be reachable in open mode", host)
		}
	}
	for _, host := range []string{"pump.co", "www.pump.co", "WWW.PUMP.CO:443", "x.y.pump.co"} {
		if _, ok := eff.Allows(host); ok {
			t.Errorf("%s is on the deny list and must stay blocked in open mode", host)
		}
	}
	if eff.AllowPrivate {
		t.Error("open mode must not open private addresses when the baseline forbids them")
	}
	if b.IsZero() {
		t.Error("an open baseline is not a no-op")
	}
}

func TestRestrictedModeIsTheDefaultAndAUserCannotOpenAWorkspaceUp(t *testing.T) {
	for _, b := range []Baseline{{}, {Mode: ModeRestricted}, {Deny: []string{"x.com"}}} {
		eff := b.Apply(Policy{Allow: []string{"a.com"}, AllowAll: true}) // a stored policy claiming allow_all
		if eff.AllowAll {
			t.Errorf("baseline %+v let a stored allow_all through", b)
		}
		if _, ok := eff.Allows("b.com"); ok {
			t.Errorf("baseline %+v: an unlisted host was reachable", b)
		}
	}
	if (Policy{AllowAll: true}).Validate() == nil {
		t.Error("a user-supplied allow_all was accepted")
	}
}

func TestOpenModeValidation(t *testing.T) {
	if (Baseline{Mode: "wide-open"}).Validate() == nil {
		t.Error("an unknown mode was accepted")
	}
	if (Baseline{Mode: ModeOpen, Ceiling: []string{"a.com"}}).Validate() == nil {
		t.Error("open mode with a ceiling was accepted")
	}
	if err := (Baseline{Mode: ModeOpen, Deny: []string{"a.com"}}).Validate(); err != nil {
		t.Error(err)
	}
}
