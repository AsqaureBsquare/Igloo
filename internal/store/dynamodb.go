package store

// Single-table DynamoDB implementation.
// All entities live in one table (default name: "igloo") using pk/sk composite keys.
// Override the table name with IGLOO_DYNAMO_TABLE env var.
//
// Key schema:
//   USER#<id>             | USER#<id>               → user record
//   EMAIL#<email>         | EMAIL#<email>            → {ref_id} email → user ID lookup
//   WS#<id>               | WS#<id>                 → workspace record  (gsi1pk=USERWSS#<userID>)
//   TPL#<id>              | TPL#<id>                 → template record   (gsi1pk=BUILTIN or ORG#<orgID>)
//   JOB#<id>              | JOB#<id>                 → job record
//   PKEY#<hash>           | PKEY#<hash>              → provisioner key   (gsi1pk=PKEYS)
//   PKEYID#<id>           | PKEYID#<id>              → {ref_hash} id → hash lookup
//   TOKEN#<hash>          | TOKEN#<hash>             → api token         (gsi1pk=TOKENSBYUSER#<userID>)
//   TOKENID#<id>          | TOKENID#<id>             → {ref_hash} id → hash lookup
//   CONFIG                | CONFIG                   → platform config
//   ORG#<id>              | ORG#<id>                 → org record        (slug field → slug-index)
//   ORG#<orgID>           | MEMBER#<userID>          → org member        (gsi1pk=USERMEMBER#<userID>)
//   TEAM#<id>             | TEAM#<id>                → team record       (gsi1pk=ORGTEAM#<orgID>)
//   TEAM#<teamID>         | MEMBER#<userID>          → team member       (gsi1pk=USERMEMBER#<userID>)
//
// GSIs:
//   gsi1-index  (gsi1pk, gsi1sk) — workspace/template/key/token/org/team list queries
//   slug-index  (slug)            — org lookup by slug

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/igloo-dev/igloo/internal/models"
)

type DynamoStore struct {
	client *dynamodb.Client
	table  string
}

func NewDynamoStore(ctx context.Context, region string) (*DynamoStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	tbl := os.Getenv("IGLOO_DYNAMO_TABLE")
	if tbl == "" {
		tbl = "igloo"
	}
	return &DynamoStore{client: dynamodb.NewFromConfig(cfg), table: tbl}, nil
}

// ─── Key helpers ──────────────────────────────────────────────────────────────

func userPK(id string) string          { return "USER#" + id }
func emailPK(email string) string      { return "EMAIL#" + email }
func wsPK(id string) string            { return "WS#" + id }
func tplPK(id string) string           { return "TPL#" + id }
func jobPK(id string) string           { return "JOB#" + id }
func pkeyHashPK(hash string) string    { return "PKEY#" + hash }
func pkeyIDPK(id string) string        { return "PKEYID#" + id }
func tokenHashPK(hash string) string   { return "TOKEN#" + hash }
func tokenIDPK(id string) string       { return "TOKENID#" + id }
func orgPK(id string) string           { return "ORG#" + id }
func teamPK(id string) string          { return "TEAM#" + id }
func memberSK(userID string) string    { return "MEMBER#" + userID }

// ─── Item structs ─────────────────────────────────────────────────────────────

type ddbUser struct {
	PK               string    `dynamodbav:"pk"`
	SK               string    `dynamodbav:"sk"`
	Type             string    `dynamodbav:"type"`
	ID               string    `dynamodbav:"id"`
	Email            string    `dynamodbav:"email"`
	Name             string    `dynamodbav:"name"`
	PasswordHash     string    `dynamodbav:"password_hash"`
	TailscaleAuthKey string    `dynamodbav:"tailscale_auth_key"`
	IsAdmin          bool      `dynamodbav:"is_admin"`
	LoginType        string    `dynamodbav:"login_type"`
	CreatedAt        time.Time `dynamodbav:"created_at"`

	PasswordChangedAt time.Time `dynamodbav:"password_changed_at"`
}

type ddbRef struct {
	PK      string `dynamodbav:"pk"`
	SK      string `dynamodbav:"sk"`
	Type    string `dynamodbav:"type"`
	RefID   string `dynamodbav:"ref_id,omitempty"`   // email → user ID
	RefHash string `dynamodbav:"ref_hash,omitempty"` // id → token/key hash
}

type ddbWorkspace struct {
	PK           string            `dynamodbav:"pk"`
	SK           string            `dynamodbav:"sk"`
	GSI1PK       string            `dynamodbav:"gsi1pk"`
	GSI1SK       string            `dynamodbav:"gsi1sk"`
	Type         string            `dynamodbav:"type"`
	ID           string            `dynamodbav:"id"`
	OrgID        string            `dynamodbav:"org_id"`
	UserID       string            `dynamodbav:"user_id"`
	Name         string            `dynamodbav:"name"`
	TemplateID   string            `dynamodbav:"template_id"`
	TemplateName string            `dynamodbav:"template_name"`
	Provider     string            `dynamodbav:"provider"`
	Status       string            `dynamodbav:"status"`
	AgentToken   string            `dynamodbav:"agent_token"`
	CostPerHr    float64           `dynamodbav:"cost_per_hr"`
	CreatedAt    time.Time         `dynamodbav:"created_at"`
	UpdatedAt    time.Time         `dynamodbav:"updated_at"`
	LastActiveAt time.Time         `dynamodbav:"last_active_at"`
	Vars         map[string]string `dynamodbav:"vars"`
	Meta         map[string]string `dynamodbav:"meta"`
}

