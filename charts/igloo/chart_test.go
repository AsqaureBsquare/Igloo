package chart

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// Renders the chart with `helm template` (skipped when helm isn't installed) and
// checks how images and pull secrets come out, since a customer with an internal
// registry has no other way to install Igloo.

func render(t *testing.T, set ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed")
	}
	args := []string{"template", "igloo", ".", "--set", "server.jwtSecret=x", "--set", "provisioner.key=y"}
	for _, s := range set {
		args = append(args, "--set", s)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	return string(out)
}

var imageLine = regexp.MustCompile(`(?m)^\s+image:\s*"?([^"\s]+)"?\s*$`)
var workspaceEnv = regexp.MustCompile(`(?m)name: IGLOO_WORKSPACE_IMAGE\s*\n\s*value:\s*"?([^"\n]*)"?`)

func images(out string) []string {
	var got []string
	for _, m := range imageLine.FindAllStringSubmatch(out, -1) {
		got = append(got, m[1])
	}
	return got
}

func TestDefaultsKeepEveryImageAsWritten(t *testing.T) {
	out := render(t)
	want := map[string]int{
		"ghcr.io/asqaurebsquare/igloo-server:latest":      1,
		"ghcr.io/asqaurebsquare/igloo-provisioner:latest": 1,
		"postgres:16-alpine":                              2, // the database and the server's wait-for-database init container
	}
	got := map[string]int{}
	for _, i := range images(out) {
		got[i]++
	}
	for img, n := range want {
		if got[img] != n {
			t.Errorf("image %q appears %d times, want %d (all: %v)", img, got[img], n, images(out))
		}
	}
	if strings.Contains(out, "imagePullSecrets") {
		t.Error("imagePullSecrets rendered although none were configured")
	}
}

func TestRegistryOverrideRewritesEveryImage(t *testing.T) {
	out := render(t, "global.imageRegistry=harbor.corp/mirror")
	imgs := images(out)
	if len(imgs) != 4 {
		t.Fatalf("expected 4 image references, got %v", imgs)
	}
	for _, i := range imgs {
		if !strings.HasPrefix(i, "harbor.corp/mirror/") || strings.Contains(i, "ghcr.io") {
			t.Errorf("image %q was not moved to the internal registry", i)
		}
	}
	if !strings.Contains(out, `"harbor.corp/mirror/postgres:16-alpine"`) {
		t.Error("an image with no registry host (postgres) should be prefixed, not left on Docker Hub")
	}
}

func TestRegistryOverrideHandlesLocalNamesPortsAndSlashes(t *testing.T) {
	out := render(t,
		"global.imageRegistry=localhost:5000/",
		"server.image.repository=igloo-server", "server.image.tag=dev",
		"provisioner.image.repository=registry.example.com:443/org/prov", "provisioner.image.tag=1")
	have := strings.Join(images(out), " ")
	for _, want := range []string{"localhost:5000/igloo-server:dev", "localhost:5000/org/prov:1"} {
		if !strings.Contains(have, want) {
			t.Errorf("missing %q in %v", want, images(out))
		}
	}
	if strings.Contains(have, "//") || strings.Contains(have, "registry.example.com") {
		t.Errorf("host not replaced cleanly: %v", images(out))
	}
}

func TestWorkspaceImageFollowsTheOverrideOnlyWhenSet(t *testing.T) {
	unset := render(t, "global.imageRegistry=harbor.corp/mirror")
	for _, m := range workspaceEnv.FindAllStringSubmatch(unset, -1) {
		if m[1] != "" {
			t.Errorf("an unset workspace image became %q", m[1])
		}
	}
	set := render(t, "global.imageRegistry=harbor.corp/mirror", "provisioner.workspace.image=ghcr.io/org/ws:1")
	ms := workspaceEnv.FindAllStringSubmatch(set, -1)
	if len(ms) != 2 {
		t.Fatalf("expected the workspace image in 2 places (server and provisioner), got %d", len(ms))
	}
	for _, m := range ms {
		if m[1] != "harbor.corp/mirror/org/ws:1" {
			t.Errorf("workspace image = %q, want harbor.corp/mirror/org/ws:1", m[1])
		}
	}
}

