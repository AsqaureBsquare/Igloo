package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

func newTestHandler() *Handler {
	return &Handler{Store: store.NewMemory(), Auth: auth.New("test-secret")}
}

func call(h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func register(h *Handler, email string) *httptest.ResponseRecorder {
	return call(h.Register, map[string]string{"name": "Test", "email": email, "password": "password123"})
}

func TestFirstUserBecomesAdminAndSignupThenCloses(t *testing.T) {
	h := newTestHandler()

	rec := register(h, "first@example.com")
	if rec.Code != http.StatusCreated {
		t.Fatalf("first signup: %d %s", rec.Code, rec.Body)
	}
	var out struct{ User models.User }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.User.IsAdmin {
		t.Error("first user should be admin")
	}

	if rec := register(h, "second@example.com"); rec.Code != http.StatusForbidden {
		t.Errorf("second signup with signup closed: got %d, want 403", rec.Code)
	}

	h.AllowSignup = true
	rec = register(h, "second@example.com")
	if rec.Code != http.StatusCreated {
		t.Fatalf("second signup with signup open: %d %s", rec.Code, rec.Body)
	}
	var second struct{ User models.User } // fresh value: is_admin is omitted when false
	json.Unmarshal(rec.Body.Bytes(), &second)
	if second.User.IsAdmin {
		t.Error("later users must not be admin")
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newTestHandler()
	for name, body := range map[string]map[string]string{
		"short password": {"name": "A", "email": "a@example.com", "password": "short"},
		"missing email":  {"name": "A", "password": "password123"},
		"missing name":   {"email": "a@example.com", "password": "password123"},
	} {
		if rec := call(h.Register, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	h := newTestHandler()
	h.AllowSignup = true
	register(h, "dup@example.com")
	if rec := register(h, "DUP@example.com"); rec.Code != http.StatusConflict {
		t.Errorf("duplicate email (different case): got %d, want 409", rec.Code)
	}
}

func TestLoginThrottlesRepeatedFailures(t *testing.T) {
	h := newTestHandler()
	register(h, "user@example.com")
	bad := map[string]string{"email": "user@example.com", "password": "nope"}
	good := map[string]string{"email": "user@example.com", "password": "password123"}

	for i := 0; i < loginMaxFails; i++ {
		if rec := call(h.Login, bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := call(h.Login, good); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after the limit, even the right password: got %d, want 429", rec.Code)
	}
}

func TestSuccessfulLoginResetsTheCounter(t *testing.T) {
	h := newTestHandler()
	register(h, "user@example.com")
	bad := map[string]string{"email": "user@example.com", "password": "nope"}
	good := map[string]string{"email": "user@example.com", "password": "password123"}

	for i := 0; i < loginMaxFails-1; i++ {
		call(h.Login, bad)
	}
	if rec := call(h.Login, good); rec.Code != http.StatusOK {
		t.Fatalf("good login: %d", rec.Code)
	}
	for i := 0; i < loginMaxFails-1; i++ {
		if rec := call(h.Login, bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("counter was not reset (attempt %d got %d)", i+1, rec.Code)
		}
	}
}

func TestLoginDoesNotRevealWhetherTheEmailExists(t *testing.T) {
	h := newTestHandler()
	register(h, "user@example.com")
	a := call(h.Login, map[string]string{"email": "user@example.com", "password": "wrong"})
	b := call(h.Login, map[string]string{"email": "nobody@example.com", "password": "wrong"})
	if a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("responses differ: %d %q vs %d %q", a.Code, a.Body, b.Code, b.Body)
	}
}

func TestAuthConfigAdvertisesSignup(t *testing.T) {
	h := newTestHandler()
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		h.AuthConfig(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		var m map[string]any
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	if get()["signup_enabled"] != true {
		t.Error("signup should be offered before the first account exists")
	}
	register(h, "first@example.com")
	if get()["signup_enabled"] != false {
		t.Error("signup should be hidden once an admin exists")
	}
	h.AllowSignup = true
	if get()["signup_enabled"] != true {
		t.Error("signup should be offered when IGLOO_ALLOW_SIGNUP is on")
	}
}

func TestRequireAuth(t *testing.T) {
	h := newTestHandler()
	ctx := context.Background()
	h.Store.CreateAPIToken(ctx, &models.APIToken{ID: "ok", UserID: "u-api", TokenHash: HashToken("igl_valid"), ExpiresAt: time.Now().Add(time.Hour)})
	h.Store.CreateAPIToken(ctx, &models.APIToken{ID: "old", UserID: "u-api", TokenHash: HashToken("igl_expired"), ExpiresAt: time.Now().Add(-time.Hour)})
	jwtTok, _ := h.Auth.SignUser("u-jwt", "j@example.com")

	var gotUser string
	protected := h.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotUser = ctxUserID(r) }))

	for _, tc := range []struct {
		name, header string
		code         int
		user         string
	}{
		{"no header", "", 401, ""},
		{"garbage", "Bearer not-a-token", 401, ""},
		{"malformed jwt", "Bearer a.b.c", 401, ""},
		{"expired api token", "Bearer igl_expired", 401, ""},
		{"valid api token", "Bearer igl_valid", 200, "u-api"},
		{"valid jwt", "Bearer " + jwtTok, 200, "u-jwt"},
	} {
		gotUser = ""
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		if rec.Code != tc.code || gotUser != tc.user {
			t.Errorf("%s: got %d user=%q, want %d user=%q", tc.name, rec.Code, gotUser, tc.code, tc.user)
		}
	}
}
