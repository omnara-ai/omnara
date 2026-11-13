package modelstore

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

const (
	resourceModelProviderConfigs = "model_provider_configs"
	resourceConfiguredModels     = "configured_models"
)

type Config struct {
	EndpointAllowlist *ssrf.Allowlist
}

type Store struct {
	pool              *storeutil.Pool
	q                 *dbsqlc.Queries
	endpointAllowlist *ssrf.Allowlist
}

func New(pool *pgxpool.Pool, config Config) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{pool: db, q: dbsqlc.New(db), endpointAllowlist: config.EndpointAllowlist}
}

func stringFromSQLCText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
