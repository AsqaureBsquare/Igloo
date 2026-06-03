package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/models"
)

// ─── Message types for HTTP/WS tunnelling ─────────────────────────────────────

type proxyHTTPReq struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64
}

type proxyHTTPRes struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64
}

type proxyWSMsg struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Data    string            `json:"data,omitempty"` // base64
}

// ─── Token endpoint ───────────────────────────────────────────────────────────

// GET /api/workspaces/{id}/vscode-token
func (h *Handler) VScodeToken(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.GetWorkspace(r.Context(), chi.URLParam(r, "id"))
	if err != nil || ws.UserID != ctxUserID(r) {
		h.err(w, http.StatusNotFound, "workspace not found")
		return
	}
	if ws.Status != models.StatusRunning {
		h.err(w, http.StatusConflict, "workspace is not running")
		return
	}
	token, err := h.Auth.SignConn(ws.ID, "proxy", time.Hour)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, map[string]string{
		"url": "/proxy/" + ws.ID + "/?_t=" + token,
	})
}

// ─── Proxy auth middleware ────────────────────────────────────────────────────

// ProxyAuth validates the proxy token either from the _t query param (first
// request) or from a session cookie set on that first request.
func (h *Handler) ProxyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsID := chi.URLParam(r, "id")
		cookieName := "igloo-proxy-" + wsID[:8]

		if t := r.URL.Query().Get("_t"); t != "" {
			claims, err := h.Auth.ParseConn(t)
			if err != nil || claims.WorkspaceID != wsID || claims.Role != "proxy" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    t,
				Path:     "/proxy/" + wsID + "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   3600,
			})
			q := r.URL.Query()
			q.Del("_t")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
			return
		}

		cookie, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "unauthorized — open VS Code via the workspace button", http.StatusUnauthorized)
			return
		}
		claims, err := h.Auth.ParseConn(cookie.Value)
		if err != nil || claims.WorkspaceID != wsID || claims.Role != "proxy" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Proxy handler ────────────────────────────────────────────────────────────

// Proxy handles both HTTP and WebSocket upgrade requests for code-server.
func (h *Handler) Proxy(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		h.proxyWS(w, r, wsID)
		return
	}
	h.proxyHTTP(w, r, wsID)
}

