package integrationstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type IntegrationDestination struct {
	OrgID          uuid.UUID
	ProjectID      uuid.UUID
	AgentProfileID uuid.UUID
	AgentID        uuid.UUID
}

type Access interface {
	ValidateIntegrationDestination(context.Context, pgx.Tx, IntegrationDestination) error
	ClearIntegrationTargetsFromAgents(context.Context, pgx.Tx, uuid.UUID, uuid.UUID) error
}

type Store struct {
	pool   *pgxpool.Pool
	q      *dbsqlc.Queries
	access Access
}

func New(pool *pgxpool.Pool, access Access) *Store {
	return &Store{
		pool:   pool,
		q:      dbsqlc.New(pool),
		access: access,
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
