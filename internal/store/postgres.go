package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store against any PostgreSQL-compatible database
// (RDS, Aurora, Cloud SQL, self-hosted). Selected when IGLOO_DATABASE_URL is set.
type PostgresStore struct {
	db *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, url string) (*PostgresStore, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		return nil, err
	}
	s := &PostgresStore{db: db}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// migrate runs schema.sql idempotently (all statements use IF NOT EXISTS).
func (s *PostgresStore) migrate(ctx context.Context) error {
	_, err := s.db.Exec(ctx, sqlSchema)
	return err
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateUser(ctx context.Context, u *models.User) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, ssh_public_key, tailscale_auth_key, is_admin, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.SSHPublicKey, u.TailscaleAuthKey, u.IsAdmin, u.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *PostgresStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	row := s.db.QueryRow(ctx, `SELECT id,email,name,password_hash,ssh_public_key,tailscale_auth_key,is_admin,created_at FROM users WHERE id=$1`, id)
	return scanUser(row)
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	row := s.db.QueryRow(ctx, `SELECT id,email,name,password_hash,ssh_public_key,tailscale_auth_key,is_admin,created_at FROM users WHERE email=$1`, email)
	return scanUser(row)
}

func (s *PostgresStore) UpdateUser(ctx context.Context, u *models.User) error {
	_, err := s.db.Exec(ctx, `
		UPDATE users SET email=$2, name=$3, password_hash=$4, ssh_public_key=$5,
		tailscale_auth_key=$6, is_admin=$7 WHERE id=$1`,
		u.ID, u.Email, u.Name, u.PasswordHash, u.SSHPublicKey, u.TailscaleAuthKey, u.IsAdmin)
	return err
}

func (s *PostgresStore) ListUsers(ctx context.Context) ([]*models.User, error) {
	rows, err := s.db.Query(ctx, `SELECT id,email,name,password_hash,ssh_public_key,tailscale_auth_key,is_admin,created_at FROM users WHERE id != '__platform__' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM users WHERE id=$1`, id)
	return err
}

// ─── Workspaces ───────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateWorkspace(ctx context.Context, w *models.Workspace) error {
	vars, _ := json.Marshal(w.Vars)
	meta, _ := json.Marshal(w.Meta)
	_, err := s.db.Exec(ctx, `
		INSERT INTO workspaces (id,user_id,name,template_id,template_name,provider,status,agent_token,cost_per_hr,created_at,updated_at,last_active_at,vars,meta)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		w.ID, w.UserID, w.Name, w.TemplateID, w.TemplateName, w.Provider, string(w.Status),
		w.AgentToken, w.CostPerHr, w.CreatedAt, w.UpdatedAt, w.LastActiveAt, vars, meta)
	return err
}

func (s *PostgresStore) GetWorkspace(ctx context.Context, id string) (*models.Workspace, error) {
	row := s.db.QueryRow(ctx, `SELECT id,user_id,name,template_id,template_name,provider,status,agent_token,cost_per_hr,created_at,updated_at,last_active_at,vars,meta FROM workspaces WHERE id=$1`, id)
	return scanWorkspace(row)
}

func (s *PostgresStore) ListWorkspaces(ctx context.Context, userID string) ([]*models.Workspace, error) {
	var rows pgx.Rows
	var err error
	if userID == "" {
		rows, err = s.db.Query(ctx, `SELECT id,user_id,name,template_id,template_name,provider,status,agent_token,cost_per_hr,created_at,updated_at,last_active_at,vars,meta FROM workspaces WHERE status != 'deleted' ORDER BY created_at DESC`)
	} else {
		rows, err = s.db.Query(ctx, `SELECT id,user_id,name,template_id,template_name,provider,status,agent_token,cost_per_hr,created_at,updated_at,last_active_at,vars,meta FROM workspaces WHERE user_id=$1 AND status != 'deleted' ORDER BY created_at DESC`, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err == nil {
			out = append(out, w)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateWorkspace(ctx context.Context, w *models.Workspace) error {
	vars, _ := json.Marshal(w.Vars)
	meta, _ := json.Marshal(w.Meta)
	_, err := s.db.Exec(ctx, `
		UPDATE workspaces SET user_id=$2,name=$3,template_id=$4,template_name=$5,provider=$6,
		status=$7,agent_token=$8,cost_per_hr=$9,updated_at=$10,last_active_at=$11,vars=$12,meta=$13
		WHERE id=$1`,
		w.ID, w.UserID, w.Name, w.TemplateID, w.TemplateName, w.Provider, string(w.Status),
		w.AgentToken, w.CostPerHr, w.UpdatedAt, w.LastActiveAt, vars, meta)
	return err
}

func (s *PostgresStore) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM workspaces WHERE id=$1`, id)
	return err
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (s *PostgresStore) UpsertTemplate(ctx context.Context, t *models.Template) error {
	vars, _ := json.Marshal(t.Variables)
	files, _ := json.Marshal(t.Files)
	_, err := s.db.Exec(ctx, `
		INSERT INTO templates (id,name,description,provider,icon,builtin,variables,files,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET name=$2,description=$3,provider=$4,icon=$5,builtin=$6,variables=$7,files=$8`,
		t.ID, t.Name, t.Description, t.Provider, t.Icon, t.Builtin, vars, files, t.CreatedAt)
	return err
}

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (*models.Template, error) {
	row := s.db.QueryRow(ctx, `SELECT id,name,description,provider,icon,builtin,variables,files,created_at FROM templates WHERE id=$1`, id)
	return scanTemplate(row)
}

