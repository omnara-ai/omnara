package integrationstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type SetIntegrationRouteProfileInput struct {
	CreateIntegrationRouteInput
	// Supplied top-level configuration keys are merged under the route lock. Omission
	// preserves all configuration keys. Provider-specific interpretation belongs to the caller.
	// First-party behaviors reject unknown keys; their callers must validate each patch.
	ConfigurationPatch json.RawMessage
}

func (s *Store) GetIntegrationRouteByDeploymentKey(
	ctx context.Context, projectID, installID uuid.UUID, key string,
) (IntegrationRouteRecord, error) {
	row, err := s.q.GetIntegrationRouteByDeploymentKey(ctx, dbsqlc.GetIntegrationRouteByDeploymentKeyParams{
		ProjectID: projectID, IntegrationInstallID: installID, DeploymentKey: key,
	})
	if err != nil {
		return IntegrationRouteRecord{}, integrationChannelReadError("get integration behavior", err)
	}
	if row.DeletedAt != nil {
		return IntegrationRouteRecord{}, storeerr.ErrNotFound
	}
	return integrationRouteRecordFromSQLC(row), nil
}

// SetIntegrationRouteProfile changes future launches, preserving route identity,
// existing workflow agents and grants. A missing route uses the supplied initial
// definition; changing an existing behavior or undeleting a route is not allowed.
// Configuration applies only to creation; existing values are preserved.
// ConfigurationPatch changes only supplied keys, under the same route lock.
// Disabled connections may be configured before re-enabling them. Admission still
// requires both the installation and its app to be active.
func (s *Store) SetIntegrationRouteProfile(
	ctx context.Context, update SetIntegrationRouteProfileInput,
) (IntegrationRouteRecord, error) {
	input, err := normalizeCreateIntegrationRouteInput(update.CreateIntegrationRouteInput)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	configurationPatch, err := normalizedJSONObject(update.ConfigurationPatch, "integration route configuration patch")
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	install, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationRouteRecord{}, err
	}
	if _, err := q.LockIntegrationInstallForRouteMutation(ctx, dbsqlc.LockIntegrationInstallForRouteMutationParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
	}); err != nil {
		return IntegrationRouteRecord{}, integrationChannelReadError("lock integration behavior owner", err)
	}
	app, err := q.LockIntegrationAppForInstallation(ctx, dbsqlc.LockIntegrationAppForInstallationParams{
		OrgID: install.OrgID, ID: install.IntegrationAppID,
	})
	if err != nil {
		return IntegrationRouteRecord{}, integrationChannelReadError("lock integration behavior app", err)
	}
	if app.DeletedAt != nil {
		return IntegrationRouteRecord{}, storeerr.ErrNotFound
	}
	row, err := q.LockIntegrationRouteByDeploymentKey(ctx, dbsqlc.LockIntegrationRouteByDeploymentKeyParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID, DeploymentKey: input.DeploymentKey,
	})
	var result IntegrationRouteRecord
	if errors.Is(err, pgx.ErrNoRows) {
		result, err = s.createIntegrationRouteTx(ctx, tx, input)
	} else if err == nil {
		if row.DeletedAt != nil || row.BehaviorKey != input.BehaviorKey {
			return IntegrationRouteRecord{}, storeerr.ErrConflict
		}
		// Match admission's installation -> app -> route -> profile order.
		// The installation mutation lock also excludes concurrent launch.
		if input.AgentProfileID != uuid.Nil {
			if err := s.access.ValidateInstallBinding(ctx, tx, InstallBinding{
				OrgID: install.OrgID, ProjectID: input.ProjectID, AgentProfileID: input.AgentProfileID,
			}); err != nil {
				return IntegrationRouteRecord{}, err
			}
		}
		result = integrationRouteRecordFromSQLC(row)
	}
	if err != nil {
		return IntegrationRouteRecord{}, integrationChannelWriteError("set integration launch profile", err)
	}
	row, err = q.UpdateIntegrationRouteProfile(ctx, dbsqlc.UpdateIntegrationRouteProfileParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		ID: result.ID, AgentProfileID: storeutil.IDFromNil(input.AgentProfileID),
		ConfigurationPatch: configurationPatch,
	})
	if err != nil {
		return IntegrationRouteRecord{}, integrationChannelWriteError("set integration launch profile", err)
	}
	result = integrationRouteRecordFromSQLC(row)
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRouteRecord{}, err
	}
	return result, nil
}
