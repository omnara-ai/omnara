package integrationstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type Access interface {
	ClearIntegrationTargetsFromAgents(context.Context, *agentexecution.Unit, uuid.UUID, uuid.UUID) error
}

type Config struct {
	PostCommitPublisher notifications.PostCommitPublisher
	Access              Access
}

type Store struct {
	cell   *agentexecution.AgentCell
	pool   *storeutil.Pool
	q      *dbsqlc.Queries
	access Access
}

func New(pool *pgxpool.Pool, config Config) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{
		cell:   agentexecution.NewCell("primary", pool, config.PostCommitPublisher, nil),
		pool:   db,
		q:      dbsqlc.New(db),
		access: config.Access,
	}
}

func normalizedJSONObject(value json.RawMessage, fieldName string) (json.RawMessage, error) {
	value = storeutil.NormalizeJSON(value)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, fmt.Errorf("parse %s: %w", fieldName, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", fieldName)
	}
	return value, nil
}
