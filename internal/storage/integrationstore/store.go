package integrationstore

import (
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type Config struct {
	PostCommitPublisher notifications.PostCommitPublisher
}

type Store struct {
	cell *agentexecution.AgentCell
	pool *storeutil.Pool
	q    *dbsqlc.Queries
}

func New(pool *pgxpool.Pool, config Config) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{
		cell: agentexecution.NewCell(pool, config.PostCommitPublisher, nil),
		pool: db,
		q:    dbsqlc.New(db),
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
