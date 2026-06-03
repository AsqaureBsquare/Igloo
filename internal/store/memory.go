// Package store provides an in-memory Store implementation for local development.
// No AWS required — start with: igloo server --local
package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("already exists")

type MemoryStore struct {
	mu         sync.RWMutex
	users      map[string]*models.User      // id → user
	byEmail    map[string]string            // email → id
	workspaces map[string]*models.Workspace // id → workspace
	templates  map[string]*models.Template  // id → template
	jobs       map[string]*models.ProvisionJob
}

func NewMemory() *MemoryStore {
	return &MemoryStore{
		users:      make(map[string]*models.User),
		byEmail:    make(map[string]string),
		workspaces: make(map[string]*models.Workspace),
		templates:  make(map[string]*models.Template),
		jobs:       make(map[string]*models.ProvisionJob),
	}
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateUser(_ context.Context, u *models.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byEmail[u.Email]; exists {
		return ErrConflict
	}
	cp := *u
	s.users[u.ID] = &cp
	s.byEmail[u.Email] = u.ID
	return nil
}

func (s *MemoryStore) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[email]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s.users[id]
	return &cp, nil
}

func (s *MemoryStore) GetUser(_ context.Context, id string) (*models.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *u
	return &cp, nil
}

func (s *MemoryStore) UpdateUser(_ context.Context, u *models.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; !ok {
		return ErrNotFound
	}
	cp := *u
	s.users[u.ID] = &cp
	return nil
}

func (s *MemoryStore) ListUsers(_ context.Context) ([]*models.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.User, 0, len(s.users))
	for _, u := range s.users {
		cp := *u
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemoryStore) DeleteUser(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.byEmail, u.Email)
	delete(s.users, id)
	return nil
}

// ─── Workspaces ───────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateWorkspace(_ context.Context, w *models.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *w
	if cp.Meta != nil {
		m := make(map[string]string, len(cp.Meta))
		for k, v := range cp.Meta {
			m[k] = v
		}
		cp.Meta = m
	}
	s.workspaces[w.ID] = &cp
	return nil
}

func (s *MemoryStore) GetWorkspace(_ context.Context, id string) (*models.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.workspaces[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *w
	return &cp, nil
}

func (s *MemoryStore) ListWorkspaces(_ context.Context, userID string) ([]*models.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.Workspace
	for _, w := range s.workspaces {
		if w.UserID == userID {
			cp := *w
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateWorkspace(_ context.Context, w *models.Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[w.ID]; !ok {
		return ErrNotFound
	}
	cp := *w
	cp.UpdatedAt = time.Now()
	s.workspaces[w.ID] = &cp
	return nil
}

func (s *MemoryStore) DeleteWorkspace(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workspaces[id]; !ok {
		return ErrNotFound
	}
	delete(s.workspaces, id)
	return nil
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (s *MemoryStore) UpsertTemplate(_ context.Context, t *models.Template) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *t
	s.templates[t.ID] = &cp
	return nil
}

func (s *MemoryStore) GetTemplate(_ context.Context, id string) (*models.Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.templates[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *MemoryStore) ListTemplates(_ context.Context) ([]*models.Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Template, 0, len(s.templates))
	for _, t := range s.templates {
		cp := *t
		out = append(out, &cp)
	}
	return out, nil
}

// ─── Jobs ─────────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateJob(_ context.Context, j *models.ProvisionJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *j
	s.jobs[j.ID] = &cp
	return nil
}

func (s *MemoryStore) GetJob(_ context.Context, id string) (*models.ProvisionJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *j
	return &cp, nil
}

func (s *MemoryStore) ClaimNextJob(_ context.Context, provisionerID string) (*models.ProvisionJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var oldest *models.ProvisionJob
	for _, j := range s.jobs {
		if j.Status != models.JobPending {
			continue
		}
		if oldest == nil || j.CreatedAt.Before(oldest.CreatedAt) {
			oldest = j
		}
	}
	if oldest == nil {
		return nil, nil
	}
	now := time.Now()
	oldest.Status = models.JobRunning
	oldest.UpdatedAt = now
	oldest.ProvisionerID = provisionerID
	oldest.Heartbeat = now
	cp := *oldest
	return &cp, nil
}

func (s *MemoryStore) UpdateJobHeartbeat(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	j.Heartbeat = time.Now()
	return nil
}

func (s *MemoryStore) UpdateJob(_ context.Context, j *models.ProvisionJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.UpdatedAt = time.Now()
	cp := *j
	s.jobs[j.ID] = &cp
	return nil
}

func (s *MemoryStore) ListStaleJobs(_ context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-olderThan)
	var stale []*models.ProvisionJob
	for _, j := range s.jobs {
		if j.Status == models.JobRunning && j.UpdatedAt.Before(cutoff) {
			cp := *j
			stale = append(stale, &cp)
		}
	}
	return stale, nil
}

func (s *MemoryStore) ListStaleDaemonJobs(_ context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-olderThan)
	var stale []*models.ProvisionJob
	for _, j := range s.jobs {
		if j.Status == models.JobRunning && !j.Heartbeat.IsZero() &&
			j.ProvisionerID != "embedded" && j.Heartbeat.Before(cutoff) {
			cp := *j
			stale = append(stale, &cp)
		}
	}
	return stale, nil
}
