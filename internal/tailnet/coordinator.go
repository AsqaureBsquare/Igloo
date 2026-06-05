// Package tailnet implements Igloo's built-in WireGuard coordination layer.
// The server acts as a relay hub: agents and CLI clients both connect via
// WebSocket, exchange WireGuard public keys, and the server relays encrypted
// WireGuard packets between them. No Tailscale account required.
package tailnet

import (
	"encoding/json"
	"log"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// ─── Wire protocol (text WebSocket frames = JSON control, binary = WG packets) ─

type ctrlMsg struct {
	Type    string `json:"type"`
	PubKey  string `json:"pubkey,omitempty"`   // WireGuard public key (hex)
	IP      string `json:"ip,omitempty"`        // assigned IP for this node
	PeerKey string `json:"peer_key,omitempty"`  // peer's WireGuard public key
	PeerIP  string `json:"peer_ip,omitempty"`   // peer's assigned IP
}

// ─── Coordinator ─────────────────────────────────────────────────────────────

type peer struct {
	mu     sync.Mutex
	ws     *websocket.Conn
	pubkey string
	ip     netip.Addr
	outCh  chan outMsg
}

type outMsg struct {
	text []byte // non-nil → send as TextMessage
	bin  []byte // non-nil → send as BinaryMessage
}

func newPeer(ws *websocket.Conn) *peer {
	p := &peer{ws: ws, outCh: make(chan outMsg, 512)}
	go p.writeLoop()
	return p
}

func (p *peer) writeLoop() {
	for m := range p.outCh {
		if m.text != nil {
			p.ws.WriteMessage(websocket.TextMessage, m.text)
		} else if m.bin != nil {
			p.ws.WriteMessage(websocket.BinaryMessage, m.bin)
		}
	}
}

func (p *peer) sendCtrl(m ctrlMsg) {
	data, _ := json.Marshal(m)
	select {
	case p.outCh <- outMsg{text: data}:
	default:
	}
}

func (p *peer) sendPacket(pkt []byte) {
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	select {
	case p.outCh <- outMsg{bin: cp}:
	default:
	}
}

func (p *peer) close() {
	close(p.outCh)
}

type wsEntry struct {
	mu      sync.RWMutex
	agent   *peer
	clients []*peer
	agentUp chan struct{} // closed once agent registers
}

// Coordinator is the per-server singleton. Each workspace gets one agent slot
// and N client slots. The coordinator relays encrypted WireGuard packets.
type Coordinator struct {
	mu         sync.Mutex
	workspaces map[string]*wsEntry
	counter    atomic.Uint32
}

func NewCoordinator() *Coordinator {
	return &Coordinator{workspaces: make(map[string]*wsEntry)}
}

func (c *Coordinator) entry(wsID string) *wsEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.workspaces[wsID]; ok {
		return e
	}
	e := &wsEntry{agentUp: make(chan struct{})}
	c.workspaces[wsID] = e
	return e
}

func (c *Coordinator) nextIP() netip.Addr {
	n := c.counter.Add(1)
	return netip.AddrFrom4([4]byte{100, 64, byte(n >> 8), byte(n) + 1})
}

// ServeAgent is called when a workspace agent connects via WebSocket.
func (c *Coordinator) ServeAgent(wsID string, ws *websocket.Conn) {
	entry := c.entry(wsID)

	// Read hello
	hello, err := readCtrl(ws)
	if err != nil || hello.Type != "hello" {
		ws.Close()
		return
	}

	p := newPeer(ws)
	p.pubkey = hello.PubKey
	p.ip = c.nextIP()

	entry.mu.Lock()
	entry.agent = p
	entry.mu.Unlock()

	// Signal that agent is up so waiting clients unblock
	select {
	case <-entry.agentUp:
	default:
		close(entry.agentUp)
	}

	// Tell agent its assigned IP
	p.sendCtrl(ctrlMsg{Type: "config", IP: p.ip.String()})

	// Notify any already-connected clients
	entry.mu.RLock()
	clients := append([]*peer{}, entry.clients...)
	entry.mu.RUnlock()
	for _, cl := range clients {
		cl.sendCtrl(ctrlMsg{Type: "peer", PeerKey: p.pubkey, PeerIP: p.ip.String()})
		p.sendCtrl(ctrlMsg{Type: "peer", PeerKey: cl.pubkey, PeerIP: cl.ip.String()})
	}

	log.Printf("[tailnet] agent up workspace=%s ip=%s", wsID, p.ip)
	defer func() {
		p.close()
		entry.mu.Lock()
		if entry.agent == p {
			entry.agent = nil
		}
		entry.mu.Unlock()
		log.Printf("[tailnet] agent down workspace=%s", wsID)
	}()

	// Relay: agent → clients
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		entry.mu.RLock()
		targets := append([]*peer{}, entry.clients...)
		entry.mu.RUnlock()
		for _, cl := range targets {
			cl.sendPacket(data)
		}
	}
}

// ServeClient is called when a CLI user connects via WebSocket.
func (c *Coordinator) ServeClient(wsID string, ws *websocket.Conn) {
	entry := c.entry(wsID)

	// Read hello
	hello, err := readCtrl(ws)
	if err != nil || hello.Type != "hello" {
		ws.Close()
		return
	}

	p := newPeer(ws)
	p.pubkey = hello.PubKey
	p.ip = c.nextIP()

	entry.mu.Lock()
	entry.clients = append(entry.clients, p)
	agent := entry.agent
	entry.mu.Unlock()

	// Send config, including agent peer info if agent is already up
	cfg := ctrlMsg{Type: "config", IP: p.ip.String()}
	if agent != nil {
		cfg.PeerKey = agent.pubkey
		cfg.PeerIP = agent.ip.String()
	}
	p.sendCtrl(cfg)

	// Tell agent about this new client
	if agent != nil {
		agent.sendCtrl(ctrlMsg{Type: "peer", PeerKey: p.pubkey, PeerIP: p.ip.String()})
	}

	log.Printf("[tailnet] client up workspace=%s ip=%s", wsID, p.ip)
	defer func() {
		p.close()
		entry.mu.Lock()
		for i, cl := range entry.clients {
			if cl == p {
				entry.clients = append(entry.clients[:i], entry.clients[i+1:]...)
				break
			}
		}
		entry.mu.Unlock()
		log.Printf("[tailnet] client down workspace=%s", wsID)
	}()

	// Relay: client → agent
	for {
		mt, data, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.BinaryMessage {
			continue
		}
		entry.mu.RLock()
		ag := entry.agent
		entry.mu.RUnlock()
		if ag != nil {
			ag.sendPacket(data)
		}
	}
}

func readCtrl(ws *websocket.Conn) (ctrlMsg, error) {
	_, data, err := ws.ReadMessage()
	if err != nil {
		return ctrlMsg{}, err
	}
	var m ctrlMsg
	return m, json.Unmarshal(data, &m)
}
