package store

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/igloo-dev/igloo/internal/models"
)

type DynamoStore struct {
	client          *dynamodb.Client
	users           string
	workspaces      string
	templates       string
	jobs            string
	provisionerKeys string
	entities        string
}

func NewDynamoStore(ctx context.Context, region string) (*DynamoStore, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return &DynamoStore{
		client:          dynamodb.NewFromConfig(cfg),
		users:           "igloo-users",
		workspaces:      "igloo-workspaces",
		templates:       "igloo-templates",
		jobs:            "igloo-ws-jobs",
		provisionerKeys: "igloo-provisioner-keys",
		entities:        "igloo-entities",
	}, nil
}

// ─── DynamoDB item types (include fields hidden from JSON) ────────────────────

type userItem struct {
	ID           string    `dynamodbav:"id"`
	Email        string    `dynamodbav:"email"`
	Name         string    `dynamodbav:"name"`
	PasswordHash string    `dynamodbav:"password_hash"`
	SSHPublicKey     string `dynamodbav:"ssh_public_key"`
	TailscaleAuthKey string `dynamodbav:"tailscale_auth_key"`
	IsAdmin      bool      `dynamodbav:"is_admin"`
	CreatedAt    time.Time `dynamodbav:"created_at"`
}

type workspaceItem struct {
	ID           string            `dynamodbav:"id"`
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

type templateItem struct {
	ID          string               `dynamodbav:"id"`
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

func userToItem(u *models.User) userItem {
	return userItem{ID: u.ID, Email: u.Email, Name: u.Name, PasswordHash: u.PasswordHash, SSHPublicKey: u.SSHPublicKey, TailscaleAuthKey: u.TailscaleAuthKey, IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt}
}
func itemToUser(i userItem) *models.User {
	return &models.User{ID: i.ID, Email: i.Email, Name: i.Name, PasswordHash: i.PasswordHash, SSHPublicKey: i.SSHPublicKey, TailscaleAuthKey: i.TailscaleAuthKey, IsAdmin: i.IsAdmin, CreatedAt: i.CreatedAt}
}

func wsToItem(ws *models.Workspace) workspaceItem {
	return workspaceItem{
		ID: ws.ID, UserID: ws.UserID, Name: ws.Name,
		TemplateID: ws.TemplateID, TemplateName: ws.TemplateName,
		Provider: ws.Provider, Status: string(ws.Status), AgentToken: ws.AgentToken,
		CostPerHr: ws.CostPerHr, CreatedAt: ws.CreatedAt, UpdatedAt: ws.UpdatedAt,
		LastActiveAt: ws.LastActiveAt, Vars: ws.Vars, Meta: ws.Meta,
	}
}
func itemToWS(i workspaceItem) *models.Workspace {
	meta := i.Meta
	if meta == nil {
		meta = make(map[string]string)
	}
	vars := i.Vars
	if vars == nil {
		vars = make(map[string]string)
	}
	return &models.Workspace{
		ID: i.ID, UserID: i.UserID, Name: i.Name,
		TemplateID: i.TemplateID, TemplateName: i.TemplateName,
		Provider: i.Provider, Status: models.WorkspaceStatus(i.Status), AgentToken: i.AgentToken,
		CostPerHr: i.CostPerHr, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		LastActiveAt: i.LastActiveAt, Vars: vars, Meta: meta,
	}
}

func tplToItem(t *models.Template) templateItem {
	return templateItem{ID: t.ID, Name: t.Name, Description: t.Description, Provider: t.Provider, Icon: t.Icon, Builtin: t.Builtin, Version: t.Version, Variables: t.Variables, Files: t.Files, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}
func itemToTpl(i templateItem) *models.Template {
	return &models.Template{ID: i.ID, Name: i.Name, Description: i.Description, Provider: i.Provider, Icon: i.Icon, Builtin: i.Builtin, Version: i.Version, Variables: i.Variables, Files: i.Files, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
}

// ─── Users ────────────────────────────────────────────────────────────────────

func (d *DynamoStore) CreateUser(ctx context.Context, u *models.User) error {
	if _, err := d.GetUserByEmail(ctx, u.Email); err == nil {
		return ErrConflict
	}
	item, err := attributevalue.MarshalMap(userToItem(u))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &d.users,
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	if err != nil {
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return ErrConflict
		}
	}
	return err
}

func (d *DynamoStore) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.users,
		IndexName:              aws.String("email-index"),
		KeyConditionExpression: aws.String("email = :e"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":e": &types.AttributeValueMemberS{Value: email},
		},
		Limit: aws.Int32(1),
	})
	if err != nil || len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	var i userItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &i); err != nil {
		return nil, err
	}
	return itemToUser(i), nil
}

