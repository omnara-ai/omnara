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
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
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

// lockIntegrationInstallLifecycleShared enters scope gates before any agent or
// installation row locks, matching installation deletion's admission boundary.
func lockIntegrationInstallLifecycleShared(
	ctx context.Context,
	tx pgx.Tx,
	projectID, installID ID,
) (IntegrationInstallRecord, error) {
	q := dbsqlc.New(tx)
	install, err := getIntegrationInstall(ctx, q, projectID, installID)
	if err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, projectID); err != nil {
		return IntegrationInstallRecord{}, err
	}
	if err := q.LockIntegrationInstallLifecycleShared(
		ctx,
		dbsqlc.LockIntegrationInstallLifecycleSharedParams{InstallID: installID},
	); err != nil {
		return IntegrationInstallRecord{}, fmt.Errorf("lock integration install lifecycle: %w", err)
	}
	return getIntegrationInstall(ctx, q, projectID, installID)
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