func (s *PostgresStore) ListTemplates(ctx context.Context) ([]*models.Template, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,description,provider,icon,builtin,variables,files,created_at FROM templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err == nil {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// ─── Jobs ─────────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateJob(ctx context.Context, j *models.ProvisionJob) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO jobs (id,workspace_id,action,status,provisioner_id,retry_count,created_at,updated_at,heartbeat,error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		j.ID, j.WorkspaceID, string(j.Action), string(j.Status), j.ProvisionerID,
		j.RetryCount, j.CreatedAt, j.UpdatedAt, nilTime(j.Heartbeat), j.Error)
	return err
}

func (s *PostgresStore) GetJob(ctx context.Context, id string) (*models.ProvisionJob, error) {
	row := s.db.QueryRow(ctx, `SELECT id,workspace_id,action,status,provisioner_id,retry_count,created_at,updated_at,heartbeat,error FROM jobs WHERE id=$1`, id)
	return scanJob(row)
}

func (s *PostgresStore) ClaimNextJob(ctx context.Context, provisionerID string) (*models.ProvisionJob, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		SELECT id,workspace_id,action,status,provisioner_id,retry_count,created_at,updated_at,heartbeat,error
		FROM jobs WHERE status='pending'
		ORDER BY created_at ASC
		LIMIT 1 FOR UPDATE SKIP LOCKED`)
	j, err := scanJob(row)
	if err != nil {
		return nil, nil // no pending jobs
	}
	now := time.Now()
	_, err = tx.Exec(ctx, `UPDATE jobs SET status='running', provisioner_id=$2, updated_at=$3 WHERE id=$1`,
		j.ID, provisionerID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	j.Status = models.JobRunning
	j.ProvisionerID = provisionerID
	j.UpdatedAt = now
	return j, nil
}

func (s *PostgresStore) UpdateJob(ctx context.Context, j *models.ProvisionJob) error {
	_, err := s.db.Exec(ctx, `
		UPDATE jobs SET status=$2,provisioner_id=$3,retry_count=$4,updated_at=$5,heartbeat=$6,error=$7 WHERE id=$1`,
		j.ID, string(j.Status), j.ProvisionerID, j.RetryCount, j.UpdatedAt, nilTime(j.Heartbeat), j.Error)
	return err
}

func (s *PostgresStore) UpdateJobHeartbeat(ctx context.Context, jobID string) error {
	_, err := s.db.Exec(ctx, `UPDATE jobs SET heartbeat=$2, updated_at=$2 WHERE id=$1`, jobID, time.Now())
	return err
}

func (s *PostgresStore) ListStaleJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoff := time.Now().Add(-olderThan)
	rows, err := s.db.Query(ctx, `SELECT id,workspace_id,action,status,provisioner_id,retry_count,created_at,updated_at,heartbeat,error FROM jobs WHERE status='running' AND provisioner_id='' AND updated_at < $1`, cutoff)
	if err != nil {
		return nil, err
	}
	return collectJobs(rows)
}

func (s *PostgresStore) ListStaleDaemonJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoff := time.Now().Add(-olderThan)
	rows, err := s.db.Query(ctx, `SELECT id,workspace_id,action,status,provisioner_id,retry_count,created_at,updated_at,heartbeat,error FROM jobs WHERE status='running' AND provisioner_id!='' AND (heartbeat IS NULL OR heartbeat < $1)`, cutoff)
	if err != nil {
		return nil, err
	}
	return collectJobs(rows)
}

// ─── Provisioner keys ─────────────────────────────────────────────────────────

func (s *PostgresStore) CreateProvisionerKey(ctx context.Context, k *models.ProvisionerKey) error {
	_, err := s.db.Exec(ctx, `INSERT INTO provisioner_keys (id,name,created_by,token_hash,created_at) VALUES ($1,$2,$3,$4,$5)`,
		k.ID, k.Name, k.CreatedBy, k.TokenHash, k.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *PostgresStore) GetProvisionerKeyByTokenHash(ctx context.Context, hash string) (*models.ProvisionerKey, error) {
	row := s.db.QueryRow(ctx, `SELECT id,name,created_by,token_hash,created_at,last_used_at FROM provisioner_keys WHERE token_hash=$1`, hash)
	return scanProvisionerKey(row)
}

func (s *PostgresStore) ListProvisionerKeys(ctx context.Context) ([]*models.ProvisionerKey, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,created_by,token_hash,created_at,last_used_at FROM provisioner_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.ProvisionerKey
	for rows.Next() {
		k, err := scanProvisionerKey(rows)
		if err == nil {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) DeleteProvisionerKey(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM provisioner_keys WHERE id=$1`, id)
	return err
}

