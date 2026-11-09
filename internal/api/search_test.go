package api

import (
	"reflect"
	"testing"
)

func TestTokeniseRespectsQuotes(t *testing.T) {
	got := tokenise(`name:"my project" status:running`)
	want := []string{`name:my project`, `status:running`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseWorkspaceFilter(t *testing.T) {
	f := ParseWorkspaceFilter(`api owner:me template:Kubernetes status:RUNNING org:acme`)
	want := WorkspaceFilter{Owner: "me", Template: "kubernetes", Name: "api", Status: "running", Org: "acme"}
	if f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
	if got := ParseWorkspaceFilter(""); got != (WorkspaceFilter{}) {
		t.Errorf("empty query should give an empty filter, got %+v", got)
	}
}
