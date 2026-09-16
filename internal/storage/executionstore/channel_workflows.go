package executionstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ErrChannelWorkflowAgentChanged means a concurrent first event selected the final
// agent while this request was preparing files. Retry preparation for that winner
// outside all DB locks.
var ErrChannelWorkflowAgentChanged = errors.New("channel workflow agent changed during preparation")

type ChannelWorkflowIdentity struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationRouteID   uuid.UUID
	InstanceKey          string
	Capabilities         []channelconnector.Capability
}

type PreparedChannelWorkflow struct {
	store      *Store
	identity   ChannelWorkflowIdentity
	agentID    uuid.UUID
	exists     bool
	canLaunch  bool
	inputScope string
}

func (p PreparedChannelWorkflow) AgentID() uuid.UUID { return p.agentID }

type DeliverChannelWorkflowInput struct {
	Prepared               PreparedChannelWorkflow
	Receipt                ChannelEventLease
	InputKey               string
	InputPrecondition      *ChannelInputPrecondition
	OnlyIfUnbound          bool
	Target                 integrationstore.CreateIntegrationTargetInput
	ParentTarget           *integrationstore.CreateIntegrationTargetInput
	ReadAllowed            bool
	SendAllowed            bool
	ProviderUserID         string
	ActorDisplayName       string
	Content                PreparedInputContent
	Metadata               json.RawMessage
	DeliveryMode           AgentInputDeliveryMode
	CancelOpenInteractions bool
}

// PrepareChannelWorkflow selects the identity needed for blob keys. It creates
// no agent or association. Final admission rechecks all authority and identity.
func (s *Store) PrepareChannelWorkflow(
	ctx context.Context,
	identity ChannelWorkflowIdentity,
) (PreparedChannelWorkflow, error) {
	prepared, err := s.resolveChannelWorkflow(ctx, identity)
	if err != nil {
		return PreparedChannelWorkflow{}, err
	}
	if !prepared.exists {
		if !prepared.canLaunch {
			return PreparedChannelWorkflow{}, storeerr.ErrUnauthorized
		}
		prepared.agentID, err = uuid.NewV7()
		if err != nil {
			return PreparedChannelWorkflow{}, err
		}
	}
	return prepared, nil
}