func (s *PostgresStore) TouchProvisionerKey(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE provisioner_keys SET last_used_at=$2 WHERE id=$1`, id, time.Now())
	return err
}

// ─── Platform config ──────────────────────────────────────────────────────────

func (s *PostgresStore) GetPlatformConfig(ctx context.Context) (*models.PlatformConfig, error) {
	var data []byte
	err := s.db.QueryRow(ctx, `SELECT data FROM platform_config WHERE id='singleton'`).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return &models.PlatformConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg models.PlatformConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *PostgresStore) SavePlatformConfig(ctx context.Context, cfg *models.PlatformConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO platform_config (id, data) VALUES ('singleton', $1)
		ON CONFLICT (id) DO UPDATE SET data=$1`, data)
	return err
}

// ─── Organizations ────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateOrg(ctx context.Context, o *models.Organization) error {
	claims, _ := json.Marshal(o.GroupClaims)
	mapping, _ := json.Marshal(o.GroupMapping)
	_, err := s.db.Exec(ctx, `
		INSERT INTO organizations (id,name,slug,description,created_by,created_at,group_claims,group_mapping,group_auto_create)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		o.ID, o.Name, o.Slug, o.Description, o.CreatedBy, o.CreatedAt, claims, mapping, o.GroupAutoCreate)
	if err != nil && isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *PostgresStore) GetOrg(ctx context.Context, id string) (*models.Organization, error) {
	row := s.db.QueryRow(ctx, `SELECT id,name,slug,description,created_by,created_at,group_claims,group_mapping,group_auto_create FROM organizations WHERE id=$1`, id)
	return scanOrg(row)
}

func (s *PostgresStore) GetOrgBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	row := s.db.QueryRow(ctx, `SELECT id,name,slug,description,created_by,created_at,group_claims,group_mapping,group_auto_create FROM organizations WHERE slug=$1`, slug)
	return scanOrg(row)
}

func (s *PostgresStore) ListOrgs(ctx context.Context) ([]*models.Organization, error) {
	rows, err := s.db.Query(ctx, `SELECT id,name,slug,description,created_by,created_at,group_claims,group_mapping,group_auto_create FROM organizations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Organization
	for rows.Next() {
		o, err := scanOrg(rows)
		if err == nil {
			out = append(out, o)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateOrg(ctx context.Context, o *models.Organization) error {
	claims, _ := json.Marshal(o.GroupClaims)
	mapping, _ := json.Marshal(o.GroupMapping)
	_, err := s.db.Exec(ctx, `
		UPDATE organizations SET name=$2,slug=$3,description=$4,group_claims=$5,group_mapping=$6,group_auto_create=$7 WHERE id=$1`,
		o.ID, o.Name, o.Slug, o.Description, claims, mapping, o.GroupAutoCreate)
	return err
}

func (s *PostgresStore) DeleteOrg(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, id)
	return err
}

// ─── Org members ──────────────────────────────────────────────────────────────

func (s *PostgresStore) AddOrgMember(ctx context.Context, m *models.OrgMember) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO org_members (org_id,user_id,user_email,user_name,role,joined_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (org_id,user_id) DO UPDATE SET role=$5,user_email=$3,user_name=$4`,
		m.OrgID, m.UserID, m.UserEmail, m.UserName, string(m.Role), m.JoinedAt)
	return err
}

func (s *PostgresStore) GetOrgMember(ctx context.Context, orgID, userID string) (*models.OrgMember, error) {
	row := s.db.QueryRow(ctx, `SELECT org_id,user_id,user_email,user_name,role,joined_at FROM org_members WHERE org_id=$1 AND user_id=$2`, orgID, userID)
	return scanOrgMember(row)
}

func (s *PostgresStore) ListOrgMembers(ctx context.Context, orgID string) ([]*models.OrgMember, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id,user_id,user_email,user_name,role,joined_at FROM org_members WHERE org_id=$1`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListUserOrgs(ctx context.Context, userID string) ([]*models.OrgMember, error) {
	rows, err := s.db.Query(ctx, `SELECT org_id,user_id,user_email,user_name,role,joined_at FROM org_members WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.OrgMember
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateOrgMember(ctx context.Context, m *models.OrgMember) error {
	return s.AddOrgMember(ctx, m)
}

func (s *PostgresStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM org_members WHERE org_id=$1 AND user_id=$2`, orgID, userID)
	return err
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateTeam(ctx context.Context, t *models.Team) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO teams (id,org_id,name,description,created_by,created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		t.ID, t.OrgID, t.Name, t.Description, t.CreatedBy, t.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *PostgresStore) GetTeam(ctx context.Context, id string) (*models.Team, error) {
	row := s.db.QueryRow(ctx, `SELECT id,org_id,name,description,created_by,created_at FROM teams WHERE id=$1`, id)
	return scanTeam(row)
}

func (s *PostgresStore) ListTeams(ctx context.Context, orgID string) ([]*models.Team, error) {
	rows, err := s.db.Query(ctx, `SELECT id,org_id,name,description,created_by,created_at FROM teams WHERE org_id=$1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err == nil {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdateTeam(ctx context.Context, t *models.Team) error {
	_, err := s.db.Exec(ctx, `UPDATE teams SET name=$2,description=$3 WHERE id=$1`, t.ID, t.Name, t.Description)
	return err
}

func (s *PostgresStore) DeleteTeam(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM teams WHERE id=$1`, id)
	return err
}

// ─── Team members ─────────────────────────────────────────────────────────────

func (s *PostgresStore) AddTeamMember(ctx context.Context, m *models.TeamMember) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO team_members (team_id,user_id,user_email,user_name,joined_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (team_id,user_id) DO NOTHING`,
		m.TeamID, m.UserID, m.UserEmail, m.UserName, m.JoinedAt)
	return err
}

func (s *PostgresStore) ListTeamMembers(ctx context.Context, teamID string) ([]*models.TeamMember, error) {
	rows, err := s.db.Query(ctx, `SELECT team_id,user_id,user_email,user_name,joined_at FROM team_members WHERE team_id=$1`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.TeamMember
	for rows.Next() {
		m, err := scanTeamMember(rows)
		if err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListUserTeams(ctx context.Context, userID string) ([]*models.TeamMember, error) {
	rows, err := s.db.Query(ctx, `SELECT team_id,user_id,user_email,user_name,joined_at FROM team_members WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.TeamMember
	for rows.Next() {
		m, err := scanTeamMember(rows)
		if err == nil {
			out = append(out, m)
		}
	}
	return out, rows.Err()
}

func (s *PostgresStore) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM team_members WHERE team_id=$1 AND user_id=$2`, teamID, userID)
	return err
}

func (s *PostgresStore) SyncTeamMembers(ctx context.Context, teamID string, userIDs []string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `DELETE FROM team_members WHERE team_id=$1`, teamID)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, uid := range userIDs {
		_, err = tx.Exec(ctx, `INSERT INTO team_members (team_id,user_id,joined_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, teamID, uid, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ─── Scan helpers ─────────────────────────────────────────────────────────────

type scanner interface {
	Scan(...any) error
}

func scanUser(row scanner) (*models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.SSHPublicKey, &u.TailscaleAuthKey, &u.IsAdmin, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func scanWorkspace(row scanner) (*models.Workspace, error) {
	var w models.Workspace
	var status string
	var vars, meta []byte
	err := row.Scan(&w.ID, &w.UserID, &w.Name, &w.TemplateID, &w.TemplateName, &w.Provider,
		&status, &w.AgentToken, &w.CostPerHr, &w.CreatedAt, &w.UpdatedAt, &w.LastActiveAt, &vars, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.Status = models.WorkspaceStatus(status)
	w.Vars = map[string]string{}
	w.Meta = map[string]string{}
	json.Unmarshal(vars, &w.Vars)
	json.Unmarshal(meta, &w.Meta)
	return &w, nil
}

func scanTemplate(row scanner) (*models.Template, error) {
	var t models.Template
	var vars, files []byte
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Provider, &t.Icon, &t.Builtin, &vars, &files, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(vars, &t.Variables)
	json.Unmarshal(files, &t.Files)
	return &t, nil
}

func scanJob(row scanner) (*models.ProvisionJob, error) {
	var j models.ProvisionJob
	var action, status string
	var heartbeat *time.Time
	err := row.Scan(&j.ID, &j.WorkspaceID, &action, &status, &j.ProvisionerID,
		&j.RetryCount, &j.CreatedAt, &j.UpdatedAt, &heartbeat, &j.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Action = models.JobAction(action)
	j.Status = models.JobStatus(status)
	if heartbeat != nil {
		j.Heartbeat = *heartbeat
	}
	return &j, nil
}

func scanProvisionerKey(row scanner) (*models.ProvisionerKey, error) {
	var k models.ProvisionerKey
	err := row.Scan(&k.ID, &k.Name, &k.CreatedBy, &k.TokenHash, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &k, err
}

func scanOrg(row scanner) (*models.Organization, error) {
	var o models.Organization
	var claims, mapping []byte
	err := row.Scan(&o.ID, &o.Name, &o.Slug, &o.Description, &o.CreatedBy, &o.CreatedAt, &claims, &mapping, &o.GroupAutoCreate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(claims, &o.GroupClaims)
	json.Unmarshal(mapping, &o.GroupMapping)
	return &o, nil
}

func scanOrgMember(row scanner) (*models.OrgMember, error) {
	var m models.OrgMember
	var role string
	err := row.Scan(&m.OrgID, &m.UserID, &m.UserEmail, &m.UserName, &role, &m.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Role = models.OrgRole(role)
	return &m, nil
}

func scanTeam(row scanner) (*models.Team, error) {
	var t models.Team
	err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Description, &t.CreatedBy, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func scanTeamMember(row scanner) (*models.TeamMember, error) {
	var m models.TeamMember
	err := row.Scan(&m.TeamID, &m.UserID, &m.UserEmail, &m.UserName, &m.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}

func collectJobs(rows pgx.Rows) ([]*models.ProvisionJob, error) {
	defer rows.Close()
	var out []*models.ProvisionJob
	for rows.Next() {
		j, err := scanJob(rows)
		if err == nil {
			out = append(out, j)
		}
	}
	return out, rows.Err()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func nilTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func isUniqueViolation(err error) bool {
	return pgErrCode(err) == "23505"
}

func pgErrCode(err error) string {
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState()
	}
	return ""
}

// sqlSchema is embedded at compile time from schema.sql.
const sqlSchema = `
CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,
    email           TEXT UNIQUE NOT NULL,
    name            TEXT NOT NULL DEFAULT '',
    password_hash   TEXT NOT NULL DEFAULT '',
    ssh_public_key  TEXT NOT NULL DEFAULT '',
    tailscale_auth_key TEXT NOT NULL DEFAULT '',
    is_admin        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS workspaces (
    id            TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL,
    name          TEXT NOT NULL,
    template_id   TEXT NOT NULL,
    template_name TEXT NOT NULL,
    provider      TEXT NOT NULL,
    status        TEXT NOT NULL,
    agent_token   TEXT NOT NULL DEFAULT '',
    cost_per_hr   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    last_active_at TIMESTAMPTZ NOT NULL,
    vars          JSONB NOT NULL DEFAULT '{}',
    meta          JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS workspaces_user_id_idx ON workspaces(user_id);
CREATE TABLE IF NOT EXISTS templates (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL,
    icon        TEXT NOT NULL DEFAULT '',
    builtin     BOOLEAN NOT NULL DEFAULT FALSE,
    variables   JSONB NOT NULL DEFAULT '[]',
    files       JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    action         TEXT NOT NULL,
    status         TEXT NOT NULL,
    provisioner_id TEXT NOT NULL DEFAULT '',
    retry_count    INT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    heartbeat      TIMESTAMPTZ,
    error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS jobs_status_created_at_idx ON jobs(status, created_at);
CREATE INDEX IF NOT EXISTS jobs_workspace_id_idx ON jobs(workspace_id);
CREATE TABLE IF NOT EXISTS provisioner_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    created_by   TEXT NOT NULL DEFAULT '',
    token_hash   TEXT UNIQUE NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS platform_config (
    id   TEXT PRIMARY KEY DEFAULT 'singleton',
    data JSONB NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS organizations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    slug             TEXT UNIQUE NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    created_by       TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    group_claims     JSONB NOT NULL DEFAULT '[]',
    group_mapping    JSONB NOT NULL DEFAULT '{}',
    group_auto_create BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE IF NOT EXISTS org_members (
    org_id     TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_email TEXT NOT NULL DEFAULT '',
    user_name  TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL,
    joined_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS org_members_user_id_idx ON org_members(user_id);
CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS teams_org_id_idx ON teams(org_id);
CREATE TABLE IF NOT EXISTS team_members (
    team_id    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    user_email TEXT NOT NULL DEFAULT '',
    user_name  TEXT NOT NULL DEFAULT '',
    joined_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX IF NOT EXISTS team_members_user_id_idx ON team_members(user_id);
`
