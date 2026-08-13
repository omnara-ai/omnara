package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type RecordModelOutputAndCompleteContextInput struct {
	ProjectID          ID
	AgentID            ID
	RuntimeLockID      ID
	ModelCallContextID ID
	ProviderRequestID  string
	// ProviderResponse is consumed inside the completion transaction and
	// never durably stored. Storage and the model package share this type so
	// the envelope shape is checked at compile time (no JSON re-parsing).
	ProviderResponse modelenvelope.ResponseEnvelope
}

type ToolCallBindingInput struct {
	ID             ID
	ProviderCallID string
	Type           string
}

type RecordToolCallSourceAndCompleteContextInput struct {
	ProjectID          ID
	AgentID            ID
	RuntimeLockID      ID
	ModelCallContextID ID
	ProviderRequestID  string
	// ProviderResponse is consumed inside the completion transaction and
	// never durably stored. Storage and the model package share this type so
	// the envelope shape is checked at compile time (no JSON re-parsing).
	ProviderResponse modelenvelope.ResponseEnvelope
	ToolCallBindings []ToolCallBindingInput
}

type boundToolCall struct {
	ID             ID
	ProviderCallID string
	Name           string
	Input          []byte
	Type           string
}

func bindToolCalls(
	envelope modelenvelope.ResponseEnvelope,
	bindings []ToolCallBindingInput,
) ([]boundToolCall, error) {
	if len(bindings) == 0 {
		return nil, errors.New("tool call bindings are required")
	}
	byProviderCallID := make(map[string]ToolCallBindingInput, len(bindings))
	for _, binding := range bindings {
		if binding.ProviderCallID == "" {
			return nil, errors.New("tool call binding provider call id is required")
		}
		switch binding.Type {
		case toolcatalog.ToolTypeBuiltIn, toolcatalog.ToolTypeCustom, toolcatalog.ToolTypeMCP:
		default:
			return nil, fmt.Errorf("unsupported tool call type %q", binding.Type)
		}
		if _, exists := byProviderCallID[binding.ProviderCallID]; exists {
			return nil, fmt.Errorf(
				"duplicate tool call binding for provider call id %q",
				binding.ProviderCallID,
			)
		}
		byProviderCallID[binding.ProviderCallID] = binding
	}

	toolCalls := make([]boundToolCall, 0, len(bindings))
	seenProviderCallIDs := make(map[string]struct{}, len(bindings))
	for _, part := range envelope.Normalized.Content {
		if part.Type != modelenvelope.ResponsePartTypeToolCall {
			continue
		}
		if _, exists := seenProviderCallIDs[part.ProviderCallID]; exists {
			return nil, fmt.Errorf(
				"duplicate provider call id %q in provider response",
				part.ProviderCallID,
			)
		}
		if err := modelenvelope.ValidateToolInput(part.ToolInput); err != nil {
			return nil, fmt.Errorf(
				"provider call id %q input must be a JSON object",
				part.ProviderCallID,
			)
		}
		seenProviderCallIDs[part.ProviderCallID] = struct{}{}
		binding, ok := byProviderCallID[part.ProviderCallID]
		if !ok {
			return nil, fmt.Errorf(
				"provider call id %q has no tool call binding",
				part.ProviderCallID,
			)
		}
		toolCalls = append(toolCalls, boundToolCall{
			ID:             binding.ID,
			ProviderCallID: part.ProviderCallID,
			Name:           part.ToolName,
			Input:          part.ToolInput,
			Type:           binding.Type,
		})
	}
	if len(toolCalls) == 0 {
		return nil, errors.New("provider response contains no tool calls")
	}
	for _, binding := range bindings {
		if _, used := seenProviderCallIDs[binding.ProviderCallID]; !used {
			return nil, fmt.Errorf(
				"tool call binding for provider call id %q has no provider response tool call",
				binding.ProviderCallID,
			)
		}
	}
	return toolCalls, nil
}