func (h *Handler) proxyHTTP(w http.ResponseWriter, r *http.Request, wsID string) {
	// Fail fast if no agent is connected rather than hanging for 30s
	if !h.Relay.AgentConnected(wsID) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`<!doctype html><html><head><title>Workspace not ready</title>
<style>body{font-family:sans-serif;background:#0d1117;color:#c9d1d9;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.box{text-align:center}.spin{font-size:2rem;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}</style></head>
<body><div class="box"><div class="spin">⟳</div><h2>Workspace agent not connected</h2>
<p>Make sure the workspace is running, then refresh.</p>
<script>setTimeout(()=>location.reload(),3000)</script></div></body></html>`))
		return
	}

	reqID := uuid.NewString()

	// Strip /proxy/{wsID} prefix — code-server serves from /
	proxyPrefix := "/proxy/" + wsID
	subPath := strings.TrimPrefix(r.URL.RequestURI(), proxyPrefix)
	if subPath == "" || subPath[0] != '/' {
		subPath = "/" + subPath
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	headers := make(map[string]string, len(r.Header))
	for k, vals := range r.Header {
		if !strings.EqualFold(k, "Cookie") {
			headers[k] = vals[0]
		}
	}

	msg := proxyHTTPReq{
		Type:    "http_req",
		ID:      reqID,
		Method:  r.Method,
		Path:    subPath,
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString(bodyBytes),
	}
	raw, _ := json.Marshal(msg)

	ch, cancel := h.Relay.Subscribe(wsID)
	defer cancel()
	h.Relay.SendToAgent(wsID, raw)

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case agentMsg := <-ch:
			var res proxyHTTPRes
			if json.Unmarshal(agentMsg, &res) != nil || res.Type != "http_res" || res.ID != reqID {
				continue
			}
			// code-server not ready yet — show loading page
			if (res.Status == 502 || res.Status == 503) && subPath == "/" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`<!doctype html><html><head><title>VS Code starting</title>
<style>body{font-family:sans-serif;background:#0d1117;color:#c9d1d9;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.box{text-align:center}.spin{font-size:2rem;animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}</style></head>
<body><div class="box"><div class="spin">⟳</div><h2>VS Code is starting...</h2>
<p>This usually takes 5–10 seconds on first launch.</p>
<script>setTimeout(()=>location.reload(),2000)</script></div></body></html>`))
				return
			}
			for k, v := range res.Headers {
				// Rewrite Location redirects to keep them under the proxy path
				if strings.EqualFold(k, "Location") && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, proxyPrefix) {
					v = proxyPrefix + v
				}
				w.Header().Set(k, v)
			}
			w.WriteHeader(res.Status)
			if res.Body != "" {
				body, _ := base64.StdEncoding.DecodeString(res.Body)
				// Rewrite <base href="/"> so VS Code assets load through the proxy path
				if ct := res.Headers["Content-Type"]; strings.Contains(ct, "text/html") {
					body = bytes.ReplaceAll(body,
						[]byte(`<base href="/">`),
						[]byte(`<base href="`+proxyPrefix+`/">`),
					)
				}
				w.Write(body)
			}
			return
		case <-timer.C:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`<!doctype html><html><head><title>Timeout</title>
<style>body{font-family:sans-serif;background:#0d1117;color:#c9d1d9;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}</style></head>
<body><h2>VS Code timed out. <a href="" style="color:#58a6ff">Retry</a></h2></body></html>`))
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Handler) proxyWS(w http.ResponseWriter, r *http.Request, wsID string) {
	chanID := uuid.NewString()

	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[proxy] ws upgrade failed: %v", err)
		return
	}
	defer browserConn.Close()

	ch, cancel := h.Relay.Subscribe(wsID)
	defer cancel()

	// Tell agent to open a WebSocket to code-server.
	// Drop hop-by-hop headers and Origin (code-server checks origin matches its
	// bind address; the agent's dialer supplies the correct localhost origin).
	headers := make(map[string]string)
	for k, vals := range r.Header {
		k2 := strings.ToLower(k)
		if k2 == "upgrade" || k2 == "connection" || strings.HasPrefix(k2, "sec-websocket") || k2 == "cookie" || k2 == "origin" {
			continue
		}
		headers[k] = vals[0]
	}
	wsSubPath := strings.TrimPrefix(r.URL.RequestURI(), "/proxy/"+wsID)
	if wsSubPath == "" || wsSubPath[0] != '/' {
		wsSubPath = "/" + wsSubPath
	}
	openMsg := proxyWSMsg{Type: "ws_open", ID: chanID, Path: wsSubPath, Headers: headers}
	raw, _ := json.Marshal(openMsg)
	h.Relay.SendToAgent(wsID, raw)

	// Wait for ws_ready
	ctx, ctxCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer ctxCancel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var m proxyWSMsg
			if json.Unmarshal(msg, &m) == nil && m.Type == "ws_ready" && m.ID == chanID {
				goto ready
			}
		case <-ctx.Done():
			return
		}
	}
ready:
	ctxCancel()

	runCtx, runCancel := context.WithCancel(r.Context())
	defer runCancel()

	// Browser → Agent
	go func() {
		defer runCancel()
		for {
			_, data, err := browserConn.ReadMessage()
			if err != nil {
				break
			}
			m := proxyWSMsg{Type: "ws_data", ID: chanID, Data: base64.StdEncoding.EncodeToString(data)}
			raw, _ := json.Marshal(m)
			h.Relay.SendToAgent(wsID, raw)
		}
		m := proxyWSMsg{Type: "ws_close", ID: chanID}
		raw, _ := json.Marshal(m)
		h.Relay.SendToAgent(wsID, raw)
	}()

	// Agent → Browser
	for {
		select {
		case agentMsg, ok := <-ch:
			if !ok {
				return
			}
			var m proxyWSMsg
			if json.Unmarshal(agentMsg, &m) != nil || m.ID != chanID {
				continue
			}
			switch m.Type {
			case "ws_data":
				data, _ := base64.StdEncoding.DecodeString(m.Data)
				browserConn.WriteMessage(websocket.BinaryMessage, data)
			case "ws_close":
				return
			}
		case <-runCtx.Done():
			return
		}
	}
}
