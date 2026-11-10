package provisioner

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Both images download the Terraform binary. Each download must be checked against a
// pinned digest, and the two files must agree on version and digests, so a change in
// one place can't leave the other fetching something different or unchecked.
func tfPins(t *testing.T, path string) (version string, digests map[string]string, raw string) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = string(b)
	ver := regexp.MustCompile(`(?m)^ARG (?:TF|TERRAFORM)_VERSION=(\S+)`).FindStringSubmatch(raw)
	if ver == nil {
		t.Fatalf("%s: no Terraform version ARG", path)
	}
	digests = map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^ARG (?:TF|TERRAFORM)_SHA256_(AMD64|ARM64)=(\S+)`).FindAllStringSubmatch(raw, -1) {
		digests[m[1]] = m[2]
	}
	return ver[1], digests, raw
}

func TestTerraformDownloadsArePinnedAndVerified(t *testing.T) {
	files := []string{"../../Dockerfile", "../../Dockerfile.provisioner"}
	var wantVer string
	var want map[string]string
	for i, f := range files {
		ver, digests, raw := tfPins(t, f)
		for _, arch := range []string{"AMD64", "ARM64"} {
			if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digests[arch]) {
				t.Errorf("%s: %s digest %q is not a SHA-256", f, arch, digests[arch])
			}
		}
		if !strings.Contains(raw, "sha256sum -c -") {
			t.Errorf("%s downloads Terraform but never checks the digest", f)
		}
		if i == 0 {
			wantVer, want = ver, digests
			continue
		}
		if ver != wantVer {
			t.Errorf("%s uses Terraform %s but %s uses %s", f, ver, files[0], wantVer)
		}
		for arch, d := range want {
			if digests[arch] != d {
				t.Errorf("%s: %s digest differs from %s", f, arch, files[0])
			}
		}
	}
}
