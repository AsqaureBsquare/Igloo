package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means the Igloo server could not be asked. The gateway refuses traffic
// it cannot authorize rather than guessing.
var ErrUnavailable = errors.New("igloo server unavailable")

// ServerClient asks the Igloo server who a token belongs to and sends it audit events.
// It authenticates with the guard key and caches answers briefly, so a busy workspace
// does not turn every request into a server call and a policy change still takes effect
// within TTL.
type ServerClient struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	TTL     time.Duration // how long an answer is reused (default 15s)

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	id      *Identity // nil = known-bad token
	expires time.Time
	stale   time.Time // until when it may stand in if the server is down
}

func (c *ServerClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (c *ServerClient) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return 15 * time.Second
}

func (c *ServerClient) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	return c.client().Do(req)
}

// Authorize implements Authorizer.
func (c *ServerClient) Authorize(ctx context.Context, token string) (*Identity, error) {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := time.Now()

	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cacheEntry{}
	}
	e, have := c.cache[key]
	c.mu.Unlock()
	if have && now.Before(e.expires) {
		if e.id == nil {
			return nil, ErrUnauthorized
		}
		cp := *e.id
		return &cp, nil
	}

	resp, err := c.post(ctx, "/api/guard/authorize", map[string]string{"token": token})
	if err != nil {
		return c.fallback(e, have, now, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		c.store(key, cacheEntry{id: nil, expires: now.Add(c.ttl() / 3), stale: now})
		return nil, ErrUnauthorized
	case resp.StatusCode != http.StatusOK:
		return c.fallback(e, have, now, fmt.Errorf("server answered %d", resp.StatusCode))
	}
	var id Identity
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil || id.WorkspaceID == "" {
		return c.fallback(e, have, now, fmt.Errorf("unreadable answer: %v", err))
	}
	c.store(key, cacheEntry{id: &id, expires: now.Add(c.ttl()), stale: now.Add(5 * c.ttl())})
	return &id, nil
}

// fallback covers a server outage: a recently seen good answer keeps working for a short
// while (a restart of the Igloo server should not cut every workspace off); anything
// else is refused.
func (c *ServerClient) fallback(e cacheEntry, have bool, now time.Time, cause error) (*Identity, error) {
	if have && e.id != nil && now.Before(e.stale) {
		cp := *e.id
		return &cp, nil
	}
	return nil, fmt.Errorf("%w: %v", ErrUnavailable, cause)
}

func (c *ServerClient) store(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) > 4096 { // bound memory; entries are cheap to refetch
		for k, v := range c.cache {
			if time.Now().After(v.stale) {
				delete(c.cache, k)
			}
		}
	}
	c.cache[key] = e
}

// SendEvents delivers a batch of audit events.
func (c *ServerClient) SendEvents(ctx context.Context, evs []Event) error {
	resp, err := c.post(ctx, "/api/guard/events", map[string]any{"events": evs})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("server answered %d", resp.StatusCode)
	}
	return nil
}