func (d *DynamoStore) GetUser(ctx context.Context, id string) (*models.User, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.users,
		Key:       strKey("id", id),
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i userItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return itemToUser(i), nil
}

func (d *DynamoStore) UpdateUser(ctx context.Context, u *models.User) error {
	item, err := attributevalue.MarshalMap(userToItem(u))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.users,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) ListUsers(ctx context.Context) ([]*models.User, error) {
	out, err := d.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        &d.users,
		FilterExpression: aws.String("id <> :platform"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":platform": &types.AttributeValueMemberS{Value: "__platform__"},
		},
	})
	if err != nil {
		return nil, err
	}
	users := make([]*models.User, 0, len(out.Items))
	for _, item := range out.Items {
		var i userItem
		if err := attributevalue.UnmarshalMap(item, &i); err == nil {
			users = append(users, itemToUser(i))
		}
	}
	return users, nil
}

func (d *DynamoStore) DeleteUser(ctx context.Context, id string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.users,
		Key:       strKey("id", id),
	})
	return err
}

// ─── Workspaces ───────────────────────────────────────────────────────────────

func (d *DynamoStore) CreateWorkspace(ctx context.Context, ws *models.Workspace) error {
	item, err := attributevalue.MarshalMap(wsToItem(ws))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &d.workspaces,
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	})
	return err
}

func (d *DynamoStore) GetWorkspace(ctx context.Context, id string) (*models.Workspace, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.workspaces,
		Key:       strKey("id", id),
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i workspaceItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return itemToWS(i), nil
}

func (d *DynamoStore) ListWorkspaces(ctx context.Context, userID string) ([]*models.Workspace, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.workspaces,
		IndexName:              aws.String("user-index"),
		KeyConditionExpression: aws.String("user_id = :u"),
		FilterExpression:       aws.String("#s <> :deleted"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":u":       &types.AttributeValueMemberS{Value: userID},
			":deleted": &types.AttributeValueMemberS{Value: "deleted"},
		},
	})
	if err != nil {
		return nil, err
	}
	wss := make([]*models.Workspace, 0, len(out.Items))
	for _, item := range out.Items {
		var i workspaceItem
		if err := attributevalue.UnmarshalMap(item, &i); err == nil {
			wss = append(wss, itemToWS(i))
		}
	}
	return wss, nil
}

func (d *DynamoStore) UpdateWorkspace(ctx context.Context, ws *models.Workspace) error {
	ws.UpdatedAt = time.Now()
	item, err := attributevalue.MarshalMap(wsToItem(ws))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.workspaces,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.workspaces,
		Key:       strKey("id", id),
	})
	return err
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (d *DynamoStore) UpsertTemplate(ctx context.Context, t *models.Template) error {
	item, err := attributevalue.MarshalMap(tplToItem(t))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.templates,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) GetTemplate(ctx context.Context, id string) (*models.Template, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.templates,
		Key:       strKey("id", id),
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i templateItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return itemToTpl(i), nil
}

func (d *DynamoStore) ListTemplates(ctx context.Context) ([]*models.Template, error) {
	out, err := d.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: &d.templates,
	})
	if err != nil {
		return nil, err
	}
	templates := make([]*models.Template, 0, len(out.Items))
	for _, item := range out.Items {
		var i templateItem
		if err := attributevalue.UnmarshalMap(item, &i); err == nil {
			templates = append(templates, itemToTpl(i))
		}
	}
	return templates, nil
}

