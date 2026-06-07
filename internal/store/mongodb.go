package store

import (
	"context"
	"strings"
	"time"

	"github.com/igloo-dev/igloo/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoStore implements Store against any MongoDB-compatible database
// (Atlas, DocumentDB, self-hosted). Selected when IGLOO_MONGO_URL is set.
type MongoStore struct {
	users       *mongo.Collection
	workspaces  *mongo.Collection
	templates   *mongo.Collection
	jobs        *mongo.Collection
	pkeys       *mongo.Collection
	apiTokens   *mongo.Collection
	config      *mongo.Collection
	orgs        *mongo.Collection
	orgMembers  *mongo.Collection
	teams       *mongo.Collection
	teamMembers *mongo.Collection
}

func NewMongoStore(ctx context.Context, uri string) (*MongoStore, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}
	db := client.Database(mongoDBName(uri))
	s := &MongoStore{
		users:       db.Collection("users"),
		workspaces:  db.Collection("workspaces"),
		templates:   db.Collection("templates"),
		jobs:        db.Collection("jobs"),
		pkeys:       db.Collection("provisioner_keys"),
		apiTokens:   db.Collection("api_tokens"),
		config:      db.Collection("platform_config"),
		orgs:        db.Collection("organizations"),
		orgMembers:  db.Collection("org_members"),
		teams:       db.Collection("teams"),
		teamMembers: db.Collection("team_members"),
	}
	return s, s.ensureIndexes(ctx)
}

func mongoDBName(uri string) string {
	if i := strings.LastIndex(uri, "/"); i >= 0 && len(uri) > i+1 {
		rest := uri[i+1:]
		if j := strings.IndexByte(rest, '?'); j >= 0 {
			rest = rest[:j]
		}
		if rest != "" {
			return rest
		}
	}
	return "igloo"
}

func (s *MongoStore) ensureIndexes(ctx context.Context) error {
	type idxSpec struct {
		coll   *mongo.Collection
		keys   bson.D
		unique bool
	}
	specs := []idxSpec{
		{s.users, bson.D{{Key: "email", Value: 1}}, true},
		{s.workspaces, bson.D{{Key: "user_id", Value: 1}}, false},
		{s.jobs, bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}}, false},
		{s.jobs, bson.D{{Key: "workspace_id", Value: 1}}, false},
		{s.pkeys, bson.D{{Key: "token_hash", Value: 1}}, true},
		{s.orgs, bson.D{{Key: "slug", Value: 1}}, true},
		{s.orgMembers, bson.D{{Key: "org_id", Value: 1}, {Key: "user_id", Value: 1}}, true},
		{s.orgMembers, bson.D{{Key: "user_id", Value: 1}}, false},
		{s.teams, bson.D{{Key: "org_id", Value: 1}}, false},
		{s.teamMembers, bson.D{{Key: "team_id", Value: 1}, {Key: "user_id", Value: 1}}, true},
		{s.teamMembers, bson.D{{Key: "user_id", Value: 1}}, false},
	}
	for _, sp := range specs {
		opts := options.Index().SetBackground(true)
		if sp.unique {
			opts.SetUnique(true)
		}
		if _, err := sp.coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: sp.keys, Options: opts}); err != nil {
			return err
		}
	}
	return nil
}

// ─── Document types ───────────────────────────────────────────────────────────

type mUserDoc struct {
	ID               string    `bson:"_id"`
	Email            string    `bson:"email"`
	Name             string    `bson:"name"`
	PasswordHash     string    `bson:"password_hash"`
	TailscaleAuthKey string    `bson:"tailscale_auth_key"`
	IsAdmin          bool      `bson:"is_admin"`
	CreatedAt        time.Time `bson:"created_at"`
}

type mWorkspaceDoc struct {
	ID           string            `bson:"_id"`
	OrgID        string            `bson:"org_id,omitempty"`
	UserID       string            `bson:"user_id"`
	Name         string            `bson:"name"`
	TemplateID   string            `bson:"template_id"`
	TemplateName string            `bson:"template_name"`
	Provider     string            `bson:"provider"`
	Status       string            `bson:"status"`
	AgentToken   string            `bson:"agent_token"`
	CostPerHr    float64           `bson:"cost_per_hr"`
	CreatedAt    time.Time         `bson:"created_at"`
	UpdatedAt    time.Time         `bson:"updated_at"`
	LastActiveAt time.Time         `bson:"last_active_at"`
	Vars         map[string]string `bson:"vars"`
	Meta         map[string]string `bson:"meta"`
}

