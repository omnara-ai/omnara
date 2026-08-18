package modelstore

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ID = uuid.UUID

var NilID = uuid.Nil

const (
	resourceModelProviderConfigs = "model_provider_configs"
	resourceConfiguredModels     = "configured_models"
)

type Store struct {
	pool *pgxpool.Pool
	q    *dbsqlc.Queries
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool)}
}

func isNilID(id ID) bool {
	return id == uuid.Nil
}

func idFromSQLCPtr(value *ID) ID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func stringFromSQLCText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func resourceLimitExceeded(resource string, limit int64) error {
	return fmt.Errorf("%s limit of %d reached: %w", resource, limit, storeerr.ErrConflict)
}