func (d *DynamoStore) DeleteTemplate(ctx context.Context, id string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.templates,
		Key:       strKey("id", id),
	})
	return err
}

func strKey(name, value string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		name: &types.AttributeValueMemberS{Value: value},
	}
}

// ─── Jobs ─────────────────────────────────────────────────────────────────────

type jobItem struct {
	ID            string    `dynamodbav:"id"`
	WorkspaceID   string    `dynamodbav:"workspace_id"`
	Action        string    `dynamodbav:"action"`
	Status        string    `dynamodbav:"status"`
	Error         string    `dynamodbav:"error"`
	RetryCount    int       `dynamodbav:"retry_count"`
	ProvisionerID string    `dynamodbav:"provisioner_id"`
	Heartbeat     time.Time `dynamodbav:"heartbeat"`
	CreatedAt     time.Time `dynamodbav:"created_at"`
	UpdatedAt     time.Time `dynamodbav:"updated_at"`
	TTL           int64     `dynamodbav:"ttl"`
}

func jobToItem(j *models.ProvisionJob) jobItem {
	return jobItem{
		ID: j.ID, WorkspaceID: j.WorkspaceID,
		Action: string(j.Action), Status: string(j.Status),
		Error: j.Error, RetryCount: j.RetryCount,
		ProvisionerID: j.ProvisionerID, Heartbeat: j.Heartbeat,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, TTL: j.TTL,
	}
}

func itemToJob(i jobItem) *models.ProvisionJob {
	return &models.ProvisionJob{
		ID: i.ID, WorkspaceID: i.WorkspaceID,
		Action: models.JobAction(i.Action), Status: models.JobStatus(i.Status),
		Error: i.Error, RetryCount: i.RetryCount,
		ProvisionerID: i.ProvisionerID, Heartbeat: i.Heartbeat,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, TTL: i.TTL,
	}
}

func (d *DynamoStore) CreateJob(ctx context.Context, j *models.ProvisionJob) error {
	item, err := attributevalue.MarshalMap(jobToItem(j))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.jobs,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) GetJob(ctx context.Context, id string) (*models.ProvisionJob, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.jobs,
		Key:       strKey("id", id),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, ErrNotFound
	}
	var i jobItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return itemToJob(i), nil
}

func (d *DynamoStore) ClaimNextJob(ctx context.Context, provisionerID string) (*models.ProvisionJob, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.jobs,
		IndexName:              aws.String("status-created_at-index"),
		KeyConditionExpression: aws.String("#s = :pending"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": &types.AttributeValueMemberS{Value: "pending"},
		},
		ScanIndexForward: aws.Bool(true),
		Limit:            aws.Int32(5),
	})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	nowVal, _ := attributevalue.Marshal(now)
	pidVal := &types.AttributeValueMemberS{Value: provisionerID}
	for _, rawItem := range out.Items {
		var i jobItem
		if err := attributevalue.UnmarshalMap(rawItem, &i); err != nil {
			continue
		}
		_, err = d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:           &d.jobs,
			Key:                 strKey("id", i.ID),
			UpdateExpression:    aws.String("SET #s = :running, updated_at = :now, provisioner_id = :pid, heartbeat = :now"),
			ConditionExpression: aws.String("#s = :pending"),
			ExpressionAttributeNames: map[string]string{"#s": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":running": &types.AttributeValueMemberS{Value: "running"},
				":pending": &types.AttributeValueMemberS{Value: "pending"},
				":now":     nowVal,
				":pid":     pidVal,
			},
		})
		if err != nil {
			var cce *types.ConditionalCheckFailedException
			if errors.As(err, &cce) {
				continue
			}
			return nil, err
		}
		job := itemToJob(i)
		job.Status = models.JobRunning
		job.UpdatedAt = now
		job.ProvisionerID = provisionerID
		job.Heartbeat = now
		return job, nil
	}
	return nil, nil
}

