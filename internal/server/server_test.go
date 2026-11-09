package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(allowed map[string]bool, method, origin string) *httptest.ResponseRecorder {
	h := corsMiddleware(allowed)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(method, "/api/x", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCORSIsOffByDefault(t *testing.T) {
	rec := serve(parseOrigins(""), http.MethodGet, "https://evil.example")
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("default config sent Access-Control-Allow-Origin=%q", got)
	}
}

func TestCORSAllowsOnlyListedOrigins(t *testing.T) {
	allowed := parseOrigins("https://app.example.com, https://other.example.com")
	if got := serve(allowed, http.MethodGet, "https://app.example.com").Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("listed origin: got %q", got)
	}
	if got := serve(allowed, http.MethodGet, "https://evil.example").Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin allowed: %q", got)
	}
}

func TestCORSPreflightIsAnsweredWithoutReachingTheHandler(t *testing.T) {
	rec := serve(parseOrigins("*"), http.MethodOptions, "https://x.example")
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
}
