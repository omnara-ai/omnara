package executionstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// PreparedChannelOutput pins an automatic presentation or runtime notice to one
// live binding. It is process-local authority to recheck, not a delivery record.
// Presentation success never resolves the canonical interaction.
type PreparedChannelOutput struct {
	store                 *Store
	input                 integrationstore.PrepareChannelBindingInput
	binding               integrationstore.IntegrationTargetBindingRecord
	access                integrationstore.ChannelAccess
	interaction           AgentInteractionRecord
	turnID, runtimeLockID ID
}

func (p PreparedChannelOutput) Access() integrationstore.ChannelAccess {
	access := p.access
	access.SendParamsSchema = append([]byte(nil), access.SendParamsSchema...)
	access.ProviderMetadata = append([]byte(nil), access.ProviderMetadata...)
	return access
}

// PrepareChannelPresentation derives the destination and form owner from the
// canonical open interaction. A worker runtime is not its durable authority.
func (s *Store) PrepareChannelPresentation(
	ctx context.Context, projectID, agentID, interactionID ID,
) (PreparedChannelOutput, error) {
	interaction, found, err := s.GetAgentInteraction(ctx, projectID, agentID, interactionID)
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	if !found || interaction.State != AgentInteractionStateOpen || isNilID(interaction.IntegrationTargetID) {
		return PreparedChannelOutput{}, storeerr.ErrNotFound
	}
	return s.prepareChannelOutput(ctx, PreparedChannelOutput{
		store: s, interaction: interaction,
		input: integrationstore.PrepareChannelBindingInput{
			ProjectID: projectID, AgentID: agentID, IntegrationTargetID: interaction.IntegrationTargetID,
			Operation: integrationstore.ChannelBindingOperationSend,
		},
	})
}

type PrepareChannelNoticeInput struct{ ProjectID, AgentID, TurnID, RuntimeLockID ID }

// PrepareChannelNotice selects the current channel under the live runtime owner.
// It does not acquire delegation to create or bind a continuation channel.
func (s *Store) PrepareChannelNotice(
	ctx context.Context, input PrepareChannelNoticeInput,
) (PreparedChannelOutput, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.TurnID) || isNilID(input.RuntimeLockID) {
		return PreparedChannelOutput{}, storeerr.InvalidRequest(errors.New("runtime notice owner is required"))
	}
	channelID, err := s.GetAgentCurrentChannelID(ctx, input.ProjectID, input.AgentID)
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	if isNilID(channelID) {
		return PreparedChannelOutput{}, storeerr.ErrNotFound
	}
	return s.prepareChannelOutput(ctx, PreparedChannelOutput{
		store: s, turnID: input.TurnID, runtimeLockID: input.RuntimeLockID,
		input: integrationstore.PrepareChannelBindingInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationTargetID: channelID,
			Operation: integrationstore.ChannelBindingOperationSend,
		},
	})
}

func (s *Store) prepareChannelOutput(
	ctx context.Context, prepared PreparedChannelOutput,
) (PreparedChannelOutput, error) {
	access, err := s.integrations.GetAgentChannelAccess(ctx,
		prepared.input.ProjectID, prepared.input.AgentID, prepared.input.IntegrationTargetID)
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	if err := validateManagedChannelAccess(access, integrationstore.ChannelBindingOperationSend); err != nil {
		return PreparedChannelOutput{}, err
	}
	prepared.access = access
	prepared.input.IntegrationInstallID = access.IntegrationInstallID
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	prepared.binding, err = s.integrations.PrepareChannelBindingTx(ctx, tx, prepared.input)
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	prepared.access, err = s.checkChannelOutputOwnerTx(ctx, tx, prepared)
	if err != nil {
		return PreparedChannelOutput{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PreparedChannelOutput{}, err
	}
	return prepared, nil
}

// RecheckChannelOutput checks the exact binding and canonical owner immediately
// before bounded I/O. A newly granted binding cannot replace a revoked pin.
func (s *Store) RecheckChannelOutput(
	ctx context.Context, prepared PreparedChannelOutput,
) (integrationstore.ChannelAccess, error) {
	if prepared.store != s || isNilID(prepared.binding.ID) {
		return integrationstore.ChannelAccess{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := s.integrations.RecheckChannelBindingTx(ctx, tx, prepared.input, prepared.binding); err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	access, err := s.checkChannelOutputOwnerTx(ctx, tx, prepared)
	if err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	return access, nil
}

func (s *Store) checkChannelOutputOwnerTx(
	ctx context.Context, tx pgx.Tx, p PreparedChannelOutput,
) (integrationstore.ChannelAccess, error) {
	q := s.q.WithTx(tx)
	if !isNilID(p.interaction.ID) {
		row, err := q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
			ProjectID: p.input.ProjectID, AgentID: p.input.AgentID, ID: p.interaction.ID,
		})
		if err != nil {
			return integrationstore.ChannelAccess{}, err
		}
		interaction := agentInteractionRecordFromSQLC(row)
		if interaction.State != AgentInteractionStateOpen || interaction.TurnID != p.interaction.TurnID ||
			interaction.IntegrationTargetID != p.input.IntegrationTargetID ||
			interaction.InteractionKind != p.interaction.InteractionKind ||
			!sameJSON(interaction.Request, p.interaction.Request) {
			return integrationstore.ChannelAccess{}, storeerr.ErrStateTransitionConflict
		}
	} else {
		if err := ensureRuntimeLockActiveTx(ctx, tx, p.input.ProjectID, p.input.AgentID, p.runtimeLockID); err != nil {
			return integrationstore.ChannelAccess{}, err
		}
		exists, err := q.AgentTurnExistsInProject(ctx, dbsqlc.AgentTurnExistsInProjectParams{
			ProjectID: p.input.ProjectID, AgentID: p.input.AgentID, ID: p.turnID,
		})
		if err != nil {
			return integrationstore.ChannelAccess{}, err
		}
		if !exists {
			return integrationstore.ChannelAccess{}, storeerr.ErrNotFound
		}
		current, err := getAgentCurrentChannelID(ctx, q, p.input.ProjectID, p.input.AgentID)
		if err != nil {
			return integrationstore.ChannelAccess{}, err
		}
		if current != p.input.IntegrationTargetID {
			return integrationstore.ChannelAccess{}, storeerr.ErrStateTransitionConflict
		}
	}
	access, err := s.integrations.GetAgentChannelAccessTx(
		ctx, tx, p.input.ProjectID, p.input.AgentID, p.input.IntegrationTargetID)
	if err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	if err := validateManagedChannelAccess(access, integrationstore.ChannelBindingOperationSend); err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	old := p.access
	if access.IntegrationInstallID != old.IntegrationInstallID || access.IntegrationAppID != old.IntegrationAppID ||
		access.DefinitionID != old.DefinitionID || access.ImplementationKey != old.ImplementationKey ||
		access.ConnectorKey != old.ConnectorKey || access.Provider != old.Provider ||
		access.ProviderRef != old.ProviderRef || access.ProviderRefKind != old.ProviderRefKind {
		return integrationstore.ChannelAccess{}, storeerr.ErrUnauthorized
	}
	if (p.interaction.InteractionKind == AgentInteractionKindPermission && !access.Capabilities.Permissions) ||
		(p.interaction.InteractionKind == AgentInteractionKindQuestion && !access.Capabilities.Questions) {
		return integrationstore.ChannelAccess{}, storeerr.ErrUnauthorized
	}
	access.Capabilities.CreatesReplyChannel = false
	return access, nil
}