func (d *DynamoStore) UpdateJobHeartbeat(ctx context.Context, jobID string) error {
	now := time.Now()
	nowVal, _ := attributevalue.Marshal(now)
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &d.jobs,
		Key:              strKey("id", jobID),
		UpdateExpression: aws.String("SET heartbeat = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": nowVal,
		},
	})
	return err
}

func (d *DynamoStore) UpdateJob(ctx context.Context, j *models.ProvisionJob) error {
	j.UpdatedAt = time.Now()
	item, err := attributevalue.MarshalMap(jobToItem(j))
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.jobs,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) ListStaleJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.jobs,
		IndexName:              aws.String("status-created_at-index"),
		KeyConditionExpression: aws.String("#s = :running"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":running": &types.AttributeValueMemberS{Value: "running"},
		},
	})
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-olderThan)
	var stale []*models.ProvisionJob
	for _, rawItem := range out.Items {
		var i jobItem
		if err := attributevalue.UnmarshalMap(rawItem, &i); err != nil {
			continue
		}
		if i.UpdatedAt.Before(cutoff) {
			stale = append(stale, itemToJob(i))
		}
	}
	return stale, nil
}

func (d *DynamoStore) ListStaleDaemonJobs(ctx context.Context, olderThan time.Duration) ([]*models.ProvisionJob, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.jobs,
		IndexName:              aws.String("status-created_at-index"),
		KeyConditionExpression: aws.String("#s = :running"),
		ExpressionAttributeNames:  map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":running": &types.AttributeValueMemberS{Value: "running"},
		},
	})
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-olderThan)
	var stale []*models.ProvisionJob
	for _, rawItem := range out.Items {
		var i jobItem
		if err := attributevalue.UnmarshalMap(rawItem, &i); err != nil {
			continue
		}
		// Only daemon-claimed jobs have a non-zero Heartbeat
		if !i.Heartbeat.IsZero() && i.ProvisionerID != "embedded" && i.Heartbeat.Before(cutoff) {
			stale = append(stale, itemToJob(i))
		}
	}
	return stale, nil
}

// ─── ProvisionerKeys ──────────────────────────────────────────────────────────

type provisionerKeyItem struct {
	ID         string     `dynamodbav:"id"`
	Name       string     `dynamodbav:"name"`
	Tags       map[string]string `dynamodbav:"tags,omitempty"`
	TokenHash  string     `dynamodbav:"token_hash"`
	CreatedBy  string     `dynamodbav:"created_by"`
	CreatedAt  time.Time  `dynamodbav:"created_at"`
	LastUsedAt *time.Time `dynamodbav:"last_used_at,omitempty"`
}

func keyItemToModel(item provisionerKeyItem) *models.ProvisionerKey {
	return &models.ProvisionerKey{
		ID:         item.ID,
		Name:       item.Name,
		Tags:       item.Tags,
		TokenHash:  item.TokenHash,
		CreatedBy:  item.CreatedBy,
		CreatedAt:  item.CreatedAt,
		LastUsedAt: item.LastUsedAt,
	}
}

func (d *DynamoStore) CreateProvisionerKey(ctx context.Context, k *models.ProvisionerKey) error {
	item, err := attributevalue.MarshalMap(provisionerKeyItem{
		ID:        k.ID,
		Name:      k.Name,
		Tags:      k.Tags,
		TokenHash: k.TokenHash,
		CreatedBy: k.CreatedBy,
		CreatedAt: k.CreatedAt,
	})
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.provisionerKeys),
		Item:      item,
	})
	return err
}

func (d *DynamoStore) GetProvisionerKeyByTokenHash(ctx context.Context, hash string) (*models.ProvisionerKey, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(d.provisionerKeys),
		IndexName:              aws.String("token-hash-index"),
		KeyConditionExpression: aws.String("token_hash = :h"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":h": &types.AttributeValueMemberS{Value: hash},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	var item provisionerKeyItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &item); err != nil {
		return nil, err
	}
	return keyItemToModel(item), nil
}

