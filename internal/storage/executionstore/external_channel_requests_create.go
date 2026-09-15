package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CreateExternalChannelRequestForToolCall gives a real builtin a durable owner
// before releasing runtime ownership. Polling and completion use this same row
// across worker restarts; applying the command again never extends its deadline.
func CreateExternalChannelRequestForToolCall(input CreateExternalChannelRequestInput) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, t *toolCallTransaction) (any, error) {
		if input.TurnID == uuid.Nil || input.ChannelID == uuid.Nil ||
			(input.Operation != channelconnector.OperationSend && input.Operation != channelconnector.OperationRead) {
			return nil, storeerr.InvalidRequest(errors.New("channel, turn and builtin operation are required"))
		}
		payload, err := normalizeExternalChannelPayload(input.Payload)
		if err != nil {
			return nil, err
		}
		request := ExternalChannelRequestRecord{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, TurnID: input.TurnID,
			ToolCallID: t.input.ToolCallID, IntegrationTargetID: input.ChannelID,
			Operation: input.Operation, Payload: payload,
			CreatesReplyChannel: input.CreatesReplyChannel,
		}
		existing, err := externalChannelRequestByOwner(ctx, t.q, request)
		if err == nil {
			if !sameExternalChannelRequest(existing, request) {
				return nil, storeerr.ErrIdempotencyConflict
			}
			t.hasDurableCompletionOwner = true
			if err := t.lockOrAcceptExisting(ctx); err != nil {
				return nil, err
			}
			existing.Replayed = true
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		request, err = t.store.prepareExternalChannelRequestTx(ctx, t.tx, request, input.Timeout)
		if err != nil {
			return nil, err
		}
		if err := t.lockForMutation(ctx); err != nil {
			return nil, err
		}
		call, err := getToolCallTx(ctx, t.tx, request.ProjectID, request.AgentID, request.ToolCallID)
		if err != nil {
			return nil, err
		}
		if call.TurnID != request.TurnID ||
			call.Name != channelOperationToolName(externalChannelBindingOperation(request.Operation)) {
			return nil, storeerr.ErrStateTransitionConflict
		}
		request, err = insertExternalChannelRequest(ctx, t.q, request, input.Timeout)
		if err != nil {
			return nil, err
		}
		t.hasDurableCompletionOwner = true
		t.requiresWaitingDisposition = true
		return request, t.startToolCall(ctx, false)
	})
}