type ddbTemplate struct {
	PK          string               `dynamodbav:"pk"`
	SK          string               `dynamodbav:"sk"`
	GSI1PK      string               `dynamodbav:"gsi1pk"`
	GSI1SK      string               `dynamodbav:"gsi1sk"`
	Type        string               `dynamodbav:"type"`
	ID          string               `dynamodbav:"id"`
	OrgID       string               `dynamodbav:"org_id"`
	Name        string               `dynamodbav:"name"`
	Description string               `dynamodbav:"description"`
	Provider    string               `dynamodbav:"provider"`
	Icon        string               `dynamodbav:"icon"`
	Builtin     bool                 `dynamodbav:"builtin"`
	Version     int                  `dynamodbav:"version"`
	Variables   []models.TemplateVar `dynamodbav:"variables"`
	Files       map[string]string    `dynamodbav:"files"`
	CreatedAt   time.Time            `dynamodbav:"created_at"`
	UpdatedAt   time.Time            `dynamodbav:"updated_at"`
}

type ddbJob struct {
	PK            string    `dynamodbav:"pk"`
	SK            string    `dynamodbav:"sk"`
	Type          string    `dynamodbav:"type"`
	ID            string    `dynamodbav:"id"`
	WorkspaceID   string    `dynamodbav:"workspace_id"`
	Action        string    `dynamodbav:"action"`
	Status        string    `dynamodbav:"status"`
	ProvisionerID string    `dynamodbav:"provisioner_id"`
	RetryCount    int       `dynamodbav:"retry_count"`
	Error         string    `dynamodbav:"error"`
	CreatedAt     time.Time `dynamodbav:"created_at"`
	UpdatedAt     time.Time `dynamodbav:"updated_at"`
	Heartbeat     time.Time `dynamodbav:"heartbeat"`
}

type ddbProvisionerKey struct {
	PK         string     `dynamodbav:"pk"`
	SK         string     `dynamodbav:"sk"`
	GSI1PK     string     `dynamodbav:"gsi1pk"`
	GSI1SK     string     `dynamodbav:"gsi1sk"`
	Type       string     `dynamodbav:"type"`
	ID         string     `dynamodbav:"id"`
	Name       string     `dynamodbav:"name"`
	CreatedBy  string     `dynamodbav:"created_by"`
	TokenHash  string     `dynamodbav:"token_hash"`
	CreatedAt  time.Time  `dynamodbav:"created_at"`
	LastUsedAt *time.Time `dynamodbav:"last_used_at"`
}

type ddbAPIToken struct {
	PK          string    `dynamodbav:"pk"`
	SK          string    `dynamodbav:"sk"`
	GSI1PK      string    `dynamodbav:"gsi1pk"`
	GSI1SK      string    `dynamodbav:"gsi1sk"`
	Type        string    `dynamodbav:"type"`
	ID          string    `dynamodbav:"id"`
	UserID      string    `dynamodbav:"user_id"`
	TokenHash   string    `dynamodbav:"token_hash"`
	TokenPrefix string    `dynamodbav:"token_prefix"`
	Name        string    `dynamodbav:"name"`
	ExpiresAt   time.Time `dynamodbav:"expires_at"`
	LastUsedAt  time.Time `dynamodbav:"last_used_at"`
	CreatedAt   time.Time `dynamodbav:"created_at"`
	CreatedBy   string    `dynamodbav:"created_by"`
}

type ddbConfig struct {
	PK   string `dynamodbav:"pk"`
	SK   string `dynamodbav:"sk"`
	Type string `dynamodbav:"type"`
	Data string `dynamodbav:"data"`
}

type ddbOrg struct {
	PK              string            `dynamodbav:"pk"`
	SK              string            `dynamodbav:"sk"`
	Slug            string            `dynamodbav:"slug"`
	Type            string            `dynamodbav:"type"`
	ID              string            `dynamodbav:"id"`
	Name            string            `dynamodbav:"name"`
	Description     string            `dynamodbav:"description"`
	IsDefault       bool              `dynamodbav:"is_default"`
	CreatedBy       string            `dynamodbav:"created_by"`
	CreatedAt       time.Time         `dynamodbav:"created_at"`
	GroupClaims     []string          `dynamodbav:"group_claims"`
	GroupMapping    map[string]string `dynamodbav:"group_mapping"`
	GroupAutoCreate bool              `dynamodbav:"group_auto_create"`
	RoleMapping     map[string]string `dynamodbav:"role_mapping"`
}

type ddbOrgMember struct {
	PK        string    `dynamodbav:"pk"`
	SK        string    `dynamodbav:"sk"`
	GSI1PK    string    `dynamodbav:"gsi1pk"`
	GSI1SK    string    `dynamodbav:"gsi1sk"`
	Type      string    `dynamodbav:"type"`
	OrgID     string    `dynamodbav:"org_id"`
	UserID    string    `dynamodbav:"user_id"`
	UserEmail string    `dynamodbav:"user_email"`
	UserName  string    `dynamodbav:"user_name"`
	Role      string    `dynamodbav:"role"`
	JoinedAt  time.Time `dynamodbav:"joined_at"`
}

type ddbTeam struct {
	PK          string    `dynamodbav:"pk"`
	SK          string    `dynamodbav:"sk"`
	GSI1PK      string    `dynamodbav:"gsi1pk"`
	GSI1SK      string    `dynamodbav:"gsi1sk"`
	Type        string    `dynamodbav:"type"`
	ID          string    `dynamodbav:"id"`
	OrgID       string    `dynamodbav:"org_id"`
	Name        string    `dynamodbav:"name"`
	Description string    `dynamodbav:"description"`
	CreatedBy   string    `dynamodbav:"created_by"`
	CreatedAt   time.Time `dynamodbav:"created_at"`
}

type ddbTeamMember struct {
	PK        string    `dynamodbav:"pk"`
	SK        string    `dynamodbav:"sk"`
	GSI1PK    string    `dynamodbav:"gsi1pk"`
	GSI1SK    string    `dynamodbav:"gsi1sk"`
	Type      string    `dynamodbav:"type"`
	TeamID    string    `dynamodbav:"team_id"`
	UserID    string    `dynamodbav:"user_id"`
	UserEmail string    `dynamodbav:"user_email"`
	UserName  string    `dynamodbav:"user_name"`
	JoinedAt  time.Time `dynamodbav:"joined_at"`
}