func (s *Store) RecordToolCallSourceAndCompleteContext(
	ctx context.Context,
	input RecordToolCallSourceAndCompleteContextInput,
) (events.Event, []ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) {
		return events.Event{}, nil, errors.New(
			"project, agent, runtime lock, and model context are required",
		)
	}
	if err := validateModelResponseEnvelope(input.ProviderResponse); err != nil {
		return events.Event{}, nil, err
	}
	toolCalls, err := bindToolCalls(input.ProviderResponse, input.ToolCallBindings)
	if err != nil {
		return events.Event{}, nil, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return events.Event{}, nil, fmt.Errorf("begin record tool call source: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return events.Event{}, nil, err
	}
	contextRow, err := loadModelCallContextByIDTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return events.Event{}, nil, err
	}
	completionReplay, err := validateNormalModelCallCompletionState(
		contextRow,
		input.ModelCallContextID,
		input.RuntimeLockID,
	)
	if err != nil {
		return events.Event{}, nil, err
	}
	if err := validateResponseEnvelopeForModelCallContext(
		ctx,
		dbsqlc.New(tx),
		input.ProviderResponse,
		contextRow,
	); err != nil {
		return events.Event{}, nil, err
	}
	if completionReplay && !sameSuccessfulModelCallCompletionEvidence(
		contextRow,
		input.ProviderRequestID,
		input.ProviderResponse,
	) {
		return events.Event{}, nil, storeerr.ErrIdempotencyConflict
	}
	authorityInput := modelOutputAuthorityInputFromToolSourceContext(input)
	modelOutput, err := createModelOutputAuthorityTx(ctx, tx, authorityInput)
	if err != nil {
		return events.Event{}, nil, err
	}
	eventRecord, foundExistingEvent, err := loadTypedEventByModelOutputMaybeTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		modelOutput.ID,
	)
	if err != nil {
		return events.Event{}, nil, err
	}
	if !foundExistingEvent {
		eventRecord, err = appendTypedAgentEventTx(
			ctx,
			txNotifications,
			tx,
			AppendTypedAgentEventInput{
				ProjectID:     input.ProjectID,
				AgentID:       input.AgentID,
				TurnID:        modelOutput.TurnID,
				Kind:          events.KindModelOutput,
				ModelOutputID: modelOutput.ID,
			},
		)
		if err != nil {
			return events.Event{}, nil, err
		}
		if err := updateAgentTurnLatestEventTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			modelOutput.TurnID,
			eventRecord.Event.ID,
			NilID,
		); err != nil {
			return events.Event{}, nil, err
		}
	}
	qtx := dbsqlc.New(tx)
	if completionReplay {
		if !foundExistingEvent {
			return events.Event{}, nil, storeerr.ErrIdempotencyConflict
		}
		if err := validateModelOutputContentReplayTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			modelOutput.ID,
			input.ProviderResponse,
		); err != nil {
			return events.Event{}, nil, err
		}
		rows, err := qtx.ListToolCallsForModelContext(
			ctx,
			dbsqlc.ListToolCallsForModelContextParams{
				ProjectID:          input.ProjectID,
				AgentID:            input.AgentID,
				ModelCallContextID: input.ModelCallContextID,
			},
		)
		if err != nil {
			return events.Event{}, nil, fmt.Errorf("list replayed tool calls: %w", err)
		}
		records := make([]ToolCallRecord, 0, len(rows))
		for _, row := range rows {
			records = append(records, toolCallRecordFromContextSQLC(row))
		}
		if !sameBoundToolCallBatch(records, toolCalls) {
			return events.Event{}, nil, storeerr.ErrIdempotencyConflict
		}
		return eventRecord.Event, records, nil
	}
	if foundExistingEvent {
		return events.Event{}, nil, storeerr.ErrIdempotencyConflict
	}

	records := make([]ToolCallRecord, 0, len(toolCalls))
	for _, call := range toolCalls {
		row, err := qtx.InsertToolCall(ctx, dbsqlc.InsertToolCallParams{
			ToolCallID:         sqlcIDFromNil(call.ID),
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			SourceEventID:      eventRecord.Event.ID,
			ModelCallContextID: input.ModelCallContextID,
			RuntimeLockID:      input.RuntimeLockID,
			ProviderCallID:     call.ProviderCallID,
			Name:               call.Name,
			Input:              call.Input,
			Type:               call.Type,
		})
		if err != nil {
			if storeutil.IsUniqueViolation(err) {
				return events.Event{}, nil, storeerr.ErrIdempotencyConflict
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if runtimeErr := agentRuntimeLockActiveTx(
					ctx,
					qtx,
					input.ProjectID,
					input.AgentID,
					input.RuntimeLockID,
				); runtimeErr != nil {
					return events.Event{}, nil, runtimeErr
				}
				return events.Event{}, nil, storeerr.ErrIdempotencyConflict
			}
			return events.Event{}, nil, fmt.Errorf("insert tool call proposal: %w", err)
		}
		record := toolCallRecordFromInsertSQLC(row)
		records = append(records, record)
		txNotifications.AddToolCallUpdate(record.AgentID, record.ID, string(record.State))
	}
	toolCallContentBlockArgsByProviderCallID := make(
		map[string]toolCallContentBlockArgs,
		len(records),
	)
	for _, record := range records {
		toolCallContentBlockArgsByProviderCallID[record.ProviderCallID] = toolCallContentBlockArgs{
			ToolCallID: record.ID,
		}
	}
	contentBlockByProviderCallID, err := createModelOutputContentBlocksTx(
		ctx,
		tx,
		contextRow,
		input.ProviderResponse,
		modelOutput.ID,
		toolCallContentBlockArgsByProviderCallID,
	)
	if err != nil {
		return events.Event{}, nil, err
	}
	for _, record := range records {
		if isNilID(contentBlockByProviderCallID[record.ProviderCallID]) {
			return events.Event{}, nil, fmt.Errorf(
				"tool call %q has no matching tool_call content block in envelope content",
				record.ProviderCallID,
			)
		}
	}
	if err := completeSuccessfulNormalModelCallTx(
		ctx,
		qtx,
		contextRow,
		input.RuntimeLockID,
		input.ProviderRequestID,
		input.ProviderResponse,
	); err != nil {
		return events.Event{}, nil, err
	}
	if err := qtx.MarkAgentWakeup(ctx, dbsqlc.MarkAgentWakeupParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		Metadata:  []byte(`{"reason":"tool_work"}`),
	}); err != nil {
		return events.Event{}, nil, fmt.Errorf("mark tool work wakeup: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "record tool call source"); err != nil {
		return events.Event{}, nil, err
	}
	return eventRecord.Event, records, nil
}

