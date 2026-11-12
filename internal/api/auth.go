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
		"signup_enabled":        h.signupOpen(r.Context()),
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

	email, name, groups, roles, err := h.OIDC.Exchange(r.Context(), code)
	if err != nil {
		http.Redirect(w, r, "/?oidc_error="+err.Error(), http.StatusFound)
		return
	}

	// Global allowed-groups gate — reject if user isn't in any permitted group.
	if len(h.AllowedGroups) > 0 && !hasGroupOverlap(h.AllowedGroups, groups) {
		http.Redirect(w, r, "/?oidc_error=not in an allowed group", http.StatusFound)
		return
	}

	// Find or create the user — link by email. Creation takes the registration lock:
	// "the first user becomes admin" must be decided once, even across replicas.
	user, err := h.Store.GetUserByEmail(r.Context(), email)
	if err != nil {
		h.withRegistrationLock(r.Context(), func() error {
			if u, err := h.Store.GetUserByEmail(r.Context(), email); err == nil {
				user = u // another login created it while we waited for the lock
				return nil
			}
			u := &models.User{
				ID:        uuid.NewString(),
				Email:     email,
				Name:      name,
				CreatedAt: time.Now(),
			}
			existing, _ := h.Store.ListUsers(r.Context())
			u.IsAdmin = len(existing) == 0
			if err := h.Store.CreateUser(r.Context(), u); err != nil {
				return err
			}
			user = u
			return nil
		})
		if user == nil {
			http.Redirect(w, r, "/?oidc_error=failed to create user", http.StatusFound)
			return
		}
	}

	// Full Coder-style group + role sync on every login.
	h.syncOIDCGroups(r.Context(), user, groups, roles)

	tok, err := h.Auth.SignUser(user.ID, user.Email)
	if err != nil {
		http.Redirect(w, r, "/?oidc_error=internal error", http.StatusFound)
		return
	}

	// Redirect SPA — it reads ?token= on load and stores it in localStorage.
	http.Redirect(w, r, "/?token="+tok, http.StatusFound)
}

// signupOpen reports whether the sign-up form should be offered: always for the
// first account, otherwise only when IGLOO_ALLOW_SIGNUP is set.
func (h *Handler) signupOpen(ctx context.Context) bool {
	if h.DisablePasswordAuth {
		return false
	}
	if h.AllowSignup {
		return true
	}
	users, _ := h.Store.ListUsers(ctx)
	return len(users) == 0
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
	if err := validNewPassword(body.Password); err != nil {
		h.err(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := h.Auth.HashPassword(body.Password)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "internal error")
		return
	}

	// First user to register becomes admin. After that, signup stays closed unless
	// IGLOO_ALLOW_SIGNUP=true; otherwise an admin creates accounts.
	h.withRegistrationLock(r.Context(), func() error {
		existing, _ := h.Store.ListUsers(r.Context())
		isFirst := len(existing) == 0
		if !isFirst && !h.AllowSignup {
			h.err(w, http.StatusForbidden, "sign-up is closed — ask an admin to create your account")
			return nil
		}

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
				return nil
			}
			h.err(w, http.StatusInternalServerError, "internal error")
			return nil
		}

		token, err := h.Auth.SignUser(user.ID, user.Email)
		if err != nil {
			h.err(w, http.StatusInternalServerError, "internal error")
			return nil
		}

		h.json(w, http.StatusCreated, map[string]any{
			"token": token,
			"user":  user,
		})
		return nil
	})
}

