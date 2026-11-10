package api

import (
	"net/http"
	"strings"
)

// bearerOrQuery reads a credential from the Authorization header, falling back to
// the named query parameter. Non-browser clients (agent, CLI, provisioner daemon)
// send the header so the credential stays out of URLs, which proxies and access
// logs record; browsers can't set headers on a WebSocket, so they use the query.
// When a header is present it is the only thing consulted: an invalid header is
// never rescued by a valid query value.
func bearerOrQuery(r *http.Request, param string) string {
	if h := r.Header.Get("Authorization"); h != "" {
		t, _ := strings.CutPrefix(h, "Bearer ")
		return t
	}
	return r.URL.Query().Get(param)
}

// connToken returns the connection token of a WebSocket request.
func connToken(r *http.Request) string { return bearerOrQuery(r, "token") }