func (d *DynamoStore) ListProvisionerKeys(ctx context.Context) ([]*models.ProvisionerKey, error) {
	out, err := d.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(d.provisionerKeys),
	})
	if err != nil {
		return nil, err
	}
	keys := make([]*models.ProvisionerKey, 0, len(out.Items))
	for _, raw := range out.Items {
		var item provisionerKeyItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			continue
		}
		keys = append(keys, keyItemToModel(item))
	}
	return keys, nil
}

func (d *DynamoStore) DeleteProvisionerKey(ctx context.Context, id string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.provisionerKeys),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
	})
	return err
}

// ─── PlatformConfig ───────────────────────────────────────────────────────────
// Stored as a single item in the users table with the reserved ID "__platform__".

type platformItem struct {
	ID               string `dynamodbav:"id"`
	TailscaleAPIKey  string `dynamodbav:"tailscale_api_key"`
	TailscaleTailnet string `dynamodbav:"tailscale_tailnet"`
}

func (d *DynamoStore) GetPlatformConfig(ctx context.Context) (*models.PlatformConfig, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.users,
		Key:       strKey("id", "__platform__"),
	})
	if err != nil {
		return &models.PlatformConfig{}, nil
	}
	if out.Item == nil {
		return &models.PlatformConfig{}, nil
	}
	var item platformItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return &models.PlatformConfig{}, nil
	}
	return &models.PlatformConfig{
		TailscaleAPIKey:  item.TailscaleAPIKey,
		TailscaleTailnet: item.TailscaleTailnet,
	}, nil
}

func (d *DynamoStore) SavePlatformConfig(ctx context.Context, cfg *models.PlatformConfig) error {
	item, err := attributevalue.MarshalMap(platformItem{
		ID:               "__platform__",
		TailscaleAPIKey:  cfg.TailscaleAPIKey,
		TailscaleTailnet: cfg.TailscaleTailnet,
	})
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &d.users,
		Item:      item,
	})
	return err
}

func (d *DynamoStore) TouchProvisionerKey(ctx context.Context, id string) error {
	now := time.Now()
	nowStr := now.Format(time.RFC3339)
	_, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(d.provisionerKeys),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		UpdateExpression: aws.String("SET last_used_at = :t"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": &types.AttributeValueMemberS{Value: nowStr},
		},
	})
	return err
}

// ─── Organizations & Teams (single-table: igloo-entities) ────────────────────
// pk/sk key patterns — see dynamodb.tf for full scheme.

type entityItem struct {
	PK              string            `dynamodbav:"pk"`
	SK              string            `dynamodbav:"sk"`
	Slug            string            `dynamodbav:"slug,omitempty"`
	GSI1PK          string            `dynamodbav:"gsi1pk,omitempty"`
	GSI1SK          string            `dynamodbav:"gsi1sk,omitempty"`
	ID              string            `dynamodbav:"id,omitempty"`
	OrgID           string            `dynamodbav:"org_id,omitempty"`
	Name            string            `dynamodbav:"name,omitempty"`
	Description     string            `dynamodbav:"description,omitempty"`
	CreatedBy       string            `dynamodbav:"created_by,omitempty"`
	CreatedAt       time.Time         `dynamodbav:"created_at"`
	GroupClaims     []string          `dynamodbav:"group_claims,omitempty"`
	GroupMapping    map[string]string `dynamodbav:"group_mapping,omitempty"`
	GroupAutoCreate bool              `dynamodbav:"group_auto_create"`
	UserID          string            `dynamodbav:"user_id,omitempty"`
	UserEmail       string            `dynamodbav:"user_email,omitempty"`
	UserName        string            `dynamodbav:"user_name,omitempty"`
	Role            string            `dynamodbav:"role,omitempty"`
	JoinedAt        time.Time         `dynamodbav:"joined_at"`
}

