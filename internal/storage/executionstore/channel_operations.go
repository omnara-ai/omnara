package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type PrepareChannelOperationInput struct {
	ExecuteToolCallInput
	TurnID    uuid.UUID
	ChannelID uuid.UUID
	Operation integrationstore.ChannelBindingOperation
}

// PreparedChannelOperation pins one running tool/runtime and one source binding
// across bounded provider I/O. It is process-local correlation, not a reusable
// authorization token. Recheck and completion consult current durable authority.
type PreparedChannelOperation struct {
	store   *Store
	owner   PrepareChannelOperationInput
	input   integrationstore.PrepareChannelBindingInput
	access  integrationstore.ChannelAccess
	binding integrationstore.IntegrationTargetBindingRecord
}

func (p PreparedChannelOperation) Access() integrationstore.ChannelAccess {
	access := p.access
	access.SendParamsSchema = append([]byte(nil), access.SendParamsSchema...)
	access.ProviderMetadata = append([]byte(nil), access.ProviderMetadata...)
	return access
}

func (p PreparedChannelOperation) Binding() integrationstore.IntegrationTargetBindingRecord {
	binding := p.binding
	if binding.ReplyChannelGrants != nil {
		grants := *binding.ReplyChannelGrants
		binding.ReplyChannelGrants = &grants
	}
	return binding
}

// PrepareChannelOperation runs after ordinary async admission. Its short
// transaction commits before any artifact streaming or provider request starts.
func (s *Store) PrepareChannelOperation(
	ctx context.Context,
	input PrepareChannelOperationInput,
) (PreparedChannelOperation, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ToolCallID == uuid.Nil ||
		input.RuntimeLockID == uuid.Nil || input.TurnID == uuid.Nil || input.ChannelID == uuid.Nil ||
		channelOperationToolName(input.Operation) == "" {
		return PreparedChannelOperation{}, storeerr.InvalidRequest(
			errors.New("channel operation and tool owner are required"))
	}
	access, err := s.integrations.GetAgentChannelAccess(ctx, input.ProjectID, input.AgentID, input.ChannelID)
	if err != nil {
		return PreparedChannelOperation{}, err
	}
	if err := validateManagedChannelAccess(access, input.Operation); err != nil {
		return PreparedChannelOperation{}, err
	}
	prepared := PreparedChannelOperation{store: s, owner: input, access: access,
		input: integrationstore.PrepareChannelBindingInput{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			IntegrationInstallID: access.IntegrationInstallID, IntegrationTargetID: input.ChannelID,
			Operation: input.Operation,
			CreatesReplyChannel: input.Operation == integrationstore.ChannelBindingOperationSend &&
				access.Capabilities.CreatesReplyChannel,
		}}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PreparedChannelOperation{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// This helper enters org/project/install gates before locking the agent and
	// its channel state. Re-locking that same agent for the runtime is safe.
	prepared.binding, err = s.integrations.PrepareChannelBindingTx(ctx, tx, prepared.input)
	if err != nil {
		return PreparedChannelOperation{}, err
	}
	prepared.access, _, err = s.checkChannelOperationOwnerTx(ctx, tx, prepared)
	if err != nil {
		return PreparedChannelOperation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PreparedChannelOperation{}, err
	}
	return prepared, nil
}

// RecheckChannelOperation fences dispatch against the same live binding, turn,
// running tool and runtime. A different eligible binding never replaces the pin.
func (s *Store) RecheckChannelOperation(
	ctx context.Context,
	prepared PreparedChannelOperation,
) (integrationstore.ChannelAccess, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	access, _, err := s.recheckChannelOperationTx(ctx, tx, prepared)
	if err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return integrationstore.ChannelAccess{}, err
	}
	return access, nil
}

type CompleteChannelOperationInput struct {
	Prepared PreparedChannelOperation
	// Payload is the validated typed payload actually dispatched. It is not a
	// saved model definition or a replacement for the tool's original arguments.
	Payload json.RawMessage
	Result  channelconnector.OperationResult
}

