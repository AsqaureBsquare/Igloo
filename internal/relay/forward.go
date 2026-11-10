// Replica-to-replica forwarding. With more than one server replica, the agent for a
// workspace is connected to exactly one of them; a replica that needs an agent it does
// not hold looks the owner up in the shared store and opens one internal WebSocket to
// it, and the two relays join the streams. With a single replica (no JoinCluster) none
// of this runs and the relay behaves exactly as before.
package relay

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/igloo-dev/igloo/internal/models"
)

// OwnerStore is the slice of the store the relay needs to know which replica holds a
// workspace's agent. store.AgentOwnerStore satisfies it.
type OwnerStore interface {
	UpsertAgentOwner(ctx context.Context, o *models.AgentOwner) error
	GetAgentOwner(ctx context.Context, workspaceID string) (*models.AgentOwner, error)
	RenewAgentOwner(ctx context.Context, workspaceID, replicaID string, leaseUntil time.Time) error
	DeleteAgentOwner(ctx context.Context, workspaceID, replicaID string) error
}

// Cluster configures one replica's membership: who it is, where peers can reach its
// internal listener, and the shared key peers must present.
type Cluster struct {
	ReplicaID string
	PodAddr   string // host:port of this replica's internal listener, reachable by peers
	Key       string
	Owners    OwnerStore
	Lease     time.Duration // how long an ownership row lives unrenewed (default 15s)
	Renew     time.Duration // how often leases are renewed (default 5s)
}

const storeTimeout = 3 * time.Second

// JoinCluster turns on ownership tracking and forwarding. Call it once, before the
// relay serves connections; the renewal loop stops when ctx is cancelled.
func (r *Relay) JoinCluster(ctx context.Context, c Cluster) {
	if c.Lease == 0 {
		c.Lease = 15 * time.Second
	}
	if c.Renew == 0 {
		c.Renew = 5 * time.Second
	}
	r.mu.Lock()
	r.cluster = &c
	r.mu.Unlock()
	go r.renewLoop(ctx, &c)
}

func (r *Relay) clusterConfig() *Cluster {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cluster
}

// renewLoop extends the lease of every agent this replica still holds. Renewal only
// touches rows that still name this replica, so a replica with a stale half-dead
// connection cannot take a workspace back from the one the agent reconnected to.
func (r *Relay) renewLoop(ctx context.Context, c *Cluster) {
	t := time.NewTicker(c.Renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.mu.RLock()
			ids := make([]string, 0, len(r.agents))
			for id := range r.agents {
				ids = append(ids, id)
			}
			r.mu.RUnlock()
			until := time.Now().Add(c.Lease)
			for _, id := range ids {
				cctx, cancel := context.WithTimeout(ctx, storeTimeout)
				if err := c.Owners.RenewAgentOwner(cctx, id, c.ReplicaID, until); err != nil {
					log.Printf("[relay] lease renew workspace=%s: %v", id, err)
				}
				cancel()
			}
		}
	}
}

// claimOwnership records this replica as the agent's owner. Unconditional on purpose:
// the agent just connected here, and the most recent connection wins.
func (r *Relay) claimOwnership(workspaceID string) {
	c := r.clusterConfig()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	err := c.Owners.UpsertAgentOwner(ctx, &models.AgentOwner{
		WorkspaceID: workspaceID,
		ReplicaID:   c.ReplicaID,
		PodAddr:     c.PodAddr,
		LeaseUntil:  time.Now().Add(c.Lease),
	})
	if err != nil {
		log.Printf("[relay] claim ownership workspace=%s: %v", workspaceID, err)
	}
}

// releaseOwnership runs when the last local agent connection for the workspace closes:
// the row goes away (only if still ours) and the streams served to peer replicas close,
// so their next message re-looks-up the owner instead of feeding a dead end.
func (r *Relay) releaseOwnership(workspaceID string) {
	c := r.clusterConfig()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := c.Owners.DeleteAgentOwner(ctx, workspaceID, c.ReplicaID); err != nil {
		log.Printf("[relay] release ownership workspace=%s: %v", workspaceID, err)
	}
	r.mu.Lock()
	peers := r.peers[workspaceID]
	delete(r.peers, workspaceID)
	r.mu.Unlock()
	for _, p := range peers {
		p.close()
	}
}

