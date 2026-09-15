package executionstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CompleteExternalChannelRequest(
	ctx context.Context,
	input CompleteExternalChannelRequestInput,
) (CompleteExternalChannelRequestResult, error) {
	request, err := s.GetExternalChannelRequest(ctx, input.ProjectID, input.IntegrationInstallID, input.ID)
	if err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	acceptedResult, err := normalizeExternalChannelResult(input.ID, input.Result)
	if err != nil {
		return CompleteExternalChannelRequestResult{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request, err = s.lockExternalChannelRequestTx(ctx, tx, request)
	if err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	if request.State == ExternalChannelRequestCompleted {
		if !sameJSON(request.Result, acceptedResult) {
			return CompleteExternalChannelRequestResult{}, storeerr.ErrIdempotencyConflict
		}
		// Caller/connection authorization precedes this method. Already accepted
		// results do not revalidate changed definitions or revive revoked grants.
		result := CompleteExternalChannelRequestResult{Request: request, Replayed: true}
		result.Request.Replayed = true
		if !isNilID(request.ToolCallID) {
			call, err := getToolCallTx(ctx, tx, request.ProjectID, request.AgentID, request.ToolCallID)
			if err != nil {
				return CompleteExternalChannelRequestResult{}, err
			}
			result.ToolCall = &call
		}
		return result, nil
	}
	if request.State != ExternalChannelRequestPending {
		return CompleteExternalChannelRequestResult{}, storeerr.ErrStateTransitionConflict
	}
	// Database time is authoritative even if no maintenance tick has run. This
	// transition rolls back with any subsequent authority or envelope rejection.
	row, err := s.q.WithTx(tx).CompleteExternalChannelRequest(ctx, dbsqlc.CompleteExternalChannelRequestParams{
		ProjectID: request.ProjectID, IntegrationInstallID: request.IntegrationInstallID,
		ID: request.ID, Result: acceptedResult,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CompleteExternalChannelRequestResult{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	access, binding, err := s.recheckExternalChannelRequestTx(ctx, tx, request)
	if err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	if request.Operation == channelconnector.OperationInteraction &&
		input.Result.Outcome == channelconnector.OperationCompleted {
		if _, err := channelconnector.DecodeInteractionResult(input.Result.Payload); err != nil {
			return CompleteExternalChannelRequestResult{}, storeerr.InvalidRequest(err)
		}
	}
	var completion ToolCallCompletionInput
	if !isNilID(request.ToolCallID) || request.NoticeKey != "" {
		completion, err = s.channelOperationCompletionTx(ctx, tx, channelOperationCompletionInput{
			RequestID: input.Result.RequestID, Operation: request.Operation, Payload: request.Payload,
			Access: access, Binding: binding, CreatesReplyChannel: request.CreatesReplyChannel, Result: input.Result,
		})
		if err != nil {
			return CompleteExternalChannelRequestResult{}, err
		}
	}
	result := CompleteExternalChannelRequestResult{Request: externalChannelRequestRecord(row)}
	notifications := s.newTxNotifications()
	if !isNilID(request.ToolCallID) {
		call, err := finishExternalChannelToolTx(ctx, tx, notifications, result.Request, completion)
		if err != nil {
			return CompleteExternalChannelRequestResult{}, err
		}
		result.ToolCall = &call
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "complete external channel request"); err != nil {
		return CompleteExternalChannelRequestResult{}, err
	}
	return result, nil
}

func normalizeExternalChannelResult(id ID, result channelconnector.OperationResult) (json.RawMessage, error) {
	requestID, err := publicid.Encode(publicid.KindExternalChannelRequest, id)
	if err != nil || result.RequestID != requestID {
		return nil, errors.New("channel result request ID does not match")
	}
	switch result.Outcome {
	case channelconnector.OperationCompleted, channelconnector.OperationFailed, channelconnector.OperationUnknown:
	default:
		return nil, errors.New("channel result requires a terminal provider outcome")
	}
	if result.Outcome != channelconnector.OperationCompleted {
		if err := validateChannelFailurePayload(result.Payload); err != nil {
			return nil, err
		}
	}
	if len(result.Payload) == 0 {
		result.Payload = json.RawMessage(`{}`)
	}
	object, err := jsoncanonical.ParseObject(result.Payload, int(channelconnector.MaxOperationResponseBytes))
	if err != nil {
		return nil, err
	}
	result.Payload, err = json.Marshal(object)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := dbsafe.JSONB(raw, int(channelconnector.MaxOperationResponseBytes)); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Store) recheckExternalChannelRequestTx(
	ctx context.Context,
	tx pgx.Tx,
	request ExternalChannelRequestRecord,
) (integrationstore.ChannelAccess, integrationstore.IntegrationTargetBindingRecord, error) {
	q := s.q.WithTx(tx)
	row, err := q.GetIntegrationTargetBinding(ctx, dbsqlc.GetIntegrationTargetBindingParams{
		ProjectID: request.ProjectID, ID: request.IntegrationTargetBindingID,
	})
	if err != nil {
		return integrationstore.ChannelAccess{}, integrationstore.IntegrationTargetBindingRecord{},
			externalChannelRequestError(err)
	}
	binding := integrationstore.IntegrationTargetBindingRecord{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID,
		IntegrationInstallID: row.IntegrationInstallID, IntegrationTargetID: row.IntegrationTargetID,
		ReceiveAllowed: row.ReceiveAllowed, ReadAllowed: row.ReadAllowed, SendAllowed: row.SendAllowed,
	}
	if row.ReplyReceiveAllowed != nil {
		binding.ReplyChannelGrants = &integrationstore.ChannelGrants{
			ReceiveAllowed: *row.ReplyReceiveAllowed, ReadAllowed: *row.ReplyReadAllowed, SendAllowed: *row.ReplySendAllowed,
		}
	}
	binding, err = s.integrations.RecheckChannelBindingTx(ctx, tx, externalChannelBindingInput(request), binding)
	if err != nil {
		return integrationstore.ChannelAccess{}, integrationstore.IntegrationTargetBindingRecord{}, err
	}
	if request.Operation == channelconnector.OperationSend {
		var grants *integrationstore.ChannelGrants
		if request.CreatesReplyChannel {
			grants = binding.ReplyChannelGrants
		}
		if _, err := pinExternalReplyGrants(request.Payload, grants); err != nil {
			return integrationstore.ChannelAccess{}, binding, storeerr.ErrUnauthorized
		}
	}
	access, err := s.integrations.GetAgentChannelAccessTx(ctx, tx,
		request.ProjectID, request.AgentID, request.IntegrationTargetID)
	if err != nil {
		return access, binding, err
	}
	if err := validateExternalChannelAccess(ctx, q, request, access); err != nil {
		return access, binding, err
	}
	if !isNilID(request.ToolCallID) {
		call, err := getToolCallTx(ctx, tx, request.ProjectID, request.AgentID, request.ToolCallID)
		if err != nil {
			return access, binding, err
		}
		if call.State != ToolCallStateWaiting || call.TurnID != request.TurnID ||
			call.Name != channelOperationToolName(externalChannelBindingOperation(request.Operation)) {
			return access, binding, storeerr.ErrStateTransitionConflict
		}
	}
	return access, binding, nil
}
