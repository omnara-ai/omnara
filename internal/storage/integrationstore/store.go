package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	pool   *pgxpool.Pool
	q      *dbsqlc.Queries
	access Access
}

func New(pool *pgxpool.Pool, access Access) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), access: access}
}

func lockProjectLifecycleShared(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
) error {
	if err := qtx.LockProjectLifecycleShared(
		ctx,
		dbsqlc.LockProjectLifecycleSharedParams{ProjectID: projectID.String()},
	); err != nil {
		return fmt.Errorf("lock project lifecycle: %w", err)
	}
	return nil
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
	normalized, err := channelconnector.NormalizeOpaqueObject(value)
	if err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("normalize %s: %w", fieldName, err))
	}
	return normalized, nil
}

func integrationChannelWriteError(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	if isIntegrationJSONBoundsViolation(err) {
		return storeerr.InvalidRequest(wrapped)
	}
	return wrapped
}

func isIntegrationJSONBoundsViolation(err error) bool {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23514" {
		return false
	}
	switch databaseError.ConstraintName {
	case "integration_apps_provider_config_bytes_check",
		"integration_apps_provider_metadata_bytes_check",
		"integration_installs_channel_payload_bounds_check",
		"integration_routes_configuration_bytes_check",
		"integration_targets_channel_payload_bounds_check",
		"integration_target_bindings_metadata_bytes_check",
		"integration_deliveries_payload_bytes_check",
		"integration_deliveries_last_error_bytes_check",
		"integration_runtime_units_configuration_bytes_check",
		"integration_runtime_units_checkpoint_bytes_check",
		"integration_runtime_units_last_error_bytes_check":
		return true
	default:
		return false
	}
}
