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
	ListWorkspaces(ctx context.Context, userID string) ([]*models.Workspace, error)
	UpdateWorkspace(ctx context.Context, w *models.Workspace) error
	DeleteWorkspace(ctx context.Context, id string) error

	// Templates
	UpsertTemplate(ctx context.Context, t *models.Template) error
	GetTemplate(ctx context.Context, id string) (*models.Template, error)
	ListTemplates(ctx context.Context) ([]*models.Template, error)

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