// remoteFor returns a live link to the replica that owns the workspace's agent, dialing
// one when needed. nil when there is no cluster, no live owner, or the owner is this
// replica itself (then the agent is simply gone).
func (r *Relay) remoteFor(workspaceID string) *conn {
	c := r.clusterConfig()
	if c == nil {
		return nil
	}
	r.mu.RLock()
	link := r.remotes[workspaceID]
	r.mu.RUnlock()
	if link != nil {
		return link
	}

	lock := r.dialLock(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	r.mu.RLock()
	link = r.remotes[workspaceID]
	r.mu.RUnlock()
	if link != nil {
		return link
	}

	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	owner, err := c.Owners.GetAgentOwner(ctx, workspaceID)
	cancel()
	if err != nil || owner == nil || owner.ReplicaID == c.ReplicaID {
		return nil
	}

	d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{"Authorization": []string{"Bearer " + c.Key}}
	ws, _, err := d.Dial("ws://"+owner.PodAddr+"/internal/relay/"+workspaceID, hdr)
	if err != nil {
		log.Printf("[relay] dial owner replica=%s workspace=%s: %v", owner.ReplicaID, workspaceID, err)
		return nil
	}
	link = newConn(ws)
	go link.writePump()
	r.mu.Lock()
	r.remotes[workspaceID] = link
	r.mu.Unlock()
	log.Printf("[relay] forwarding workspace=%s to replica=%s", workspaceID, owner.ReplicaID)

	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			r.broadcast(workspaceID, msg)
		}
		r.mu.Lock()
		if r.remotes[workspaceID] == link {
			delete(r.remotes, workspaceID)
		}
		r.mu.Unlock()
		link.close()
	}()
	return link
}

func (r *Relay) dialLock(workspaceID string) *sync.Mutex {
	r.dialMu.Lock()
	defer r.dialMu.Unlock()
	if r.dials == nil {
		r.dials = map[string]*sync.Mutex{}
	}
	m := r.dials[workspaceID]
	if m == nil {
		m = &sync.Mutex{}
		r.dials[workspaceID] = m
	}
	return m
}

// InternalHandler serves replica-to-replica streams at .../{workspaceID}. Mount it on
// the internal listener only — it must never be reachable through the ingress, and a
// NetworkPolicy should allow only server pods to reach its port.
func (r *Relay) InternalHandler() http.Handler {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		c := r.clusterConfig()
		if c == nil {
			http.Error(w, "not clustered", http.StatusServiceUnavailable)
			return
		}
		want := "Bearer " + c.Key
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		workspaceID := path.Base(req.URL.Path)
		if workspaceID == "" || workspaceID == "." || workspaceID == "/" {
			http.Error(w, "workspace id missing", http.StatusBadRequest)
			return
		}
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.serveInternal(workspaceID, ws)
	})
}

// serveInternal joins a peer replica's stream to this relay: everything the local agent
// sends goes to the peer (which applies the browser filter on its own side), and
// everything the peer sends goes to the agent — the peer stream is a server-side
// subscriber, so the output rules for browsers are untouched.
func (r *Relay) serveInternal(workspaceID string, ws *websocket.Conn) {
	c := newConn(ws)
	go c.writePump()

	id := randomID()
	r.mu.Lock()
	if r.peers[workspaceID] == nil {
		r.peers[workspaceID] = make(map[string]*conn)
	}
	r.peers[workspaceID][id] = c
	r.mu.Unlock()

	ch, cancel := r.Subscribe(workspaceID)
	defer cancel()
	go func() {
		for {
			select {
			case msg := <-ch:
				c.enqueue(msg)
			case <-c.done:
				return
			}
		}
	}()

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		r.forwardToAgent(workspaceID, msg)
	}

	r.mu.Lock()
	delete(r.peers[workspaceID], id)
	if len(r.peers[workspaceID]) == 0 {
		delete(r.peers, workspaceID)
	}
	r.mu.Unlock()
	c.close()
}