// withRegistrationLock serialises "is this the first user, who becomes admin": within
// this process always, and across replicas when the store can hold a lock.
func (h *Handler) withRegistrationLock(ctx context.Context, fn func() error) error {
	h.regMu.Lock()
	defer h.regMu.Unlock()
	if rl, ok := h.Store.(store.RegistrationLocker); ok {
		return rl.WithRegistrationLock(ctx, fn)
	}
	return fn()
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

	email := strings.ToLower(strings.TrimSpace(body.Email))
	limitKey := email + "|" + clientIP(r)
	if h.loginRL.blocked(limitKey) {
		w.Header().Set("Retry-After", "600")
		h.err(w, http.StatusTooManyRequests, "too many failed attempts — try again in a few minutes")
		return
	}

	user, err := h.Store.GetUserByEmail(r.Context(), email)
	if err != nil {
		h.loginRL.fail(limitKey)
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if user.LoginType == "none" {
		h.err(w, http.StatusForbidden, "this account cannot log in interactively — use an API token")
		return
	}

	if !h.Auth.CheckPassword(user.PasswordHash, body.Password) {
		h.loginRL.fail(limitKey)
		h.err(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	h.loginRL.reset(limitKey)

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

// POST /api/auth/device/init — CLI registers a one-time code (public, no auth)
func (h *Handler) DeviceInit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := h.decode(r, &body); err != nil || body.Code == "" {
		h.err(w, http.StatusBadRequest, "missing code")
		return
	}
	if len(body.Code) > 32 {
		h.err(w, http.StatusBadRequest, "invalid code")
		return
	}

	// Codes live in the store when it can hold them, so the login works when the CLI
	// registers its code on one replica and polls another.
	if dcs, ok := h.Store.(store.DeviceCodeStore); ok {
		n, err := dcs.CountDeviceCodes(r.Context())
		if err != nil {
			h.err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if n >= 1000 {
			h.err(w, http.StatusTooManyRequests, "too many pending logins — try again shortly")
			return
		}
		switch err := dcs.CreateDeviceCode(r.Context(), body.Code, time.Now().Add(5*time.Minute)); err {
		case nil:
			w.WriteHeader(http.StatusOK)
		case store.ErrConflict:
			h.err(w, http.StatusConflict, "code already in use — generate a new one")
		default:
			h.err(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	h.initDeviceCodes()
	h.gcDeviceCodes()
	h.deviceMu.Lock()
	if len(h.deviceCodes) >= 1000 {
		h.deviceMu.Unlock()
		h.err(w, http.StatusTooManyRequests, "too many pending logins — try again shortly")
		return
	}
	h.deviceCodes[body.Code] = &deviceEntry{expiresAt: time.Now().Add(5 * time.Minute)}
	h.deviceMu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// POST /api/auth/device/approve — browser approves a code (requires auth)
func (h *Handler) DeviceApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := h.decode(r, &body); err != nil || body.Code == "" {
		h.err(w, http.StatusBadRequest, "missing code")
		return
	}
	user, err := h.Store.GetUser(r.Context(), ctxUserID(r))
	if err != nil {
		h.err(w, http.StatusNotFound, "user not found")
		return
	}
	token, err := h.Auth.SignCLI(user.ID, user.Email)
	if err != nil {
		h.err(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if dcs, ok := h.Store.(store.DeviceCodeStore); ok {
		if err := dcs.ApproveDeviceCode(r.Context(), body.Code, token); err != nil {
			h.err(w, http.StatusGone, "code expired or invalid")
			return
		}
		h.json(w, http.StatusOK, map[string]string{"email": user.Email})
		return
	}

	h.deviceMu.Lock()
	entry, ok := h.deviceCodes[body.Code]
	if !ok || time.Now().After(entry.expiresAt) {
		h.deviceMu.Unlock()
		h.err(w, http.StatusGone, "code expired or invalid")
		return
	}
	entry.token = token
	h.deviceMu.Unlock()

	h.json(w, http.StatusOK, map[string]string{"email": user.Email})
}

// GET /api/auth/device/poll?code=XXXX — CLI polls until approved (public, no auth)
func (h *Handler) DevicePoll(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		h.err(w, http.StatusBadRequest, "missing code")
		return
	}
	if dcs, ok := h.Store.(store.DeviceCodeStore); ok {
		token, err := dcs.TakeDeviceCode(r.Context(), code)
		switch {
		case err == store.ErrNotFound:
			h.err(w, http.StatusGone, "code expired or invalid")
		case err != nil:
			h.err(w, http.StatusInternalServerError, "internal error")
		case token == "":
			w.WriteHeader(http.StatusAccepted) // 202 = still pending
		default:
			h.json(w, http.StatusOK, map[string]string{"token": token})
		}
		return
	}

	h.deviceMu.Lock()
	entry, ok := h.deviceCodes[code]
	if !ok || time.Now().After(entry.expiresAt) {
		h.deviceMu.Unlock()
		h.err(w, http.StatusGone, "code expired or invalid")
		return
	}
	token := entry.token
	if token != "" {
		delete(h.deviceCodes, code) // one-time use
	}
	h.deviceMu.Unlock()

	if token == "" {
		w.WriteHeader(http.StatusAccepted) // 202 = still pending
		return
	}
	h.json(w, http.StatusOK, map[string]string{"token": token})
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

// syncOIDCGroups is the full Coder-style group + role sync run on every OIDC login:
//
//  1. Always add the user to any org marked IsDefault.
//  2. Find all orgs whose GroupClaims intersect the user's IdP groups.
//  3. Add the user to qualifying orgs with the correct role (via RoleMapping); remove from non-qualifying.
//  4. Within each qualifying org, resolve which team each IdP group maps to and sync memberships.
func (h *Handler) syncOIDCGroups(ctx context.Context, user *models.User, groups, roles []string) {
	groupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		groupSet[strings.ToLower(g)] = struct{}{}
	}

	allOrgs, err := h.Store.ListOrgs(ctx)
	if err != nil {
		return
	}

	// Current org memberships so we can detect removals.
	currentMemberships, _ := h.Store.ListUserOrgs(ctx, user.ID)
	currentOrgIDs := make(map[string]struct{}, len(currentMemberships))
	for _, m := range currentMemberships {
		currentOrgIDs[m.OrgID] = struct{}{}
	}

	for _, org := range allOrgs {
		// Default org — add every OIDC user automatically, never remove.
		if org.IsDefault {
			if _, already := currentOrgIDs[org.ID]; !already {
				_ = h.Store.AddOrgMember(ctx, &models.OrgMember{
					OrgID:     org.ID,
					UserID:    user.ID,
					UserEmail: user.Email,
					UserName:  user.Name,
					Role:      models.OrgRoleMember,
					JoinedAt:  time.Now(),
				})
			}
			continue
		}

		if len(org.GroupClaims) == 0 {
			// No group sync configured — skip.
			continue
		}

		qualifies := hasGroupOverlap(org.GroupClaims, groups)
		if qualifies {
			role := resolveOrgRole(org.RoleMapping, roles)
			if _, already := currentOrgIDs[org.ID]; !already {
				_ = h.Store.AddOrgMember(ctx, &models.OrgMember{
					OrgID:     org.ID,
					UserID:    user.ID,
					UserEmail: user.Email,
					UserName:  user.Name,
					Role:      role,
					JoinedAt:  time.Now(),
				})
			} else {
				// Already a member — update role in case it changed in the IdP.
				_ = h.Store.UpdateOrgMember(ctx, &models.OrgMember{
					OrgID:     org.ID,
					UserID:    user.ID,
					UserEmail: user.Email,
					UserName:  user.Name,
					Role:      role,
					JoinedAt:  time.Now(),
				})
			}
			h.syncOrgTeams(ctx, org, user, groupSet)
		} else {
			// User no longer qualifies — remove.
			if _, wasMember := currentOrgIDs[org.ID]; wasMember {
				_ = h.Store.RemoveOrgMember(ctx, org.ID, user.ID)
			}
		}
	}
}

// resolveOrgRole picks the highest role the user qualifies for based on their
// IdP roles and the org's RoleMapping. Falls back to OrgRoleMember.
func resolveOrgRole(mapping map[string]models.OrgRole, idpRoles []string) models.OrgRole {
	role := models.OrgRoleMember
	for _, idpRole := range idpRoles {
		if mapped, ok := mapping[strings.ToLower(idpRole)]; ok {
			// Escalate: admin > member > viewer
			if mapped == models.OrgRoleAdmin {
				return models.OrgRoleAdmin
			}
			if mapped == models.OrgRoleMember && role == models.OrgRoleViewer {
				role = models.OrgRoleMember
			}
		}
	}
	return role
}

// syncOrgTeams syncs team membership for one org based on the user's group set.
func (h *Handler) syncOrgTeams(ctx context.Context, org *models.Organization, user *models.User, groupSet map[string]struct{}) {
	existingTeams, _ := h.Store.ListTeams(ctx, org.ID)
	teamsByName := make(map[string]*models.Team, len(existingTeams))
	for _, t := range existingTeams {
		teamsByName[strings.ToLower(t.Name)] = t
	}

	// Determine which teams the user should be in after this sync.
	targetTeamIDs := make(map[string]struct{})

	for idpGroup := range groupSet {
		// Apply name mapping if configured, otherwise use the group name as-is.
		teamName := idpGroup
		if org.GroupMapping != nil {
			if mapped, ok := org.GroupMapping[idpGroup]; ok {
				teamName = strings.ToLower(mapped)
			}
		}

		team, exists := teamsByName[teamName]
		if !exists {
			if !org.GroupAutoCreate {
				continue
			}
			// Auto-create the team.
			team = &models.Team{
				ID:        uuid.NewString(),
				OrgID:     org.ID,
				Name:      teamName,
				CreatedBy: "__oidc__",
				CreatedAt: time.Now(),
			}
			if err := h.Store.CreateTeam(ctx, team); err != nil {
				continue
			}
			teamsByName[teamName] = team
		}
		targetTeamIDs[team.ID] = struct{}{}
	}

	// Add to target teams.
	for teamID := range targetTeamIDs {
		_ = h.Store.AddTeamMember(ctx, &models.TeamMember{
			TeamID:    teamID,
			UserID:    user.ID,
			UserEmail: user.Email,
			UserName:  user.Name,
			JoinedAt:  time.Now(),
		})
	}

	// Remove from teams the user is in but no longer qualifies for.
	currentTeams, _ := h.Store.ListUserTeams(ctx, user.ID)
	for _, tm := range currentTeams {
		// Only remove from teams belonging to this org.
		isOrgTeam := false
		for _, t := range existingTeams {
			if t.ID == tm.TeamID {
				isOrgTeam = true
				break
			}
		}
		if isOrgTeam {
			if _, keep := targetTeamIDs[tm.TeamID]; !keep {
				_ = h.Store.RemoveTeamMember(ctx, tm.TeamID, user.ID)
			}
		}
	}
}

// hasGroupOverlap returns true if any element in allowed appears (case-insensitively) in userGroups.
func hasGroupOverlap(allowed, userGroups []string) bool {
	for _, a := range allowed {
		al := strings.ToLower(a)
		for _, g := range userGroups {
			if strings.ToLower(g) == al {
				return true
			}
		}
	}
	return false
}

// RequireAuth validates either a JWT session token or a DB-backed API token.
// JWTs have three dot-separated segments; opaque API tokens do not.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var userID string
		if strings.Count(token, ".") == 2 {
			// JWT path
			claims, err := h.Auth.ParseUser(token)
			if err != nil || h.sessionRevoked(r.Context(), claims) {
				h.err(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			userID = claims.UserID
		} else {
			// DB API token path
			hash := hashToken(token)
			t, err := h.Store.GetAPITokenByHash(r.Context(), hash)
			if err != nil || time.Now().After(t.ExpiresAt) {
				h.err(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			go h.Store.TouchAPIToken(context.Background(), t.ID)
			userID = t.UserID
		}
		if userID == "" { // never let an anonymous identity reach handlers that treat "" as "everyone"
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