func sameBoundToolCallBatch(
	existing []ToolCallRecord,
	bound []boundToolCall,
) bool {
	if len(existing) != len(bound) {
		return false
	}
	existingByProviderCallID := make(map[string]ToolCallRecord, len(existing))
	for _, record := range existing {
		existingByProviderCallID[record.ProviderCallID] = record
	}
	for _, call := range bound {
		record, ok := existingByProviderCallID[call.ProviderCallID]
		if !ok {
			return false
		}
		if record.ProviderCallID != call.ProviderCallID ||
			record.Name != call.Name ||
			!sameJSON(record.Input, call.Input) ||
			record.Type != call.Type ||
			(!isNilID(call.ID) && record.ID != call.ID) {
			return false
		}
	}
	return true
}

func (s *Store) RecordModelOutputAndCompleteContext(
	ctx context.Context,
	input RecordModelOutputAndCompleteContextInput,
) (events.Event, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.RuntimeLockID) ||
		isNilID(input.ModelCallContextID) {
		return events.Event{}, errors.New(
			"project, agent, runtime lock, and model context are required",
		)
	}
	if err := validateModelResponseEnvelope(input.ProviderResponse); err != nil {
		return events.Event{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return events.Event{}, fmt.Errorf("begin record model output: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return events.Event{}, err
	}
	contextRow, err := loadModelCallContextByIDTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return events.Event{}, err
	}
	completionReplay, err := validateNormalModelCallCompletionState(
		contextRow,
		input.ModelCallContextID,
		input.RuntimeLockID,
	)
	if err != nil {
		return events.Event{}, err
	}
	if err := validateResponseEnvelopeForModelCallContext(
		ctx,
		dbsqlc.New(tx),
		input.ProviderResponse,
		contextRow,
	); err != nil {
		return events.Event{}, err
	}
	if completionReplay && !sameSuccessfulModelCallCompletionEvidence(
		contextRow,
		input.ProviderRequestID,
		input.ProviderResponse,
	) {
		return events.Event{}, storeerr.ErrIdempotencyConflict
	}
	authorityInput := modelOutputAuthorityInputFromContext(input)
	modelOutput, err := createModelOutputAuthorityTx(ctx, tx, authorityInput)
	if err != nil {
		return events.Event{}, err
	}
	if completionReplay {
		if err := validateModelOutputContentReplayTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			modelOutput.ID,
			input.ProviderResponse,
		); err != nil {
			return events.Event{}, err
		}
	}
	eventRecord, foundExistingEvent, err := loadTypedEventByModelOutputMaybeTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		modelOutput.ID,
	)
	if err != nil {
		return events.Event{}, err
	}
	if !foundExistingEvent {
		eventRecord, err = appendTypedAgentEventTx(
			ctx,
			txNotifications,
			tx,
			AppendTypedAgentEventInput{
				ProjectID:     input.ProjectID,
				AgentID:       input.AgentID,
				TurnID:        modelOutput.TurnID,
				Kind:          events.KindModelOutput,
				ModelOutputID: modelOutput.ID,
			},
		)
		if err != nil {
			return events.Event{}, err
		}
		if err := updateAgentTurnLatestEventTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			modelOutput.TurnID,
			eventRecord.Event.ID,
			eventRecord.Event.ID,
		); err != nil {
			return events.Event{}, err
		}
	}
	if hasBlocks, err := modelOutputHasContentBlocksTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		modelOutput.ID,
	); err != nil {
		return events.Event{}, err
	} else if !hasBlocks {
		if _, err := createModelOutputContentBlocksTx(
			ctx,
			tx,
			contextRow,
			input.ProviderResponse,
			modelOutput.ID,
			nil,
		); err != nil {
			return events.Event{}, err
		}
	}
	if !completionReplay {
		if err := completeSuccessfulNormalModelCallTx(
			ctx,
			dbsqlc.New(tx),
			contextRow,
			input.RuntimeLockID,
			input.ProviderRequestID,
			input.ProviderResponse,
		); err != nil {
			return events.Event{}, err
		}
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "record model output"); err != nil {
		return events.Event{}, err
	}
	return eventRecord.Event, nil
}

