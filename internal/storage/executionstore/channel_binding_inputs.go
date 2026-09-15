package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ChannelBindingInputIdentity struct {
	ProjectID            ID
	IntegrationInstallID ID
	BindingID            ID
	Capabilities         []channelconnector.Capability
}

type PreparedBoundChannelInput struct {
	store    *Store
	identity ChannelBindingInputIdentity
	binding  integrationstore.ChannelBindingIdentity
	install  integrationstore.IntegrationInstallRecord
}

func (p PreparedBoundChannelInput) AgentID() ID { return p.binding.AgentID }

type DeliverBoundChannelInput struct {
	Prepared               PreparedBoundChannelInput
	Receipt                ChannelEventLease
	InputKey               string
	InputPrecondition      *ChannelInputPrecondition
	ProviderUserID         string
	ActorDisplayName       string
	Content                PreparedInputContent
	Metadata               json.RawMessage
	DeliveryMode           AgentInputDeliveryMode
	CancelOpenInteractions bool
}

// PrepareBoundChannelInput selects immutable identities for artifact storage. It
// creates no association and grants no authority; admission rechecks the exact
// live receive binding. Historical identities also allow accepted-input replay.
func (s *Store) PrepareBoundChannelInput(
	ctx context.Context, identity ChannelBindingInputIdentity,
) (PreparedBoundChannelInput, error) {
	if isNilID(identity.ProjectID) || isNilID(identity.IntegrationInstallID) || isNilID(identity.BindingID) {
		return PreparedBoundChannelInput{}, storeerr.InvalidRequest(
			errors.New("project, installation and binding are required"))
	}
	install, err := s.integrations.GetIntegrationInstall(ctx, identity.ProjectID, identity.IntegrationInstallID)
	if err != nil {
		return PreparedBoundChannelInput{}, err
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return PreparedBoundChannelInput{}, storeerr.ErrNotFound
	}
	if _, err := s.integrations.GetConnectorIntegrationApp(
		ctx, install.IntegrationAppID, identity.Capabilities,
	); err != nil {
		return PreparedBoundChannelInput{}, err
	}
	binding, err := s.integrations.GetChannelBindingIdentity(
		ctx, identity.ProjectID, identity.IntegrationInstallID, identity.BindingID)
	if err != nil {
		return PreparedBoundChannelInput{}, err
	}
	identity.Capabilities = append([]channelconnector.Capability(nil), identity.Capabilities...)
	return PreparedBoundChannelInput{store: s, identity: identity, binding: binding, install: install}, nil
}

