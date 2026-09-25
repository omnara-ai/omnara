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

type InstallBinding struct {
	OrgID          uuid.UUID
	ProjectID      uuid.UUID
	AgentProfileID uuid.UUID
	AgentID        uuid.UUID
}

type Access interface {
	ValidateInstallBinding(context.Context, pgx.Tx, InstallBinding) error
	ClearInstallTargetsFromAgents(context.Context, pgx.Tx, uuid.UUID, uuid.UUID) error
}

type Store struct {
	pool               *storeutil.Pool
	q                  *dbsqlc.Queries
	access             Access
	targetRefGenerator func(string) (string, error)
}

func New(pool *pgxpool.Pool, access Access) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{
		pool:               db,
		q:                  dbsqlc.New(db),
		access:             access,
		targetRefGenerator: newIntegrationTargetRef,
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
