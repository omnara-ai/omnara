package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type RecordModelCallErrorAndCompleteContextInput struct {
	ProjectID               ID
	AgentID                 ID
	RuntimeLockID           ID
	ModelCallContextID      ID
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ServedProviderModelSlug string
	ProviderRequestID       string
	ProviderResponseID      string
	ErrorKind               modelprotocol.ErrorKind
	ErrorCode               string
	ErrorMessage            string
	ErrorDetails            json.RawMessage
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
}

func (s *Store) RecordModelCallErrorAndCompleteContext(
	ctx context.Context,
	input RecordModelCallErrorAndCompleteContextInput,
) (events.Event, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) ||
		input.ErrorKind == "" || input.ErrorMessage == "" {
		return events.Event{}, errors.New(
			"project, agent, runtime, context, and error are required",
		)
	}
	var err error
	input.ErrorDetails, err = normalizedJSONObject(input.ErrorDetails, "model call error details")
	if err != nil {
		return events.Event{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return events.Event{}, fmt.Errorf("begin record model call error: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return events.Event{}, err
	}
	result, err := recordTerminalModelCallFailureTx(
		ctx,
		txNotifications,
		tx,
		q,
		input,
		modelCallContextRuntimeOwned,
		ModelCallOperationNormal,
	)
	if err != nil {
		return events.Event{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "record model call error"); err != nil {
		return events.Event{}, err
	}
	return result.event, nil
}

type terminalModelCallFailureResult struct {
	context ModelCallContextRecord
	event   events.Event
}

func recordTerminalModelCallFailureTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	input RecordModelCallErrorAndCompleteContextInput,
	runtimeAuthority modelCallContextRuntimeAuthority,
	operationKind ModelCallOperation,
) (terminalModelCallFailureResult, error) {
	contextRow, err := loadModelCallContextByIDTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return terminalModelCallFailureResult{}, err
	}
	if contextRow.OperationKind != operationKind {
		return terminalModelCallFailureResult{}, storeerr.ErrStateTransitionConflict
	}
	if contextRow.RuntimeLockID != input.RuntimeLockID {
		return terminalModelCallFailureResult{}, storeerr.ErrRuntimeLockInactive
	}
	normalizedUsage := modelUsageForStorage(input.Usage)
	if err := validateModelCallFailureEvidence(
		input.APIFormat,
		input.APIVariant,
		input.ServedProviderModelSlug,
		input.ProviderRequestID,
		input.ProviderResponseID,
		normalizedUsage != (modelenvelope.Usage{}),
		input.ProviderReportedCostUSD,
	); err != nil {
		return terminalModelCallFailureResult{}, err
	}
	if contextRow.State == ModelCallContextFailed {
		if !sameTerminalModelCallErrorContextIntent(contextRow, input, normalizedUsage) {
			return terminalModelCallFailureResult{}, storeerr.ErrIdempotencyConflict
		}
		event, replayErr := replayModelCallErrorOutputTx(ctx, tx, q, contextRow, input)
		if replayErr != nil {
			return terminalModelCallFailureResult{}, replayErr
		}
		return terminalModelCallFailureResult{context: contextRow, event: event.Event}, nil
	}
	if contextRow.State != ModelCallContextStarted {
		return terminalModelCallFailureResult{}, storeerr.ErrRuntimeLockInactive
	}
	finishedContext, err := finishModelCallContextWithAuthorityTx(ctx, q, finishModelCallContextInput{
		ProjectID:               input.ProjectID,
		AgentID:                 input.AgentID,
		ModelCallContextID:      input.ModelCallContextID,
		RuntimeLockID:           input.RuntimeLockID,
		ToState:                 ModelCallContextFailed,
		APIFormat:               input.APIFormat,
		APIVariant:              input.APIVariant,
		ProviderRequestID:       input.ProviderRequestID,
		ProviderResponseID:      input.ProviderResponseID,
		ErrorKind:               input.ErrorKind,
		ErrorCode:               input.ErrorCode,
		ErrorMessage:            input.ErrorMessage,
		ErrorDetails:            input.ErrorDetails,
		Usage:                   normalizedUsage,
		ProviderReportedCostUSD: input.ProviderReportedCostUSD,
	}, runtimeAuthority)
	if err != nil {
		return terminalModelCallFailureResult{}, err
	}
	eventRecord, err := publishModelCallErrorOutputTx(
		ctx,
		txNotifications,
		tx,
		q,
		finishedContext,
		modelCallErrorOutputInput{
			ServedProviderModelSlug: input.ServedProviderModelSlug,
			ErrorMessage:            input.ErrorMessage,
			Usage:                   normalizedUsage,
		},
	)
	if err != nil {
		return terminalModelCallFailureResult{}, err
	}
	return terminalModelCallFailureResult{
		context: finishedContext,
		event:   eventRecord.Event,
	}, nil
}

