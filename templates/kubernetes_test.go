package templates

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Kubernetes template's confinement settings are Terraform, so nothing else would notice if one
// were dropped. These checks are crude on purpose: they only guard the two lines that matter.
func TestKubernetesTemplateKeepsItsWorkspacesConfined(t *testing.T) {
	b, err := os.ReadFile("kubernetes/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	tf := string(b)
	if !regexp.MustCompile(`automount_service_account_token\s*=\s*false`).MatchString(tf) {
		t.Error("workspace pods must not get a Kubernetes service-account token")
	}
	if !regexp.MustCompile(`policy_types\s*=\s*\["Egress",\s*"Ingress"\]`).MatchString(tf) {
		t.Error("a guarded workspace's network policy must cover ingress as well as egress, so nothing can connect into it")
	}
	// No DNS for a guarded workspace: the policy has no DNS rule, the pod has no resolver, and the two
	// addresses it needs are written into its hosts file.
	if regexp.MustCompile(`# DNS|port\s*=\s*"53"`).MatchString(tf) {
		t.Error("a guarded workspace's network policy must not allow DNS: a name lookup is a way to carry data out")
	}
	for _, want := range []string{`dns_policy = local.guarded ? "None"`, `dynamic "host_aliases"`, `nameservers = ["127.0.0.1"]`} {
		if !strings.Contains(tf, want) {
			t.Errorf("template lost %q, which keeps a guarded workspace off DNS", want)
		}
	}
}
