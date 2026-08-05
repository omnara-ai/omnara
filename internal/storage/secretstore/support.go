package secretstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Access interface {
	HasOrgMembership(ctx context.Context, principal identitystore.PrincipalRecord, orgID uuid.UUID) (bool, error)
	AuthorizeOrg(context.Context, identitystore.AuthorizeOrgInput) (bool, error)
	AuthorizeProject(context.Context, identitystore.AuthorizeProjectInput) (bool, error)
	GetProject(ctx context.Context, orgID, projectID uuid.UUID) (identitystore.ProjectRecord, error)
}

type Store struct {
	pool             *pgxpool.Pool
	q                *dbsqlc.Queries
	secretKeyWrapper secrets.KeyWrapper
	access           Access
}

func New(pool *pgxpool.Pool, keyWrapper secrets.KeyWrapper, access Access) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), secretKeyWrapper: keyWrapper, access: access}
}

type ID = uuid.UUID
type PrincipalRecord = identitystore.PrincipalRecord

var NilID = uuid.Nil

type ProjectRecord struct {
	ID        ID
	OrgID     ID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	resourceSecrets                = "secrets"
	MaxActiveTenantSecretsPerOwner = int64(1_000)
	orgActionSecretsList           = authz.OrgSecretsList
	orgActionSecretsManage         = authz.OrgSecretsManage
	projectActionSecretsList       = authz.ProjectSecretsList
	projectActionSecretsManage     = authz.ProjectSecretsManage
	principalTypeUser              = authz.PrincipalUser
)

func isNilID(id ID) bool {
	return id == uuid.Nil
}

func idFromSQLCPtr(value *ID) ID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func sqlcIDFromNil(value ID) *ID {
	if isNilID(value) {
		return nil
	}
	return &value
}

func resourceLimitExceeded(resource string, limit int64) error {
	return fmt.Errorf("%s limit of %d reached: %w", resource, limit, storeerr.ErrConflict)
}