// CompleteChannelOperation atomically registers any continuation and settles the
// running tool through the normal result/event/wakeup owner. The shared mapper
// also serves external requests, whose distinct wait authority is checked by
// their own completion path. This method never calls a provider.
func (s *Store) CompleteChannelOperation(
	ctx context.Context,
	input CompleteChannelOperationInput,
) (ToolCallRecord, error) {
	prepared := input.Prepared
	if prepared.store != s || prepared.binding.ID == uuid.Nil {
		return ToolCallRecord{}, storeerr.ErrUnauthorized
	}
	requestID, err := publicid.Encode(publicid.KindToolCall, prepared.owner.ToolCallID)
	if err != nil || input.Result.RequestID != requestID {
		return ToolCallRecord{}, storeerr.InvalidRequest(errors.New("channel result request ID does not match"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ToolCallRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	access, _, err := s.recheckChannelOperationTx(ctx, tx, prepared)
	if err != nil {
		return ToolCallRecord{}, err
	}
	completion, err := s.channelOperationCompletionTx(ctx, tx, channelOperationCompletionInput{
		RequestID: requestID, Operation: channelconnector.OperationKind(prepared.owner.Operation),
		Payload: input.Payload, Result: input.Result, Access: access, Binding: prepared.binding,
		CreatesReplyChannel: prepared.input.CreatesReplyChannel,
	})
	if err != nil {
		return ToolCallRecord{}, err
	}
	txNotifications := s.newTxNotifications()
	record, err := completeRuntimeToolCallTx(ctx, txNotifications, tx, CompleteRuntimeToolCallInput{
		ProjectID: prepared.owner.ProjectID, AgentID: prepared.owner.AgentID,
		ID: prepared.owner.ToolCallID, RuntimeLockID: prepared.owner.RuntimeLockID,
		Outcome: completion.Outcome, ResultContentParts: completion.ResultContentParts,
	})
	if err != nil {
		return ToolCallRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete managed channel operation"); err != nil {
		return ToolCallRecord{}, err
	}
	return record, nil
}

func (s *Store) recheckChannelOperationTx(
	ctx context.Context,
	tx pgx.Tx,
	prepared PreparedChannelOperation,
) (integrationstore.ChannelAccess, ToolCallRecord, error) {
	if prepared.store != s || prepared.binding.ID == uuid.Nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, storeerr.ErrUnauthorized
	}
	if _, err := s.integrations.RecheckChannelBindingTx(ctx, tx, prepared.input, prepared.binding); err != nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, err
	}
	return s.checkChannelOperationOwnerTx(ctx, tx, prepared)
}

func (s *Store) checkChannelOperationOwnerTx(
	ctx context.Context,
	tx pgx.Tx,
	prepared PreparedChannelOperation,
) (integrationstore.ChannelAccess, ToolCallRecord, error) {
	owner := prepared.owner
	if err := ensureRuntimeLockActiveTx(ctx, tx, owner.ProjectID, owner.AgentID, owner.RuntimeLockID); err != nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, err
	}
	call, err := getToolCallTx(ctx, tx, owner.ProjectID, owner.AgentID, owner.ToolCallID)
	if err != nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, err
	}
	if call.State != ToolCallStateRunning || call.RuntimeLockID != owner.RuntimeLockID ||
		call.TurnID != owner.TurnID || call.Type != toolcatalog.ToolTypeBuiltIn ||
		call.Name != channelOperationToolName(owner.Operation) {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, storeerr.ErrStateTransitionConflict
	}
	access, err := s.integrations.GetAgentChannelAccessTx(ctx, tx, owner.ProjectID, owner.AgentID, owner.ChannelID)
	if err != nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, err
	}
	if err := validateManagedChannelAccess(access, owner.Operation); err != nil {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, err
	}
	old := prepared.access
	if access.IntegrationInstallID != old.IntegrationInstallID || access.IntegrationAppID != old.IntegrationAppID ||
		access.DefinitionID != old.DefinitionID || access.ImplementationKey != old.ImplementationKey ||
		access.ConnectorKey != old.ConnectorKey || access.Provider != old.Provider ||
		access.ProviderRef != old.ProviderRef || access.ProviderRefKind != old.ProviderRefKind ||
		(prepared.input.CreatesReplyChannel && !access.Capabilities.CreatesReplyChannel) {
		return integrationstore.ChannelAccess{}, ToolCallRecord{}, storeerr.ErrUnauthorized
	}
	// Newly granted delegation cannot widen an operation already prepared without it.
	access.Capabilities.CreatesReplyChannel = prepared.input.CreatesReplyChannel
	return access, call, nil
}

func validateManagedChannelAccess(
	access integrationstore.ChannelAccess,
	operation integrationstore.ChannelBindingOperation,
) error {
	if !access.Active || access.IntegrationKind != integrationstore.IntegrationKindManaged ||
		access.IntegrationAppID == uuid.Nil || access.ConnectorKey == "" || access.Provider == "" {
		return storeerr.ErrUnauthorized
	}
	switch operation {
	case integrationstore.ChannelBindingOperationRead:
		if access.Capabilities.Read {
			return nil
		}
	case integrationstore.ChannelBindingOperationSend:
		if access.Capabilities.Send {
			return nil
		}
	}
	return storeerr.ErrUnauthorized
}

func channelOperationToolName(operation integrationstore.ChannelBindingOperation) string {
	switch operation {
	case integrationstore.ChannelBindingOperationRead:
		return toolcatalog.ToolNameReadChannel
	case integrationstore.ChannelBindingOperationSend:
		return toolcatalog.ToolNameSendChannelMessage
	default:
		return ""
	}
}
