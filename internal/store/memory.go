// Package store provides an in-memory Store implementation for local development.
// No AWS required — start with: igloo server --local
package store

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("already exists")

type MemoryStore struct {
	mu             sync.RWMutex
	users          map[string]*models.User           // id → user
	byEmail        map[string]string                 // email → id
	workspaces     map[string]*models.Workspace      // id → workspace
	templates      map[string]*models.Template       // id → template
	jobs           map[string]*models.ProvisionJob
	provKeys       map[string]*models.ProvisionerKey // id → key
	byTokenHash    map[string]string                 // tokenHash → provKey id
	apiTokens      map[string]*models.APIToken       // id → token
	apiByHash      map[string]string                 // tokenHash → id
	platformConfig *models.PlatformConfig
	orgs           map[string]*models.Organization   // id → org
	orgsBySlug     map[string]string                 // slug → id
	orgMembers     map[string]*models.OrgMember      // orgID+userID → member
	teams          map[string]*models.Team            // id → team
	teamMembers    map[string]*models.TeamMember      // teamID+userID → member
	guardEvents    map[string][]models.GuardEvent     // workspaceID → events, oldest first
	agentOwners    map[string]*models.AgentOwner      // workspaceID → owning replica lease
}

func NewMemory() *MemoryStore {
	return &MemoryStore{
		users:       make(map[string]*models.User),
		byEmail:     make(map[string]string),
		workspaces:  make(map[string]*models.Workspace),
		templates:   make(map[string]*models.Template),
		jobs:        make(map[string]*models.ProvisionJob),
		provKeys:    make(map[string]*models.ProvisionerKey),
		byTokenHash: make(map[string]string),
		apiTokens:   make(map[string]*models.APIToken),
		apiByHash:   make(map[string]string),
		orgs:        make(map[string]*models.Organization),
		orgsBySlug:  make(map[string]string),
		orgMembers:  make(map[string]*models.OrgMember),
		teams:       make(map[string]*models.Team),
		teamMembers: make(map[string]*models.TeamMember),
		guardEvents: make(map[string][]models.GuardEvent),
		agentOwners: make(map[string]*models.AgentOwner),
	}
}

// ─── Agent ownership (multi-replica relay) ───────────────────────────────────

func (s *MemoryStore) UpsertAgentOwner(_ context.Context, o *models.AgentOwner) error {
	cp := *o
	s.mu.Lock()
	s.agentOwners[o.WorkspaceID] = &cp
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) GetAgentOwner(_ context.Context, workspaceID string) (*models.AgentOwner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o := s.agentOwners[workspaceID]
	if o == nil || !o.LeaseUntil.After(time.Now()) {
		return nil, nil
	}
	cp := *o
	return &cp, nil
}

func (s *MemoryStore) RenewAgentOwner(_ context.Context, workspaceID, replicaID string, leaseUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.agentOwners[workspaceID]; o != nil && o.ReplicaID == replicaID {
		o.LeaseUntil = leaseUntil
	}
	return nil
}

func (s *MemoryStore) DeleteAgentOwner(_ context.Context, workspaceID, replicaID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.agentOwners[workspaceID]; o != nil && o.ReplicaID == replicaID {
		delete(s.agentOwners, workspaceID)
	}
	return nil
}

// ─── APITokens ────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateAPIToken(_ context.Context, t *models.APIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiTokens[t.ID] = t
	s.apiByHash[t.TokenHash] = t.ID
	return nil
}

func (s *MemoryStore) GetAPITokenByHash(_ context.Context, hash string) (*models.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.apiByHash[hash]
	if !ok {
		return nil, ErrNotFound
	}
	t := s.apiTokens[id]
	cp := *t
	return &cp, nil
}

