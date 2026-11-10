package server

import (
	"net/http"
	"net/url"
	"strings"
)

// secretParams are query parameters that carry credentials (WebSocket/SSE
// clients can't set headers, so they pass tokens in the URL). They must never
// reach the access log.
var secretParams = map[string]bool{
	"token": true, "access_token": true, "id_token": true, "code": true, "key": true,
}

// redactRequestURI returns uri with the value of every secret query parameter replaced by "REDACTED".
// Other parameters and their order are preserved.
func redactRequestURI(uri string) string {
	path, query, found := strings.Cut(uri, "?")
	if !found || query == "" {
		return uri
	}
	parts := strings.Split(query, "&")
	for i, p := range parts {
		name, _, hasVal := strings.Cut(p, "=")
		if key, err := url.QueryUnescape(name); err == nil && hasVal && secretParams[strings.ToLower(key)] {
			parts[i] = name + "=REDACTED"
		}
	}
	return path + "?" + strings.Join(parts, "&")
}

// redactLogURI must run before the request logger. It only rewrites r.RequestURI,
// which the logger prints; handlers read r.URL, so behavior is unchanged.
func redactLogURI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RequestURI = redactRequestURI(r.RequestURI)
		next.ServeHTTP(w, r)
	})
}