// ─── Low-level helpers ────────────────────────────────────────────────────────

func (d *DynamoStore) put(ctx context.Context, item any) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: &d.table, Item: av})
	return err
}

func (d *DynamoStore) putNew(ctx context.Context, item any) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &d.table,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (d *DynamoStore) get(ctx context.Context, pk, sk string, out any) error {
	pkAV, _ := attributevalue.Marshal(pk)
	skAV, _ := attributevalue.Marshal(sk)
	res, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.table,
		Key:       map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
	})
	if err != nil {
		return err
	}
	if res.Item == nil {
		return ErrNotFound
	}
	return attributevalue.UnmarshalMap(res.Item, out)
}

func (d *DynamoStore) del(ctx context.Context, pk, sk string) error {
	pkAV, _ := attributevalue.Marshal(pk)
	skAV, _ := attributevalue.Marshal(sk)
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.table,
		Key:       map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
	})
	return err
}

func (d *DynamoStore) queryGSI1(ctx context.Context, gsi1pk string) ([]map[string]types.AttributeValue, error) {
	pkAV, _ := attributevalue.Marshal(gsi1pk)
	var out []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		res, err := d.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              &d.table,
			IndexName:              aws.String("gsi1-index"),
			KeyConditionExpression: aws.String("gsi1pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": pkAV},
			ExclusiveStartKey:      lastKey,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Items...)
		if res.LastEvaluatedKey == nil {
			break
		}
		lastKey = res.LastEvaluatedKey
	}
	return out, nil
}

func (d *DynamoStore) queryGSI1Prefix(ctx context.Context, gsi1pk, skPrefix string) ([]map[string]types.AttributeValue, error) {
	pkAV, _ := attributevalue.Marshal(gsi1pk)
	prefAV, _ := attributevalue.Marshal(skPrefix)
	var out []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		res, err := d.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              &d.table,
			IndexName:              aws.String("gsi1-index"),
			KeyConditionExpression: aws.String("gsi1pk = :pk AND begins_with(gsi1sk, :prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": pkAV, ":prefix": prefAV},
			ExclusiveStartKey:      lastKey,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Items...)
		if res.LastEvaluatedKey == nil {
			break
		}
		lastKey = res.LastEvaluatedKey
	}
	return out, nil
}

func (d *DynamoStore) scanByType(ctx context.Context, typ string) ([]map[string]types.AttributeValue, error) {
	typAV, _ := attributevalue.Marshal(typ)
	var out []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		res, err := d.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 &d.table,
			FilterExpression:          aws.String("#t = :typ"),
			ExpressionAttributeNames:  map[string]string{"#t": "type"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":typ": typAV},
			ExclusiveStartKey:         lastKey,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Items...)
		if res.LastEvaluatedKey == nil {
			break
		}
		lastKey = res.LastEvaluatedKey
	}
	return out, nil
}

// ─── Users ────────────────────────────────────────────────────────────────────

func userToItem(u *models.User) ddbUser {
	return ddbUser{
		PK: userPK(u.ID), SK: userPK(u.ID), Type: "user",
		ID: u.ID, Email: u.Email, Name: u.Name,
		PasswordHash: u.PasswordHash, TailscaleAuthKey: u.TailscaleAuthKey,
		IsAdmin: u.IsAdmin, LoginType: u.LoginType, CreatedAt: u.CreatedAt, PasswordChangedAt: u.PasswordChangedAt,
	}
}

func itemToUser(i ddbUser) *models.User {
	return &models.User{
		ID: i.ID, Email: i.Email, Name: i.Name,
		PasswordHash: i.PasswordHash, TailscaleAuthKey: i.TailscaleAuthKey,
		IsAdmin: i.IsAdmin, LoginType: i.LoginType, CreatedAt: i.CreatedAt, PasswordChangedAt: i.PasswordChangedAt,
	}
}

func (d *DynamoStore) CreateUser(ctx context.Context, u *models.User) error {
	if _, err := d.GetUserByEmail(ctx, u.Email); err == nil {
		return ErrConflict
	}
	if err := d.putNew(ctx, userToItem(u)); err != nil {
		return err
	}
	return d.put(ctx, ddbRef{PK: emailPK(u.Email), SK: emailPK(u.Email), Type: "email_ref", RefID: u.ID})
}

func (d *DynamoStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	var i ddbUser
	if err := d.get(ctx, userPK(id), userPK(id), &i); err != nil {
		return nil, err
	}
	return itemToUser(i), nil
}

func (d *DynamoStore) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	var ref ddbRef
	if err := d.get(ctx, emailPK(email), emailPK(email), &ref); err != nil {
		return nil, err
	}
	return d.GetUser(ctx, ref.RefID)
}

func (d *DynamoStore) UpdateUser(ctx context.Context, u *models.User) error {
	return d.put(ctx, userToItem(u))
}

