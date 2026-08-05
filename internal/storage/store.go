package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/accountsecurity"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

type Store struct {
	pool            *pgxpool.Pool
	blobs           blobstore.Store
	identity        *identitystore.Store
	models          *modelstore.Store
	artifacts       *artifactstore.Store
	execution       *executionstore.Store
	integrations    *integrationstore.Store
	secrets         *secretstore.Store
	skills          *skillstore.Store
	organizations   *orglifecycle.Service
	accountSecurity *accountsecurity.Service
}

type storeConfig struct {
	postCommitPublisher   notifications.PostCommitPublisher
	secretKeyWrapper      secrets.KeyWrapper
	blobs                 blobstore.Store
	machinePoolProviders  executionstore.MachinePoolProviders
	modelCallRetryBackoff func(int, string) time.Duration
}

type Option func(*storeConfig)

func WithPostCommitPublisher(publisher notifications.PostCommitPublisher) Option {
	return func(config *storeConfig) {
		config.postCommitPublisher = publisher
	}
}

func WithSecretKeyWrapper(keyWrapper secrets.KeyWrapper) Option {
	return func(config *storeConfig) {
		config.secretKeyWrapper = keyWrapper
	}
}

func WithBlobStore(blobs blobstore.Store) Option {
	return func(config *storeConfig) {
		config.blobs = blobs
	}
}

func WithMachinePoolProviders(machinePoolProviders executionstore.MachinePoolProviders) Option {
	return func(config *storeConfig) {
		config.machinePoolProviders = machinePoolProviders
	}
}

func WithModelCallRetryBackoff(backoff func(int, string) time.Duration) Option {
	return func(config *storeConfig) {
		config.modelCallRetryBackoff = backoff
	}
}

func NewStore(pool *pgxpool.Pool, opts ...Option) *Store {
	config := storeConfig{}
	for _, opt := range opts {
		opt(&config)
	}
	store := &Store{
		pool:  pool,
		blobs: config.blobs,
	}
	store.identity = identitystore.New(pool, config.secretKeyWrapper, config.blobs)
	store.models = modelstore.New(pool)
	store.secrets = secretstore.New(pool, config.secretKeyWrapper, store.identity)
	store.skills = skillstore.New(pool, config.blobs, store.identity)
	store.artifacts = artifactstore.New(pool, config.blobs)
	store.integrations = integrationstore.New(pool, executionstore.IntegrationInstallAccess{})
	store.execution = executionstore.New(pool, executionstore.Config{
		PostCommitPublisher:   config.postCommitPublisher,
		ModelCallRetryBackoff: config.modelCallRetryBackoff,
		Integrations:          store.integrations,
		MachinePoolProviders:  config.machinePoolProviders,
		Identity:              store.identity,
		Secrets:               store.secrets,
	})
	store.organizations = orglifecycle.New(pool, orglifecycle.Config{
		Blobs:               config.blobs,
		PostCommitPublisher: config.postCommitPublisher,
		Identity:            store.identity,
		Execution:           store.execution,
		Models:              store.models,
		Secrets:             store.secrets,
	})
	store.accountSecurity = accountsecurity.New(
		pool,
		store.identity,
	)
	return store
}

func (s *Store) Models() *modelstore.Store {
	return s.models
}

func (s *Store) Identity() *identitystore.Store {
	return s.identity
}

func (s *Store) Artifacts() *artifactstore.Store {
	return s.artifacts
}

func (s *Store) Integrations() *integrationstore.Store {
	return s.integrations
}

func (s *Store) Execution() *executionstore.Store {
	return s.execution
}

func (s *Store) Secrets() *secretstore.Store {
	return s.secrets
}

func (s *Store) Skills() *skillstore.Store {
	return s.skills
}

func (s *Store) Organizations() *orglifecycle.Service {
	return s.organizations
}

func (s *Store) AccountSecurity() *accountsecurity.Service {
	return s.accountSecurity
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
