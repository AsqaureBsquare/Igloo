package store

import (
	"context"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
)

type Store interface {
	// Users
	CreateUser(ctx context.Context, u *models.User) error
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUser(ctx context.Context, id string) (*models.User, error)
	UpdateUser(ctx context.Context, u *models.User) error
	ListUsers(ctx context.Context) ([]*models.User, error)
	DeleteUser(ctx context.Context, id string) error

	// Workspaces
	CreateWorkspace(ctx context.Context, w *models.Workspace) error
	GetWorkspace(ctx context.Context, id string) (*models.Workspace, error)
	// ListWorkspaces returns workspaces for a user in an org. orgID="" returns across all orgs.
	ListWorkspaces(ctx context.Context, userID, orgID string) ([]*models.Workspace, error)
	UpdateWorkspace(ctx context.Context, w *models.Workspace) error
	DeleteWorkspace(ctx context.Context, id string) error

	// Templates
	UpsertTemplate(ctx context.Context, t *models.Template) error
	GetTemplate(ctx context.Context, id string) (*models.Template, error)
	// ListTemplates returns builtin templates plus org-scoped ones. orgID="" returns only builtins.
	ListTemplates(ctx context.Context, orgID string) ([]*models.Template, error)
	DeleteTemplate(ctx context.Context, id string) error

	// Jobs
	CreateJob(ctx context.Context, j *models.ProvisionJob) error
	GetJob(ctx context.Context, id string) (*models.ProvisionJob, error)
	// ClaimNextJob atomically claims the oldest pending job, setting provisionerID on the record.
	// Returns nil, nil when no pending jobs exist.
	ClaimNextJob(ctx context.Context, provisionerID string) (*models.ProvisionJob, error)
	UpdateJob(ctx context.Context, j *models.ProvisionJob) error
	// UpdateJobHeartbeat records a keepalive from a provisioner daemon.
	UpdateJobHeartbeat(ctx context.Context, jobID string) error
	// ListStaleJobs returns running jobs whose UpdatedAt is older than olderThan (catches embedded worker stalls).
	ListStaleJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error)
	// ListStaleDaemonJobs returns running daemon-claimed jobs whose Heartbeat is older than olderThan.
	ListStaleDaemonJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error)

	// APITokens (service accounts / CI)
	CreateAPIToken(ctx context.Context, t *models.APIToken) error
	GetAPITokenByHash(ctx context.Context, hash string) (*models.APIToken, error)
	ListAPITokens(ctx context.Context, userID string) ([]*models.APIToken, error)
	DeleteAPIToken(ctx context.Context, id string) error
	TouchAPIToken(ctx context.Context, id string) error

	// ProvisionerKeys
	CreateProvisionerKey(ctx context.Context, k *models.ProvisionerKey) error
	GetProvisionerKeyByTokenHash(ctx context.Context, hash string) (*models.ProvisionerKey, error)
	ListProvisionerKeys(ctx context.Context) ([]*models.ProvisionerKey, error)
	DeleteProvisionerKey(ctx context.Context, id string) error
	TouchProvisionerKey(ctx context.Context, id string) error

	// PlatformConfig holds admin-managed platform-level settings.
	GetPlatformConfig(ctx context.Context) (*models.PlatformConfig, error)
	SavePlatformConfig(ctx context.Context, cfg *models.PlatformConfig) error

	// Organizations
	CreateOrg(ctx context.Context, o *models.Organization) error
	GetOrg(ctx context.Context, id string) (*models.Organization, error)
	GetOrgBySlug(ctx context.Context, slug string) (*models.Organization, error)
	ListOrgs(ctx context.Context) ([]*models.Organization, error)
	UpdateOrg(ctx context.Context, o *models.Organization) error
	DeleteOrg(ctx context.Context, id string) error

	// Org members
	AddOrgMember(ctx context.Context, m *models.OrgMember) error
	GetOrgMember(ctx context.Context, orgID, userID string) (*models.OrgMember, error)
	ListOrgMembers(ctx context.Context, orgID string) ([]*models.OrgMember, error)
	ListUserOrgs(ctx context.Context, userID string) ([]*models.OrgMember, error)
	UpdateOrgMember(ctx context.Context, m *models.OrgMember) error
	RemoveOrgMember(ctx context.Context, orgID, userID string) error

	// Teams
	CreateTeam(ctx context.Context, t *models.Team) error
	GetTeam(ctx context.Context, id string) (*models.Team, error)
	ListTeams(ctx context.Context, orgID string) ([]*models.Team, error)
	UpdateTeam(ctx context.Context, t *models.Team) error
	DeleteTeam(ctx context.Context, id string) error

	// Team members
	AddTeamMember(ctx context.Context, m *models.TeamMember) error
	ListTeamMembers(ctx context.Context, teamID string) ([]*models.TeamMember, error)
	ListUserTeams(ctx context.Context, userID string) ([]*models.TeamMember, error)
	RemoveTeamMember(ctx context.Context, teamID, userID string) error
	SyncTeamMembers(ctx context.Context, teamID string, userIDs []string) error
}

