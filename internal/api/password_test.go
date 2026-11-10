package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/igloo-dev/igloo/internal/models"
)

type pwRig struct {
	h  *Handler
	rt http.Handler
}

func newPWRig(t *testing.T) *pwRig {
	h := newTestHandler()
	h.AllowSignup = true
	r := chi.NewRouter()
	r.Post("/api/auth/register", h.Register)
	r.Post("/api/auth/login", h.Login)
	r.Group(func(r chi.Router) {
		r.Use(h.RequireAuth)
		r.Get("/api/auth/me", h.Me)
		r.Post("/api/auth/password", h.ChangePassword)
		r.Group(func(r chi.Router) {
			r.Use(h.RequireAdmin)
			r.Put("/api/admin/users/{id}/password", h.AdminResetPassword)
		})
	})
	g := &pwRig{h: h, rt: r}
	if rec := g.do("POST", "/api/auth/register", "", map[string]string{"name": "Admin", "email": "admin@x.dev", "password": "admin-pass-1"}); rec.Code != 201 {
		t.Fatalf("register admin: %d %s", rec.Code, rec.Body)
	}
	if rec := g.do("POST", "/api/auth/register", "", map[string]string{"name": "Bob", "email": "bob@x.dev", "password": "bobs-pass-1"}); rec.Code != 201 {
		t.Fatalf("register bob: %d %s", rec.Code, rec.Body)
	}
	return g
}

func (g *pwRig) do(method, path, bearer string, body any) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, jsonBody(body))
	req.RemoteAddr = "192.0.2.7:1000"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	g.rt.ServeHTTP(rec, req)
	return rec
}

