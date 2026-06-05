package api

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

var slugRe = regexp.MustCompile(`[^a-z0-9-]`)

func toSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// ─── Organizations ────────────────────────────────────────────────────────────

// GET /api/orgs — list orgs the caller belongs to (admins see all)
func (h *Handler) ListOrgs(w http.ResponseWriter, r *http.Request) {
	userID := ctxUserID(r)
	user, _ := h.Store.GetUser(r.Context(), userID)

	if user != nil && user.IsAdmin {
		orgs, err := h.Store.ListOrgs(r.Context())
		if err != nil {
			h.err(w, http.StatusInternalServerError, "internal error")
			return
		}
		h.json(w, http.StatusOK, orgs)
		return
	}

	memberships, err := h.Store.ListUserOrgs(r.Context(), userID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	orgs := make([]*models.Organization, 0, len(memberships))
	for _, m := range memberships {
		if o, err := h.Store.GetOrg(r.Context(), m.OrgID); err == nil {
			orgs = append(orgs, o)
		}
	}
	h.json(w, http.StatusOK, orgs)
}

// POST /api/orgs
func (h *Handler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string            `json:"name"`
		Description     string            `json:"description"`
		GroupClaims     []string          `json:"group_claims"`
		GroupMapping    map[string]string `json:"group_mapping"`
		GroupAutoCreate bool              `json:"group_auto_create"`
	}
	if err := h.decode(r, &body); err != nil || body.Name == "" {
		h.err(w, http.StatusBadRequest, "name is required")
		return
	}
	userID := ctxUserID(r)
	org := &models.Organization{
		ID:              uuid.NewString(),
		Name:            strings.TrimSpace(body.Name),
		Slug:            toSlug(body.Name),
		Description:     body.Description,
		GroupClaims:     body.GroupClaims,
		GroupMapping:    body.GroupMapping,
		GroupAutoCreate: body.GroupAutoCreate,
		CreatedBy:       userID,
		CreatedAt:       time.Now(),
	}
	if err := h.Store.CreateOrg(r.Context(), org); err != nil {
		if err == store.ErrConflict {
			h.err(w, http.StatusConflict, "an organization with that name already exists")
			return
		}
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Creator becomes org admin automatically
	_ = h.Store.AddOrgMember(r.Context(), &models.OrgMember{
		OrgID:    org.ID,
		UserID:   userID,
		Role:     models.OrgRoleAdmin,
		JoinedAt: time.Now(),
	})
	h.json(w, http.StatusCreated, org)
}

// GET /api/orgs/{id}
func (h *Handler) GetOrg(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrgAccess(w, r)
	if !ok {
		return
	}
	h.json(w, http.StatusOK, org)
}

// PUT /api/orgs/{id}
func (h *Handler) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	org, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Name            string            `json:"name"`
		Description     string            `json:"description"`
		GroupClaims     []string          `json:"group_claims"`
		GroupMapping    map[string]string `json:"group_mapping"`
		GroupAutoCreate *bool             `json:"group_auto_create"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != "" {
		org.Name = strings.TrimSpace(body.Name)
		org.Slug = toSlug(body.Name)
	}
	org.Description = body.Description
	if body.GroupClaims != nil {
		org.GroupClaims = body.GroupClaims
	}
	if body.GroupMapping != nil {
		org.GroupMapping = body.GroupMapping
	}
	if body.GroupAutoCreate != nil {
		org.GroupAutoCreate = *body.GroupAutoCreate
	}
	if err := h.Store.UpdateOrg(r.Context(), org); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, org)
}

// DELETE /api/orgs/{id}
func (h *Handler) DeleteOrg(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteOrg(r.Context(), id); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Org members ──────────────────────────────────────────────────────────────

// GET /api/orgs/{id}/members
func (h *Handler) ListOrgMembers(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAccess(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	members, err := h.Store.ListOrgMembers(r.Context(), orgID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Enrich with user info
	for _, m := range members {
		if u, err := h.Store.GetUser(r.Context(), m.UserID); err == nil {
			m.UserEmail = u.Email
			m.UserName = u.Name
		}
	}
	h.json(w, http.StatusOK, members)
}

// POST /api/orgs/{id}/members
func (h *Handler) AddOrgMember(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	var body struct {
		Email string          `json:"email"`
		Role  models.OrgRole  `json:"role"`
	}
	if err := h.decode(r, &body); err != nil || body.Email == "" {
		h.err(w, http.StatusBadRequest, "email is required")
		return
	}
	if body.Role == "" {
		body.Role = models.OrgRoleMember
	}
	user, err := h.Store.GetUserByEmail(r.Context(), strings.ToLower(body.Email))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found — they must log in to Igloo first")
		return
	}
	m := &models.OrgMember{
		OrgID:    orgID,
		UserID:   user.ID,
		Role:     body.Role,
		JoinedAt: time.Now(),
	}
	if err := h.Store.AddOrgMember(r.Context(), m); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.UserEmail = user.Email
	m.UserName = user.Name
	h.json(w, http.StatusCreated, m)
}

// PUT /api/orgs/{id}/members/{userID}
func (h *Handler) UpdateOrgMember(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userID")
	var body struct {
		Role models.OrgRole `json:"role"`
	}
	if err := h.decode(r, &body); err != nil || body.Role == "" {
		h.err(w, http.StatusBadRequest, "role is required")
		return
	}
	m, err := h.Store.GetOrgMember(r.Context(), orgID, userID)
	if err != nil {
		h.err(w, http.StatusNotFound, "member not found")
		return
	}
	m.Role = body.Role
	if err := h.Store.UpdateOrgMember(r.Context(), m); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, m)
}

// DELETE /api/orgs/{id}/members/{userID}
func (h *Handler) RemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userID")
	if err := h.Store.RemoveOrgMember(r.Context(), orgID, userID); err != nil {
		h.err(w, http.StatusNotFound, "member not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Teams ────────────────────────────────────────────────────────────────────

// GET /api/orgs/{id}/teams
func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAccess(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	teams, err := h.Store.ListTeams(r.Context(), orgID)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, teams)
}

// POST /api/orgs/{id}/teams
func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	orgID := chi.URLParam(r, "id")
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := h.decode(r, &body); err != nil || body.Name == "" {
		h.err(w, http.StatusBadRequest, "name is required")
		return
	}
	team := &models.Team{
		ID:          uuid.NewString(),
		OrgID:       orgID,
		Name:        strings.TrimSpace(body.Name),
		Description: body.Description,
		CreatedBy:   ctxUserID(r),
		CreatedAt:   time.Now(),
	}
	if err := h.Store.CreateTeam(r.Context(), team); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusCreated, team)
}

// GET /api/orgs/{id}/teams/{teamID}
func (h *Handler) GetTeam(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAccess(w, r)
	if !ok {
		return
	}
	team, err := h.Store.GetTeam(r.Context(), chi.URLParam(r, "teamID"))
	if err != nil {
		h.err(w, http.StatusNotFound, "team not found")
		return
	}
	h.json(w, http.StatusOK, team)
}

// DELETE /api/orgs/{id}/teams/{teamID}
func (h *Handler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteTeam(r.Context(), chi.URLParam(r, "teamID")); err != nil {
		h.err(w, http.StatusNotFound, "team not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Team members ─────────────────────────────────────────────────────────────

// GET /api/orgs/{id}/teams/{teamID}/members
func (h *Handler) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAccess(w, r)
	if !ok {
		return
	}
	members, err := h.Store.ListTeamMembers(r.Context(), chi.URLParam(r, "teamID"))
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, m := range members {
		if u, err := h.Store.GetUser(r.Context(), m.UserID); err == nil {
			m.UserEmail = u.Email
			m.UserName = u.Name
		}
	}
	h.json(w, http.StatusOK, members)
}

// POST /api/orgs/{id}/teams/{teamID}/members
func (h *Handler) AddTeamMember(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	teamID := chi.URLParam(r, "teamID")
	var body struct {
		Email string `json:"email"`
	}
	if err := h.decode(r, &body); err != nil || body.Email == "" {
		h.err(w, http.StatusBadRequest, "email is required")
		return
	}
	user, err := h.Store.GetUserByEmail(r.Context(), strings.ToLower(body.Email))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	m := &models.TeamMember{
		TeamID:   teamID,
		UserID:   user.ID,
		JoinedAt: time.Now(),
	}
	if err := h.Store.AddTeamMember(r.Context(), m); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	m.UserEmail = user.Email
	m.UserName = user.Name
	h.json(w, http.StatusCreated, m)
}

// DELETE /api/orgs/{id}/teams/{teamID}/members/{userID}
func (h *Handler) RemoveTeamMember(w http.ResponseWriter, r *http.Request) {
	_, ok := h.requireOrgAdmin(w, r)
	if !ok {
		return
	}
	if err := h.Store.RemoveTeamMember(r.Context(), chi.URLParam(r, "teamID"), chi.URLParam(r, "userID")); err != nil {
		h.err(w, http.StatusNotFound, "member not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Access helpers ───────────────────────────────────────────────────────────

func (h *Handler) requireOrgAccess(w http.ResponseWriter, r *http.Request) (*models.Organization, bool) {
	orgID := chi.URLParam(r, "id")
	org, err := h.Store.GetOrg(r.Context(), orgID)
	if err != nil {
		h.err(w, http.StatusNotFound, "organization not found")
		return nil, false
	}
	userID := ctxUserID(r)
	user, _ := h.Store.GetUser(r.Context(), userID)
	if user != nil && user.IsAdmin {
		return org, true
	}
	if _, err := h.Store.GetOrgMember(r.Context(), orgID, userID); err != nil {
		h.err(w, http.StatusForbidden, "not a member of this organization")
		return nil, false
	}
	return org, true
}

func (h *Handler) requireOrgAdmin(w http.ResponseWriter, r *http.Request) (*models.Organization, bool) {
	orgID := chi.URLParam(r, "id")
	org, err := h.Store.GetOrg(r.Context(), orgID)
	if err != nil {
		h.err(w, http.StatusNotFound, "organization not found")
		return nil, false
	}
	userID := ctxUserID(r)
	user, _ := h.Store.GetUser(r.Context(), userID)
	if user != nil && user.IsAdmin {
		return org, true
	}
	m, err := h.Store.GetOrgMember(r.Context(), orgID, userID)
	if err != nil || m.Role != models.OrgRoleAdmin {
		h.err(w, http.StatusForbidden, "org admin role required")
		return nil, false
	}
	return org, true
}
