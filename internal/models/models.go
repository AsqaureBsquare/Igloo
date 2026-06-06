package models

import "time"

type ProvisionerKey struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Tags       map[string]string `json:"tags,omitempty"`
	TokenHash  string            `json:"-"`
	CreatedBy  string            `json:"created_by"`
	CreatedAt  time.Time         `json:"created_at"`
	LastUsedAt *time.Time        `json:"last_used_at,omitempty"`
}

type WorkspaceStatus string

const (
	StatusCreating WorkspaceStatus = "creating"
	StatusRunning  WorkspaceStatus = "running"
	StatusSleeping WorkspaceStatus = "sleeping"
	StatusDeleting WorkspaceStatus = "deleting"
	StatusDeleted  WorkspaceStatus = "deleted"
	StatusError    WorkspaceStatus = "error"
)

type JobAction string

const (
	JobStart   JobAction = "start"
	JobStop    JobAction = "stop"
	JobDestroy JobAction = "destroy"
)

type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobFailed  JobStatus = "failed"
)

const JobMaxRetries = 3

type ProvisionJob struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Action        JobAction `json:"action"`
	Status        JobStatus `json:"status"`
	Error         string    `json:"error,omitempty"`
	RetryCount    int       `json:"retry_count"`
	ProvisionerID string    `json:"provisioner_id,omitempty"` // "embedded" or daemon UUID
	Heartbeat     time.Time `json:"heartbeat,omitempty"`      // last ping from daemon
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	TTL           int64     `json:"-"`
}

type Workspace struct {
	ID           string            `json:"id"`
	UserID       string            `json:"user_id"`
	Name         string            `json:"name"`
	TemplateID   string            `json:"template_id"`
	TemplateName string            `json:"template_name"`
	Provider     string            `json:"provider"`
	Status       WorkspaceStatus   `json:"status"`
	AgentToken   string            `json:"-"`
	CostPerHr    float64           `json:"cost_per_hr"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	Vars         map[string]string `json:"vars,omitempty"`        // user-supplied template variables
	Meta         map[string]string `json:"meta,omitempty"`        // provider metadata (task ARN, volume ID, etc.)
}

type User struct {
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	Name             string    `json:"name"`
	PasswordHash     string    `json:"-"`
	SSHPublicKey     string    `json:"ssh_public_key,omitempty"`
	TailscaleAuthKey string    `json:"tailscale_auth_key,omitempty"`
	IsAdmin          bool      `json:"is_admin,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ─── Organizations ────────────────────────────────────────────────────────────

type OrgRole string

const (
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
	OrgRoleViewer OrgRole = "viewer"
)

type Organization struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"` // url-safe identifier
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`

	// OIDC group sync (Coder-style)
	GroupClaims     []string          `json:"group_claims,omitempty"`  // IdP groups that grant org membership
	GroupMapping    map[string]string `json:"group_mapping,omitempty"` // idp_group → team name override
	GroupAutoCreate bool              `json:"group_auto_create"`       // create teams that don't exist yet
}

type OrgMember struct {
	OrgID     string    `json:"org_id"`
	UserID    string    `json:"user_id"`
	UserEmail string    `json:"user_email,omitempty"`
	UserName  string    `json:"user_name,omitempty"`
	Role      OrgRole   `json:"role"`
	JoinedAt  time.Time `json:"joined_at"`
}

// ─── Teams ────────────────────────────────────────────────────────────────────

type Team struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type TeamMember struct {
	TeamID    string    `json:"team_id"`
	UserID    string    `json:"user_id"`
	UserEmail string    `json:"user_email,omitempty"`
	UserName  string    `json:"user_name,omitempty"`
	JoinedAt  time.Time `json:"joined_at"`
}

type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Provider    string            `json:"provider"` // docker, aws-fargate, kubernetes, etc.
	Icon        string            `json:"icon"`
	Builtin     bool              `json:"builtin"`
	Version     int               `json:"version"`
	Variables   []TemplateVar     `json:"variables,omitempty"`
	Files       map[string]string `json:"files,omitempty"` // filename → terraform source
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// PlatformConfig holds admin-managed platform-level settings (stored in the users table).
type PlatformConfig struct {
	TailscaleAPIKey  string `json:"tailscale_api_key,omitempty"`
	TailscaleTailnet string `json:"tailscale_tailnet,omitempty"`
}

// TemplateVar defines a user-configurable input shown in the UI at workspace creation time.
type TemplateVar struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Default     string   `json:"default,omitempty"`
	Required    bool     `json:"required"`
	Type        string   `json:"type,omitempty"`    // string | number | bool | select
	Options     []string `json:"options,omitempty"` // for type=select
	Min         string   `json:"min,omitempty"`     // for type=number
	Max         string   `json:"max,omitempty"`     // for type=number
}
