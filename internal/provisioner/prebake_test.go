package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The provisioner image bakes a local Terraform provider mirror from prebake/main.tf
// so workspace creation needs no internet. A template whose provider isn't in the
// mirror, or asks for a version the mirror doesn't hold, silently falls back to the
// public registry (or fails where that is blocked). Keep the two in step.
var providerBlock = regexp.MustCompile(`(?s)\w+\s*=\s*\{\s*source\s*=\s*"([^"]+)"\s*version\s*=\s*"([^"]+)"`)

func providers(t *testing.T, path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range providerBlock.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = m[2]
	}
	return out
}

func TestPrebakedProvidersCoverEveryTemplateExactly(t *testing.T) {
	baked := providers(t, "../../prebake/main.tf")
	if len(baked) == 0 {
		t.Fatal("found no providers in prebake/main.tf")
	}
	templates, _ := filepath.Glob("../../templates/*/main.tf")
	if len(templates) == 0 {
		t.Fatal("found no templates")
	}
	for _, tpl := range templates {
		for source, version := range providers(t, tpl) {
			got, ok := baked[source]
			switch {
			case !ok:
				t.Errorf("%s needs %s, which prebake/main.tf does not bake", tpl, source)
			case got != version:
				t.Errorf("%s pins %s %q but prebake/main.tf bakes %q: the image would hold a version the template rejects", tpl, source, version, got)
			}
		}
	}
}
