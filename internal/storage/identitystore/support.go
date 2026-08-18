package identitystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Store struct {
	pool             *pgxpool.Pool
	q                *dbsqlc.Queries
	secretKeyWrapper secrets.KeyWrapper
	blobs            blobstore.Store
}

func New(pool *pgxpool.Pool, keyWrapper secrets.KeyWrapper, blobs blobstore.Store) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), secretKeyWrapper: keyWrapper, blobs: blobs}
}

func (s *Store) GetInstallationID(ctx context.Context) (ID, error) {
	installationID, err := s.q.GetInstallationID(ctx)
	if err != nil {
		return NilID, fmt.Errorf("get installation id: %w", err)
	}
	return installationID, nil
}

type ID = uuid.UUID

var NilID = uuid.Nil

const (
	resourceOrgInvitations               = "org_invitations"
	MaxActivePersonalAccessTokensPerUser = int64(1_000)
)

func isNilID(id ID) bool {
	return id == uuid.Nil
}

func resourceLimitExceeded(resource string, limit int64) error {
	return fmt.Errorf("%s limit of %d reached: %w", resource, limit, storeerr.ErrConflict)
}

func mapSkillOpsError(err error) error {
	switch {
	case errors.Is(err, skillops.ErrNotFound):
		return fmt.Errorf("%w: %w", err, storeerr.ErrNotFound)
	case errors.Is(err, skillops.ErrConflict):
		return fmt.Errorf("%w: %w", err, storeerr.ErrConflict)
	default:
		return err
	}
}
