package tailnet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/tailscale/wireguard-go/device"
	"github.com/tailscale/wireguard-go/tun/netstack"
	"golang.org/x/crypto/curve25519"
)

// Node is a WireGuard node (agent or CLI client) connected to the Igloo
// coordinator. After Connect returns, Tnet provides a virtual TCP/IP stack
// routed through the WireGuard tunnel via the server relay.
type Node struct {
	Tnet   *netstack.Net
	MyIP   netip.Addr
	PeerIP netip.Addr
	dev    *device.Device
	bind   *WSBind
}

// Connect dials the Igloo coordinator WebSocket, exchanges WireGuard keys, and
// sets up a userspace WireGuard interface. role is "agent" or "client".
func Connect(serverURL, wsID, token, role string) (*Node, error) {
	privKey, pubKey, err := genKeyPair()
	if err != nil {
		return nil, fmt.Errorf("keygen: %w", err)
	}

	wsBase := strings.NewReplacer("https://", "wss://", "http://", "ws://").Replace(serverURL)
	u, err := url.Parse(fmt.Sprintf("%s/api/workspaces/%s/tailnet", wsBase, wsID))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("role", role)
	q.Set("token", token)
	u.RawQuery = q.Encode()

	ws, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial coordinator: %w", err)
	}

	// Send hello — identify role and share our WireGuard public key
	hello, _ := json.Marshal(ctrlMsg{Type: "hello", PubKey: hex.EncodeToString(pubKey[:])})
	if err := ws.WriteMessage(websocket.TextMessage, hello); err != nil {
		ws.Close()
		return nil, err
	}

	// Receive coordinator config (our assigned IP, and optionally the peer's key+IP)
	cfg, err := readCtrl(ws)
	if err != nil || cfg.Type != "config" {
		ws.Close()
		return nil, fmt.Errorf("unexpected coordinator response: type=%q err=%v", cfg.Type, err)
	}

	myIP, err := netip.ParseAddr(cfg.IP)
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("invalid assigned IP %q: %w", cfg.IP, err)
	}

	// Userspace WireGuard + gvisor TCP/IP stack — works in Fargate (no kernel modules)
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{myIP}, nil, 1420)
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("netstack: %w", err)
	}

	bind := NewWSBind(ws)
	dev := device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelSilent, "[wg] "))

	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\n", hex.EncodeToString(privKey[:]))); err != nil {
		dev.Close()
		ws.Close()
		return nil, fmt.Errorf("wg ipc: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		ws.Close()
		return nil, fmt.Errorf("wg up: %w", err)
	}

	node := &Node{Tnet: tnet, MyIP: myIP, dev: dev, bind: bind}

	// Peer info in initial config (client role gets this immediately)
	if cfg.PeerKey != "" && cfg.PeerIP != "" {
		peerIP, _ := netip.ParseAddr(cfg.PeerIP)
		node.PeerIP = peerIP
		if err := node.addPeer(cfg.PeerKey, cfg.PeerIP); err != nil {
			log.Printf("[tailnet] add initial peer: %v", err)
		}
	}

	// Background reader: binary frames → WireGuard, text frames → peer updates
	go func() {
		defer dev.Close()
		defer bind.Close()
		for {
			mt, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				bind.Deliver(data)
			case websocket.TextMessage:
				var m ctrlMsg
				if json.Unmarshal(data, &m) == nil && m.Type == "peer" && m.PeerKey != "" {
					peerIP, _ := netip.ParseAddr(m.PeerIP)
					node.PeerIP = peerIP
					if err := node.addPeer(m.PeerKey, m.PeerIP); err != nil {
						log.Printf("[tailnet] add peer: %v", err)
					} else {
						log.Printf("[tailnet] peer added ip=%s role=%s", m.PeerIP, role)
					}
				}
			}
		}
	}()

	return node, nil
}

func (n *Node) addPeer(pubkeyHex, ip string) error {
	// endpoint value is ignored by WSBind (all traffic goes via WebSocket relay),
	// but WireGuard requires a non-empty endpoint to send handshake initiations.
	// No persistent_keepalive_interval: only the TCP dialer should trigger the
	// WG handshake. When both sides proactively initiate simultaneously they end
	// up with mismatched sessions and the handshake never completes.
	ipc := fmt.Sprintf(
		"public_key=%s\nallowed_ip=%s/32\nendpoint=127.0.0.1:51820\n",
		pubkeyHex, ip,
	)
	return n.dev.IpcSet(ipc)
}

// DialSSH opens a TCP connection to port 22 on the peer's WireGuard IP.
func (n *Node) DialSSH(ctx context.Context) (net.Conn, error) {
	if !n.PeerIP.IsValid() {
		return nil, fmt.Errorf("peer IP not yet known")
	}
	return n.Tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(n.PeerIP, 22))
}

// DialTCP opens a TCP connection to an arbitrary port on the peer.
func (n *Node) DialTCP(ctx context.Context, port uint16) (net.Conn, error) {
	if !n.PeerIP.IsValid() {
		return nil, fmt.Errorf("peer IP not yet known")
	}
	return n.Tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(n.PeerIP, port))
}

// Close tears down the WireGuard device.
func (n *Node) Close() {
	n.dev.Close()
}

// ─── Curve25519 key generation ────────────────────────────────────────────────

func genKeyPair() (priv, pub [32]byte, err error) {
	if _, err = rand.Read(priv[:]); err != nil {
		return
	}
	pubSlice, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return
	}
	copy(pub[:], pubSlice)
	return
}