// Resolution is read-only: append-only behaviors can observe that a conversation
// is absent even though they have no permission to launch its agent.
func (s *Store) resolveChannelWorkflow(
	ctx context.Context, identity ChannelWorkflowIdentity,
) (PreparedChannelWorkflow, error) {
	if identity.ProjectID == uuid.Nil || identity.IntegrationInstallID == uuid.Nil ||
		identity.IntegrationRouteID == uuid.Nil ||
		strings.TrimSpace(identity.InstanceKey) == "" || len(identity.InstanceKey) > 512 {
		return PreparedChannelWorkflow{}, storeerr.InvalidRequest(
			errors.New("project, connection, behavior and bounded instance key are required"))
	}
	if err := dbsafe.Text(identity.InstanceKey); err != nil {
		return PreparedChannelWorkflow{}, storeerr.InvalidRequest(err)
	}
	install, err := s.integrations.GetIntegrationInstallByID(ctx, identity.IntegrationInstallID)
	if err != nil {
		return PreparedChannelWorkflow{}, err
	}
	if install.ProjectID != identity.ProjectID || install.State != integrationstore.IntegrationInstallStateActive {
		return PreparedChannelWorkflow{}, storeerr.ErrNotFound
	}
	if _, err := s.integrations.GetConnectorIntegrationApp(
		ctx, install.IntegrationAppID, identity.Capabilities,
	); err != nil {
		return PreparedChannelWorkflow{}, err
	}
	route, err := s.q.GetIntegrationRoute(ctx, dbsqlc.GetIntegrationRouteParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID, ID: identity.IntegrationRouteID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (route.State != "active" || route.DeletedAt != nil)) {
		return PreparedChannelWorkflow{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PreparedChannelWorkflow{}, err
	}
	agentID, found, err := integrationWorkflowAgent(ctx, s.q, identity)
	if err != nil {
		return PreparedChannelWorkflow{}, err
	}
	identity.Capabilities = append([]channelconnector.Capability(nil), identity.Capabilities...)
	return PreparedChannelWorkflow{
		store: s, identity: identity, agentID: agentID, exists: found,
		canLaunch:  route.AgentProfileID != nil,
		inputScope: integrationstore.IdempotencyScope(install),
	}, nil
}

// DeliverChannelWorkflow atomically admits the agent, stable workflow, channel
// binding, prepared attachments, input and wakeup. The receipt is completed by
// the gateway separately after this returns; replay then reuses this input.
func (s *Store) DeliverChannelWorkflow(
	ctx context.Context,
	input DeliverChannelWorkflowInput,
) (ChannelInputResult, error) {
	outcome := artifactstore.ArtifactTransactionRolledBack
	defer func() { s.finishInputContent(ctx, input.Content, outcome) }()
	prepared := input.Prepared
	if prepared.store != s || prepared.agentID == uuid.Nil || input.Receipt.ReceiptID == uuid.Nil ||
		input.Receipt.LeaseToken == uuid.Nil || input.Receipt.LeaseGeneration <= 0 {
		return ChannelInputResult{}, storeerr.InvalidRequest(
			errors.New("prepared workflow and current receipt lease are required"))
	}
	if err := validateChannelInputKeys(input.InputKey, input.InputPrecondition); err != nil {
		return ChannelInputResult{}, err
	}
	identity := prepared.identity
	project, err := loadProjectTx(ctx, s.q, identity.ProjectID)
	if err != nil {
		return ChannelInputResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelInputResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, identity.ProjectID); err != nil {
		return ChannelInputResult{}, err
	}
	// Installation deletion takes this gate before locking associated agents.
	// Enter it before the workflow and agent locks, even when reusing an agent.
	if err := q.LockIntegrationInstallLifecycleShared(ctx, dbsqlc.LockIntegrationInstallLifecycleSharedParams{
		InstallID: identity.IntegrationInstallID,
	}); err != nil {
		return ChannelInputResult{}, err
	}
	if err := q.LockIntegrationWorkflowIdentity(ctx, dbsqlc.LockIntegrationWorkflowIdentityParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		IntegrationRouteID: identity.IntegrationRouteID, InstanceKey: identity.InstanceKey,
	}); err != nil {
		return ChannelInputResult{}, err
	}
	winner, found, err := integrationWorkflowAgent(ctx, q, identity)
	if err != nil {
		return ChannelInputResult{}, err
	}
	if found && winner != prepared.agentID {
		return ChannelInputResult{}, ErrChannelWorkflowAgentChanged
	}
	if found {
		if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{
			{ProjectID: identity.ProjectID, AgentID: winner},
		}); err != nil {
			return ChannelInputResult{}, err
		}
	}
	install, err := s.integrations.LockConnectorInstallationTx(
		ctx, tx, identity.ProjectID, identity.IntegrationInstallID, identity.Capabilities,
	)
	if err != nil {
		return ChannelInputResult{}, err
	}
	route, err := q.LockIntegrationWorkflowRoute(ctx, dbsqlc.LockIntegrationWorkflowRouteParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		IntegrationRouteID: identity.IntegrationRouteID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelInputResult{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ChannelInputResult{}, err
	}
	_, err = q.LockIntegrationEventForExecution(ctx, dbsqlc.LockIntegrationEventForExecutionParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		ID: input.Receipt.ReceiptID, LeaseToken: &input.Receipt.LeaseToken, LeaseGeneration: input.Receipt.LeaseGeneration,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelInputResult{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return ChannelInputResult{}, err
	}
	outcomeKey := integrationstore.IntegrationEventOutcomeKey{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		ReceiptID: input.Receipt.ReceiptID, DeliveryKey: channelWorkflowDeliveryKey(identity),
	}
	accepted, receiptReplay, err := s.integrations.GetIntegrationEventOutcomeTx(ctx, tx, outcomeKey)
	if err != nil {
		return ChannelInputResult{}, err
	}
	if receiptReplay {
		if !found || accepted.AgentID != prepared.agentID {
			return ChannelInputResult{}, storeerr.ErrStateTransitionConflict
		}
		row, err := q.GetAgentInput(ctx, dbsqlc.GetAgentInputParams{
			ProjectID: identity.ProjectID, AgentID: accepted.AgentID, ID: accepted.AgentInputID,
		})
		if err != nil {
			return ChannelInputResult{}, err
		}
		previous, err := channelInputReplayResult(ctx, q, agentInputRecordFromGetSQLC(row))
		if err == nil {
			err = checkChannelEventLease(ctx, q, identity.ProjectID, identity.IntegrationInstallID, input.Receipt)
		}
		return previous, err
	}
	if found {
		// Receipt identity and payload are immutable. Once admitted, a replay
		// returns the accepted input, even if behavior code or grants changed.
		// No current grant is used as authority to write another input.
		previous, replay, err := s.channelWorkflowReplay(ctx, tx, q, prepared, input.InputKey)
		if err != nil {
			return ChannelInputResult{}, err
		}
		if replay {
			if err := s.recordChannelInputOutcomeTx(ctx, tx, q, outcomeKey, input.Receipt, previous); err != nil {
				return ChannelInputResult{}, err
			}
			outcome = artifactstore.ArtifactTransactionUnknown
			if err := tx.Commit(ctx); err != nil {
				return ChannelInputResult{}, err
			}
			outcome = artifactstore.ArtifactTransactionCommitted
			return previous, nil
		}
	}
	if err := checkChannelInputPrecondition(ctx, tx, identity.ProjectID, prepared.agentID,
		integrationstore.IdempotencyScope(install), input.InputPrecondition); err != nil {
		return ChannelInputResult{}, err
	}
	if strings.TrimSpace(input.ProviderUserID) == "" {
		return ChannelInputResult{}, storeerr.InvalidRequest(
			errors.New("message author is required for a new input"))
	}
	if input.ParentTarget != nil {
		if input.Target.ParentChannelID != uuid.Nil || input.ParentTarget.ParentChannelID != uuid.Nil {
			return ChannelInputResult{}, storeerr.InvalidRequest(
				errors.New("inline parent requires no existing child parent ID or nested parent ID"))
		}
		if strings.TrimSpace(input.ParentTarget.ProviderRef) == strings.TrimSpace(input.Target.ProviderRef) {
			return ChannelInputResult{}, storeerr.InvalidRequest(
				errors.New("parent and child channel addresses must differ"))
		}
	}
	notifications := s.newTxNotifications()
	result := ChannelInputResult{}
	var agent AgentRecord
	if found {
		agent, err = loadAgentInProjectTx(ctx, tx, identity.ProjectID, winner)
		if err == nil && agent.State != AgentStateActive {
			err = storeerr.ErrStateTransitionConflict
		}
	} else if route.AgentProfileID == nil {
		err = storeerr.ErrUnauthorized
	} else {
		profile, profileErr := lockAgentProfileTx(ctx, q, identity.ProjectID, *route.AgentProfileID)
		if profileErr != nil {
			return ChannelInputResult{}, profileErr
		}
		var launch LaunchAgentResult
		launch, err = s.launchAgentTx(ctx, tx, q, notifications, LaunchAgentInput{
			ProjectID: identity.ProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
			LaunchedBy: install.InstalledBy, preparedAgentID: prepared.agentID,
		})
		agent, result.CreatedAgent = launch.Agent, launch.Created
		if err == nil {
			err = q.InsertIntegrationWorkflow(ctx, dbsqlc.InsertIntegrationWorkflowParams{
				ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
				IntegrationRouteID: identity.IntegrationRouteID, InstanceKey: identity.InstanceKey, AgentID: prepared.agentID,
			})
		}
	}
	if err != nil {
		return ChannelInputResult{}, err
	}
	if input.ParentTarget != nil {
		parentInput := *input.ParentTarget
		parentInput.ProjectID, parentInput.IntegrationInstallID = identity.ProjectID, identity.IntegrationInstallID
		parent, err := s.integrations.CreateIntegrationTargetTx(ctx, tx, parentInput)
		if err != nil {
			return ChannelInputResult{}, err
		}
		input.Target.ParentChannelID = parent.ID
	}
	input.Target.ProjectID, input.Target.IntegrationInstallID =
		identity.ProjectID, identity.IntegrationInstallID
	target, err := s.integrations.CreateIntegrationTargetTx(ctx, tx, input.Target)
	if err != nil {
		return ChannelInputResult{}, err
	}
	if input.OnlyIfUnbound && !found {
		if err := checkChannelWorkflowInitialBinding(ctx, q, identity, input.Receipt, target); err != nil {
			return ChannelInputResult{}, err
		}
	}
	binding, err := s.integrations.InitialChannelReceiveBindingTx(
		ctx, tx, integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: identity.ProjectID, AgentID: prepared.agentID, IntegrationInstallID: identity.IntegrationInstallID,
			IntegrationTargetID: target.ID, IntegrationRouteID: identity.IntegrationRouteID,
			ReceiveAllowed: true, ReadAllowed: input.ReadAllowed, SendAllowed: input.SendAllowed,
			Source: "channel", Metadata: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		return ChannelInputResult{}, err
	}
	blocks, canonical, err := s.persistInputContentTx(ctx, tx, identity.ProjectID, prepared.agentID, input.Content)
	if err != nil {
		return ChannelInputResult{}, err
	}
	contentInput, err := prepareCreateAgentContentInput(CreateAgentContentInputInput{
		ProjectID: identity.ProjectID, AgentID: prepared.agentID,
		Actor: &ActorParams{
			Provider: install.Provider, ProviderTenantID: install.ProviderTenantID,
			ProviderUserID: input.ProviderUserID, DisplayName: &input.ActorDisplayName,
		},
		IntegrationTargetID: target.ID, IntegrationTargetBindingID: binding.ID,
		ContentBlocks: canonical, Metadata: input.Metadata, DeliveryMode: input.DeliveryMode,
		IdempotencyScope: integrationstore.IdempotencyScope(install),
		IdempotencyKey:   input.InputKey, CancelOpenInteractions: input.CancelOpenInteractions,
	})
	if err != nil {
		return ChannelInputResult{}, err
	}
	created, err := createAgentContentInputTx(ctx, notifications, tx, q, agent, contentInput, blocks)
	if err != nil {
		return ChannelInputResult{}, err
	}
	result.AgentInput, result.ContentBlocks, result.CreatedInput =
		created.agentInput, created.contentBlocks, created.created
	result.ChannelID, result.BindingID = target.ID, binding.ID
	result.CanceledInteractionIDs = created.canceledInteractionIDs
	if err := s.recordChannelInputOutcomeTx(ctx, tx, q, outcomeKey, input.Receipt, result); err != nil {
		return ChannelInputResult{}, err
	}
	outcome = artifactstore.ArtifactTransactionUnknown
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "deliver channel workflow input"); err != nil {
		return ChannelInputResult{}, err
	}
	outcome = artifactstore.ArtifactTransactionCommitted
	return result, nil
}

func integrationWorkflowAgent(
	ctx context.Context,
	q *dbsqlc.Queries,
	identity ChannelWorkflowIdentity,
) (uuid.UUID, bool, error) {
	id, err := q.GetIntegrationWorkflowAgent(ctx, dbsqlc.GetIntegrationWorkflowAgentParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		IntegrationRouteID: identity.IntegrationRouteID, InstanceKey: identity.InstanceKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return id, err == nil, err
}

func channelWorkflowDeliveryKey(identity ChannelWorkflowIdentity) string {
	digest := sha256.Sum256([]byte(identity.InstanceKey))
	return "workflow:" + identity.IntegrationRouteID.String() + ":" + hex.EncodeToString(digest[:])
}

func (s *Store) channelWorkflowReplay(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	prepared PreparedChannelWorkflow,
	inputKey string,
) (ChannelInputResult, bool, error) {
	identity := prepared.identity
	input, found, err := loadAgentInputByIdempotencyMaybeTx(ctx, tx, identity.ProjectID, prepared.agentID,
		prepared.inputScope, inputKey)
	if err != nil || !found {
		return ChannelInputResult{}, false, err
	}
	result, err := channelInputReplayResult(ctx, q, input)
	return result, err == nil, err
}