func (d *DynamoStore) putEntity(ctx context.Context, item entityItem) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: &d.entities, Item: av})
	return err
}

func (d *DynamoStore) deleteEntity(ctx context.Context, pk, sk string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &d.entities,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
			"sk": &types.AttributeValueMemberS{Value: sk},
		},
	})
	return err
}

// ─── Organizations ────────────────────────────────────────────────────────────

func (d *DynamoStore) CreateOrg(ctx context.Context, o *models.Organization) error {
	av, err := attributevalue.MarshalMap(entityItem{
		PK: "ORG#" + o.ID, SK: "METADATA", Slug: o.Slug,
		ID: o.ID, Name: o.Name, Description: o.Description,
		CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt,
		GroupClaims: o.GroupClaims, GroupMapping: o.GroupMapping, GroupAutoCreate: o.GroupAutoCreate,
	})
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &d.entities,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return ErrConflict
		}
	}
	return err
}

func (d *DynamoStore) GetOrg(ctx context.Context, id string) (*models.Organization, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.entities,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "ORG#" + id},
			"sk": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i entityItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return &models.Organization{ID: i.ID, Name: i.Name, Slug: i.Slug, Description: i.Description, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt, GroupClaims: i.GroupClaims, GroupMapping: i.GroupMapping, GroupAutoCreate: i.GroupAutoCreate}, nil
}

func (d *DynamoStore) GetOrgBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		IndexName:              aws.String("slug-index"),
		KeyConditionExpression: aws.String("slug = :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: slug},
		},
		Limit: aws.Int32(1),
	})
	if err != nil || len(out.Items) == 0 {
		return nil, ErrNotFound
	}
	var i entityItem
	if err := attributevalue.UnmarshalMap(out.Items[0], &i); err != nil {
		return nil, err
	}
	return &models.Organization{ID: i.ID, Name: i.Name, Slug: i.Slug, Description: i.Description, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt, GroupClaims: i.GroupClaims, GroupMapping: i.GroupMapping, GroupAutoCreate: i.GroupAutoCreate}, nil
}

func (d *DynamoStore) ListOrgs(ctx context.Context) ([]*models.Organization, error) {
	out, err := d.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        &d.entities,
		FilterExpression: aws.String("sk = :meta AND begins_with(pk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":meta": &types.AttributeValueMemberS{Value: "METADATA"},
			":pfx":  &types.AttributeValueMemberS{Value: "ORG#"},
		},
	})
	if err != nil {
		return nil, err
	}
	orgs := make([]*models.Organization, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			orgs = append(orgs, &models.Organization{ID: i.ID, Name: i.Name, Slug: i.Slug, Description: i.Description, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt})
		}
	}
	return orgs, nil
}

func (d *DynamoStore) UpdateOrg(ctx context.Context, o *models.Organization) error {
	return d.putEntity(ctx, entityItem{
		PK: "ORG#" + o.ID, SK: "METADATA", Slug: o.Slug,
		ID: o.ID, Name: o.Name, Description: o.Description,
		CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt,
		GroupClaims: o.GroupClaims, GroupMapping: o.GroupMapping, GroupAutoCreate: o.GroupAutoCreate,
	})
}

func (d *DynamoStore) DeleteOrg(ctx context.Context, id string) error {
	return d.deleteEntity(ctx, "ORG#"+id, "METADATA")
}

// ─── Org members ──────────────────────────────────────────────────────────────

func (d *DynamoStore) AddOrgMember(ctx context.Context, m *models.OrgMember) error {
	return d.putEntity(ctx, entityItem{
		PK: "ORG#" + m.OrgID, SK: "MEMBER#" + m.UserID,
		GSI1PK: "USER#" + m.UserID, GSI1SK: "ORG#" + m.OrgID,
		OrgID: m.OrgID, UserID: m.UserID, UserEmail: m.UserEmail,
		UserName: m.UserName, Role: string(m.Role), JoinedAt: m.JoinedAt,
	})
}

