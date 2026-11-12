package api

// Two Handlers sharing one store stand in for two server replicas: in a cluster, each
// HTTP request can land on either one. These tests pin the cross-replica behaviour of
// the state that used to live in one handler's memory.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/igloo-dev/igloo/internal/auth"
	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

func twoReplicaHandlers() (*Handler, *Handler, *store.MemoryStore) {
	st := store.NewMemory()
	a := &Handler{Store: st, Auth: auth.New("test-secret")}
	b := &Handler{Store: st, Auth: auth.New("test-secret")}
	return a, b, st
}

func pollDevice(h *Handler, code string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/?code="+code, nil)
	rec := httptest.NewRecorder()
	h.DevicePoll(rec, req)
	return rec
}

// igloo login registers its code on whichever replica the ingress picks, the browser
// approves on another, and the CLI's next poll can land on a third.
func TestDeviceLoginWorksAcrossReplicas(t *testing.T) {
	a, b, _ := twoReplicaHandlers()

	rec := register(a, "cli@example.com")
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var reg struct{ User models.User }
	json.Unmarshal(rec.Body.Bytes(), &reg)

	if rec := call(a.DeviceInit, map[string]string{"code": "code-1234"}); rec.Code != http.StatusOK {
		t.Fatalf("init on A: %d %s", rec.Code, rec.Body)
	}
	if rec := pollDevice(b, "code-1234"); rec.Code != http.StatusAccepted {
		t.Fatalf("pending poll on B: got %d, want 202", rec.Code)
	}

	// The browser approves on A.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"code":"code-1234"}`))
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserID, reg.User.ID))
	arec := httptest.NewRecorder()
	a.DeviceApprove(arec, req)
	if arec.Code != http.StatusOK {
		t.Fatalf("approve on A: %d %s", arec.Code, arec.Body)
	}

	// The CLI's poll lands on B and must walk away with the token — once.
	rec = pollDevice(b, "code-1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("approved poll on B: got %d %s, want 200", rec.Code, rec.Body)
	}
	var out map[string]string
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["token"] == "" {
		t.Fatal("poll on B returned no token")
	}
	if rec := pollDevice(a, "code-1234"); rec.Code != http.StatusGone {
		t.Fatalf("second poll: got %d, want 410 (one-time use)", rec.Code)
	}
}

// Two replicas each serving a registration at the same moment must still mint exactly
// one admin.
func TestFirstAdminIsDecidedOnceAcrossReplicas(t *testing.T) {
	a, b, st := twoReplicaHandlers()
	a.AllowSignup, b.AllowSignup = true, true

	handlers := []*Handler{a, b}
	emails := []string{"u1@x.dev", "u2@x.dev", "u3@x.dev", "u4@x.dev", "u5@x.dev", "u6@x.dev"}
	var wg sync.WaitGroup
	for i, email := range emails {
		wg.Add(1)
		go func(h *Handler, email string) {
			defer wg.Done()
			register(h, email)
		}(handlers[i%2], email)
	}
	wg.Wait()

	users, err := st.ListUsers(context.Background())
	if err != nil || len(users) != len(emails) {
		t.Fatalf("users=%d err=%v, want %d", len(users), err, len(emails))
	}
	admins := 0
	for _, u := range users {
		if u.IsAdmin {
			admins++
		}
	}
	if admins != 1 {
		t.Fatalf("%d admins were minted, want exactly 1", admins)
	}
}
