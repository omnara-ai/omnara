package orglifecycle

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

type Config struct {
	Blobs               blobstore.Store
	MemoryFilesystem    *memoryops.Filesystem
	PostCommitPublisher notifications.PostCommitPublisher
	Identity            *identitystore.Store
	Execution           *executionstore.Store
	Models              *modelstore.Store
	Secrets             *secretstore.Store
}

type Service struct {
	pool                *pgxpool.Pool
	q                   *dbsqlc.Queries
	blobs               blobstore.Store
	memoryFS            *memoryops.Filesystem
	postCommitPublisher notifications.PostCommitPublisher
	identity            *identitystore.Store
	execution           *executionstore.Store
	models              *modelstore.Store
	secrets             *secretstore.Store
}

func New(pool *pgxpool.Pool, config Config) *Service {
	return &Service{
		pool:                pool,
		q:                   dbsqlc.New(pool),
		blobs:               config.Blobs,
		memoryFS:            config.MemoryFilesystem,
		postCommitPublisher: config.PostCommitPublisher,
		identity:            config.Identity,
		execution:           config.Execution,
		models:              config.Models,
		secrets:             config.Secrets,
	}
}

func (s *Service) newTxNotifications() *notifications.TxNotifications {
	return notifications.NewTxNotifications()
}