type mTemplateDoc struct {
	ID          string               `bson:"_id"`
	OrgID       string               `bson:"org_id,omitempty"`
	Name        string               `bson:"name"`
	Description string               `bson:"description"`
	Provider    string               `bson:"provider"`
	Icon        string               `bson:"icon"`
	Builtin     bool                 `bson:"builtin"`
	Version     int                  `bson:"version"`
	Variables   []models.TemplateVar `bson:"variables"`
	Files       map[string]string    `bson:"files"`
	CreatedAt   time.Time            `bson:"created_at"`
	UpdatedAt   time.Time            `bson:"updated_at"`
}

type mJobDoc struct {
	ID            string    `bson:"_id"`
	WorkspaceID   string    `bson:"workspace_id"`
	Action        string    `bson:"action"`
	Status        string    `bson:"status"`
	Error         string    `bson:"error"`
	RetryCount    int       `bson:"retry_count"`
	ProvisionerID string    `bson:"provisioner_id"`
	Heartbeat     time.Time `bson:"heartbeat"`
	CreatedAt     time.Time `bson:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at"`
}

type mKeyDoc struct {
	ID         string     `bson:"_id"`
	Name       string     `bson:"name"`
	CreatedBy  string     `bson:"created_by"`
	TokenHash  string     `bson:"token_hash"`
	CreatedAt  time.Time  `bson:"created_at"`
	LastUsedAt *time.Time `bson:"last_used_at,omitempty"`
}

type mConfigDoc struct {
	ID   string               `bson:"_id"`
	Data models.PlatformConfig `bson:"data"`
}

type mOrgDoc struct {
	ID              string            `bson:"_id"`
	Name            string            `bson:"name"`
	Slug            string            `bson:"slug"`
	Description     string            `bson:"description"`
	CreatedBy       string            `bson:"created_by"`
	CreatedAt       time.Time         `bson:"created_at"`
	GroupClaims     []string          `bson:"group_claims"`
	GroupMapping    map[string]string `bson:"group_mapping"`
	GroupAutoCreate bool              `bson:"group_auto_create"`
}

type mOrgMemberDoc struct {
	OrgID     string    `bson:"org_id"`
	UserID    string    `bson:"user_id"`
	UserEmail string    `bson:"user_email"`
	UserName  string    `bson:"user_name"`
	Role      string    `bson:"role"`
	JoinedAt  time.Time `bson:"joined_at"`
}

type mTeamDoc struct {
	ID          string    `bson:"_id"`
	OrgID       string    `bson:"org_id"`
	Name        string    `bson:"name"`
	Description string    `bson:"description"`
	CreatedBy   string    `bson:"created_by"`
	CreatedAt   time.Time `bson:"created_at"`
}

