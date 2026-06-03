package store

import (
	"context"

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
}
