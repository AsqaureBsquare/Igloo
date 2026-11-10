package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
	"github.com/igloo-dev/igloo/internal/store"
)

func newJob(t *testing.T, st store.Store, id, ws string, action models.JobAction) *models.ProvisionJob {
	t.Helper()
	j := &models.ProvisionJob{ID: id, WorkspaceID: ws, Action: action, Status: models.JobPending,
		CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now().Add(-time.Minute)}
	if err := st.CreateJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

func jobStatus(t *testing.T, st store.Store, id string) *models.ProvisionJob {
	t.Helper()
	j, err := st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestEnqueueCreatesPendingJob(t *testing.T) {
	st := store.NewMemory()
	if err := Enqueue(context.Background(), st, "w1", models.JobStart); err != nil {
		t.Fatal(err)
	}
	j, _ := st.ClaimNextJob(context.Background(), "p")
	if j == nil || j.WorkspaceID != "w1" || j.Action != models.JobStart {
		t.Fatalf("claimed %+v", j)
	}
}

func TestFinishJobRetriesThenFails(t *testing.T) {
	st := store.NewMemory()
	w := New(st, "http://x")
	ctx := context.Background()
	j := newJob(t, st, "j1", "w1", models.JobStart)

	boom := errors.New("terraform exploded")
	for i := 1; i <= models.JobMaxRetries; i++ {
		w.finishJob(ctx, j, boom)
		if j.Status != models.JobPending || j.RetryCount != i || j.Error != boom.Error() {
			t.Fatalf("attempt %d: status=%s retries=%d err=%q; want pending, %d, error kept", i, j.Status, j.RetryCount, j.Error, i)
		}
	}
	w.finishJob(ctx, j, boom)
	if j.Status != models.JobFailed {
		t.Errorf("after %d retries: status=%s, want failed", models.JobMaxRetries, j.Status)
	}
	if got := jobStatus(t, st, "j1"); got.Status != models.JobFailed {
		t.Errorf("failure was not persisted: %s", got.Status)
	}

	ok := newJob(t, st, "j2", "w1", models.JobStop)
	w.finishJob(ctx, ok, nil)
	if ok.Status != models.JobDone {
		t.Errorf("success: status=%s, want done", ok.Status)
	}
}

func TestRecoverStaleJobs(t *testing.T) {
	st := store.NewMemory()
	w := New(st, "http://x")
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)

	// Seed through CreateJob: the memory store's UpdateJob always stamps UpdatedAt=now.
	mk := func(id string, retries int, updated time.Time) {
		j := &models.ProvisionJob{ID: id, WorkspaceID: "w-" + id, Action: models.JobStart, Status: models.JobRunning,
			RetryCount: retries, CreatedAt: updated, UpdatedAt: updated}
		if err := st.CreateJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	mk("stale", 0, old)
	mk("exhausted", models.JobMaxRetries, old)
	mk("fresh", 0, time.Now())

	w.recoverStaleJobs(ctx)

	if j := jobStatus(t, st, "stale"); j.Status != models.JobPending || j.RetryCount != 1 {
		t.Errorf("stale: %s retries=%d, want pending, 1", j.Status, j.RetryCount)
	}
	if j := jobStatus(t, st, "exhausted"); j.Status != models.JobFailed {
		t.Errorf("exhausted: %s, want failed", j.Status)
	}
	if j := jobStatus(t, st, "fresh"); j.Status != models.JobRunning {
		t.Errorf("fresh job was touched: %s", j.Status)
	}
}

func TestProcessNextSkipsJobsForMissingOrDeletingWorkspaces(t *testing.T) {
	st := store.NewMemory()
	w := New(st, "http://x")
	ctx := context.Background()
	now := time.Now()

	newJob(t, st, "orphan", "gone", models.JobStart)
	w.processNext(ctx)
	if j := jobStatus(t, st, "orphan"); j.Status != models.JobDone {
		t.Errorf("job for missing workspace: %s, want done (nothing to do)", j.Status)
	}

	st.CreateWorkspace(ctx, &models.Workspace{ID: "dying", UserID: "u", Status: models.StatusDeleting, CreatedAt: now, UpdatedAt: now, LastActiveAt: now})
	newJob(t, st, "late-start", "dying", models.JobStart)
	w.processNext(ctx)
	if j := jobStatus(t, st, "late-start"); j.Status != models.JobDone {
		t.Errorf("start for a deleting workspace: %s, want done", j.Status)
	}
	if ws, _ := st.GetWorkspace(ctx, "dying"); ws == nil || ws.Status != models.StatusDeleting {
		t.Error("skipped job changed the workspace")
	}
}

func TestProcessNextRequeuesWhenWorkspaceBusy(t *testing.T) {
	st := store.NewMemory()
	w := New(st, "http://x")
	ctx := context.Background()
	now := time.Now()
	st.CreateWorkspace(ctx, &models.Workspace{ID: "w1", UserID: "u", Status: models.StatusRunning, CreatedAt: now, UpdatedAt: now, LastActiveAt: now})
	newJob(t, st, "j1", "w1", models.JobStop)

	w.inFlight.Store("w1", struct{}{}) // another goroutine is already running terraform for w1
	w.processNext(ctx)
	if j := jobStatus(t, st, "j1"); j.Status != models.JobPending {
		t.Errorf("job for a busy workspace: %s, want pending (retry later, don't run twice)", j.Status)
	}
	if _, still := w.inFlight.Load("w1"); !still {
		t.Error("the other goroutine's in-flight lock was released by the skipped attempt")
	}
}

func TestBuildFargateVarsInjectedValuesWinOverUserVars(t *testing.T) {
	ws := &models.Workspace{ID: "real-id", Name: "n", AgentToken: "real-token",
		Vars: map[string]string{"workspace_id": "spoofed", "agent_token": "spoofed", "server_url": "http://evil", "color": "blue"}}
	v := buildFargateVars(ws, "http://igloo")
	if v["workspace_id"] != "real-id" || v["agent_token"] != "real-token" || v["server_url"] != "http://igloo" {
		t.Errorf("user vars overrode server-injected values: %v", v)
	}
	if v["color"] != "blue" {
		t.Error("ordinary user variable was dropped")
	}
}
