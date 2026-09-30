package secretstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Access interface {
	HasOrgMembership(ctx context.Context, principal identitystore.PrincipalRecord, orgID uuid.UUID) (bool, error)
	AuthorizeOrg(context.Context, identitystore.AuthorizeOrgInput) (bool, error)
	AuthorizeProject(context.Context, identitystore.AuthorizeProjectInput) (bool, error)
	GetProject(ctx context.Context, orgID, projectID uuid.UUID) (identitystore.ProjectRecord, error)
}

type Store struct {
	pool             *storeutil.Pool
	q                *dbsqlc.Queries
	secretKeyWrapper secrets.KeyWrapper
	access           Access
}

func New(pool *pgxpool.Pool, keyWrapper secrets.KeyWrapper, access Access) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{pool: db, q: dbsqlc.New(db), secretKeyWrapper: keyWrapper, access: access}
}

type ProjectRecord struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const resourceSecrets = "secrets"

func resourceLimitExceeded(resource string, limit int64) error {
	return fmt.Errorf("%s limit of %d reached: %w", resource, limit, storeerr.ErrConflict)
}