func validateModelResponseEnvelope(envelope modelenvelope.ResponseEnvelope) error {
	return envelope.Validate()
}

func validateResponseEnvelopeForModelCallContext(
	ctx context.Context,
	q *dbsqlc.Queries,
	envelope modelenvelope.ResponseEnvelope,
	contextRow ModelCallContextRecord,
) error {
	requestedProviderModelSlug, err := q.GetModelCallContextProviderModelSlug(
		ctx,
		dbsqlc.GetModelCallContextProviderModelSlugParams{
			ProjectID:          contextRow.ProjectID,
			AgentID:            contextRow.AgentID,
			ModelCallContextID: contextRow.ID,
		},
	)
	if err != nil {
		return fmt.Errorf("load configured provider model slug: %w", err)
	}
	if envelope.RequestedProviderModelSlug != requestedProviderModelSlug {
		return fmt.Errorf(
			"provider response requested provider model slug %q does not match model call context %q",
			envelope.RequestedProviderModelSlug,
			requestedProviderModelSlug,
		)
	}
	if contextRow.State == ModelCallContextSucceeded &&
		(envelope.APIFormat != contextRow.APIFormat || envelope.APIVariant != contextRow.APIVariant) {
		return fmt.Errorf(
			"provider response route %q/%q does not match completed model call context %q/%q",
			envelope.APIFormat,
			envelope.APIVariant,
			contextRow.APIFormat,
			contextRow.APIVariant,
		)
	}
	return nil
}

func validateNormalModelCallCompletionState(
	contextRow ModelCallContextRecord,
	modelCallContextID, runtimeLockID ID,
) (bool, error) {
	if contextRow.ID != modelCallContextID ||
		contextRow.OperationKind != ModelCallOperationNormal {
		return false, storeerr.ErrStateTransitionConflict
	}
	if contextRow.RuntimeLockID != runtimeLockID {
		return false, storeerr.ErrRuntimeLockInactive
	}
	if contextRow.State == ModelCallContextStarted {
		return false, nil
	}
	if contextRow.State == ModelCallContextSucceeded {
		return true, nil
	}
	return false, storeerr.ErrStateTransitionConflict
}

func sameSuccessfulModelCallCompletionEvidence(
	contextRow ModelCallContextRecord,
	providerRequestID string,
	envelope modelenvelope.ResponseEnvelope,
) bool {
	return contextRow.ProviderRequestID == providerRequestID &&
		contextRow.ProviderReportedCostUSD == envelope.ProviderReportedCostUSD
}

func completeSuccessfulNormalModelCallTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
	runtimeLockID ID,
	providerRequestID string,
	envelope modelenvelope.ResponseEnvelope,
) error {
	if contextRow.OperationKind != ModelCallOperationNormal ||
		contextRow.State != ModelCallContextStarted {
		return storeerr.ErrStateTransitionConflict
	}
	if _, err := finishModelCallContextTx(ctx, q, finishModelCallContextInput{
		ProjectID:               contextRow.ProjectID,
		AgentID:                 contextRow.AgentID,
		ModelCallContextID:      contextRow.ID,
		RuntimeLockID:           runtimeLockID,
		ToState:                 ModelCallContextSucceeded,
		APIFormat:               envelope.APIFormat,
		APIVariant:              envelope.APIVariant,
		ProviderRequestID:       providerRequestID,
		ProviderResponseID:      envelope.Normalized.ID,
		Usage:                   envelope.Normalized.Usage,
		ProviderReportedCostUSD: envelope.ProviderReportedCostUSD,
	}); err != nil {
		return err
	}
	return nil
}