func (d *DynamoStore) ListUsers(ctx context.Context) ([]*models.User, error) {
	items, err := d.scanByType(ctx, "user")
	if err != nil {
		return nil, err
	}
	var out []*models.User
	for _, av := range items {
		var i ddbUser
		if err := attributevalue.UnmarshalMap(av, &i); err == nil && i.ID != "__platform__" {
			out = append(out, itemToUser(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) DeleteUser(ctx context.Context, id string) error {
	u, err := d.GetUser(ctx, id)
	if err != nil {
		return err
	}
	_ = d.del(ctx, emailPK(u.Email), emailPK(u.Email))
	return d.del(ctx, userPK(id), userPK(id))
}

// ─── Workspaces ───────────────────────────────────────────────────────────────

func wsToItem(w *models.Workspace) ddbWorkspace {
	if w.Vars == nil {
		w.Vars = map[string]string{}
	}
	if w.Meta == nil {
		w.Meta = map[string]string{}
	}
	return ddbWorkspace{
		PK: wsPK(w.ID), SK: wsPK(w.ID),
		GSI1PK: "USERWSS#" + w.UserID, GSI1SK: wsPK(w.ID),
		Type: "workspace",
		ID: w.ID, OrgID: w.OrgID, UserID: w.UserID, Name: w.Name,
		TemplateID: w.TemplateID, TemplateName: w.TemplateName,
		Provider: w.Provider, Status: string(w.Status), AgentToken: w.AgentToken,
		CostPerHr: w.CostPerHr, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		LastActiveAt: w.LastActiveAt, Vars: w.Vars, Meta: w.Meta,
	}
}

func itemToWS(i ddbWorkspace) *models.Workspace {
	if i.Vars == nil {
		i.Vars = map[string]string{}
	}
	if i.Meta == nil {
		i.Meta = map[string]string{}
	}
	return &models.Workspace{
		ID: i.ID, OrgID: i.OrgID, UserID: i.UserID, Name: i.Name,
		TemplateID: i.TemplateID, TemplateName: i.TemplateName,
		Provider: i.Provider, Status: models.WorkspaceStatus(i.Status), AgentToken: i.AgentToken,
		CostPerHr: i.CostPerHr, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		LastActiveAt: i.LastActiveAt, Vars: i.Vars, Meta: i.Meta,
	}
}

func (d *DynamoStore) CreateWorkspace(ctx context.Context, w *models.Workspace) error {
	return d.put(ctx, wsToItem(w))
}

func (d *DynamoStore) GetWorkspace(ctx context.Context, id string) (*models.Workspace, error) {
	var i ddbWorkspace
	if err := d.get(ctx, wsPK(id), wsPK(id), &i); err != nil {
		return nil, err
	}
	return itemToWS(i), nil
}

func (d *DynamoStore) ListWorkspaces(ctx context.Context, userID, orgID string) ([]*models.Workspace, error) {
	var avItems []map[string]types.AttributeValue
	var err error
	if userID == "" {
		avItems, err = d.scanByType(ctx, "workspace")
	} else {
		avItems, err = d.queryGSI1(ctx, "USERWSS#"+userID)
	}
	if err != nil {
		return nil, err
	}
	var out []*models.Workspace
	for _, av := range avItems {
		var i ddbWorkspace
		if err := attributevalue.UnmarshalMap(av, &i); err != nil {
			continue
		}
		if i.Status == string(models.StatusDeleted) {
			continue
		}
		if orgID != "" && i.OrgID != orgID {
			continue
		}
		out = append(out, itemToWS(i))
	}
	return out, nil
}

func (d *DynamoStore) UpdateWorkspace(ctx context.Context, w *models.Workspace) error {
	return d.put(ctx, wsToItem(w))
}

func (d *DynamoStore) DeleteWorkspace(ctx context.Context, id string) error {
	return d.del(ctx, wsPK(id), wsPK(id))
}

// ─── Templates ────────────────────────────────────────────────────────────────

func tplToItem(t *models.Template) ddbTemplate {
	gsi1pk := "ORG#" + t.OrgID
	if t.Builtin || t.OrgID == "" {
		gsi1pk = "BUILTIN"
	}
	return ddbTemplate{
		PK: tplPK(t.ID), SK: tplPK(t.ID),
		GSI1PK: gsi1pk, GSI1SK: tplPK(t.ID),
		Type: "template",
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, Description: t.Description,
		Provider: t.Provider, Icon: t.Icon, Builtin: t.Builtin, Version: t.Version,
		Variables: t.Variables, Files: t.Files, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func itemToTpl(i ddbTemplate) *models.Template {
	return &models.Template{
		ID: i.ID, OrgID: i.OrgID, Name: i.Name, Description: i.Description,
		Provider: i.Provider, Icon: i.Icon, Builtin: i.Builtin, Version: i.Version,
		Variables: i.Variables, Files: i.Files, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

func (d *DynamoStore) UpsertTemplate(ctx context.Context, t *models.Template) error {
	return d.put(ctx, tplToItem(t))
}

func (d *DynamoStore) GetTemplate(ctx context.Context, id string) (*models.Template, error) {
	var i ddbTemplate
	if err := d.get(ctx, tplPK(id), tplPK(id), &i); err != nil {
		return nil, err
	}
	return itemToTpl(i), nil
}

func (d *DynamoStore) ListTemplates(ctx context.Context, orgID string) ([]*models.Template, error) {
	builtins, err := d.queryGSI1(ctx, "BUILTIN")
	if err != nil {
		return nil, err
	}
	avItems := builtins
	if orgID != "" {
		orgTpls, err := d.queryGSI1(ctx, "ORG#"+orgID)
		if err != nil {
			return nil, err
		}
		avItems = append(avItems, orgTpls...)
	}
	var out []*models.Template
	for _, av := range avItems {
		var i ddbTemplate
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToTpl(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) DeleteTemplate(ctx context.Context, id string) error {
	return d.del(ctx, tplPK(id), tplPK(id))
}

// ─── Jobs ─────────────────────────────────────────────────────────────────────

func jobToItem(j *models.ProvisionJob) ddbJob {
	return ddbJob{
		PK: jobPK(j.ID), SK: jobPK(j.ID), Type: "job",
		ID: j.ID, WorkspaceID: j.WorkspaceID,
		Action: string(j.Action), Status: string(j.Status),
		ProvisionerID: j.ProvisionerID, RetryCount: j.RetryCount, Error: j.Error,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, Heartbeat: j.Heartbeat,
	}
}

func itemToJob(i ddbJob) *models.ProvisionJob {
	return &models.ProvisionJob{
		ID: i.ID, WorkspaceID: i.WorkspaceID,
		Action: models.JobAction(i.Action), Status: models.JobStatus(i.Status),
		ProvisionerID: i.ProvisionerID, RetryCount: i.RetryCount, Error: i.Error,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, Heartbeat: i.Heartbeat,
	}
}

func (d *DynamoStore) CreateJob(ctx context.Context, j *models.ProvisionJob) error {
	return d.put(ctx, jobToItem(j))
}

func (d *DynamoStore) GetJob(ctx context.Context, id string) (*models.ProvisionJob, error) {
	var i ddbJob
	if err := d.get(ctx, jobPK(id), jobPK(id), &i); err != nil {
		return nil, err
	}
	return itemToJob(i), nil
}

// ClaimNextJob scans for pending jobs, sorts by created_at, then uses a
// conditional update for optimistic locking — safe under concurrent provisioners.
func (d *DynamoStore) ClaimNextJob(ctx context.Context, provisionerID string) (*models.ProvisionJob, error) {
	pendingAV, _ := attributevalue.Marshal("pending")
	typAV, _ := attributevalue.Marshal("job")
	var avItems []map[string]types.AttributeValue
	var lastKey map[string]types.AttributeValue
	for {
		res, err := d.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 &d.table,
			FilterExpression:          aws.String("#t = :typ AND #s = :status"),
			ExpressionAttributeNames:  map[string]string{"#t": "type", "#s": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":typ": typAV, ":status": pendingAV},
			ExclusiveStartKey:         lastKey,
		})
		if err != nil {
			return nil, err
		}
		avItems = append(avItems, res.Items...)
		if res.LastEvaluatedKey == nil {
			break
		}
		lastKey = res.LastEvaluatedKey
	}
	if len(avItems) == 0 {
		return nil, nil
	}

	var jobs []ddbJob
	for _, av := range avItems {
		var j ddbJob
		if err := attributevalue.UnmarshalMap(av, &j); err == nil {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].CreatedAt.Before(jobs[k].CreatedAt) })

	runningAV, _ := attributevalue.Marshal("running")
	pidAV, _ := attributevalue.Marshal(provisionerID)
	nowAV, _ := attributevalue.Marshal(time.Now())
	condPendingAV, _ := attributevalue.Marshal("pending")

	for _, j := range jobs {
		pkAV, _ := attributevalue.Marshal(jobPK(j.ID))
		skAV, _ := attributevalue.Marshal(jobPK(j.ID))
		_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:           &d.table,
			Key:                 map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
			UpdateExpression:    aws.String("SET #s = :running, provisioner_id = :pid, updated_at = :now"),
			ConditionExpression: aws.String("#s = :pending"),
			ExpressionAttributeNames:  map[string]string{"#s": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":running": runningAV, ":pid": pidAV,
				":now": nowAV, ":pending": condPendingAV,
			},
		})
		if err != nil {
			var cce *types.ConditionalCheckFailedException
			if errors.As(err, &cce) {
				continue // another provisioner claimed it first
			}
			return nil, err
		}
		j.Status = "running"
		j.ProvisionerID = provisionerID
		j.UpdatedAt = time.Now()
		return itemToJob(j), nil
	}
	return nil, nil
}

func (d *DynamoStore) UpdateJob(ctx context.Context, j *models.ProvisionJob) error {
	return d.put(ctx, jobToItem(j))
}

func (d *DynamoStore) UpdateJobHeartbeat(ctx context.Context, jobID string) error {
	pkAV, _ := attributevalue.Marshal(jobPK(jobID))
	skAV, _ := attributevalue.Marshal(jobPK(jobID))
	nowAV, _ := attributevalue.Marshal(time.Now())
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &d.table,
		Key:              map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
		UpdateExpression: aws.String("SET heartbeat = :now, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":now": nowAV},
	})
	return err
}

func (d *DynamoStore) ListStaleJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoffAV, _ := attributevalue.Marshal(time.Now().Add(-olderThan))
	runningAV, _ := attributevalue.Marshal("running")
	emptyAV, _ := attributevalue.Marshal("")
	return d.scanJobs(ctx,
		"#t = :typ AND #s = :running AND provisioner_id = :empty AND updated_at < :cutoff",
		map[string]string{"#t": "type", "#s": "status"},
		map[string]types.AttributeValue{
			":typ": mustMarshal("job"), ":running": runningAV,
			":empty": emptyAV, ":cutoff": cutoffAV,
		},
	)
}

func (d *DynamoStore) ListStaleDaemonJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	cutoffAV, _ := attributevalue.Marshal(time.Now().Add(-olderThan))
	runningAV, _ := attributevalue.Marshal("running")
	emptyAV, _ := attributevalue.Marshal("")
	return d.scanJobs(ctx,
		"#t = :typ AND #s = :running AND provisioner_id <> :empty AND (attribute_not_exists(heartbeat) OR heartbeat < :cutoff)",
		map[string]string{"#t": "type", "#s": "status"},
		map[string]types.AttributeValue{
			":typ": mustMarshal("job"), ":running": runningAV,
			":empty": emptyAV, ":cutoff": cutoffAV,
		},
	)
}

func (d *DynamoStore) scanJobs(ctx context.Context, filter string, names map[string]string, vals map[string]types.AttributeValue) ([]*models.ProvisionJob, error) {
	var out []*models.ProvisionJob
	var lastKey map[string]types.AttributeValue
	for {
		res, err := d.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 &d.table,
			FilterExpression:          aws.String(filter),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: vals,
			ExclusiveStartKey:         lastKey,
		})
		if err != nil {
			return nil, err
		}
		for _, av := range res.Items {
			var j ddbJob
			if err := attributevalue.UnmarshalMap(av, &j); err == nil {
				out = append(out, itemToJob(j))
			}
		}
		if res.LastEvaluatedKey == nil {
			break
		}
		lastKey = res.LastEvaluatedKey
	}
	return out, nil
}

