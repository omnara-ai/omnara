package orglifecycle

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

type Config struct {
	Blobs               blobstore.Store
	PostCommitPublisher notifications.PostCommitPublisher
	Identity            *identitystore.Store
	Execution           *executionstore.Store
	Models              *modelstore.Store
	Secrets             *secretstore.Store
}

type Service struct {
	cell      *agentexecution.AgentCell
	pool      *storeutil.Pool
	q         *dbsqlc.Queries
	blobs     blobstore.Store
	identity  *identitystore.Store
	execution *executionstore.Store
	models    *modelstore.Store
	secrets   *secretstore.Store
}

func New(pool *pgxpool.Pool, config Config) *Service {
	db := storeutil.WrapPool(pool)
	return &Service{
		cell:      agentexecution.NewCell("primary", pool, config.PostCommitPublisher, nil),
		pool:      db,
		q:         dbsqlc.New(db),
		blobs:     config.Blobs,
		identity:  config.Identity,
		execution: config.Execution,
		models:    config.Models,
		secrets:   config.Secrets,
	}
}
