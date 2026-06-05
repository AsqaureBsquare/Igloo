package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

// GET /api/auth/config — public endpoint so the SPA knows whether to show the OIDC button
func (h *Handler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]any{
		"password_auth_enabled": !h.DisablePasswordAuth,
		"oidc_enabled":          h.OIDC != nil,
	}
	if h.OIDC != nil {
		cfg["oidc_button_text"] = h.OIDC.ButtonText()
		cfg["oidc_icon_url"] = h.OIDC.IconURL()
	}
	h.json(w, http.StatusOK, cfg)
}

// GET /api/auth/oidc/login — redirect user to the IdP
func (h *Handler) OIDCLogin(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		h.err(w, http.StatusNotFound, "OIDC not configured")
		return
	}

	// Generate a random nonce, sign it as a short-lived JWT for CSRF protection.
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	nonce := hex.EncodeToString(raw[:])

	stateTok, err := h.Auth.SignOIDCState(nonce)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "igloo_oidc_state",
		Value:    stateTok,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, h.OIDC.AuthCodeURL(stateTok), http.StatusFound)
}

// GET /api/auth/oidc/callback — IdP redirects here after login
func (h *Handler) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if h.OIDC == nil {
		h.err(w, http.StatusNotFound, "OIDC not configured")
		return
	}

	// Verify CSRF: state param must match the signed cookie.
	stateParam := r.URL.Query().Get("state")
	cookie, err := r.Cookie("igloo_oidc_state")
	if err != nil || cookie.Value != stateParam {
		h.err(w, http.StatusBadRequest, "invalid state")
		return
	}
	if _, err := h.Auth.ParseOIDCState(stateParam); err != nil {
		h.err(w, http.StatusBadRequest, "state token expired or invalid")
		return
	}

	// Clear the state cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name:     "igloo_oidc_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		http.Redirect(w, r, "/?oidc_error="+errParam+": "+desc, http.StatusFound)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		h.err(w, http.StatusBadRequest, "missing code")
		return
	}

	email, name, groups, err := h.OIDC.Exchange(r.Context(), code)
	if err != nil {
		http.Redirect(w, r, "/?oidc_error="+err.Error(), http.StatusFound)
		return
	}

	// Find or create the user — link by email.
	user, err := h.Store.GetUserByEmail(r.Context(), email)
	if err != nil {
		existing, _ := h.Store.ListUsers(r.Context())
		user = &models.User{
			ID:        uuid.NewString(),
			Email:     email,
			Name:      name,
			IsAdmin:   len(existing) == 0,
			CreatedAt: time.Now(),
		}
		if err := h.Store.CreateUser(r.Context(), user); err != nil {
			http.Redirect(w, r, "/?oidc_error=failed to create user", http.StatusFound)
			return
		}
	}

	// Sync OIDC groups → Igloo teams.
	// For each group name in the id_token, find or create a team in every org
	// the user belongs to, then add the user to matching teams.
	if len(groups) > 0 {
		h.syncOIDCGroups(r.Context(), user.ID, groups)
	}

	tok, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		http.Redirect(w, r, "/?oidc_error=internal error", http.StatusFound)
		return
	}

	// Redirect SPA — it reads ?token= on load and stores it in localStorage.
	http.Redirect(w, r, "/?token="+tok, http.StatusFound)
}

// POST /api/auth/register
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if h.DisablePasswordAuth {
		h.err(w, http.StatusForbidden, "password auth disabled — use SSO")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Email == "" || body.Password == "" || body.Name == "" {
		h.err(w, http.StatusBadRequest, "name, email, and password are required")
		return
	}
	if len(body.Password) < 8 {
		h.err(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := h.Auth.HashPassword(body.Password)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	// First user to register becomes admin.
	existing, _ := h.Store.ListUsers(r.Context())
	isFirst := len(existing) == 0

	user := &models.User{
		ID:           uuid.NewString(),
		Email:        strings.ToLower(strings.TrimSpace(body.Email)),
		Name:         strings.TrimSpace(body.Name),
		PasswordHash: hash,
		IsAdmin:      isFirst,
		CreatedAt:    time.Now(),
	}

	if err := h.Store.CreateUser(r.Context(), user); err != nil {
		if err == store.ErrConflict {
			h.err(w, http.StatusConflict, "email already registered")
			return
		}
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	token, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusCreated, map[string]any{
		"token": token,
		"user":  user,
	})
}

// POST /api/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if h.DisablePasswordAuth {
		h.err(w, http.StatusForbidden, "password auth disabled — use SSO")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid request")
		return
	}

	user, err := h.Store.GetUserByEmail(r.Context(), strings.ToLower(strings.TrimSpace(body.Email)))
	if err != nil {
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if !h.Auth.CheckPassword(user.PasswordHash, body.Password) {
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.json(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  user,
	})
}

// GET /api/auth/me
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	h.json(w, http.StatusOK, user)
}

// PUT /api/auth/me/ssh-key
func (h *Handler) UpdateSSHKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SSHPublicKey string `json:"ssh_public_key"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	user.SSHPublicKey = strings.TrimSpace(body.SSHPublicKey)
	if err := h.Store.UpdateUser(r.Context(), user); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, user)
}

// PUT /api/auth/me/tailscale-key
func (h *Handler) UpdateTailscaleKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TailscaleAuthKey string `json:"tailscale_auth_key"`
	}
	if err := h.decode(r, &body); err != nil {
		h.err(w, http.StatusBadRequest, "invalid body")
		return
	}
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	user.TailscaleAuthKey = strings.TrimSpace(body.TailscaleAuthKey)
	if err := h.Store.UpdateUser(r.Context(), user); err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.json(w, http.StatusOK, user)
}

// syncOIDCGroups maps the user into existing teams whose names match their OIDC
// group claims. Teams and orgs are never created automatically — the admin owns
// that structure. This only assigns membership to teams that already exist.
func (h *Handler) syncOIDCGroups(ctx context.Context, userID string, groups []string) {
	memberships, err := h.Store.ListUserOrgs(ctx, userID)
	if err != nil || len(memberships) == 0 {
		return
	}
	groupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		groupSet[strings.ToLower(g)] = struct{}{}
	}
	for _, membership := range memberships {
		existingTeams, _ := h.Store.ListTeams(ctx, membership.OrgID)
		for _, team := range existingTeams {
			if _, match := groupSet[strings.ToLower(team.Name)]; match {
				_ = h.Store.AddTeamMember(ctx, &models.TeamMember{
					TeamID:   team.ID,
					UserID:   userID,
					JoinedAt: time.Now(),
				})
			}
		}
	}
}

// Middleware: require valid user JWT, inject user_id into context.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		claims, err := h.Auth.ParseUser(token)
		if err != nil {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