func sameTerminalModelCallErrorContextIntent(
	contextRecord ModelCallContextRecord,
	input RecordModelCallErrorAndCompleteContextInput,
	usage modelenvelope.Usage,
) bool {
	return contextRecord.State == ModelCallContextFailed &&
		contextRecord.RecoveryKind == "" &&
		contextRecord.APIFormat == input.APIFormat &&
		contextRecord.APIVariant == input.APIVariant &&
		contextRecord.ProviderRequestID == input.ProviderRequestID &&
		contextRecord.ProviderResponseID == input.ProviderResponseID &&
		contextRecord.ErrorKind == input.ErrorKind &&
		contextRecord.ErrorCode == input.ErrorCode &&
		contextRecord.ErrorMessage == input.ErrorMessage &&
		sameJSON(contextRecord.ErrorDetails, input.ErrorDetails) &&
		contextRecord.RetryAt == nil &&
		contextRecord.Usage == usage &&
		contextRecord.ProviderReportedCostUSD == input.ProviderReportedCostUSD
}

type modelCallErrorOutputInput struct {
	ServedProviderModelSlug string
	ErrorMessage            string
	Usage                   modelenvelope.Usage
}

func publishModelCallErrorOutputTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
	input modelCallErrorOutputInput,
) (TypedAgentEventRecord, error) {
	authorityInput := modelCallErrorAuthorityInput(contextRow, input)
	modelOutput, err := createModelOutputAuthorityTx(ctx, tx, authorityInput)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	eventRecord, err := appendTypedAgentEventTx(ctx, txNotifications, tx, AppendTypedAgentEventInput{
		ProjectID:     contextRow.ProjectID,
		AgentID:       contextRow.AgentID,
		TurnID:        modelOutput.TurnID,
		Kind:          events.KindModelOutput,
		ModelOutputID: modelOutput.ID,
	})
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	if _, err := createContentBlockTx(ctx, tx, CreateContentBlockInput{
		ProjectID:          contextRow.ProjectID,
		AgentID:            contextRow.AgentID,
		OwnerKind:          ContentBlockOwnerModelOutput,
		OwnerModelOutputID: modelOutput.ID,
		BlockKind:          ContentBlockKindError,
		TextContent:        input.ErrorMessage,
	}); err != nil {
		return TypedAgentEventRecord{}, err
	}
	if err := updateAgentTurnLatestEventTx(
		ctx,
		tx,
		contextRow.ProjectID,
		contextRow.AgentID,
		modelOutput.TurnID,
		eventRecord.Event.ID,
		eventRecord.Event.ID,
	); err != nil {
		return TypedAgentEventRecord{}, err
	}
	if err := q.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: contextRow.ProjectID,
		AgentID:   contextRow.AgentID,
		Metadata:  json.RawMessage(`{"reason":"model_call_error"}`),
	}); err != nil {
		return TypedAgentEventRecord{}, fmt.Errorf("reconcile wakeup after model call error: %w", err)
	}
	return eventRecord, nil
}

func modelCallErrorAuthorityInput(
	contextRow ModelCallContextRecord,
	input modelCallErrorOutputInput,
) CreateModelOutputAuthorityInput {
	return CreateModelOutputAuthorityInput{
		ProjectID:               contextRow.ProjectID,
		AgentID:                 contextRow.AgentID,
		ModelCallContextID:      contextRow.ID,
		ServedProviderModelSlug: input.ServedProviderModelSlug,
		StopReason:              modelenvelope.StopReasonError,
		Usage:                   input.Usage,
	}
}

func replayModelCallErrorOutputTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
	input RecordModelCallErrorAndCompleteContextInput,
) (TypedAgentEventRecord, error) {
	authorityInput := modelCallErrorAuthorityInput(contextRow, modelCallErrorOutputInput{
		ServedProviderModelSlug: input.ServedProviderModelSlug,
		ErrorMessage:            input.ErrorMessage,
		Usage:                   input.Usage,
	})
	modelOutput, err := createModelOutputAuthorityTx(ctx, tx, authorityInput)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	eventRecord, found, err := loadTypedEventByModelOutputMaybeTx(
		ctx,
		tx,
		contextRow.ProjectID,
		contextRow.AgentID,
		modelOutput.ID,
	)
	if err != nil {
		return TypedAgentEventRecord{}, err
	}
	if !found || eventRecord.Event.Kind != events.KindModelOutput ||
		eventRecord.ModelOutputID != modelOutput.ID {
		return TypedAgentEventRecord{}, storeerr.ErrIdempotencyConflict
	}
	blocks, err := q.ListContentBlocksForModelOutput(ctx, dbsqlc.ListContentBlocksForModelOutputParams{
		ProjectID:     contextRow.ProjectID,
		AgentID:       contextRow.AgentID,
		ModelOutputID: sqlcIDFromNil(modelOutput.ID),
	})
	if err != nil {
		return TypedAgentEventRecord{}, fmt.Errorf("list terminal model error content blocks for replay: %w", err)
	}
	if len(blocks) != 1 || blocks[0].Ordinal != 0 || blocks[0].BlockKind != string(ContentBlockKindError) ||
		blocks[0].TextContent != input.ErrorMessage || blocks[0].ArtifactID != nil || blocks[0].ToolCallID != nil {
		return TypedAgentEventRecord{}, storeerr.ErrIdempotencyConflict
	}
	return eventRecord, nil
}
