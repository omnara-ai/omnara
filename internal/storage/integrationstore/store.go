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

type ID = uuid.UUID

var NilID = uuid.Nil

type InstallBinding struct {
	OrgID          ID
	ProjectID      ID
	AgentProfileID ID
	AgentID        ID
}

type Access interface {
	ValidateInstallBinding(context.Context, pgx.Tx, InstallBinding) error
	ClearInstallTargetsFromAgents(context.Context, pgx.Tx, ID, ID) error
}

type Store struct {
	pool               *pgxpool.Pool
	q                  *dbsqlc.Queries
	access             Access
	targetRefGenerator func(string) (string, error)
}

func New(pool *pgxpool.Pool, access Access) *Store {
	return &Store{
		pool:               pool,
		q:                  dbsqlc.New(pool),
		access:             access,
		targetRefGenerator: newIntegrationTargetRef,
	}
}

func isNilID(id ID) bool {
	return id == NilID
}

func sqlcTextFromEmpty(value string) *string {
	return storeutil.TextFromEmpty(value)
}

func sqlcIDFromNil(value ID) *ID {
	return storeutil.IDFromNil(value)
}

func idFromSQLCPtr(value *ID) ID {
	return storeutil.IDFromPtr(value)
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
