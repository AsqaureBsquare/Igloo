package api

import (
	"context"
	"net/http"

	"github.com/igloo-dev/igloo/internal/models"
)

type orgCtxKey struct{}

type orgCtx struct {
	OrgID string
	Role  models.OrgRole
}

// ctxOrg returns the org context injected by RequireOrgMember, or nil.
func ctxOrg(r *http.Request) *orgCtx {
	v, _ := r.Context().Value(orgCtxKey{}).(*orgCtx)
	return v
}

func ctxOrgID(r *http.Request) string {
	if o := ctxOrg(r); o != nil {
		return o.OrgID
	}
	return ""
}

// RequireOrgMember reads X-Org-ID, validates the caller is a member, and
// injects the org context. Site admins bypass the membership check.
func (h *Handler) RequireOrgMember(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orgID := r.Header.Get("X-Org-ID")
		if orgID == "" {
			next.ServeHTTP(w, r)
			return
		}

		userID := ctxUserID(r)
		user, err := h.Store.GetUser(r.Context(), userID)
		if err != nil {
			h.err(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		// Site admins have org-admin rights everywhere.
		if user.IsAdmin {
			ctx := context.WithValue(r.Context(), orgCtxKey{}, &orgCtx{
				OrgID: orgID,
				Role:  models.OrgRoleAdmin,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		member, err := h.Store.GetOrgMember(r.Context(), orgID, userID)
		if err != nil {
			h.err(w, http.StatusForbidden, "not a member of this organization")
			return
		}

		ctx := context.WithValue(r.Context(), orgCtxKey{}, &orgCtx{
			OrgID: orgID,
			Role:  member.Role,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireOrgAdmin returns 403 if the caller is not an org admin.
func requireOrgAdmin(h *Handler, w http.ResponseWriter, r *http.Request) bool {
	o := ctxOrg(r)
	if o == nil || o.Role != models.OrgRoleAdmin {
		h.err(w, http.StatusForbidden, "org admin role required")
		return false
	}
	return true
}