func (s *MemoryStore) ListAPITokens(_ context.Context, userID string) ([]*models.APIToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.APIToken
	for _, t := range s.apiTokens {
		if t.UserID == userID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteAPIToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.apiTokens[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.apiByHash, t.TokenHash)
	delete(s.apiTokens, id)
	return nil
}

func (s *MemoryStore) TouchAPIToken(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.apiTokens[id]; ok {
		t.LastUsedAt = time.Now()
	}
	return nil
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

func (s *MemoryStore) ListWorkspaces(_ context.Context, userID, orgID string) ([]*models.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.Workspace
	for _, w := range s.workspaces {
		if w.Status == models.StatusDeleted {
			continue
		}
		if userID != "" && w.UserID != userID {
			continue
		}
		if orgID != "" && w.OrgID != orgID {
			continue
		}
		cp := *w
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) }) // newest first, like Postgres
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

func (s *MemoryStore) ListTemplates(_ context.Context, orgID string) ([]*models.Template, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Template, 0, len(s.templates))
	for _, t := range s.templates {
		if t.Builtin || (orgID != "" && t.OrgID == orgID) {
			cp := *t
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name }) // same order as Postgres
	return out, nil
}

func (s *MemoryStore) DeleteTemplate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templates[id]; !ok {
		return ErrNotFound
	}
	delete(s.templates, id)
	return nil
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

// ─── ProvisionerKeys ──────────────────────────────────────────────────────────

func (s *MemoryStore) CreateProvisionerKey(_ context.Context, k *models.ProvisionerKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *k
	s.provKeys[k.ID] = &cp
	s.byTokenHash[k.TokenHash] = k.ID
	return nil
}

func (s *MemoryStore) GetProvisionerKeyByTokenHash(_ context.Context, hash string) (*models.ProvisionerKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byTokenHash[hash]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s.provKeys[id]
	return &cp, nil
}

func (s *MemoryStore) ListProvisionerKeys(_ context.Context) ([]*models.ProvisionerKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.ProvisionerKey, 0, len(s.provKeys))
	for _, k := range s.provKeys {
		cp := *k
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemoryStore) DeleteProvisionerKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.provKeys[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.byTokenHash, k.TokenHash)
	delete(s.provKeys, id)
	return nil
}