func mustMarshal(v any) types.AttributeValue {
	av, _ := attributevalue.Marshal(v)
	return av
}

// ─── Provisioner Keys ─────────────────────────────────────────────────────────

func (d *DynamoStore) CreateProvisionerKey(ctx context.Context, k *models.ProvisionerKey) error {
	item := ddbProvisionerKey{
		PK: pkeyHashPK(k.TokenHash), SK: pkeyHashPK(k.TokenHash),
		GSI1PK: "PKEYS", GSI1SK: pkeyHashPK(k.TokenHash),
		Type: "pkey",
		ID: k.ID, Name: k.Name, CreatedBy: k.CreatedBy,
		TokenHash: k.TokenHash, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt,
	}
	if err := d.putNew(ctx, item); err != nil {
		return err
	}
	return d.put(ctx, ddbRef{PK: pkeyIDPK(k.ID), SK: pkeyIDPK(k.ID), Type: "pkey_ref", RefHash: k.TokenHash})
}

func (d *DynamoStore) GetProvisionerKeyByTokenHash(ctx context.Context, hash string) (*models.ProvisionerKey, error) {
	var i ddbProvisionerKey
	if err := d.get(ctx, pkeyHashPK(hash), pkeyHashPK(hash), &i); err != nil {
		return nil, err
	}
	return &models.ProvisionerKey{
		ID: i.ID, Name: i.Name, CreatedBy: i.CreatedBy,
		TokenHash: i.TokenHash, CreatedAt: i.CreatedAt, LastUsedAt: i.LastUsedAt,
	}, nil
}