type mTeamMemberDoc struct {
	TeamID    string    `bson:"team_id"`
	UserID    string    `bson:"user_id"`
	UserEmail string    `bson:"user_email"`
	UserName  string    `bson:"user_name"`
	JoinedAt  time.Time `bson:"joined_at"`
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (s *MongoStore) CreateUser(ctx context.Context, u *models.User) error {
	doc := mUserDoc{
		ID: u.ID, Email: u.Email, Name: u.Name,
		PasswordHash: u.PasswordHash, TailscaleAuthKey: u.TailscaleAuthKey,
		IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt,
	}
	_, err := s.users.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	return err
}

func (s *MongoStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	var d mUserDoc
	err := s.users.FindOne(ctx, bson.M{"_id": id}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return mUserFromDoc(d), nil
}

func (s *MongoStore) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	var d mUserDoc
	err := s.users.FindOne(ctx, bson.M{"email": email}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return mUserFromDoc(d), nil
}

func (s *MongoStore) UpdateUser(ctx context.Context, u *models.User) error {
	doc := mUserDoc{
		ID: u.ID, Email: u.Email, Name: u.Name,
		PasswordHash: u.PasswordHash, TailscaleAuthKey: u.TailscaleAuthKey,
		IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt,
	}
	_, err := s.users.ReplaceOne(ctx, bson.M{"_id": u.ID}, doc)
	return err
}

func (s *MongoStore) ListUsers(ctx context.Context) ([]*models.User, error) {
	cur, err := s.users.Find(ctx, bson.M{"_id": bson.M{"$ne": "__platform__"}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.User
	for cur.Next(ctx) {
		var d mUserDoc
		if cur.Decode(&d) == nil {
			out = append(out, mUserFromDoc(d))
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) DeleteUser(ctx context.Context, id string) error {
	_, err := s.users.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func mUserFromDoc(d mUserDoc) *models.User {
	return &models.User{
		ID: d.ID, Email: d.Email, Name: d.Name,
		PasswordHash: d.PasswordHash, TailscaleAuthKey: d.TailscaleAuthKey,
		IsAdmin: d.IsAdmin, CreatedAt: d.CreatedAt,
	}
}

// ─── Workspaces ───────────────────────────────────────────────────────────────

func (s *MongoStore) CreateWorkspace(ctx context.Context, w *models.Workspace) error {
	_, err := s.workspaces.InsertOne(ctx, mWsToDoc(w))
	return err
}

func (s *MongoStore) GetWorkspace(ctx context.Context, id string) (*models.Workspace, error) {
	var d mWorkspaceDoc
	if err := s.workspaces.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mWsFromDoc(d), nil
}

func (s *MongoStore) ListWorkspaces(ctx context.Context, userID, orgID string) ([]*models.Workspace, error) {
	filter := bson.M{"status": bson.M{"$ne": "deleted"}}
	if userID != "" {
		filter["user_id"] = userID
	}
	if orgID != "" {
		filter["org_id"] = orgID
	}
	cur, err := s.workspaces.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.Workspace
	for cur.Next(ctx) {
		var d mWorkspaceDoc
		if cur.Decode(&d) == nil {
			out = append(out, mWsFromDoc(d))
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) UpdateWorkspace(ctx context.Context, w *models.Workspace) error {
	_, err := s.workspaces.ReplaceOne(ctx, bson.M{"_id": w.ID}, mWsToDoc(w))
	return err
}

func (s *MongoStore) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := s.workspaces.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func mWsToDoc(w *models.Workspace) mWorkspaceDoc {
	vars := w.Vars
	if vars == nil {
		vars = map[string]string{}
	}
	meta := w.Meta
	if meta == nil {
		meta = map[string]string{}
	}
	return mWorkspaceDoc{
		ID: w.ID, OrgID: w.OrgID, UserID: w.UserID, Name: w.Name,
		TemplateID: w.TemplateID, TemplateName: w.TemplateName, Provider: w.Provider,
		Status: string(w.Status), AgentToken: w.AgentToken, CostPerHr: w.CostPerHr,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, LastActiveAt: w.LastActiveAt,
		Vars: vars, Meta: meta,
	}
}

func mWsFromDoc(d mWorkspaceDoc) *models.Workspace {
	return &models.Workspace{
		ID: d.ID, OrgID: d.OrgID, UserID: d.UserID, Name: d.Name,
		TemplateID: d.TemplateID, TemplateName: d.TemplateName, Provider: d.Provider,
		Status: models.WorkspaceStatus(d.Status), AgentToken: d.AgentToken, CostPerHr: d.CostPerHr,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, LastActiveAt: d.LastActiveAt,
		Vars: d.Vars, Meta: d.Meta,
	}
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (s *MongoStore) UpsertTemplate(ctx context.Context, t *models.Template) error {
	vars := t.Variables
	if vars == nil {
		vars = []models.TemplateVar{}
	}
	files := t.Files
	if files == nil {
		files = map[string]string{}
	}
	doc := mTemplateDoc{
		ID: t.ID, Name: t.Name, Description: t.Description, Provider: t.Provider,
		Icon: t.Icon, Builtin: t.Builtin, Version: t.Version, Variables: vars, Files: files, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	_, err := s.templates.ReplaceOne(ctx, bson.M{"_id": t.ID}, doc,
		options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetTemplate(ctx context.Context, id string) (*models.Template, error) {
	var d mTemplateDoc
	if err := s.templates.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mTemplateFromDoc(d), nil
}

func (s *MongoStore) ListTemplates(ctx context.Context, orgID string) ([]*models.Template, error) {
	var filter bson.M
	if orgID != "" {
		filter = bson.M{"$or": []bson.M{{"builtin": true}, {"org_id": orgID}}}
	} else {
		filter = bson.M{"builtin": true}
	}
	cur, err := s.templates.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.Template
	for cur.Next(ctx) {
		var d mTemplateDoc
		if cur.Decode(&d) == nil {
			out = append(out, mTemplateFromDoc(d))
		}
	}
	return out, cur.Err()
}

func mTemplateFromDoc(d mTemplateDoc) *models.Template {
	return &models.Template{
		ID: d.ID, OrgID: d.OrgID, Name: d.Name, Description: d.Description, Provider: d.Provider,
		Icon: d.Icon, Builtin: d.Builtin, Version: d.Version, Variables: d.Variables, Files: d.Files, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func (s *MongoStore) DeleteTemplate(ctx context.Context, id string) error {
	_, err := s.templates.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// ─── Jobs ─────────────────────────────────────────────────────────────────────

func (s *MongoStore) CreateJob(ctx context.Context, j *models.ProvisionJob) error {
	_, err := s.jobs.InsertOne(ctx, mJobToDoc(j))
	return err
}

func (s *MongoStore) GetJob(ctx context.Context, id string) (*models.ProvisionJob, error) {
	var d mJobDoc
	if err := s.jobs.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mJobFromDoc(d), nil
}

func (s *MongoStore) ClaimNextJob(ctx context.Context, provisionerID string) (*models.ProvisionJob, error) {
	now := time.Now()
	var d mJobDoc
	err := s.jobs.FindOneAndUpdate(ctx,
		bson.M{"status": "pending"},
		bson.M{"$set": bson.M{
			"status":         "running",
			"provisioner_id": provisionerID,
			"updated_at":     now,
		}},
		options.FindOneAndUpdate().
			SetSort(bson.D{{Key: "created_at", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return mJobFromDoc(d), nil
}

func (s *MongoStore) UpdateJob(ctx context.Context, j *models.ProvisionJob) error {
	_, err := s.jobs.ReplaceOne(ctx, bson.M{"_id": j.ID}, mJobToDoc(j))
	return err
}

func (s *MongoStore) UpdateJobHeartbeat(ctx context.Context, jobID string) error {
	now := time.Now()
	_, err := s.jobs.UpdateOne(ctx, bson.M{"_id": jobID},
		bson.M{"$set": bson.M{"heartbeat": now, "updated_at": now}})
	return err
}

func (s *MongoStore) ListStaleJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoff := time.Now().Add(-olderThan)
	return s.listJobs(ctx, bson.M{
		"status":         "running",
		"provisioner_id": "",
		"updated_at":     bson.M{"$lt": cutoff},
	})
}

func (s *MongoStore) ListStaleDaemonJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoff := time.Now().Add(-olderThan)
	return s.listJobs(ctx, bson.M{
		"status":         "running",
		"provisioner_id": bson.M{"$ne": ""},
		"$or": bson.A{
			bson.M{"heartbeat": bson.M{"$exists": false}},
			bson.M{"heartbeat": bson.M{"$lt": cutoff}},
		},
	})
}

func (s *MongoStore) listJobs(ctx context.Context, filter bson.M) ([]*models.ProvisionJob, error) {
	cur, err := s.jobs.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.ProvisionJob
	for cur.Next(ctx) {
		var d mJobDoc
		if cur.Decode(&d) == nil {
			out = append(out, mJobFromDoc(d))
		}
	}
	return out, cur.Err()
}

func mJobToDoc(j *models.ProvisionJob) mJobDoc {
	return mJobDoc{
		ID: j.ID, WorkspaceID: j.WorkspaceID, Action: string(j.Action),
		Status: string(j.Status), Error: j.Error, RetryCount: j.RetryCount,
		ProvisionerID: j.ProvisionerID, Heartbeat: j.Heartbeat,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
	}
}

func mJobFromDoc(d mJobDoc) *models.ProvisionJob {
	return &models.ProvisionJob{
		ID: d.ID, WorkspaceID: d.WorkspaceID, Action: models.JobAction(d.Action),
		Status: models.JobStatus(d.Status), Error: d.Error, RetryCount: d.RetryCount,
		ProvisionerID: d.ProvisionerID, Heartbeat: d.Heartbeat,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// ─── Provisioner keys ─────────────────────────────────────────────────────────

func (s *MongoStore) CreateProvisionerKey(ctx context.Context, k *models.ProvisionerKey) error {
	doc := mKeyDoc{
		ID: k.ID, Name: k.Name, CreatedBy: k.CreatedBy,
		TokenHash: k.TokenHash, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
	}
	_, err := s.pkeys.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	return err
}

func (s *MongoStore) GetProvisionerKeyByTokenHash(ctx context.Context, hash string) (*models.ProvisionerKey, error) {
	var d mKeyDoc
	if err := s.pkeys.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &models.ProvisionerKey{
		ID: d.ID, Name: d.Name, CreatedBy: d.CreatedBy,
		TokenHash: d.TokenHash, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt,
	}, nil
}

func (s *MongoStore) ListProvisionerKeys(ctx context.Context) ([]*models.ProvisionerKey, error) {
	cur, err := s.pkeys.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.ProvisionerKey
	for cur.Next(ctx) {
		var d mKeyDoc
		if cur.Decode(&d) == nil {
			out = append(out, &models.ProvisionerKey{
				ID: d.ID, Name: d.Name, CreatedBy: d.CreatedBy,
				TokenHash: d.TokenHash, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt,
			})
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) DeleteProvisionerKey(ctx context.Context, id string) error {
	_, err := s.pkeys.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *MongoStore) TouchProvisionerKey(ctx context.Context, id string) error {
	now := time.Now()
	_, err := s.pkeys.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"last_used_at": now}})
	return err
}

// ─── Platform config ──────────────────────────────────────────────────────────

func (s *MongoStore) GetPlatformConfig(ctx context.Context) (*models.PlatformConfig, error) {
	var d mConfigDoc
	err := s.config.FindOne(ctx, bson.M{"_id": "singleton"}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return &models.PlatformConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &d.Data, nil
}

func (s *MongoStore) SavePlatformConfig(ctx context.Context, cfg *models.PlatformConfig) error {
	doc := mConfigDoc{ID: "singleton", Data: *cfg}
	_, err := s.config.ReplaceOne(ctx, bson.M{"_id": "singleton"}, doc,
		options.Replace().SetUpsert(true))
	return err
}

// ─── Organizations ────────────────────────────────────────────────────────────

func (s *MongoStore) CreateOrg(ctx context.Context, o *models.Organization) error {
	_, err := s.orgs.InsertOne(ctx, mOrgToDoc(o))
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	return err
}

func (s *MongoStore) GetOrg(ctx context.Context, id string) (*models.Organization, error) {
	var d mOrgDoc
	if err := s.orgs.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mOrgFromDoc(d), nil
}

func (s *MongoStore) GetOrgBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	var d mOrgDoc
	if err := s.orgs.FindOne(ctx, bson.M{"slug": slug}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mOrgFromDoc(d), nil
}

func (s *MongoStore) ListOrgs(ctx context.Context) ([]*models.Organization, error) {
	cur, err := s.orgs.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.Organization
	for cur.Next(ctx) {
		var d mOrgDoc
		if cur.Decode(&d) == nil {
			out = append(out, mOrgFromDoc(d))
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) UpdateOrg(ctx context.Context, o *models.Organization) error {
	_, err := s.orgs.ReplaceOne(ctx, bson.M{"_id": o.ID}, mOrgToDoc(o))
	return err
}

func (s *MongoStore) DeleteOrg(ctx context.Context, id string) error {
	_, err := s.orgs.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func mOrgToDoc(o *models.Organization) mOrgDoc {
	claims := o.GroupClaims
	if claims == nil {
		claims = []string{}
	}
	mapping := o.GroupMapping
	if mapping == nil {
		mapping = map[string]string{}
	}
	return mOrgDoc{
		ID: o.ID, Name: o.Name, Slug: o.Slug, Description: o.Description,
		CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt,
		GroupClaims: claims, GroupMapping: mapping, GroupAutoCreate: o.GroupAutoCreate,
	}
}

func mOrgFromDoc(d mOrgDoc) *models.Organization {
	return &models.Organization{
		ID: d.ID, Name: d.Name, Slug: d.Slug, Description: d.Description,
		CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt,
		GroupClaims: d.GroupClaims, GroupMapping: d.GroupMapping, GroupAutoCreate: d.GroupAutoCreate,
	}
}

// ─── Org members ──────────────────────────────────────────────────────────────

func (s *MongoStore) AddOrgMember(ctx context.Context, m *models.OrgMember) error {
	doc := mOrgMemberDoc{
		OrgID: m.OrgID, UserID: m.UserID, UserEmail: m.UserEmail,
		UserName: m.UserName, Role: string(m.Role), JoinedAt: m.JoinedAt,
	}
	_, err := s.orgMembers.ReplaceOne(ctx,
		bson.M{"org_id": m.OrgID, "user_id": m.UserID}, doc,
		options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) GetOrgMember(ctx context.Context, orgID, userID string) (*models.OrgMember, error) {
	var d mOrgMemberDoc
	if err := s.orgMembers.FindOne(ctx, bson.M{"org_id": orgID, "user_id": userID}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mOrgMemberFromDoc(d), nil
}

func (s *MongoStore) ListOrgMembers(ctx context.Context, orgID string) ([]*models.OrgMember, error) {
	return s.collectOrgMembers(ctx, bson.M{"org_id": orgID})
}

func (s *MongoStore) ListUserOrgs(ctx context.Context, userID string) ([]*models.OrgMember, error) {
	return s.collectOrgMembers(ctx, bson.M{"user_id": userID})
}

func (s *MongoStore) UpdateOrgMember(ctx context.Context, m *models.OrgMember) error {
	return s.AddOrgMember(ctx, m)
}

func (s *MongoStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	_, err := s.orgMembers.DeleteOne(ctx, bson.M{"org_id": orgID, "user_id": userID})
	return err
}

func (s *MongoStore) collectOrgMembers(ctx context.Context, filter bson.M) ([]*models.OrgMember, error) {
	cur, err := s.orgMembers.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.OrgMember
	for cur.Next(ctx) {
		var d mOrgMemberDoc
		if cur.Decode(&d) == nil {
			out = append(out, mOrgMemberFromDoc(d))
		}
	}
	return out, cur.Err()
}

func mOrgMemberFromDoc(d mOrgMemberDoc) *models.OrgMember {
	return &models.OrgMember{
		OrgID: d.OrgID, UserID: d.UserID, UserEmail: d.UserEmail,
		UserName: d.UserName, Role: models.OrgRole(d.Role), JoinedAt: d.JoinedAt,
	}
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func (s *MongoStore) CreateTeam(ctx context.Context, t *models.Team) error {
	doc := mTeamDoc{
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, Description: t.Description,
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
	}
	_, err := s.teams.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	return err
}

func (s *MongoStore) GetTeam(ctx context.Context, id string) (*models.Team, error) {
	var d mTeamDoc
	if err := s.teams.FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return mTeamFromDoc(d), nil
}

func (s *MongoStore) ListTeams(ctx context.Context, orgID string) ([]*models.Team, error) {
	cur, err := s.teams.Find(ctx, bson.M{"org_id": orgID},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.Team
	for cur.Next(ctx) {
		var d mTeamDoc
		if cur.Decode(&d) == nil {
			out = append(out, mTeamFromDoc(d))
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) UpdateTeam(ctx context.Context, t *models.Team) error {
	_, err := s.teams.UpdateOne(ctx, bson.M{"_id": t.ID},
		bson.M{"$set": bson.M{"name": t.Name, "description": t.Description}})
	return err
}

func (s *MongoStore) DeleteTeam(ctx context.Context, id string) error {
	_, err := s.teams.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func mTeamFromDoc(d mTeamDoc) *models.Team {
	return &models.Team{
		ID: d.ID, OrgID: d.OrgID, Name: d.Name, Description: d.Description,
		CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt,
	}
}

// ─── Team members ─────────────────────────────────────────────────────────────

func (s *MongoStore) AddTeamMember(ctx context.Context, m *models.TeamMember) error {
	doc := mTeamMemberDoc{
		TeamID: m.TeamID, UserID: m.UserID, UserEmail: m.UserEmail,
		UserName: m.UserName, JoinedAt: m.JoinedAt,
	}
	_, err := s.teamMembers.ReplaceOne(ctx,
		bson.M{"team_id": m.TeamID, "user_id": m.UserID}, doc,
		options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) ListTeamMembers(ctx context.Context, teamID string) ([]*models.TeamMember, error) {
	return s.collectTeamMembers(ctx, bson.M{"team_id": teamID})
}

func (s *MongoStore) ListUserTeams(ctx context.Context, userID string) ([]*models.TeamMember, error) {
	return s.collectTeamMembers(ctx, bson.M{"user_id": userID})
}

func (s *MongoStore) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	_, err := s.teamMembers.DeleteOne(ctx, bson.M{"team_id": teamID, "user_id": userID})
	return err
}

func (s *MongoStore) SyncTeamMembers(ctx context.Context, teamID string, userIDs []string) error {
	if _, err := s.teamMembers.DeleteMany(ctx, bson.M{"team_id": teamID}); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	now := time.Now()
	docs := make([]interface{}, len(userIDs))
	for i, uid := range userIDs {
		docs[i] = mTeamMemberDoc{TeamID: teamID, UserID: uid, JoinedAt: now}
	}
	_, err := s.teamMembers.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	return err
}

func (s *MongoStore) collectTeamMembers(ctx context.Context, filter bson.M) ([]*models.TeamMember, error) {
	cur, err := s.teamMembers.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.TeamMember
	for cur.Next(ctx) {
		var d mTeamMemberDoc
		if cur.Decode(&d) == nil {
			out = append(out, &models.TeamMember{
				TeamID: d.TeamID, UserID: d.UserID, UserEmail: d.UserEmail,
				UserName: d.UserName, JoinedAt: d.JoinedAt,
			})
		}
	}
	return out, cur.Err()
}

// ─── APITokens ────────────────────────────────────────────────────────────────

type mAPITokenDoc struct {
	ID          string    `bson:"_id"`
	UserID      string    `bson:"user_id"`
	TokenHash   string    `bson:"token_hash"`
	TokenPrefix string    `bson:"token_prefix"`
	Name        string    `bson:"name"`
	ExpiresAt   time.Time `bson:"expires_at"`
	LastUsedAt  time.Time `bson:"last_used_at"`
	CreatedAt   time.Time `bson:"created_at"`
	CreatedBy   string    `bson:"created_by"`
}

func (s *MongoStore) CreateAPIToken(ctx context.Context, t *models.APIToken) error {
	_, err := s.apiTokens.InsertOne(ctx, mAPITokenDoc{
		ID: t.ID, UserID: t.UserID, TokenHash: t.TokenHash, TokenPrefix: t.TokenPrefix,
		Name: t.Name, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy,
	})
	return err
}

func (s *MongoStore) GetAPITokenByHash(ctx context.Context, hash string) (*models.APIToken, error) {
	var d mAPITokenDoc
	if err := s.apiTokens.FindOne(ctx, bson.M{"token_hash": hash}).Decode(&d); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &models.APIToken{
		ID: d.ID, UserID: d.UserID, TokenHash: d.TokenHash, TokenPrefix: d.TokenPrefix,
		Name: d.Name, ExpiresAt: d.ExpiresAt, LastUsedAt: d.LastUsedAt,
		CreatedAt: d.CreatedAt, CreatedBy: d.CreatedBy,
	}, nil
}

func (s *MongoStore) ListAPITokens(ctx context.Context, userID string) ([]*models.APIToken, error) {
	cur, err := s.apiTokens.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*models.APIToken
	for cur.Next(ctx) {
		var d mAPITokenDoc
		if cur.Decode(&d) == nil {
			out = append(out, &models.APIToken{
				ID: d.ID, UserID: d.UserID, TokenPrefix: d.TokenPrefix,
				Name: d.Name, ExpiresAt: d.ExpiresAt, LastUsedAt: d.LastUsedAt,
				CreatedAt: d.CreatedAt, CreatedBy: d.CreatedBy,
			})
		}
	}
	return out, cur.Err()
}

func (s *MongoStore) DeleteAPIToken(ctx context.Context, id string) error {
	_, err := s.apiTokens.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *MongoStore) TouchAPIToken(ctx context.Context, id string) error {
	_, err := s.apiTokens.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"last_used_at": time.Now()}})
	return err
}