// DeliverBoundChannelInput admits one selected recipient through its existing
// binding. Provider behavior chooses recipients; core never infers a subscription
// or creates a workflow, channel, binding or agent in this path.
func (s *Store) DeliverBoundChannelInput(
	ctx context.Context, input DeliverBoundChannelInput,
) (ChannelInputResult, error) {
	outcome := artifactstore.ArtifactTransactionRolledBack
	defer func() { s.finishInputContent(ctx, input.Content, outcome) }()
	prepared := input.Prepared
	if prepared.store != s || isNilID(prepared.binding.AgentID) || isNilID(input.Receipt.ReceiptID) ||
		isNilID(input.Receipt.LeaseToken) || input.Receipt.LeaseGeneration <= 0 {
		return ChannelInputResult{}, storeerr.InvalidRequest(
			errors.New("prepared recipient and current receipt lease are required"))
	}
	if err := validateChannelInputKeys(input.InputKey, input.InputPrecondition); err != nil {
		return ChannelInputResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelInputResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	identity, binding := prepared.identity, prepared.binding
	if err := lockIntegrationInputAgentTx(ctx, tx, prepared.install, binding.AgentID); err != nil {
		return ChannelInputResult{}, err
	}
	install, err := s.integrations.LockConnectorInstallationTx(
		ctx, tx, identity.ProjectID, identity.IntegrationInstallID, identity.Capabilities)
	if err != nil {
		return ChannelInputResult{}, err
	}
	if err := checkChannelEventLease(
		ctx, q, identity.ProjectID, identity.IntegrationInstallID, input.Receipt,
	); err != nil {
		return ChannelInputResult{}, err
	}
	key := integrationstore.IntegrationEventOutcomeKey{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		ReceiptID: input.Receipt.ReceiptID, DeliveryKey: "binding:" + binding.ID.String(),
	}
	accepted, replay, err := s.integrations.GetIntegrationEventOutcomeTx(ctx, tx, key)
	if err != nil {
		return ChannelInputResult{}, err
	}
	var previous AgentInputRecord
	if replay {
		if accepted.AgentID != binding.AgentID {
			return ChannelInputResult{}, storeerr.ErrIdempotencyConflict
		}
		row, readErr := q.GetAgentInput(ctx, dbsqlc.GetAgentInputParams{
			ProjectID: identity.ProjectID, AgentID: accepted.AgentID, ID: accepted.AgentInputID,
		})
		if readErr != nil {
			return ChannelInputResult{}, readErr
		}
		previous = agentInputRecordFromGetSQLC(row)
	} else {
		previous, replay, err = loadAgentInputByIdempotencyMaybeTx(ctx, tx,
			identity.ProjectID, binding.AgentID, integrationstore.IdempotencyScope(install), input.InputKey)
		if err != nil {
			return ChannelInputResult{}, err
		}
	}
	if replay {
		if previous.IntegrationTargetID != binding.IntegrationTargetID {
			return ChannelInputResult{}, storeerr.ErrIdempotencyConflict
		}
		result, err := channelInputReplayResult(ctx, q, previous)
		if err != nil {
			return ChannelInputResult{}, err
		}
		if err := s.recordChannelInputOutcomeTx(ctx, tx, q, key, input.Receipt, result); err != nil {
			return ChannelInputResult{}, err
		}
		outcome = artifactstore.ArtifactTransactionUnknown
		if err := tx.Commit(ctx); err != nil {
			return ChannelInputResult{}, err
		}
		outcome = artifactstore.ArtifactTransactionCommitted
		return result, nil
	}
	if err := checkChannelInputPrecondition(ctx, tx, identity.ProjectID, binding.AgentID,
		integrationstore.IdempotencyScope(install), input.InputPrecondition); err != nil {
		return ChannelInputResult{}, err
	}
	if _, err := s.integrations.GetActiveReceiveBindingTx(ctx, tx, identity.ProjectID, binding.AgentID,
		identity.IntegrationInstallID, binding.IntegrationTargetID, binding.ID); err != nil {
		return ChannelInputResult{}, err
	}
	if strings.TrimSpace(input.ProviderUserID) == "" {
		return ChannelInputResult{}, storeerr.InvalidRequest(errors.New("message author is required for a new input"))
	}
	agent, err := loadAgentInProjectTx(ctx, tx, identity.ProjectID, binding.AgentID)
	if err != nil {
		return ChannelInputResult{}, err
	}
	blocks, canonical, err := s.persistInputContentTx(ctx, tx, identity.ProjectID, binding.AgentID, input.Content)
	if err != nil {
		return ChannelInputResult{}, err
	}
	contentInput, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		ProjectID: identity.ProjectID, AgentID: binding.AgentID,
		Actor: &ActorParams{
			Provider: install.Provider, ProviderTenantID: install.ProviderTenantID,
			ProviderUserID: input.ProviderUserID, DisplayName: &input.ActorDisplayName,
		},
		IntegrationTargetID: binding.IntegrationTargetID, IntegrationTargetBindingID: binding.ID,
		ContentBlocks: canonical, Metadata: input.Metadata, DeliveryMode: input.DeliveryMode,
		IdempotencyScope: integrationstore.IdempotencyScope(install),
		IdempotencyKey:   input.InputKey, CancelOpenInteractions: input.CancelOpenInteractions,
	})
	if err != nil {
		return ChannelInputResult{}, err
	}
	notifications := s.newTxNotifications()
	created, err := createAgentContentInputTx(ctx, notifications, tx, q, agent, contentInput, blocks)
	if err != nil {
		return ChannelInputResult{}, err
	}
	result := ChannelInputResult{
		AgentInput: created.agentInput, ContentBlocks: created.contentBlocks, CreatedInput: created.created,
		ChannelID: binding.IntegrationTargetID, BindingID: binding.ID,
		CanceledInteractionIDs: created.canceledInteractionIDs,
	}
	if err := s.recordChannelInputOutcomeTx(ctx, tx, q, key, input.Receipt, result); err != nil {
		return ChannelInputResult{}, err
	}
	outcome = artifactstore.ArtifactTransactionUnknown
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "deliver bound channel input"); err != nil {
		return ChannelInputResult{}, err
	}
	outcome = artifactstore.ArtifactTransactionCommitted
	return result, nil
}
