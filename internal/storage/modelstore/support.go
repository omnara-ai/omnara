package modelstore

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	resourceModelProviderConfigs = "model_provider_configs"
	resourceConfiguredModels     = "configured_models"
)

type Store struct {
	pool *storeutil.Pool
	q    *dbsqlc.Queries
}

func New(pool *pgxpool.Pool) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{pool: db, q: dbsqlc.New(db)}
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