// AgentOwnerStore is implemented by stores that can record which server replica holds
// a workspace's agent connection. It is what lets more than one server replica run:
// a replica that does not hold the agent looks up the owner and forwards to it.
// A store without it supports a single replica only.
type AgentOwnerStore interface {
	// UpsertAgentOwner records the owner unconditionally — the agent just connected
	// there, and the most recent connection wins.
	UpsertAgentOwner(ctx context.Context, o *models.AgentOwner) error
	// GetAgentOwner returns the owner holding a live lease, or nil, nil when there is none.
	GetAgentOwner(ctx context.Context, workspaceID string) (*models.AgentOwner, error)
	// RenewAgentOwner extends the lease only while replicaID still owns the row, so a
	// replica with a stale connection cannot take a workspace back from the one the
	// agent reconnected to.
	RenewAgentOwner(ctx context.Context, workspaceID, replicaID string, leaseUntil time.Time) error
	// DeleteAgentOwner removes the row only while replicaID still owns it.
	DeleteAgentOwner(ctx context.Context, workspaceID, replicaID string) error
}

// DeviceCodeStore is implemented by stores that can hold CLI device-login codes, so the
// login works when `igloo login` registers its code on one server replica and polls
// another. A store without it keeps codes in the handler's memory (single replica only).
type DeviceCodeStore interface {
	// CreateDeviceCode registers a pending login. ErrConflict when the code is taken.
	CreateDeviceCode(ctx context.Context, code string, expiresAt time.Time) error
	// ApproveDeviceCode attaches the signed CLI token to a pending code.
	// ErrNotFound when the code is unknown or expired.
	ApproveDeviceCode(ctx context.Context, code, token string) error
	// TakeDeviceCode returns the token and deletes the code in one step (one-time use),
	// or "" while the code is still pending. ErrNotFound when unknown or expired.
	TakeDeviceCode(ctx context.Context, code string) (string, error)
	// CountDeviceCodes reports how many codes are pending (for the abuse cap).
	CountDeviceCodes(ctx context.Context) (int, error)
}

// RegistrationLocker is implemented by stores that can serialise "the first registered
// user becomes admin" across replicas. Without it the handler's local mutex still
// protects a single replica.
type RegistrationLocker interface {
	WithRegistrationLock(ctx context.Context, fn func() error) error
}

// ProvisionLogStore is implemented by stores that can keep workspace build logs, so a
// browser watching the log on one replica sees lines the provisioner reports to another.
// A store without it buffers lines in the handler's memory (single replica only).
type ProvisionLogStore interface {
	// AppendProvisionLog stores one line and returns its sequence number.
	AppendProvisionLog(ctx context.Context, workspaceID, line string) (int64, error)
	// ListProvisionLogs returns the lines with Seq > afterSeq, oldest first.
	ListProvisionLogs(ctx context.Context, workspaceID string, afterSeq int64) ([]models.ProvisionLogLine, error)
	DeleteProvisionLogs(ctx context.Context, workspaceID string) error
}

// GuardEventStore is implemented by stores that can keep the egress gateway's audit
// trail. It is separate from Store so a backend without it still works (activity is then
// reported as unsupported).
type GuardEventStore interface {
	AppendGuardEvents(ctx context.Context, evs []models.GuardEvent) error
	// ListGuardEvents returns a workspace's events, newest first, at most limit.
	ListGuardEvents(ctx context.Context, workspaceID string, limit int) ([]models.GuardEvent, error)
}

// MaxGuardEventsPerWorkspace bounds how much audit history one workspace keeps in the
// in-memory store; the SQL store keeps history until it is pruned.
const MaxGuardEventsPerWorkspace = 5000

// The backends that can run more than one server replica.
var (
	_ AgentOwnerStore    = (*MemoryStore)(nil) // one process only — for tests and --local
	_ AgentOwnerStore    = (*PostgresStore)(nil)
	_ DeviceCodeStore    = (*MemoryStore)(nil)
	_ DeviceCodeStore    = (*PostgresStore)(nil)
	_ RegistrationLocker = (*MemoryStore)(nil)
	_ RegistrationLocker = (*PostgresStore)(nil)
	_ ProvisionLogStore  = (*MemoryStore)(nil)
	_ ProvisionLogStore  = (*PostgresStore)(nil)
)
