package integrationstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
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
	db     dbconn.DB
	q      *dbsqlc.Queries
	access Access
}

func New(db dbconn.DB, access Access) *Store {
	return &Store{db: db, q: dbsqlc.New(db), access: access}
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
