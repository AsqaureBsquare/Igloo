package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileSecretsReadsFreshAndRefusesPaths(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "LLM_KEY"), []byte("sk-one\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "EMPTY"), []byte("\n"), 0o600)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside"), []byte("leak"), 0o600)
	get := fileSecrets(dir)

	if v, ok := get("LLM_KEY"); !ok || v != "sk-one" {
		t.Errorf("LLM_KEY = %q, %v (the trailing newline must go)", v, ok)
	}
	os.WriteFile(filepath.Join(dir, "LLM_KEY"), []byte("sk-two"), 0o600)
	if v, _ := get("LLM_KEY"); v != "sk-two" {
		t.Errorf("after rotation = %q, want sk-two without a restart", v)
	}
	for _, name := range []string{"", "MISSING", "EMPTY", "../outside", "a/b", "..", ".", "/etc/passwd"} {
		if v, ok := get(name); ok {
			t.Errorf("get(%q) = %q, want it refused", name, v)
		}
	}
}