// CreateExternalChannelPresentation retains only the presentation obligation.
// Its canonical interaction is already durable and remains the answer owner.
func (s *Store) CreateExternalChannelPresentation(
	ctx context.Context,
	input CreateExternalChannelPresentationInput,
) (ExternalChannelRequestRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.InteractionID == uuid.Nil {
		return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(errors.New("canonical interaction is required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	interaction, err := q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.InteractionID,
	})
	if err != nil {
		return ExternalChannelRequestRecord{}, externalChannelRequestError(err)
	}
	request := ExternalChannelRequestRecord{
		ProjectID: input.ProjectID, AgentID: input.AgentID, TurnID: interaction.TurnID,
		InteractionID: input.InteractionID, IntegrationTargetID: storeutil.IDFromPtr(interaction.IntegrationTargetID),
		Operation: channelconnector.OperationInteraction, Payload: input.Payload,
	}
	return s.createExternalChannelNoticeOrPresentationTx(ctx, tx, request, uuid.Nil, input.Timeout)
}

func (s *Store) CreateExternalChannelNotice(
	ctx context.Context,
	input CreateExternalChannelNoticeInput,
) (ExternalChannelRequestRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.TurnID == uuid.Nil ||
		input.ChannelID == uuid.Nil || input.RuntimeLockID == uuid.Nil ||
		input.NoticeKey == "" || len(input.NoticeKey) > 128 {
		return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(errors.New("runtime notice owner is required"))
	}
	if err := dbsafe.Text(input.NoticeKey); err != nil {
		return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return s.createExternalChannelNoticeOrPresentationTx(ctx, tx, ExternalChannelRequestRecord{
		ProjectID: input.ProjectID, AgentID: input.AgentID, TurnID: input.TurnID,
		NoticeKey: input.NoticeKey, IntegrationTargetID: input.ChannelID,
		Operation: channelconnector.OperationSend, Payload: input.Payload,
	}, input.RuntimeLockID, input.Timeout)
}

func (s *Store) createExternalChannelNoticeOrPresentationTx(
	ctx context.Context,
	tx pgx.Tx,
	request ExternalChannelRequestRecord,
	runtimeLockID uuid.UUID,
	timeout time.Duration,
) (ExternalChannelRequestRecord, error) {
	var err error
	request.Payload, err = normalizeExternalChannelPayload(request.Payload)
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	q := s.q.WithTx(tx)
	existing, err := externalChannelRequestByOwner(ctx, q, request)
	if err == nil {
		if !sameExternalChannelRequest(existing, request) {
			return ExternalChannelRequestRecord{}, storeerr.ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExternalChannelRequestRecord{}, err
	}
	request, err = s.prepareExternalChannelRequestTx(ctx, tx, request, timeout)
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	// Preparation takes the agent lock. Another creator may have committed the
	// same owner while this transaction waited for that lock.
	existing, err = externalChannelRequestByOwner(ctx, q, request)
	if err == nil {
		if !sameExternalChannelRequest(existing, request) {
			return ExternalChannelRequestRecord{}, storeerr.ErrIdempotencyConflict
		}
		existing.Replayed = true
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ExternalChannelRequestRecord{}, err
	}
	if runtimeLockID != uuid.Nil {
		if err := ensureRuntimeLockActiveTx(ctx, tx, request.ProjectID, request.AgentID, runtimeLockID); err != nil {
			return ExternalChannelRequestRecord{}, err
		}
	}
	request, err = insertExternalChannelRequest(ctx, q, request, timeout)
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	return request, nil
}

func (s *Store) prepareExternalChannelRequestTx(
	ctx context.Context,
	tx pgx.Tx,
	request ExternalChannelRequestRecord,
	timeout time.Duration,
) (ExternalChannelRequestRecord, error) {
	if err := validateExternalChannelTimeout(timeout); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	// Discover only immutable connection ownership before entering its lifecycle
	// gate. The authorization/definition read follows binding preparation.
	target, err := s.integrations.GetIntegrationTargetTx(
		ctx, tx, request.ProjectID, request.IntegrationTargetID,
	)
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	request.IntegrationInstallID = target.IntegrationInstallID
	binding, err := s.integrations.PrepareChannelBindingTx(ctx, tx, externalChannelBindingInput(request))
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	request.IntegrationTargetBindingID = binding.ID
	if request.Operation == channelconnector.OperationSend {
		var grants *integrationstore.ChannelGrants
		if request.CreatesReplyChannel {
			grants = binding.ReplyChannelGrants
		}
		request.Payload, err = pinExternalReplyGrants(request.Payload, grants)
		if err != nil {
			return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(err)
		}
	}
	access, err := s.integrations.GetAgentChannelAccessTx(
		ctx, tx, request.ProjectID, request.AgentID, request.IntegrationTargetID,
	)
	if err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := validateExternalChannelAccess(ctx, s.q.WithTx(tx), request, access); err != nil {
		return ExternalChannelRequestRecord{}, err
	}
	if err := validateExternalChannelAcceptedPayload(ctx, s.q.WithTx(tx), request, access); err != nil {
		return ExternalChannelRequestRecord{}, storeerr.InvalidRequest(err)
	}
	return request, nil
}

func externalChannelBindingOperation(
	operation channelconnector.OperationKind,
) integrationstore.ChannelBindingOperation {
	if operation == channelconnector.OperationRead {
		return integrationstore.ChannelBindingOperationRead
	}
	return integrationstore.ChannelBindingOperationSend
}

func externalChannelBindingInput(request ExternalChannelRequestRecord) integrationstore.PrepareChannelBindingInput {
	return integrationstore.PrepareChannelBindingInput{
		ProjectID: request.ProjectID, AgentID: request.AgentID,
		IntegrationInstallID: request.IntegrationInstallID, IntegrationTargetID: request.IntegrationTargetID,
		Operation: externalChannelBindingOperation(request.Operation), CreatesReplyChannel: request.CreatesReplyChannel,
	}
}

func sameExternalChannelRequest(a, b ExternalChannelRequestRecord) bool {
	if b.Operation == channelconnector.OperationSend {
		var pinned *integrationstore.ChannelGrants
		if grants := acceptedExternalReplyGrants(a.Payload); b.CreatesReplyChannel && grants != nil {
			pinned = &integrationstore.ChannelGrants{
				ReceiveAllowed: grants.Receive, ReadAllowed: grants.Read, SendAllowed: grants.Send,
			}
		}
		var err error
		b.Payload, err = pinExternalReplyGrants(b.Payload, pinned)
		if err != nil {
			return false
		}
	}
	return a.ProjectID == b.ProjectID && a.AgentID == b.AgentID && a.TurnID == b.TurnID &&
		a.ToolCallID == b.ToolCallID && a.InteractionID == b.InteractionID && a.NoticeKey == b.NoticeKey &&
		a.IntegrationTargetID == b.IntegrationTargetID && a.Operation == b.Operation &&
		a.CreatesReplyChannel == b.CreatesReplyChannel && sameJSON(a.Payload, b.Payload)
}

func externalChannelRequestError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	return fmt.Errorf("external channel request: %w", err)
}
