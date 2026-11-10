package provisioner

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendConfigS3(t *testing.T) {
	got, err := backendConfig(Input{WorkspaceID: "ws-1", StateBucket: "my-bucket", StateRegion: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`backend "s3"`, `bucket = "my-bucket"`, `key    = "workspaces/ws-1/terraform.tfstate"`, `region = "eu-west-1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestBackendConfigLocalKeepsStatePerWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IGLOO_TF_STATE_DIR", dir)

	a, err := backendConfig(Input{WorkspaceID: "ws-a", StateBucket: "local"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := backendConfig(Input{WorkspaceID: "ws-b", StateBucket: "local"})

	if !strings.Contains(a, `backend "local"`) || strings.Contains(a, "s3") {
		t.Errorf("expected the local backend, got:\n%s", a)
	}
	if !strings.Contains(a, filepath.Join(dir, "ws-a.tfstate")) {
		t.Errorf("state path not under IGLOO_TF_STATE_DIR:\n%s", a)
	}
	if a == b {
		t.Error("two workspaces share one state file")
	}
}