func (d *DynamoStore) GetOrgMember(ctx context.Context, orgID, userID string) (*models.OrgMember, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.entities,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "ORG#" + orgID},
			"sk": &types.AttributeValueMemberS{Value: "MEMBER#" + userID},
		},
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i entityItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return &models.OrgMember{OrgID: i.OrgID, UserID: i.UserID, UserEmail: i.UserEmail, UserName: i.UserName, Role: models.OrgRole(i.Role), JoinedAt: i.JoinedAt}, nil
}

func (d *DynamoStore) ListOrgMembers(ctx context.Context, orgID string) ([]*models.OrgMember, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":  &types.AttributeValueMemberS{Value: "ORG#" + orgID},
			":pfx": &types.AttributeValueMemberS{Value: "MEMBER#"},
		},
	})
	if err != nil {
		return nil, err
	}
	out2 := make([]*models.OrgMember, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			out2 = append(out2, &models.OrgMember{OrgID: i.OrgID, UserID: i.UserID, UserEmail: i.UserEmail, UserName: i.UserName, Role: models.OrgRole(i.Role), JoinedAt: i.JoinedAt})
		}
	}
	return out2, nil
}

func (d *DynamoStore) ListUserOrgs(ctx context.Context, userID string) ([]*models.OrgMember, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		IndexName:              aws.String("gsi1-index"),
		KeyConditionExpression: aws.String("gsi1pk = :u AND begins_with(gsi1sk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":u":   &types.AttributeValueMemberS{Value: "USER#" + userID},
			":pfx": &types.AttributeValueMemberS{Value: "ORG#"},
		},
	})
	if err != nil {
		return nil, err
	}
	members := make([]*models.OrgMember, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			members = append(members, &models.OrgMember{OrgID: i.OrgID, UserID: i.UserID, UserEmail: i.UserEmail, UserName: i.UserName, Role: models.OrgRole(i.Role), JoinedAt: i.JoinedAt})
		}
	}
	return members, nil
}

func (d *DynamoStore) UpdateOrgMember(ctx context.Context, m *models.OrgMember) error {
	return d.AddOrgMember(ctx, m)
}

func (d *DynamoStore) RemoveOrgMember(ctx context.Context, orgID, userID string) error {
	return d.deleteEntity(ctx, "ORG#"+orgID, "MEMBER#"+userID)
}

// ─── Teams ────────────────────────────────────────────────────────────────────

func (d *DynamoStore) CreateTeam(ctx context.Context, t *models.Team) error {
	av, err := attributevalue.MarshalMap(entityItem{
		PK: "TEAM#" + t.ID, SK: "METADATA",
		GSI1PK: "ORG#" + t.OrgID, GSI1SK: "TEAM#" + t.ID,
		ID: t.ID, OrgID: t.OrgID, Name: t.Name,
		Description: t.Description, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
	})
	if err != nil {
		return err
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &d.entities,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var cce *types.ConditionalCheckFailedException
		if errors.As(err, &cce) {
			return ErrConflict
		}
	}
	return err
}

func (d *DynamoStore) GetTeam(ctx context.Context, id string) (*models.Team, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &d.entities,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: "TEAM#" + id},
			"sk": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil || out.Item == nil {
		return nil, ErrNotFound
	}
	var i entityItem
	if err := attributevalue.UnmarshalMap(out.Item, &i); err != nil {
		return nil, err
	}
	return &models.Team{ID: i.ID, OrgID: i.OrgID, Name: i.Name, Description: i.Description, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt}, nil
}

func (d *DynamoStore) ListTeams(ctx context.Context, orgID string) ([]*models.Team, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		IndexName:              aws.String("gsi1-index"),
		KeyConditionExpression: aws.String("gsi1pk = :o AND begins_with(gsi1sk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":o":   &types.AttributeValueMemberS{Value: "ORG#" + orgID},
			":pfx": &types.AttributeValueMemberS{Value: "TEAM#"},
		},
	})
	if err != nil {
		return nil, err
	}
	teams := make([]*models.Team, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			teams = append(teams, &models.Team{ID: i.ID, OrgID: i.OrgID, Name: i.Name, Description: i.Description, CreatedBy: i.CreatedBy, CreatedAt: i.CreatedAt})
		}
	}
	return teams, nil
}