func (d *DynamoStore) ListProvisionerKeys(ctx context.Context) ([]*models.ProvisionerKey, error) {
	avItems, err := d.queryGSI1(ctx, "PKEYS")
	if err != nil {
		return nil, err
	}
	var out []*models.ProvisionerKey
	for _, av := range avItems {
		var i ddbProvisionerKey
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, &models.ProvisionerKey{
				ID: i.ID, Name: i.Name, CreatedBy: i.CreatedBy,
				TokenHash: i.TokenHash, CreatedAt: i.CreatedAt, LastUsedAt: i.LastUsedAt,
			})
		}
	}
	return out, nil
}

func (d *DynamoStore) DeleteProvisionerKey(ctx context.Context, id string) error {
	var ref ddbRef
	if err := d.get(ctx, pkeyIDPK(id), pkeyIDPK(id), &ref); err != nil {
		return err
	}
	_ = d.del(ctx, pkeyHashPK(ref.RefHash), pkeyHashPK(ref.RefHash))
	return d.del(ctx, pkeyIDPK(id), pkeyIDPK(id))
}

func (d *DynamoStore) TouchProvisionerKey(ctx context.Context, id string) error {
	var ref ddbRef
	if err := d.get(ctx, pkeyIDPK(id), pkeyIDPK(id), &ref); err != nil {
		return err
	}
	pkAV, _ := attributevalue.Marshal(pkeyHashPK(ref.RefHash))
	skAV, _ := attributevalue.Marshal(pkeyHashPK(ref.RefHash))
	nowAV, _ := attributevalue.Marshal(time.Now())
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &d.table,
		Key:              map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
		UpdateExpression: aws.String("SET last_used_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":now": nowAV},
	})
	return err
}

// ─── API Tokens ───────────────────────────────────────────────────────────────

func tokenToItem(t *models.APIToken) ddbAPIToken {
	return ddbAPIToken{
		PK: tokenHashPK(t.TokenHash), SK: tokenHashPK(t.TokenHash),
		GSI1PK: "TOKENSBYUSER#" + t.UserID, GSI1SK: tokenHashPK(t.TokenHash),
		Type: "api_token",
		ID: t.ID, UserID: t.UserID, TokenHash: t.TokenHash, TokenPrefix: t.TokenPrefix,
		Name: t.Name, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
		CreatedAt: t.CreatedAt, CreatedBy: t.CreatedBy,
	}
}

func itemToAPIToken(i ddbAPIToken) *models.APIToken {
	return &models.APIToken{
		ID: i.ID, UserID: i.UserID, TokenHash: i.TokenHash, TokenPrefix: i.TokenPrefix,
		Name: i.Name, ExpiresAt: i.ExpiresAt, LastUsedAt: i.LastUsedAt,
		CreatedAt: i.CreatedAt, CreatedBy: i.CreatedBy,
	}
}

func (d *DynamoStore) CreateAPIToken(ctx context.Context, t *models.APIToken) error {
	if err := d.put(ctx, tokenToItem(t)); err != nil {
		return err
	}
	return d.put(ctx, ddbRef{PK: tokenIDPK(t.ID), SK: tokenIDPK(t.ID), Type: "token_ref", RefHash: t.TokenHash})
}

func (d *DynamoStore) GetAPITokenByHash(ctx context.Context, hash string) (*models.APIToken, error) {
	var i ddbAPIToken
	if err := d.get(ctx, tokenHashPK(hash), tokenHashPK(hash), &i); err != nil {
		return nil, err
	}
	return itemToAPIToken(i), nil
}