func (g *pwRig) login(t *testing.T, email, pw string) string {
	t.Helper()
	rec := g.do("POST", "/api/auth/login", "", map[string]string{"email": email, "password": pw})
	if rec.Code != 200 {
		t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	var out struct{ Token string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Token
}

func (g *pwRig) bobID() string {
	u, _ := g.h.Store.GetUserByEmail(context.Background(), "bob@x.dev")
	return u.ID
}

func TestChangingYourPasswordEndsEveryOlderSession(t *testing.T) {
	g := newPWRig(t)
	old := g.login(t, "bob@x.dev", "bobs-pass-1")
	if g.do("GET", "/api/auth/me", old, nil).Code != 200 {
		t.Fatal("a fresh session should work")
	}
	time.Sleep(1100 * time.Millisecond) // session tokens are stamped to the second

	rec := g.do("POST", "/api/auth/password", old, map[string]string{"current_password": "bobs-pass-1", "new_password": "brand-new-pass"})
	if rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	var out struct{ Token string }
	json.Unmarshal(rec.Body.Bytes(), &out)

	if got := g.do("GET", "/api/auth/me", old, nil).Code; got != 401 {
		t.Errorf("the session from before the change: %d, want 401", got)
	}
	if got := g.do("GET", "/api/auth/me", out.Token, nil).Code; got != 200 {
		t.Errorf("the token returned by the change: %d, want 200", got)
	}
	if got := g.do("POST", "/api/auth/login", "", map[string]string{"email": "bob@x.dev", "password": "bobs-pass-1"}).Code; got != 401 {
		t.Errorf("logging in with the old password: %d, want 401", got)
	}
	g.login(t, "bob@x.dev", "brand-new-pass")
}

func TestChangePasswordChecksTheCurrentPasswordAndTheNewOne(t *testing.T) {
	g := newPWRig(t)
	tok := g.login(t, "bob@x.dev", "bobs-pass-1")
	change := func(cur, next string) int {
		return g.do("POST", "/api/auth/password", tok, map[string]string{"current_password": cur, "new_password": next}).Code
	}
	for name, c := range map[string]struct {
		cur, next string
		want      int
	}{
		"wrong current":    {"nope", "another-pass-1", 403},
		"too short":        {"bobs-pass-1", "short", 400},
		"too long":         {"bobs-pass-1", strings.Repeat("a", 73), 400},
		"unchanged":        {"bobs-pass-1", "bobs-pass-1", 400},
		"empty current":    {"", "another-pass-1", 403},
		"empty new":        {"bobs-pass-1", "", 400},
	} {
		if got := change(c.cur, c.next); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	if got := g.do("POST", "/api/auth/password", "", map[string]string{"current_password": "x", "new_password": "y"}).Code; got != 401 {
		t.Errorf("unauthenticated change: %d, want 401", got)
	}
	if got := change("bobs-pass-1", strings.Repeat("a", 72)); got != 200 { // last: it ends tok
		t.Errorf("a password of the longest accepted length: %d, want 200", got)
	}
}

func TestGuessingTheCurrentPasswordIsThrottled(t *testing.T) {
	g := newPWRig(t)
	tok := g.login(t, "bob@x.dev", "bobs-pass-1")
	var last int
	for i := 0; i < 12; i++ {
		last = g.do("POST", "/api/auth/password", tok, map[string]string{"current_password": "guess" + string(rune('a'+i)), "new_password": "another-pass-1"}).Code
	}
	if last != 429 {
		t.Errorf("after many wrong guesses: %d, want 429", last)
	}
	if got := g.do("POST", "/api/auth/password", tok, map[string]string{"current_password": "bobs-pass-1", "new_password": "another-pass-1"}).Code; got != 429 {
		t.Errorf("even the right password is refused while throttled: %d, want 429", got)
	}
}

func TestOnlyAnAdminResetsAnotherUsersPassword(t *testing.T) {
	g := newPWRig(t)
	admin, bob := g.login(t, "admin@x.dev", "admin-pass-1"), g.login(t, "bob@x.dev", "bobs-pass-1")
	path := "/api/admin/users/" + g.bobID() + "/password"
	if got := g.do("PUT", path, bob, map[string]string{"new_password": "hijacked-pass"}).Code; got != 403 {
		t.Errorf("a normal user resetting a password: %d, want 403", got)
	}
	g.login(t, "bob@x.dev", "bobs-pass-1") // still the old one
	time.Sleep(1100 * time.Millisecond)
	if got := g.do("PUT", path, admin, map[string]string{"new_password": "short"}).Code; got != 400 {
		t.Errorf("a weak reset: %d, want 400", got)
	}
	if got := g.do("PUT", "/api/admin/users/nobody/password", admin, map[string]string{"new_password": "reset-pass-1"}).Code; got != 404 {
		t.Errorf("an unknown user: %d, want 404", got)
	}
	if got := g.do("PUT", path, admin, map[string]string{"new_password": "reset-pass-1"}).Code; got != 204 {
		t.Fatalf("admin reset: %d, want 204", got)
	}
	if got := g.do("GET", "/api/auth/me", bob, nil).Code; got != 401 {
		t.Errorf("the user's open session after a reset: %d, want 401", got)
	}
	if got := g.do("GET", "/api/auth/me", admin, nil).Code; got != 200 {
		t.Errorf("the admin's own session must survive resetting someone else: %d, want 200", got)
	}
	g.login(t, "bob@x.dev", "reset-pass-1")
}

func TestAccountsWithoutAPasswordCannotHaveOneSet(t *testing.T) {
	g := newPWRig(t)
	admin := g.login(t, "admin@x.dev", "admin-pass-1")
	ctx := context.Background()
	for _, lt := range []string{"oidc", "none"} {
		g.h.Store.CreateUser(ctx, &models.User{ID: "u-" + lt, Email: lt + "@x.dev", LoginType: lt, CreatedAt: time.Now()})
		if got := g.do("PUT", "/api/admin/users/u-"+lt+"/password", admin, map[string]string{"new_password": "reset-pass-1"}).Code; got != 409 {
			t.Errorf("reset on a %s account: %d, want 409", lt, got)
		}
	}
}

func TestAnAdminCanCreateAPersonWhoSignsInAndASignInlessServiceAccount(t *testing.T) {
	g := newPWRig(t)
	g.h.AllowSignup = false // sign-up closed: an admin is the only way in
	admin := g.login(t, "admin@x.dev", "admin-pass-1")
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(g.h.RequireAuth)
		r.Use(g.h.RequireAdmin)
		r.Post("/api/admin/users", g.h.AdminCreateUser)
	})
	post := func(bearer string, body map[string]string) int {
		req := httptest.NewRequest("POST", "/api/admin/users", jsonBody(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := post(admin, map[string]string{"name": "Carol", "email": "carol@x.dev", "password": "carols-first-pw"}); got != 201 {
		t.Fatalf("create a person: %d, want 201", got)
	}
	g.login(t, "carol@x.dev", "carols-first-pw") // she can sign in, and is not an admin
	carol, _ := g.h.Store.GetUserByEmail(context.Background(), "carol@x.dev")
	if carol.IsAdmin || carol.LoginType == "none" {
		t.Errorf("created user = %+v, want a non-admin who can sign in", carol)
	}
	if got := post(admin, map[string]string{"name": "Weak", "email": "weak@x.dev", "password": "short"}); got != 400 {
		t.Errorf("a weak initial password: %d, want 400", got)
	}
	if got := post(admin, map[string]string{"name": "Odd", "email": "odd@x.dev", "password": "long-enough-pw", "login_type": "none"}); got != 400 {
		t.Errorf("a password on a headless account: %d, want 400", got)
	}
	if got := post(admin, map[string]string{"name": "CI", "email": "ci@x.dev"}); got != 201 {
		t.Errorf("a service account: %d, want 201", got)
	}
	ci, _ := g.h.Store.GetUserByEmail(context.Background(), "ci@x.dev")
	if ci.LoginType != "none" || ci.PasswordHash != "" {
		t.Errorf("service account = %+v, want login_type none and no password", ci)
	}
	if got := post(g.login(t, "bob@x.dev", "bobs-pass-1"), map[string]string{"name": "X", "email": "x@x.dev", "password": "long-enough-pw"}); got != 403 {
		t.Errorf("a non-admin creating a user: %d, want 403", got)
	}
}