func (d *DynamoStore) UpdateTeam(ctx context.Context, t *models.Team) error {
	return d.putEntity(ctx, entityItem{
		PK: "TEAM#" + t.ID, SK: "METADATA",
		GSI1PK: "ORG#" + t.OrgID, GSI1SK: "TEAM#" + t.ID,
		ID: t.ID, OrgID: t.OrgID, Name: t.Name,
		Description: t.Description, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
	})
}

func (d *DynamoStore) DeleteTeam(ctx context.Context, id string) error {
	return d.deleteEntity(ctx, "TEAM#"+id, "METADATA")
}

// ─── Team members ─────────────────────────────────────────────────────────────

func (d *DynamoStore) AddTeamMember(ctx context.Context, m *models.TeamMember) error {
	return d.putEntity(ctx, entityItem{
		PK: "TEAM#" + m.TeamID, SK: "MEMBER#" + m.UserID,
		GSI1PK: "USER#" + m.UserID, GSI1SK: "TEAM#" + m.TeamID,
		UserID: m.UserID, UserEmail: m.UserEmail, UserName: m.UserName, JoinedAt: m.JoinedAt,
	})
}

func (d *DynamoStore) ListTeamMembers(ctx context.Context, teamID string) ([]*models.TeamMember, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":  &types.AttributeValueMemberS{Value: "TEAM#" + teamID},
			":pfx": &types.AttributeValueMemberS{Value: "MEMBER#"},
		},
	})
	if err != nil {
		return nil, err
	}
	members := make([]*models.TeamMember, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			members = append(members, &models.TeamMember{TeamID: teamID, UserID: i.UserID, UserEmail: i.UserEmail, UserName: i.UserName, JoinedAt: i.JoinedAt})
		}
	}
	return members, nil
}

func (d *DynamoStore) ListUserTeams(ctx context.Context, userID string) ([]*models.TeamMember, error) {
	out, err := d.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &d.entities,
		IndexName:              aws.String("gsi1-index"),
		KeyConditionExpression: aws.String("gsi1pk = :u AND begins_with(gsi1sk, :pfx)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":u":   &types.AttributeValueMemberS{Value: "USER#" + userID},
			":pfx": &types.AttributeValueMemberS{Value: "TEAM#"},
		},
	})
	if err != nil {
		return nil, err
	}
	members := make([]*models.TeamMember, 0, len(out.Items))
	for _, raw := range out.Items {
		var i entityItem
		if err := attributevalue.UnmarshalMap(raw, &i); err == nil {
			members = append(members, &models.TeamMember{UserID: i.UserID, UserEmail: i.UserEmail, UserName: i.UserName, JoinedAt: i.JoinedAt})
		}
	}
	return members, nil
}

func (d *DynamoStore) RemoveTeamMember(ctx context.Context, teamID, userID string) error {
	return d.deleteEntity(ctx, "TEAM#"+teamID, "MEMBER#"+userID)
}

func (d *DynamoStore) SyncTeamMembers(ctx context.Context, teamID string, userIDs []string) error {
	existing, err := d.ListTeamMembers(ctx, teamID)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(userIDs))
	for _, uid := range userIDs {
		want[uid] = true
	}
	have := make(map[string]bool, len(existing))
	for _, m := range existing {
		have[m.UserID] = true
		if !want[m.UserID] {
			if err := d.RemoveTeamMember(ctx, teamID, m.UserID); err != nil {
				return err
			}
		}
	}
	for _, uid := range userIDs {
		if !have[uid] {
			if err := d.AddTeamMember(ctx, &models.TeamMember{TeamID: teamID, UserID: uid, JoinedAt: time.Now()}); err != nil {
				return err
			}
		}
	}
	return nil
}