func (s *MemoryStore) TouchProvisionerKey(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.provKeys[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now()
	k.LastUsedAt = &now
	return nil
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

func (s *MemoryStore) GetPlatformConfig(_ context.Context) (*models.PlatformConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.platformConfig == nil {
		return &models.PlatformConfig{}, nil
	}
	cp := *s.platformConfig
	return &cp, nil
}

func (s *MemoryStore) SavePlatformConfig(_ context.Context, cfg *models.PlatformConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *cfg
	s.platformConfig = &cp
	return nil
}

// ─── Organizations ────────────────────────────────────────────────────────────

func orgMemberKey(orgID, userID string) string { return orgID + ":" + userID }
func teamMemberKey(teamID, userID string) string { return teamID + ":" + userID }

func (s *MemoryStore) CreateOrg(_ context.Context, o *models.Organization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.orgsBySlug[o.Slug]; exists {
		return ErrConflict
	}
	cp := *o
	s.orgs[o.ID] = &cp
	s.orgsBySlug[o.Slug] = o.ID
	return nil
}

func (s *MemoryStore) GetOrg(_ context.Context, id string) (*models.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orgs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *o
	return &cp, nil
}

func (s *MemoryStore) GetOrgBySlug(_ context.Context, slug string) (*models.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.orgsBySlug[slug]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *s.orgs[id]
	return &cp, nil
}

func (s *MemoryStore) ListOrgs(_ context.Context) ([]*models.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Organization, 0, len(s.orgs))
	for _, o := range s.orgs {
		cp := *o
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemoryStore) UpdateOrg(_ context.Context, o *models.Organization) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.orgs[o.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.Slug != o.Slug {
		delete(s.orgsBySlug, existing.Slug)
		s.orgsBySlug[o.Slug] = o.ID
	}
	cp := *o
	s.orgs[o.ID] = &cp
	return nil
}

func (s *MemoryStore) DeleteOrg(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.orgsBySlug, o.Slug)
	delete(s.orgs, id)
	for k, m := range s.orgMembers {
		if m.OrgID == id {
			delete(s.orgMembers, k)
		}
	}
	for _, t := range s.teams {
		if t.OrgID == id {
			delete(s.teams, t.ID)
			for k, tm := range s.teamMembers {
				if tm.TeamID == t.ID {
					delete(s.teamMembers, k)
				}
			}
		}
	}
	return nil
}

// ─── Org members ──────────────────────────────────────────────────────────────

func (s *MemoryStore) AddOrgMember(_ context.Context, m *models.OrgMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *m
	s.orgMembers[orgMemberKey(m.OrgID, m.UserID)] = &cp
	return nil
}

func (s *MemoryStore) GetOrgMember(_ context.Context, orgID, userID string) (*models.OrgMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.orgMembers[orgMemberKey(orgID, userID)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

func (s *MemoryStore) ListOrgMembers(_ context.Context, orgID string) ([]*models.OrgMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.OrgMember
	for _, m := range s.orgMembers {
		if m.OrgID == orgID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListUserOrgs(_ context.Context, userID string) ([]*models.OrgMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.OrgMember
	for _, m := range s.orgMembers {
		if m.UserID == userID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateOrgMember(_ context.Context, m *models.OrgMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := orgMemberKey(m.OrgID, m.UserID)
	if _, ok := s.orgMembers[key]; !ok {
		return ErrNotFound
	}
	cp := *m
	s.orgMembers[key] = &cp
	return nil
}

func (s *MemoryStore) RemoveOrgMember(_ context.Context, orgID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := orgMemberKey(orgID, userID)
	if _, ok := s.orgMembers[key]; !ok {
		return ErrNotFound
	}
	delete(s.orgMembers, key)
	return nil
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateTeam(_ context.Context, t *models.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *t
	s.teams[t.ID] = &cp
	return nil
}

func (s *MemoryStore) GetTeam(_ context.Context, id string) (*models.Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.teams[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *MemoryStore) ListTeams(_ context.Context, orgID string) ([]*models.Team, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.Team
	for _, t := range s.teams {
		if t.OrgID == orgID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateTeam(_ context.Context, t *models.Team) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[t.ID]; !ok {
		return ErrNotFound
	}
	cp := *t
	s.teams[t.ID] = &cp
	return nil
}

func (s *MemoryStore) DeleteTeam(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.teams[id]; !ok {
		return ErrNotFound
	}
	delete(s.teams, id)
	for k, m := range s.teamMembers {
		if m.TeamID == id {
			delete(s.teamMembers, k)
		}
	}
	return nil
}

// ─── Team members ─────────────────────────────────────────────────────────────

func (s *MemoryStore) AddTeamMember(_ context.Context, m *models.TeamMember) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *m
	s.teamMembers[teamMemberKey(m.TeamID, m.UserID)] = &cp
	return nil
}

func (s *MemoryStore) ListTeamMembers(_ context.Context, teamID string) ([]*models.TeamMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.TeamMember
	for _, m := range s.teamMembers {
		if m.TeamID == teamID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListUserTeams(_ context.Context, userID string) ([]*models.TeamMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*models.TeamMember
	for _, m := range s.teamMembers {
		if m.UserID == userID {
			cp := *m
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemoryStore) RemoveTeamMember(_ context.Context, teamID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := teamMemberKey(teamID, userID)
	if _, ok := s.teamMembers[key]; !ok {
		return ErrNotFound
	}
	delete(s.teamMembers, key)
	return nil
}

func (s *MemoryStore) SyncTeamMembers(_ context.Context, teamID string, userIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Remove existing members not in the new list
	want := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		want[id] = true
	}
	for k, m := range s.teamMembers {
		if m.TeamID == teamID && !want[m.UserID] {
			delete(s.teamMembers, k)
		}
	}
	// Add new members
	for _, uid := range userIDs {
		key := teamMemberKey(teamID, uid)
		if _, exists := s.teamMembers[key]; !exists {
			s.teamMembers[key] = &models.TeamMember{
				TeamID:   teamID,
				UserID:   uid,
				JoinedAt: time.Now(),
			}
		}
	}
	return nil
}

// ─── Guard events ─────────────────────────────────────────────────────────────

func (s *MemoryStore) AppendGuardEvents(_ context.Context, evs []models.GuardEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range evs {
		list := append(s.guardEvents[e.WorkspaceID], e)
		if over := len(list) - MaxGuardEventsPerWorkspace; over > 0 {
			list = list[over:]
		}
		s.guardEvents[e.WorkspaceID] = list
	}
	return nil
}

func (s *MemoryStore) ListGuardEvents(_ context.Context, workspaceID string, limit int) ([]models.GuardEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.guardEvents[workspaceID]
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	out := make([]models.GuardEvent, 0, limit)
	for i := len(list) - 1; i >= len(list)-limit; i-- { // newest first
		out = append(out, list[i])
	}
	return out, nil
}
