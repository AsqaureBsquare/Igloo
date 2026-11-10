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
	for _, want := range []string{`dns_policy\s+= local.guarded \? "None"`, `dynamic "host_aliases"`, `nameservers = \["127.0.0.1"\]`} {
		if !regexp.MustCompile(want).MatchString(tf) {
			t.Errorf("template lost %q, which keeps a guarded workspace off DNS", want)
		}
	}
}

// The server's Service, the gateway's pod labels and the workspaces' namespace all depend on how the
// chart was installed. A name written into the template works on the default install and fails on
// every other one, which only a fresh install elsewhere would show.
func TestKubernetesTemplateDoesNotAssumeTheDefaultInstall(t *testing.T) {
	b, err := os.ReadFile("kubernetes/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") || strings.HasPrefix(code, "variable ") || strings.Contains(code, "gateway_service =") {
			continue
		}
		if strings.Contains(code, `"igloo-server`) || strings.Contains(code, `"igloo-guard`) || strings.Contains(code, `= "workspaces"`) {
			t.Errorf("template hardcodes a name that depends on the install: %s", code)
		}
	}
}

// MicroVM isolation and GPUs hang on two lines of the template: the pod's runtime_class_name
// (driven by the runtime_class parameter, absent when empty) and the GPU resource limit.
func TestKubernetesTemplateWiresRuntimeClassAndGPUs(t *testing.T) {
	b, err := os.ReadFile("kubernetes/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	tf := string(b)
	if !regexp.MustCompile(`runtime_class_name\s*=\s*igloo_parameter\.runtime_class\.value != "" \? igloo_parameter\.runtime_class\.value : null`).MatchString(tf) {
		t.Error("the pod's runtime_class_name must follow the runtime_class parameter and stay unset when it is empty")
	}
	if !regexp.MustCompile(`limits\s*=\s*igloo_parameter\.gpu\.value != "0" \? \{ "nvidia\.com/gpu" = igloo_parameter\.gpu\.value \} : \{\}`).MatchString(tf) {
		t.Error("the container must reserve nvidia.com/gpu from the gpu parameter and add no limits when it is 0")
	}
}
