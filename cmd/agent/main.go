// igloo-agent — runs inside every workspace container.
// Dials the control plane WebSocket and handles PTY sessions + VS Code proxy.
//
// Required env vars (injected by igloo server at launch):
//   IGLOO_SERVER_URL      https://your-igloo-server.com
//   IGLOO_AGENT_TOKEN     signed JWT (workspace_id + role=agent)
//   IGLOO_WORKSPACE_ID    workspace UUID
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type Message struct {
	Type    string            `json:"type"`
	Data    string            `json:"data,omitempty"`
	Cols    uint16            `json:"cols,omitempty"`
	Rows    uint16            `json:"rows,omitempty"`
	ID      string            `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Status  int               `json:"status,omitempty"`
}

const codeServerPort = "13131"

var (
	writeMu sync.Mutex
	wsConns = struct {
		mu    sync.Mutex
		conns map[string]*websocket.Conn
	}{conns: make(map[string]*websocket.Conn)}
	tcpConns = struct {
		mu    sync.Mutex
		conns map[string]net.Conn
	}{conns: make(map[string]net.Conn)}
)

func main() {
	serverURL := os.Getenv("IGLOO_SERVER_URL")
	token := os.Getenv("IGLOO_AGENT_TOKEN")
	workspaceID := os.Getenv("IGLOO_WORKSPACE_ID")

	if serverURL == "" || token == "" {
		log.Println("[igloo-agent] IGLOO_SERVER_URL / IGLOO_AGENT_TOKEN not set — exiting")
		return
	}

	log.Printf("[igloo-agent] starting (workspace %s)", workspaceID)

	// Start code-server if installed
	go startCodeServer(workspaceID)

	backoff := time.Second
	for {
		if err := run(serverURL, token); err != nil {
			log.Printf("[igloo-agent] disconnected: %v — reconnecting in %v", err, backoff)
		}
		time.Sleep(backoff)
		if backoff < 2*time.Minute {
			backoff *= 2
		}
	}
}

func startCodeServer(workspaceID string) {
	path, err := exec.LookPath("code-server")
	if err != nil {
		log.Println("[igloo-agent] code-server not found — VS Code unavailable")
		return
	}

	// Wait a moment for the workspace to settle
	time.Sleep(2 * time.Second)

	cmd := exec.Command(path,
		"--bind-addr", "127.0.0.1:"+codeServerPort,
		"--auth", "none",
		"--disable-telemetry",
		"--user-data-dir", "/workspace/.vscode-data",
		"/workspace",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[igloo-agent] failed to start code-server: %v", err)
		return
	}
	log.Printf("[igloo-agent] code-server started on :%s (workspace=%s)", codeServerPort, workspaceID)
	cmd.Wait()
}

func run(serverURL, token string) error {
	wsBase := strings.Replace(serverURL, "https://", "wss://", 1)
	wsBase = strings.Replace(wsBase, "http://", "ws://", 1)

	u, err := url.Parse(wsBase + "/ws")
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("[igloo-agent] connected to %s", serverURL)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			writeMu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	var ptmx *os.File

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {

		// ─── PTY ─────────────────────────────────────────────────────────────
		case "pty_spawn":
			cols, rows := msg.Cols, msg.Rows
			if cols == 0 {
				cols = 80
			}
			if rows == 0 {
				rows = 24
			}
			if ptmx != nil {
				ptmx.Close()
			}
			cmd := exec.Command("/bin/bash")
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			ptmx, err = pty.Start(cmd)
			if err != nil {
				sendMsg(conn, Message{Type: "pty_error", Data: err.Error()})
				continue
			}
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})

			go func(p *os.File) {
				buf := make([]byte, 4096)
				for {
					n, err := p.Read(buf)
					if n > 0 {
						sendMsg(conn, Message{
							Type: "pty_output",
							Data: base64.StdEncoding.EncodeToString(buf[:n]),
						})
					}
					if err != nil {
						sendMsg(conn, Message{Type: "pty_exit"})
						return
					}
				}
			}(ptmx)

			sendMsg(conn, Message{Type: "pty_ready"})

		case "pty_data":
			if ptmx != nil && msg.Data != "" {
				decoded, err := base64.StdEncoding.DecodeString(msg.Data)
				if err != nil {
					decoded = []byte(msg.Data)
				}
				ptmx.Write(decoded)
			}

		case "pty_resize":
			if ptmx != nil {
				cols, rows := msg.Cols, msg.Rows
				if cols == 0 {
					cols = 80
				}
				if rows == 0 {
					rows = 24
				}
				pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
			}

		// ─── HTTP proxy ───────────────────────────────────────────────────────
		case "http_req":
			go handleHTTPReq(conn, msg)

		// ─── WebSocket proxy ──────────────────────────────────────────────────
		case "ws_open":
			go handleWSOpen(conn, msg)

		case "ws_data":
			wsConns.mu.Lock()
			csConn := wsConns.conns[msg.ID]
			wsConns.mu.Unlock()
			if csConn != nil && msg.Data != "" {
				data, _ := base64.StdEncoding.DecodeString(msg.Data)
				csConn.WriteMessage(websocket.BinaryMessage, data)
			}

		case "ws_close":
			wsConns.mu.Lock()
			csConn := wsConns.conns[msg.ID]
			delete(wsConns.conns, msg.ID)
			wsConns.mu.Unlock()
			if csConn != nil {
				csConn.Close()
			}

		// ─── TCP port-forward ─────────────────────────────────────────────────
		case "tcp_open":
			go handleTCPOpen(conn, msg)

		case "tcp_data":
			tcpConns.mu.Lock()
			c := tcpConns.conns[msg.ID]
			tcpConns.mu.Unlock()
			if c != nil && msg.Data != "" {
				data, _ := base64.StdEncoding.DecodeString(msg.Data)
				c.Write(data)
			}

		case "tcp_close":
			tcpConns.mu.Lock()
			c := tcpConns.conns[msg.ID]
			delete(tcpConns.conns, msg.ID)
			tcpConns.mu.Unlock()
			if c != nil {
				c.Close()
			}
		}
	}
}

func handleHTTPReq(conn *websocket.Conn, msg Message) {
	var bodyReader io.Reader = bytes.NewReader(nil)
	if msg.Body != "" {
		b, _ := base64.StdEncoding.DecodeString(msg.Body)
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(msg.Method, "http://127.0.0.1:"+codeServerPort+msg.Path, bodyReader)
	if err != nil {
		sendMsg(conn, Message{Type: "http_res", ID: msg.ID, Status: http.StatusBadGateway})
		return
	}
	for k, v := range msg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[igloo-agent] http_req %s %s: %v", msg.Method, msg.Path, err)
		sendMsg(conn, Message{Type: "http_res", ID: msg.ID, Status: http.StatusBadGateway})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	headers := make(map[string]string, len(resp.Header))
	for k, vals := range resp.Header {
		headers[k] = vals[0]
	}

	sendMsg(conn, Message{
		Type:    "http_res",
		ID:      msg.ID,
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString(respBody),
	})
}

func handleWSOpen(serverConn *websocket.Conn, msg Message) {
	wsURL := "ws://127.0.0.1:" + codeServerPort + msg.Path

	reqHeaders := http.Header{}
	for k, v := range msg.Headers {
		reqHeaders.Set(k, v)
	}

	csConn, _, err := websocket.DefaultDialer.Dial(wsURL, reqHeaders)
	if err != nil {
		log.Printf("[igloo-agent] ws_open failed %s: %v", wsURL, err)
		sendMsg(serverConn, Message{Type: "ws_close", ID: msg.ID})
		return
	}

	wsConns.mu.Lock()
	wsConns.conns[msg.ID] = csConn
	wsConns.mu.Unlock()

	sendMsg(serverConn, Message{Type: "ws_ready", ID: msg.ID})

	defer func() {
		wsConns.mu.Lock()
		delete(wsConns.conns, msg.ID)
		wsConns.mu.Unlock()
		csConn.Close()
		sendMsg(serverConn, Message{Type: "ws_close", ID: msg.ID})
	}()

	for {
		_, data, err := csConn.ReadMessage()
		if err != nil {
			return
		}
		sendMsg(serverConn, Message{
			Type: "ws_data",
			ID:   msg.ID,
			Data: base64.StdEncoding.EncodeToString(data),
		})
	}
}

func handleTCPOpen(serverConn *websocket.Conn, msg Message) {
	port := msg.Path
	if port == "" {
		port = "22"
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		log.Printf("[igloo-agent] tcp_open failed port %s: %v", port, err)
		sendMsg(serverConn, Message{Type: "tcp_close", ID: msg.ID})
		return
	}

	tcpConns.mu.Lock()
	tcpConns.conns[msg.ID] = c
	tcpConns.mu.Unlock()

	sendMsg(serverConn, Message{Type: "tcp_ready", ID: msg.ID})

	defer func() {
		tcpConns.mu.Lock()
		delete(tcpConns.conns, msg.ID)
		tcpConns.mu.Unlock()
		c.Close()
		sendMsg(serverConn, Message{Type: "tcp_close", ID: msg.ID})
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			sendMsg(serverConn, Message{
				Type: "tcp_data",
				ID:   msg.ID,
				Data: base64.StdEncoding.EncodeToString(buf[:n]),
			})
		}
		if err != nil {
			return
		}
	}
}

func sendMsg(conn *websocket.Conn, msg Message) {
	data, _ := json.Marshal(msg)
	writeMu.Lock()
	defer writeMu.Unlock()
	conn.WriteMessage(websocket.TextMessage, data)
}
