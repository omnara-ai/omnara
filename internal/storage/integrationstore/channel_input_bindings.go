package integrationstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// GetActiveReceiveBindingForTargetTx resolves input provenance in the caller's
// transaction using the same live authority and deterministic preference as the
// unlocked lookup. It grants nothing and does not lock the selected binding;
// input admission must recheck that exact identity at its mutation boundary.
func (s *Store) GetActiveReceiveBindingForTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, integrationTargetID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if tx == nil {
		return IntegrationTargetBindingRecord{}, errors.New("transaction is required")
	}
	return getActiveReceiveBindingForTarget(ctx, s.q.WithTx(tx), projectID, agentID, integrationTargetID)
}

// InitialChannelReceiveBindingTx grants a newly discovered relationship once.
// Later events use current permissions; they cannot restore a revoked binding
// or replace grants merely because the connector's defaults changed.
func (s *Store) InitialChannelReceiveBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	if tx == nil || !input.ReceiveAllowed {
		return IntegrationTargetBindingRecord{}, storeerr.InvalidRequest(
			errors.New("transaction and receive grant are required"))
	}
	return s.initialChannelBindingTx(ctx, tx, input, true)
}

// InitialChannelBindingTx grants a newly discovered relationship once. Existing
// live grants are retained unchanged; retired history never triggers re-creation.
func (s *Store) InitialChannelBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetBindingInput,
) (IntegrationTargetBindingRecord, error) {
	if tx == nil {
		return IntegrationTargetBindingRecord{}, storeerr.InvalidRequest(errors.New("transaction is required"))
	}
	return s.initialChannelBindingTx(ctx, tx, input, false)
}

func (s *Store) initialChannelBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input CreateIntegrationTargetBindingInput,
	requireReceive bool,
) (IntegrationTargetBindingRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationTargetBindingInput(input)
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID); err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	q := s.q.WithTx(tx)
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID, ID: input.AgentID,
	}); err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError("lock initial binding agent", err)
	}
	if _, err := q.LockIntegrationTargetForBinding(ctx, dbsqlc.LockIntegrationTargetForBindingParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		IntegrationTargetID: input.IntegrationTargetID,
	}); err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError("lock initial channel binding", err)
	}
	active, err := q.LockInitialChannelBinding(ctx, dbsqlc.LockInitialChannelBindingParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationTargetID: input.IntegrationTargetID,
		RequireReceive: requireReceive,
	})
	if err == nil {
		return integrationTargetBindingRecordFromSQLC(active), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IntegrationTargetBindingRecord{}, err
	}
	hasHistory, err := q.AgentHasChannelBindingHistory(ctx, dbsqlc.AgentHasChannelBindingHistoryParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationTargetID: input.IntegrationTargetID,
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if hasHistory {
		return IntegrationTargetBindingRecord{}, storeerr.ErrUnauthorized
	}
	return s.CreateIntegrationTargetBindingTx(ctx, tx, input)
}
