package tailnet

import (
	"log"
	"net"
	"net/netip"
	"sync"

	"github.com/gorilla/websocket"
	wgconn "github.com/tailscale/wireguard-go/conn"
)

// WSBind implements wireguard-go's conn.Bind over a WebSocket connection.
// WireGuard UDP packets are sent as binary WebSocket frames.
// This lets WireGuard work through ALBs and NAT without raw UDP sockets.
type WSBind struct {
	mu        sync.Mutex
	ws        *websocket.Conn // write-protected by mu
	incoming  chan []byte      // populated by the consumer goroutine
	closed    bool
	sentOnce  sync.Once // one-shot log for first outbound WG packet
	recvdOnce sync.Once // one-shot log for first inbound WG packet
}

var _ wgconn.Bind = (*WSBind)(nil)

func NewWSBind(ws *websocket.Conn) *WSBind {
	return &WSBind{
		ws:       ws,
		incoming: make(chan []byte, 256),
	}
}

// Deliver injects a packet received from the WebSocket into the WireGuard
// device. Called by the goroutine that reads from the WebSocket connection.
func (b *WSBind) Deliver(pkt []byte) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	ch := b.incoming
	b.mu.Unlock()
	b.recvdOnce.Do(func() { log.Printf("[tailnet] first WG packet received from relay (%d bytes)", len(pkt)) })
	select {
	case ch <- cp:
	default: // drop if full
	}
}

// ─── conn.Bind interface ──────────────────────────────────────────────────────

func (b *WSBind) Open(_ uint16) ([]wgconn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	// Recreate the channel each Open call. The recv closure captures ch directly
	// (not b.incoming) so goroutines from different Open calls each block on
	// their own channel. When Close() closes b.incoming, only the goroutines
	// from the last Open() are affected — old goroutines already exited when
	// their captured channel was closed by the previous Close() call.
	ch := make(chan []byte, 256)
	b.incoming = ch
	b.closed = false
	b.mu.Unlock()

	recv := func(packets [][]byte, sizes []int, eps []wgconn.Endpoint) (int, error) {
		pkt, ok := <-ch // capture ch, not b.incoming — race-free
		if !ok {
			return 0, net.ErrClosed
		}
		n := copy(packets[0], pkt)
		sizes[0] = n
		eps[0] = wsEP{}
		return 1, nil
	}
	return []wgconn.ReceiveFunc{recv}, 0, nil
}

func (b *WSBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.incoming)
	}
	return nil
}

func (b *WSBind) SetMark(_ uint32) error { return nil }

// Send is called by the WireGuard device to transmit encrypted packets.
// We route them over the WebSocket to the server, which relays them.
func (b *WSBind) Send(bufs [][]byte, _ wgconn.Endpoint, offset int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return net.ErrClosed
	}
	for _, buf := range bufs {
		pkt := buf[offset:]
		if len(pkt) == 0 {
			continue
		}
		b.sentOnce.Do(func() { log.Printf("[tailnet] first WG packet sent to relay (%d bytes)", len(pkt)) })
		if err := b.ws.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
			return err
		}
	}
	return nil
}

func (b *WSBind) ParseEndpoint(_ string) (wgconn.Endpoint, error) {
	return wsEP{}, nil
}

func (b *WSBind) BatchSize() int { return 1 }

// ─── Endpoint (single fake endpoint — all traffic goes via WebSocket relay) ──

type wsEP struct{}

func (wsEP) ClearSrc()           {}
func (wsEP) SrcToString() string { return "" }
func (wsEP) DstToString() string { return "0.0.0.0:0" }
func (wsEP) DstToBytes() []byte  { return []byte{0, 0, 0, 0, 0, 0} }
func (wsEP) DstIP() netip.Addr   { return netip.Addr{} }
func (wsEP) SrcIP() netip.Addr   { return netip.Addr{} }
