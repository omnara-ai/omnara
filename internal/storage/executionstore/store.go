package executionstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

type Config struct {
	PostCommitPublisher   notifications.PostCommitPublisher
	ModelCallRetryBackoff func(int, string) time.Duration
	Integrations          *integrationstore.Store
	MachinePoolProviders  MachinePoolProviders
	Identity              *identitystore.Store
	Secrets               *secretstore.Store
}

type Store struct {
	pool                  *pgxpool.Pool
	db                    dbconn.DB
	q                     *dbsqlc.Queries
	postCommitPublisher   notifications.PostCommitPublisher
	modelCallRetryBackoff func(int, string) time.Duration
	integrations          *integrationstore.Store
	machinePoolProviders  MachinePoolProviders
	identity              *identitystore.Store
	secrets               *secretstore.Store
}

func New(db dbconn.DB, config Config) *Store {
	pool, _ := db.(*pgxpool.Pool)
	return &Store{
		pool:                  pool,
		db:                    db,
		q:                     dbsqlc.New(db),
		postCommitPublisher:   config.PostCommitPublisher,
		modelCallRetryBackoff: config.ModelCallRetryBackoff,
		integrations:          config.Integrations,
		machinePoolProviders:  config.MachinePoolProviders,
		identity:              config.Identity,
		secrets:               config.Secrets,
	}
}

func (s *Store) modelCallRetryDelay(attemptNumber int, contextID string) time.Duration {
	if s.modelCallRetryBackoff == nil {
		return ModelCallRetryBackoff(attemptNumber, contextID)
	}
	delay := s.modelCallRetryBackoff(attemptNumber, contextID)
	if delay < 0 {
		return 0
	}
	return delay
}

func (s *Store) newTxNotifications() *notifications.TxNotifications {
	return notifications.NewTxNotifications()
}

func (s *Store) commitTxWithNotifications(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	operation string,
) error {
	var publisher notifications.PostCommitPublisher
	if s != nil {
		publisher = s.postCommitPublisher
	}
	return storeutil.CommitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		publisher,
		operation,
	)
}