func (d *DynamoStore) ListAPITokens(ctx context.Context, userID string) ([]*models.APIToken, error) {
	avItems, err := d.queryGSI1(ctx, "TOKENSBYUSER#"+userID)
	if err != nil {
		return nil, err
	}
	var out []*models.APIToken
	for _, av := range avItems {
		var i ddbAPIToken
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToAPIToken(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) DeleteAPIToken(ctx context.Context, id string) error {
	var ref ddbRef
	if err := d.get(ctx, tokenIDPK(id), tokenIDPK(id), &ref); err != nil {
		return err
	}
	_ = d.del(ctx, tokenHashPK(ref.RefHash), tokenHashPK(ref.RefHash))
	return d.del(ctx, tokenIDPK(id), tokenIDPK(id))
}

func (d *DynamoStore) TouchAPIToken(ctx context.Context, id string) error {
	var ref ddbRef
	if err := d.get(ctx, tokenIDPK(id), tokenIDPK(id), &ref); err != nil {
		return err
	}
	pkAV, _ := attributevalue.Marshal(tokenHashPK(ref.RefHash))
	skAV, _ := attributevalue.Marshal(tokenHashPK(ref.RefHash))
	nowAV, _ := attributevalue.Marshal(time.Now())
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &d.table,
		Key:              map[string]types.AttributeValue{"pk": pkAV, "sk": skAV},
		UpdateExpression: aws.String("SET last_used_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":now": nowAV},
	})
	return err
}

// ─── Platform Config ──────────────────────────────────────────────────────────

func (d *DynamoStore) GetPlatformConfig(ctx context.Context) (*models.PlatformConfig, error) {
	var i ddbConfig
	if err := d.get(ctx, "CONFIG", "CONFIG", &i); err != nil {
		if err == ErrNotFound {
			return &models.PlatformConfig{}, nil
		}
		return nil, err
	}
	var cfg models.PlatformConfig
	if err := json.Unmarshal([]byte(i.Data), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (d *DynamoStore) SavePlatformConfig(ctx context.Context, cfg *models.PlatformConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return d.put(ctx, ddbConfig{PK: "CONFIG", SK: "CONFIG", Type: "config", Data: string(data)})
}

// ─── Organizations ────────────────────────────────────────────────────────────

func orgToItem(o *models.Organization) ddbOrg {
	rm := make(map[string]string, len(o.RoleMapping))
	for k, v := range o.RoleMapping {
		rm[k] = string(v)
	}
	return ddbOrg{
		PK: orgPK(o.ID), SK: orgPK(o.ID), Slug: o.Slug, Type: "org",
		ID: o.ID, Name: o.Name, Description: o.Description, IsDefault: o.IsDefault,
		CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt,
		GroupClaims: o.GroupClaims, GroupMapping: o.GroupMapping,
		GroupAutoCreate: o.GroupAutoCreate, RoleMapping: rm,
	}
}

func itemToOrg(i ddbOrg) *models.Organization {
	rm := make(map[string]models.OrgRole, len(i.RoleMapping))
	for k, v := range i.RoleMapping {
		rm[k] = models.OrgRole(v)
	}
	return &models.Organization{
		ID: i.ID, Name: i.Name, Slug: i.Slug, Description: i.Description,
		IsDefault: i.IsDefault, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt,
		GroupClaims: i.GroupClaims, GroupMapping: i.GroupMapping,
		GroupAutoCreate: i.GroupAutoCreate, RoleMapping: rm,
	}
}

func (d *DynamoStore) CreateOrg(ctx context.Context, o *models.Organization) error {
	if _, err := d.GetOrgBySlug(ctx, o.Slug); err == nil {
		return ErrConflict
	}
	return d.putNew(ctx, orgToItem(o))
}

func (d *DynamoStore) GetOrg(ctx context.Context, id string) (*models.Organization, error) {
	var i ddbOrg
	if err := d.get(ctx, orgPK(id), orgPK(id), &i); err != nil {
		return nil, err
	}
	return itemToOrg(i), nil
}

func (d *DynamoStore) GetOrgBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	slugAV, _ := attributevalue.Marshal(slug)
	res, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.table,
		IndexName:              aws.String("slug-index"),
		KeyConditionExpression: aws.String("#slug = :slug"),
		ExpressionAttributeNames:  map[string]string{"#slug": "slug"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":slug": slugAV},
		Limit:                  aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(res.Items) == 0 {
		return nil, ErrNotFound
	}
	var i ddbOrg
	if err := attributevalue.UnmarshalMap(res.Items[0], &i); err != nil {
		return nil, err
	}
	return itemToOrg(i), nil
}

func (d *DynamoStore) ListOrgs(ctx context.Context) ([]*models.Organization, error) {
	avItems, err := d.scanByType(ctx, "org")
	if err != nil {
		return nil, err
	}
	var out []*models.Organization
	for _, av := range avItems {
		var i ddbOrg
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToOrg(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) UpdateOrg(ctx context.Context, o *models.Organization) error {
	return d.put(ctx, orgToItem(o))
}

func (d *DynamoStore) DeleteOrg(ctx context.Context, id string) error {
	return d.del(ctx, orgPK(id), orgPK(id))
}

// ─── Org Members ──────────────────────────────────────────────────────────────

func orgMemberToItem(m *models.OrgMember) ddbOrgMember {
	return ddbOrgMember{
		PK: orgPK(m.OrgID), SK: memberSK(m.UserID),
		GSI1PK: "USERMEMBER#" + m.UserID, GSI1SK: orgPK(m.OrgID),
		Type:      "org_member",
		OrgID: m.OrgID, UserID: m.UserID, UserEmail: m.UserEmail,
		UserName: m.UserName, Role: string(m.Role), JoinedAt: m.JoinedAt,
	}
}

func itemToOrgMember(i ddbOrgMember) *models.OrgMember {
	return &models.OrgMember{
		OrgID: i.OrgID, UserID: i.UserID, UserEmail: i.UserEmail,
		UserName: i.UserName, Role: models.OrgRole(i.Role), JoinedAt: i.JoinedAt,
	}
}

func (d *DynamoStore) AddOrgMember(ctx context.Context, m *models.OrgMember) error {
	return d.put(ctx, orgMemberToItem(m))
}

func (d *DynamoStore) GetOrgMember(ctx context.Context, orgID, userID string) (*models.OrgMember, error) {
	var i ddbOrgMember
	if err := d.get(ctx, orgPK(orgID), memberSK(userID), &i); err != nil {
		return nil, err
	}
	return itemToOrgMember(i), nil
}

func (d *DynamoStore) ListOrgMembers(ctx context.Context, orgID string) ([]*models.OrgMember, error) {
	pkAV, _ := attributevalue.Marshal(orgPK(orgID))
	prefAV, _ := attributevalue.Marshal("MEMBER#")
	res, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.table,
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": pkAV, ":prefix": prefAV},
	})
	if err != nil {
		return nil, err
	}
	var out []*models.OrgMember
	for _, av := range res.Items {
		var i ddbOrgMember
		if err := attributevalue.UnmarshalMap(av, &i); err == nil && i.Type == "org_member" {
			out = append(out, itemToOrgMember(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) ListUserOrgs(ctx context.Context, userID string) ([]*models.OrgMember, error) {
	avItems, err := d.queryGSI1Prefix(ctx, "USERMEMBER#"+userID, "ORG#")
	if err != nil {
		return nil, err
	}
	var out []*models.OrgMember
	for _, av := range avItems {
		var i ddbOrgMember
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToOrgMember(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) UpdateOrgMember(ctx context.Context, m *models.OrgMember) error {
	return d.AddOrgMember(ctx, m)
}

func (d *DynamoStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	return d.del(ctx, orgPK(orgID), memberSK(userID))
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func teamToItem(t *models.Team) ddbTeam {
	return ddbTeam{
		PK: teamPK(t.ID), SK: teamPK(t.ID),
		GSI1PK: "ORGTEAM#" + t.OrgID, GSI1SK: teamPK(t.ID),
		Type: "team",
		ID: t.ID, OrgID: t.OrgID, Name: t.Name, Description: t.Description,
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
	}
}

func itemToTeam(i ddbTeam) *models.Team {
	return &models.Team{
		ID: i.ID, OrgID: i.OrgID, Name: i.Name, Description: i.Description,
		CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt,
	}
}

func (d *DynamoStore) CreateTeam(ctx context.Context, t *models.Team) error {
	return d.putNew(ctx, teamToItem(t))
}

func (d *DynamoStore) GetTeam(ctx context.Context, id string) (*models.Team, error) {
	var i ddbTeam
	if err := d.get(ctx, teamPK(id), teamPK(id), &i); err != nil {
		return nil, err
	}
	return itemToTeam(i), nil
}

func (d *DynamoStore) ListTeams(ctx context.Context, orgID string) ([]*models.Team, error) {
	avItems, err := d.queryGSI1(ctx, "ORGTEAM#"+orgID)
	if err != nil {
		return nil, err
	}
	var out []*models.Team
	for _, av := range avItems {
		var i ddbTeam
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToTeam(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) UpdateTeam(ctx context.Context, t *models.Team) error {
	return d.put(ctx, teamToItem(t))
}

func (d *DynamoStore) DeleteTeam(ctx context.Context, id string) error {
	return d.del(ctx, teamPK(id), teamPK(id))
}

// ─── Team Members ─────────────────────────────────────────────────────────────

func teamMemberToItem(m *models.TeamMember) ddbTeamMember {
	return ddbTeamMember{
		PK: teamPK(m.TeamID), SK: memberSK(m.UserID),
		GSI1PK: "USERMEMBER#" + m.UserID, GSI1SK: teamPK(m.TeamID),
		Type:   "team_member",
		TeamID: m.TeamID, UserID: m.UserID, UserEmail: m.UserEmail,
		UserName: m.UserName, JoinedAt: m.JoinedAt,
	}
}

func itemToTeamMember(i ddbTeamMember) *models.TeamMember {
	return &models.TeamMember{
		TeamID: i.TeamID, UserID: i.UserID, UserEmail: i.UserEmail,
		UserName: i.UserName, JoinedAt: i.JoinedAt,
	}
}

func (d *DynamoStore) AddTeamMember(ctx context.Context, m *models.TeamMember) error {
	return d.put(ctx, teamMemberToItem(m))
}

func (d *DynamoStore) ListTeamMembers(ctx context.Context, teamID string) ([]*models.TeamMember, error) {
	pkAV, _ := attributevalue.Marshal(teamPK(teamID))
	prefAV, _ := attributevalue.Marshal("MEMBER#")
	res, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.table,
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":pk": pkAV, ":prefix": prefAV},
	})
	if err != nil {
		return nil, err
	}
	var out []*models.TeamMember
	for _, av := range res.Items {
		var i ddbTeamMember
		if err := attributevalue.UnmarshalMap(av, &i); err == nil && i.Type == "team_member" {
			out = append(out, itemToTeamMember(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) ListUserTeams(ctx context.Context, userID string) ([]*models.TeamMember, error) {
	avItems, err := d.queryGSI1Prefix(ctx, "USERMEMBER#"+userID, "TEAM#")
	if err != nil {
		return nil, err
	}
	var out []*models.TeamMember
	for _, av := range avItems {
		var i ddbTeamMember
		if err := attributevalue.UnmarshalMap(av, &i); err == nil {
			out = append(out, itemToTeamMember(i))
		}
	}
	return out, nil
}

func (d *DynamoStore) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	return d.del(ctx, teamPK(teamID), memberSK(userID))
}

func (d *DynamoStore) SyncTeamMembers(ctx context.Context, teamID string, userIDs []string) error {
	current, err := d.ListTeamMembers(ctx, teamID)
	if err != nil {
		return err
	}
	for _, m := range current {
		if err := d.RemoveTeamMember(ctx, teamID, m.UserID); err != nil {
			return err
		}
	}
	now := time.Now()
	for _, uid := range userIDs {
		if err := d.AddTeamMember(ctx, &models.TeamMember{TeamID: teamID, UserID: uid, JoinedAt: now}); err != nil {
			return err
		}
	}
	return nil
}
