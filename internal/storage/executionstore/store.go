package executionstore

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
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
	Artifacts             *artifactstore.Store
}

type Store struct {
	cell                 *agentexecution.AgentCell
	pool                 *storeutil.Pool
	q                    *dbsqlc.Queries
	integrations         *integrationstore.Store
	machinePoolProviders MachinePoolProviders
	identity             *identitystore.Store
	secrets              *secretstore.Store
	artifacts            *artifactstore.Store
}

func New(pool *pgxpool.Pool, config Config) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{
		cell: agentexecution.NewCell(
			"primary",
			pool,
			config.PostCommitPublisher,
			config.ModelCallRetryBackoff,
		),
		pool:                 db,
		q:                    dbsqlc.New(db),
		integrations:         config.Integrations,
		machinePoolProviders: config.MachinePoolProviders,
		identity:             config.Identity,
		secrets:              config.Secrets,
		artifacts:            config.Artifacts,
	}
}
