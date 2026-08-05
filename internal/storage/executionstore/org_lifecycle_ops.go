package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) ArchiveAgentTx(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	projectID, agentID ID,
	actor *ActorParams,
) ([]MachineRecord, error) {
	return archiveAgentTx(ctx, tx, s.q.WithTx(tx), txNotifications, projectID, agentID, actor)
}

func (s *Store) ProvisionOrganizationDefaultsTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, projectID ID,
	templates []DefaultMachinePoolTemplate,
) error {
	qtx := s.q.WithTx(tx)
	for _, template := range templates {
		input := template.createInput(orgID)
		poolDefaults, err := prepareMachinePoolCreateInput(&input)
		if err != nil {
			return fmt.Errorf("default machine pool: %w", err)
		}
		if err := s.validatePoolDefaultsTx(ctx, qtx, input, poolDefaults); err != nil {
			return fmt.Errorf("default machine pool: %w", err)
		}
		if _, err := insertMachinePool(ctx, qtx, input); err != nil {
			if errors.Is(err, pgx.ErrNoRows) || storeutil.IsUniqueViolation(err) {
				return fmt.Errorf("create default machine pool: %w", storeerr.ErrIdempotencyConflict)
			}
			return fmt.Errorf("create default machine pool: %w", err)
		}
	}
	pools, err := qtx.ListClusterManagedMachinePools(
		ctx,
		dbsqlc.ListClusterManagedMachinePoolsParams{OrgID: orgID},
	)
	if err != nil {
		return fmt.Errorf("load default machine pools for project grants: %w", err)
	}
	for _, pool := range pools {
		_, err = qtx.UpsertProjectMachinePoolGrant(ctx, dbsqlc.UpsertProjectMachinePoolGrantParams{
			OrgID:                                orgID,
			ProjectID:                            projectID,
			MachinePoolID:                        pool.ID,
			Description:                          "",
			DefaultMachineEnvOverlay:             json.RawMessage(`{}`),
			DefaultMachineSecretEnvOverlay:       json.RawMessage(`{}`),
			DefaultMachineProviderOptionsOverlay: json.RawMessage(`{}`),
			DefaultCwd:                           "",
			Metadata:                             json.RawMessage(`{}`),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("create default machine pool project grant: %w", storeerr.ErrIdempotencyConflict)
		}
		if err != nil {
			return fmt.Errorf("create default machine pool project grant: %w", err)
		}
	}
	return nil
}
