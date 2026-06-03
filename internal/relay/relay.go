// Package relay manages WebSocket connections between browser clients and
// igloo-agent processes running inside workspaces.
// Both sides connect to the same server; the relay stitches them together
// in memory — no external message broker needed.
package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

type Message struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"` // base64 for pty bytes
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

type conn struct {
	ws   *websocket.Conn
	send chan []byte
}

func (c *conn) writePump() {
	defer c.ws.Close()
	for msg := range c.send {
		if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}
}

// Relay keeps track of which agent and browsers are connected per workspace.
type Relay struct {
	mu       sync.RWMutex
	agents   map[string]*conn                  // workspaceID → agent conn
	browsers map[string]map[string]*conn       // workspaceID → connID → browser conn
	subs     map[string]map[string]chan []byte  // workspaceID → subID → channel (server-side listeners)
}

func New() *Relay {
	return &Relay{
		agents:   make(map[string]*conn),
		browsers: make(map[string]map[string]*conn),
		subs:     make(map[string]map[string]chan []byte),
	}
}

// Subscribe returns a channel that receives all messages the agent sends for
// the given workspace. Call cancel when done.
func (r *Relay) Subscribe(workspaceID string) (<-chan []byte, func()) {
	b := make([]byte, 8)
	rand.Read(b)
	id := fmt.Sprintf("%x", b)
	ch := make(chan []byte, 64)

	r.mu.Lock()
	if r.subs[workspaceID] == nil {
		r.subs[workspaceID] = make(map[string]chan []byte)
	}
	r.subs[workspaceID][id] = ch
	r.mu.Unlock()

	return ch, func() {
		r.mu.Lock()
		if r.subs[workspaceID] != nil {
			delete(r.subs[workspaceID], id)
		}
		r.mu.Unlock()
	}
}

// SendToAgent forwards a raw message to the agent for the given workspace.
func (r *Relay) SendToAgent(workspaceID string, msg []byte) {
	r.forwardToAgent(workspaceID, msg)
}

// RegisterAgent registers an agent WebSocket for a workspace and starts
// forwarding its messages to all connected browsers.
func (r *Relay) RegisterAgent(workspaceID, connID string, ws *websocket.Conn) {
	c := &conn{ws: ws, send: make(chan []byte, 256)}
	go c.writePump()

	r.mu.Lock()
	r.agents[workspaceID] = c
	r.mu.Unlock()

	log.Printf("[relay] agent connected  workspace=%s", workspaceID)

	// Read loop — forward agent messages to all browsers
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		r.broadcast(workspaceID, msg)
	}

	r.mu.Lock()
	if r.agents[workspaceID] == c {
		delete(r.agents, workspaceID)
	}
	r.mu.Unlock()
	close(c.send)
	log.Printf("[relay] agent disconnected workspace=%s", workspaceID)
}

// RegisterBrowser registers a browser WebSocket for a workspace.
func (r *Relay) RegisterBrowser(workspaceID, connID string, ws *websocket.Conn) {
	c := &conn{ws: ws, send: make(chan []byte, 256)}
	go c.writePump()

	r.mu.Lock()
	if r.browsers[workspaceID] == nil {
		r.browsers[workspaceID] = make(map[string]*conn)
	}
	r.browsers[workspaceID][connID] = c
	r.mu.Unlock()

	log.Printf("[relay] browser connected  workspace=%s conn=%s", workspaceID, connID)

	// Read loop — forward browser messages to agent
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		r.forwardToAgent(workspaceID, msg)
	}

	r.mu.Lock()
	delete(r.browsers[workspaceID], connID)
	r.mu.Unlock()
	close(c.send)
	log.Printf("[relay] browser disconnected workspace=%s conn=%s", workspaceID, connID)
}

// AgentConnected reports whether a live agent is registered for the workspace.
func (r *Relay) AgentConnected(workspaceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[workspaceID]
	return ok
}

func (r *Relay) broadcast(workspaceID string, msg []byte) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.browsers[workspaceID] {
		select {
		case c.send <- msg:
		default:
		}
	}
	for _, ch := range r.subs[workspaceID] {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (r *Relay) forwardToAgent(workspaceID string, msg []byte) {
	r.mu.RLock()
	agent := r.agents[workspaceID]
	r.mu.RUnlock()

	if agent == nil {
		// No agent — send an error back: encode into pty_error message
		errMsg, _ := json.Marshal(Message{Type: "pty_error", Data: "agent not connected"})
		r.broadcast(workspaceID, errMsg)
		return
	}

	select {
	case agent.send <- msg:
	default:
	}
}
