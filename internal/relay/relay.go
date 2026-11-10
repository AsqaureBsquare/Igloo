// Package relay manages WebSocket connections between browser clients and
// igloo-agent processes running inside workspaces.
// Both sides connect to the same server; the relay stitches them together
// in memory — no external message broker needed.
package relay

import (
	"context"
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
	done chan struct{} // closed once when the connection is torn down
	once sync.Once
}

func newConn(ws *websocket.Conn) *conn {
	return &conn{ws: ws, send: make(chan []byte, 256), done: make(chan struct{})}
}

// close signals shutdown. The send channel is deliberately never closed: other
// goroutines may be mid-send, and sending on a closed channel panics.
func (c *conn) close() { c.once.Do(func() { close(c.done) }) }

// enqueue queues msg for the writer without blocking. It reports false when the
// connection is gone or its buffer is full (the message is dropped).
func (c *conn) enqueue(msg []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *conn) writePump() {
	defer c.ws.Close()
	for {
		select {
		case msg := <-c.send:
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// Relay keeps track of which agent and browsers are connected per workspace.
type Relay struct {
	mu       sync.RWMutex
	agents   map[string]*conn                 // workspaceID → agent conn
	browsers map[string]map[string]*conn      // workspaceID → connID → browser conn
	subs     map[string]map[string]chan []byte // workspaceID → subID → channel (server-side listeners)
	remotes  map[string]*conn                  // workspaceID → link to the replica that owns the agent
	peers    map[string]map[string]*conn       // workspaceID → streams served to peer replicas
	cluster  *Cluster                          // nil when running as a single replica

	dialMu sync.Mutex
	dials  map[string]*sync.Mutex // workspaceID → remote-dial-in-progress lock

	handlerMu     sync.RWMutex
	agentHandlers []func(workspaceID string, msg []byte)
}

func New() *Relay {
	return &Relay{
		agents:   make(map[string]*conn),
		browsers: make(map[string]map[string]*conn),
		subs:     make(map[string]map[string]chan []byte),
		remotes:  make(map[string]*conn),
		peers:    make(map[string]map[string]*conn),
	}
}

// OnAgentMessage registers a function called for every message any agent sends.
func (r *Relay) OnAgentMessage(fn func(workspaceID string, msg []byte)) {
	r.handlerMu.Lock()
	r.agentHandlers = append(r.agentHandlers, fn)
	r.handlerMu.Unlock()
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// Subscribe returns a channel that receives all messages the agent sends for
// the given workspace. Call cancel when done.
func (r *Relay) Subscribe(workspaceID string) (<-chan []byte, func()) {
	id := randomID()
	ch := make(chan []byte, 64)

	r.mu.Lock()
	if r.subs[workspaceID] == nil {
		r.subs[workspaceID] = make(map[string]chan []byte)
	}
	r.subs[workspaceID][id] = ch
	r.mu.Unlock()

	return ch, func() {
		r.mu.Lock()
		delete(r.subs[workspaceID], id)
		if len(r.subs[workspaceID]) == 0 {
			delete(r.subs, workspaceID)
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
	r.RegisterAgentNotify(workspaceID, connID, ws, nil)
}

// RegisterAgentNotify is RegisterAgent that calls onRegistered once the agent can
// receive messages (AgentConnected is true). Anything that announces the workspace
// as usable, such as its status, belongs there: announcing earlier lets clients act
// in the gap and get "agent not connected".
func (r *Relay) RegisterAgentNotify(workspaceID, connID string, ws *websocket.Conn, onRegistered func()) {
	c := newConn(ws)
	go c.writePump()

	r.mu.Lock()
	r.agents[workspaceID] = c
	r.mu.Unlock()
	// Record ownership before announcing the agent, so a request landing on another
	// replica right after the announcement can already find its way here.
	r.claimOwnership(workspaceID)
	if onRegistered != nil {
		onRegistered()
	}

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
	last := r.agents[workspaceID] == c
	if last {
		delete(r.agents, workspaceID)
	}
	r.mu.Unlock()
	if last {
		// A reconnect registers the new connection (and re-claims ownership, possibly
		// on another replica) before the old one closes — release only when this was
		// still the live connection.
		r.releaseOwnership(workspaceID)
	}
	c.close()
	log.Printf("[relay] agent disconnected workspace=%s", workspaceID)
}

// RegisterBrowser registers a browser WebSocket for a workspace.
func (r *Relay) RegisterBrowser(workspaceID, connID string, ws *websocket.Conn) {
	c := newConn(ws)
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
	if len(r.browsers[workspaceID]) == 0 {
		delete(r.browsers, workspaceID)
	}
	r.mu.Unlock()
	c.close()
	log.Printf("[relay] browser disconnected workspace=%s conn=%s", workspaceID, connID)
}

// AgentConnected reports whether the workspace's agent is reachable: connected to this
// replica, or (in a cluster) to a peer holding a live ownership lease.
func (r *Relay) AgentConnected(workspaceID string) bool {
	r.mu.RLock()
	_, ok := r.agents[workspaceID]
	c := r.cluster
	r.mu.RUnlock()
	if ok || c == nil {
		return ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	o, err := c.Owners.GetAgentOwner(ctx, workspaceID)
	return err == nil && o != nil && o.ReplicaID != c.ReplicaID
}

// browserMessage reports whether a message from the agent may be shown to the browsers connected
// to the workspace: only the terminal's. Everything else an agent sends answers a request made by
// the server (command results, file contents, forwarded web responses, tunnel data) and belongs to
// that request alone, so it goes to the server-side subscribers and handlers, never to a browser.
func browserMessage(msg []byte) bool {
	var m struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return false
	}
	switch m.Type {
	case "pty_output", "pty_ready", "pty_exit", "pty_error":
		return true
	}
	return false
}

func (r *Relay) broadcast(workspaceID string, msg []byte) {
	toBrowsers := browserMessage(msg)
	r.mu.RLock()
	if toBrowsers {
		for _, c := range r.browsers[workspaceID] {
			c.enqueue(msg)
		}
	}
	for _, ch := range r.subs[workspaceID] {
		select {
		case ch <- msg:
		default:
		}
	}
	r.mu.RUnlock()

	r.handlerMu.RLock()
	for _, fn := range r.agentHandlers {
		fn(workspaceID, msg)
	}
	r.handlerMu.RUnlock()
}

func (r *Relay) forwardToAgent(workspaceID string, msg []byte) {
	r.mu.RLock()
	agent := r.agents[workspaceID]
	r.mu.RUnlock()

	if agent == nil {
		// Not here — in a cluster the owning replica's relay takes it from here.
		agent = r.remoteFor(workspaceID)
	}
	if agent == nil {
		// No agent — send an error back: encode into pty_error message
		errMsg, _ := json.Marshal(Message{Type: "pty_error", Data: "agent not connected"})
		r.broadcast(workspaceID, errMsg)
		return
	}

	agent.enqueue(msg)
}
