package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedactRequestURI(t *testing.T) {
	cases := map[string]string{
		"/health":                      "/health",
		"/api/x?a=1&b=2":               "/api/x?a=1&b=2",
		"/ws?token=eyJabc.def":         "/ws?token=REDACTED",
		"/f?port=22&token=eyJabc&x=1":  "/f?port=22&token=REDACTED&x=1",
		"/f?TOKEN=eyJabc":              "/f?TOKEN=REDACTED",
		"/cb?code=abc&state=s":         "/cb?code=REDACTED&state=s",
		"/x?access_token=a&id_token=b": "/x?access_token=REDACTED&id_token=REDACTED",
		"/x?tok%65n=secret":            "/x?tok%65n=REDACTED",
		"/x?token":                     "/x?token",
		"/x?":                          "/x?",
		"/x?notoken=keep&mytoken=keep": "/x?notoken=keep&mytoken=keep",
		"/p?token=a=b&token=c":         "/p?token=REDACTED&token=REDACTED",
	}
	for in, want := range cases {
		if got := redactRequestURI(in); got != want {
			t.Errorf("redactRequestURI(%q) = %q, want %q", in, got, want)
		}
	}
}

// Handlers must still see the real token; only the logged URI is rewritten.
func TestRedactLogURILeavesHandlerInputIntact(t *testing.T) {
	var gotToken, gotURI string
	h := redactLogURI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken, gotURI = r.URL.Query().Get("token"), r.RequestURI
	}))
	req := httptest.NewRequest(http.MethodGet, "/ws?token=SECRET", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotToken != "SECRET" {
		t.Errorf("handler lost the token: %q", gotToken)
	}
	if gotURI != "/ws?token=REDACTED" {
		t.Errorf("logged URI = %q", gotURI)
	}
}