func TestPullSecretsReachEveryPodSpec(t *testing.T) {
	out := render(t, "global.imagePullSecrets[0]=regcred", "global.imagePullSecrets[1]=second")
	if n := strings.Count(out, "imagePullSecrets:"); n != 3 {
		t.Errorf("imagePullSecrets rendered %d times, want 3 (server, provisioner, database)", n)
	}
	if n := strings.Count(out, `- name: "regcred"`); n != 3 {
		t.Errorf("regcred listed %d times, want 3", n)
	}
	if n := strings.Count(out, `- name: "second"`); n != 3 {
		t.Errorf("second listed %d times, want 3", n)
	}
}

// ─── egress gateway ───────────────────────────────────────────────────────────

func TestGatewayIsAbsentUnlessEnabled(t *testing.T) {
	out := render(t)
	for _, s := range []string{"igloo-guard", "IGLOO_GUARD"} {
		if strings.Contains(out, s) {
			t.Errorf("default render mentions %q although the gateway is off", s)
		}
	}
}

func TestEnablingTheGatewayAddsItAndTellsTheServerWhereItIs(t *testing.T) {
	out := render(t, "guard.enabled=true")
	for _, want := range []string{
		"name: igloo-guard\n", // deployment, service, secret, network policy all share it
		`value: "http://igloo-guard.default.svc.cluster.local:3128"`,
		"name: IGLOO_GUARD_KEY",
		"readOnlyRootFilesystem: true",
		"runAsNonRoot: true",
		`"ghcr.io/asqaurebsquare/igloo-guard:latest"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered chart lacks %q", want)
		}
	}
	// Server and gateway must be given the same secret by reference, not as a literal.
	if n := strings.Count(out, "name: IGLOO_GUARD_KEY"); n != 2 {
		t.Errorf("IGLOO_GUARD_KEY is set %d times, want 2 (server and gateway)", n)
	}
	// Only workspaces may reach the gateway.
	if !strings.Contains(out, "kubernetes.io/metadata.name: \"workspaces\"") {
		t.Error("the gateway's ingress policy should admit only the workspaces namespace")
	}
}

func TestGatewaySecretsComeFromTheChosenSource(t *testing.T) {
	inline := render(t, "guard.enabled=true", "guard.secrets.LLM_KEY=sk-test")
	if !strings.Contains(inline, "secretName: igloo-guard-secrets") || !strings.Contains(inline, `LLM_KEY: "sk-test"`) {
		t.Error("inline secrets should become a Secret mounted into the gateway")
	}
	existing := render(t, "guard.enabled=true", "guard.existingSecret=my-keys")
	if !strings.Contains(existing, "secretName: my-keys") {
		t.Error("existingSecret should be the mounted secret")
	}
	if strings.Contains(existing, "igloo-guard-secrets") {
		t.Error("no extra Secret should be created when an existing one is named")
	}
	none := render(t, "guard.enabled=true")
	if strings.Contains(none, "igloo-guard-secrets") || !strings.Contains(none, "emptyDir: {}") {
		t.Error("without any secrets the gateway should get an empty directory, not a missing Secret")
	}
}

func TestGatewayFollowsTheRegistryOverrideAndGetsPullSecrets(t *testing.T) {
	out := render(t, "guard.enabled=true", "global.imageRegistry=harbor.corp/mirror", "global.imagePullSecrets[0]=regcred")
	if !strings.Contains(out, `"harbor.corp/mirror/asqaurebsquare/igloo-guard:latest"`) {
		t.Errorf("gateway image not moved to the internal registry: %v", images(out))
	}
	if n := strings.Count(out, "imagePullSecrets:"); n != 6 {
		t.Errorf("imagePullSecrets rendered %d times, want 6 (server, provisioner, database, gateway, two enforcement probes)", n)
	}
}

// A guarded workspace is confined by a network policy that the provisioner creates through
// Terraform. Without this permission it fails to start (safe, but unusable), and granting
// it when the gateway is off would be needless privilege.
func TestProvisionerMayManageNetworkPoliciesOnlyWhenTheGatewayIsOn(t *testing.T) {
	if off := render(t); strings.Contains(off, "networkpolicies") {
		t.Error("the provisioner was granted network-policy access although the gateway is off")
	}
	on := render(t, "guard.enabled=true")
	i := strings.Index(on, "kind: ClusterRole\n")
	if i < 0 || !strings.Contains(on[i:], `resources: ["networkpolicies"]`) {
		t.Error("with the gateway on, the provisioner's role must allow managing network policies")
	}
	// Only the provisioner needs it: the rule must not appear in any other role.
	if n := strings.Count(on, `resources: ["networkpolicies"]`); n != 1 {
		t.Errorf("network-policy rule appears %d times, want exactly 1", n)
	}
}

// A guarded workspace has no DNS; its template reads the addresses of the server and the gateway.
// That read access is limited to those two services and only exists when the gateway is on.
func TestProvisionerMayReadOnlyTheServerAndGatewayServicesAndOnlyWhenTheGatewayIsOn(t *testing.T) {
	if off := render(t); strings.Contains(off, `resources: ["services"]`) {
		t.Error("the provisioner may read services although the gateway is off")
	}
	on := render(t, "guard.enabled=true")
	if n := strings.Count(on, `resources: ["services"]`); n != 1 {
		t.Fatalf("services rule appears %d times, want exactly 1", n)
	}
	i := strings.Index(on, `resources: ["services"]`)
	rule := on[i : i+250]
	if !strings.Contains(rule, "resourceNames:") || !strings.Contains(rule, "igloo-server") || !strings.Contains(rule, "igloo-guard") || !strings.Contains(rule, `verbs: ["get"]`) {
		t.Errorf("the rule must be get-only and name exactly the server and gateway services: %s", rule)
	}
	// A Role, not a ClusterRole: it must not reach services in other namespaces.
	if j := strings.LastIndex(on[:i], "kind: "); j < 0 || !strings.HasPrefix(on[j:], "kind: Role\n") {
		t.Errorf("the services rule must live in a namespaced Role, found %q", on[j:j+20])
	}
}

func TestSandboxImageIsPassedToTheServerAndFollowsTheRegistryOverride(t *testing.T) {
	if strings.Contains(render(t), "IGLOO_SANDBOX_IMAGE") {
		t.Error("no sandbox image configured, yet the server was told one")
	}
	out := render(t, "server.sandboxImage=ghcr.io/asqaurebsquare/igloo-workspace:latest")
	if !regexp.MustCompile(`name: IGLOO_SANDBOX_IMAGE\s+value: "ghcr.io/asqaurebsquare/igloo-workspace:latest"`).MatchString(out) {
		t.Error("the configured sandbox image did not reach the server's environment")
	}
	moved := render(t, "server.sandboxImage=ghcr.io/asqaurebsquare/igloo-workspace:latest", "global.imageRegistry=harbor.corp/mirror")
	if !strings.Contains(moved, `value: "harbor.corp/mirror/asqaurebsquare/igloo-workspace:latest"`) {
		t.Error("the sandbox image should follow the internal-registry override")
	}
}

// `helm test` is how an operator learns the cluster ignores NetworkPolicy, so it must exist when the
// gateway is on, run where the gateway accepts connections, and not exist when it is off.
func TestEnforcementProbe(t *testing.T) {
	on := render(t, "guard.enabled=true")
	for _, want := range []string{
		"igloo-guard-probe-control", "igloo-guard-probe-blocked", "igloo-guard-enforcement-probe",
		`"helm.sh/hook": test`, "namespace: workspaces",
		"NetworkPolicy is NOT enforced",
	} {
		if !strings.Contains(on, want) {
			t.Errorf("rendered chart lacks %q", want)
		}
	}
	if strings.Contains(render(t), "netpol-probe") {
		t.Error("the probe must not exist when the gateway is off")
	}
}

// The namespace the chart creates must be the one the server and provisioner are told to use,
// otherwise a custom workspace namespace is never created and two releases collide on "workspaces".
func TestWorkspaceNamespaceFollowsTheValue(t *testing.T) {
	ns := regexp.MustCompile(`(?m)^kind: Namespace\s*\nmetadata:\s*\n\s+name:\s*(\S+)`)
	for value, want := range map[string]string{"": "workspaces", "team-a-ws": "team-a-ws"} {
		var set []string
		if value != "" {
			set = append(set, "provisioner.workspace.namespace="+value)
		}
		m := ns.FindStringSubmatch(render(t, set...))
		if m == nil || m[1] != want {
			t.Errorf("namespace %q: chart creates %v, want %s", value, m, want)
		}
	}
}

func TestSandboxRuntimeClassReachesTheServerOnlyWhenSet(t *testing.T) {
	if strings.Contains(render(t), "IGLOO_SANDBOX_RUNTIME_CLASS") {
		t.Error("IGLOO_SANDBOX_RUNTIME_CLASS set with no value configured")
	}
	out := render(t, "server.sandboxRuntimeClass=kata")
	if !regexp.MustCompile(`IGLOO_SANDBOX_RUNTIME_CLASS\s*\n\s*value:\s*"kata"`).MatchString(out) {
		t.Error("server.sandboxRuntimeClass=kata did not reach the server environment")
	}
}
