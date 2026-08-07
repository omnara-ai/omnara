package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type CompleteToolCallInput struct {
	ProjectID          ID
	AgentID            ID
	ID                 ID
	Outcome            ToolResultOutcome
	RuntimeLockID      ID
	ResultContentParts json.RawMessage
}

type ToolCallCompletionInput struct {
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

type CompleteRuntimeToolCallInput struct {
	ProjectID          ID
	AgentID            ID
	ID                 ID
	RuntimeLockID      ID
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

type CompleteCustomToolCallInput struct {
	ProjectID     ID
	AgentID       ID
	ID            ID
	Outcome       ToolResultOutcome
	ContentBlocks json.RawMessage
}

type CompleteCustomToolCallResult struct {
	ToolCall      ToolCallRecord
	Event         TypedAgentEventRecord
	ContentBlocks json.RawMessage
}

type MarkToolCallReadyInput struct {
	ProjectID     ID
	AgentID       ID
	ID            ID
	RuntimeLockID ID
}

type ReleaseToolCallRuntimeOwnershipInput struct {
	ProjectID     ID
	AgentID       ID
	ToolCallID    ID
	RuntimeLockID ID
}

type RequeueRuntimeToolCallInput struct {
	ProjectID     ID
	AgentID       ID
	ToolCallID    ID
	RuntimeLockID ID
}

type ListToolCallsInput struct {
	ProjectID ID
	AgentID   ID
	State     ToolCallState
	Type      string
	Limit     int
	After     listing.KeysetCursor
}

type ListToolCallsResult struct {
	ToolCalls []ToolCallRecord
	HasMore   bool
}

type ToolCallState string

const (
	ToolCallStateAwaitingAuthorization ToolCallState = "awaiting_authorization"
	ToolCallStateAwaitingPermission    ToolCallState = "awaiting_permission"
	ToolCallStateReady                 ToolCallState = "ready"
	ToolCallStateRunning               ToolCallState = "running"
	ToolCallStateWaiting               ToolCallState = "waiting"
	ToolCallStateCompleted             ToolCallState = "completed"
)

type ToolResultOutcome string

const (
	ToolResultOutcomeSucceeded ToolResultOutcome = "succeeded"
	ToolResultOutcomeFailed    ToolResultOutcome = "failed"
	ToolResultOutcomeDenied    ToolResultOutcome = "denied"
	ToolResultOutcomeCanceled  ToolResultOutcome = "canceled"
)

func (outcome ToolResultOutcome) IsTerminal() bool {
	switch outcome {
	case ToolResultOutcomeSucceeded,
		ToolResultOutcomeFailed,
		ToolResultOutcomeDenied,
		ToolResultOutcomeCanceled:
		return true
	default:
		return false
	}
}

type ToolCallRecord struct {
	ID                 ID              `json:"id"`
	ProjectID          ID              `json:"project_id"`
	AgentID            ID              `json:"agent_id"`
	TurnID             ID              `json:"turn_id"`
	SourceEventID      ID              `json:"source_event_id"`
	ModelCallContextID ID              `json:"model_call_context_id"`
	ProviderCallID     string          `json:"provider_call_id"`
	Name               string          `json:"name"`
	Input              json.RawMessage `json:"input"`
	Type               string          `json:"type"`
	CreatedAt          time.Time       `json:"created_at"`

	State         ToolCallState     `json:"state"`
	Outcome       ToolResultOutcome `json:"outcome,omitempty"`
	RuntimeLockID ID                `json:"-"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`

	ToolCallResultID        ID              `json:"tool_call_result_id,omitempty"`
	ToolResultEventID       ID              `json:"tool_result_event_id,omitempty"`
	SourceEventSequence     int64           `json:"source_event_sequence,omitempty"`
	ToolResultEventSequence int64           `json:"tool_result_event_sequence,omitempty"`
	ResultContentParts      json.RawMessage `json:"result_content_parts"`
}

func (s *Store) CompleteToolCall(
	ctx context.Context,
	input CompleteToolCallInput,
) (ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) ||
		isNilID(input.RuntimeLockID) {
		return ToolCallRecord{}, errors.New(
			"project, agent, tool call id, and runtime lock are required",
		)
	}
	if !input.Outcome.IsTerminal() {
		return ToolCallRecord{}, fmt.Errorf("invalid tool result outcome %q", input.Outcome)
	}
	ctx, artifactScope := withArtifactRollbackScope(ctx)
	defer artifactScope.rollback(ctx)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("begin complete tool call: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	prepared, err := s.prepareToolResultByID(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ID,
		input.Outcome,
		input.ResultContentParts,
	)
	if err != nil {
		return ToolCallRecord{}, err
	}
	input.ResultContentParts = prepared
	record, err := completeToolCallTx(ctx, txNotifications, tx, input)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete tool call"); err != nil {
		return ToolCallRecord{}, err
	}
	artifactScope.commit()
	return record, nil
}

func (s *Store) MarkToolCallReady(
	ctx context.Context,
	input MarkToolCallReadyInput,
) (ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) ||
		isNilID(input.RuntimeLockID) {
		return ToolCallRecord{}, errors.New(
			"project, agent, tool call id, and runtime lock are required",
		)
	}
	tx, qtx, err := s.beginAgentRuntimeOwnedMutation(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	)
	if err != nil {
		return ToolCallRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := qtx.MarkToolCallReady(
		ctx,
		dbsqlc.MarkToolCallReadyParams{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			ID:            input.ID,
			RuntimeLockID: input.RuntimeLockID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, loadErr := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
		if loadErr != nil {
			return ToolCallRecord{}, loadErr
		}
		if err := agentRuntimeLockActiveTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			input.RuntimeLockID,
		); err != nil {
			return ToolCallRecord{}, err
		}
		if existing.State == ToolCallStateReady && existing.CompletedAt == nil {
			if err := tx.Commit(ctx); err != nil {
				return ToolCallRecord{}, err
			}
			return existing, nil
		}
		return ToolCallRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("mark tool call ready: %w", err)
	}
	record := toolCallRecordFromReadySQLC(row)
	if err := tx.Commit(ctx); err != nil {
		return ToolCallRecord{}, err
	}
	return record, nil
}

func (s *Store) ReleaseToolCallRuntimeOwnership(
	ctx context.Context,
	input ReleaseToolCallRuntimeOwnershipInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ToolCallID) ||
		isNilID(input.RuntimeLockID) {
		return errors.New("project, agent, tool call, and runtime lock are required")
	}
	tx, qtx, err := s.beginAgentRuntimeOwnedMutation(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := qtx.ReleaseToolCallRuntimeOwnership(
		ctx,
		dbsqlc.ReleaseToolCallRuntimeOwnershipParams{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			ID:            input.ToolCallID,
			RuntimeLockID: input.RuntimeLockID,
		},
	)
	if err != nil {
		return fmt.Errorf("release tool call runtime ownership: %w", err)
	}
	if changed == 0 {
		existing, err := qtx.GetToolCallDispatchState(
			ctx,
			dbsqlc.GetToolCallDispatchStateParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        input.ToolCallID,
			},
		)
		if err != nil {
			return fmt.Errorf("load tool call after runtime ownership release miss: %w", err)
		}
		alreadyReleased := existing.State == string(ToolCallStateCompleted) ||
			existing.State == string(ToolCallStateWaiting)
		if !alreadyReleased {
			if existing.State == string(ToolCallStateRunning) &&
				existing.RuntimeLockID != nil &&
				*existing.RuntimeLockID == input.RuntimeLockID {
				return storeerr.ErrInvalidToolCallDisposition
			}
			return storeerr.ErrIdempotencyConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tool call runtime ownership release: %w", err)
	}
	return nil
}

func (s *Store) RequeueRuntimeToolCall(
	ctx context.Context,
	input RequeueRuntimeToolCallInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ToolCallID) ||
		isNilID(input.RuntimeLockID) {
		return errors.New("project, agent, tool call, and runtime lock are required")
	}
	tx, qtx, err := s.beginAgentRuntimeOwnedMutation(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changed, err := qtx.RequeueRuntimeToolCall(
		ctx,
		dbsqlc.RequeueRuntimeToolCallParams{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			ID:            input.ToolCallID,
			RuntimeLockID: input.RuntimeLockID,
		},
	)
	if err != nil {
		return fmt.Errorf("requeue runtime tool call: %w", err)
	}
	if changed == 0 {
		existing, err := qtx.GetToolCallDispatchState(
			ctx,
			dbsqlc.GetToolCallDispatchStateParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        input.ToolCallID,
			},
		)
		if err != nil {
			return fmt.Errorf("load tool call after requeue miss: %w", err)
		}
		if existing.State != string(ToolCallStateReady) ||
			existing.RuntimeLockID != nil {
			return storeerr.ErrStateTransitionConflict
		}
	}
	metadata, err := marshalJSON(map[string]any{
		"reason":       "async_preflight_retry",
		"tool_call_id": input.ToolCallID,
	})
	if err != nil {
		return fmt.Errorf("marshal requeued runtime tool wakeup: %w", err)
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			Metadata:  metadata,
		},
	); err != nil {
		return fmt.Errorf("mark requeued runtime tool wakeup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit requeued runtime tool call: %w", err)
	}
	return nil
}

func completeToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input CompleteToolCallInput,
) (ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) ||
		isNilID(input.RuntimeLockID) {
		return ToolCallRecord{}, errors.New(
			"project, agent, tool call id, and runtime lock are required",
		)
	}
	if !input.Outcome.IsTerminal() {
		return ToolCallRecord{}, fmt.Errorf("invalid tool result outcome %q", input.Outcome)
	}
	if len(input.ResultContentParts) == 0 {
		input.ResultContentParts = json.RawMessage(`[]`)
	}
	blocks, err := parseToolResultContentBlocks(input.ResultContentParts)
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("invalid model-visible content parts: %w", err)
	}
	normalizedParts, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("canonicalize model-visible content parts: %w", err)
	}
	input.ResultContentParts = normalizedParts
	qtx := dbsqlc.New(tx)
	if err := lockAgentRuntimeForOwnedMutationTx(
		ctx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ToolCallRecord{}, err
	}
	row, err := qtx.CompleteToolCall(
		ctx,
		dbsqlc.CompleteToolCallParams{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			ID:            input.ID,
			RuntimeLockID: input.RuntimeLockID,
			Outcome:       string(input.Outcome),
		})
	if err == nil {
		record := toolCallRecordFromCompleteSQLC(row)
		record.ResultContentParts = input.ResultContentParts
		admitted, err := appendToolResultRecordTx(
			ctx,
			txNotifications,
			tx,
			record,
		)
		if err != nil {
			return ToolCallRecord{}, err
		}
		applyAdmittedToolResult(&record, admitted)
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, fmt.Errorf("complete tool call: %w", err)
	}
	existing, loadErr := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if loadErr != nil {
		return ToolCallRecord{}, loadErr
	}
	if err := agentRuntimeLockActiveTx(
		ctx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ToolCallRecord{}, err
	}
	if toolcatalog.IsPlatformManagedToolType(existing.Type) &&
		existing.State == ToolCallStateCompleted &&
		existing.Outcome == input.Outcome {
		ok, err := completedToolCallMatchesTx(
			ctx,
			dbsqlc.New(tx),
			input.ProjectID,
			input.AgentID,
			input.ID,
			input.Outcome,
			input.ResultContentParts,
		)
		if err != nil {
			return ToolCallRecord{}, err
		}
		if !ok {
			return ToolCallRecord{}, storeerr.ErrIdempotencyConflict
		}
		return existing, nil
	}
	return ToolCallRecord{}, storeerr.ErrStateTransitionConflict
}

func (s *Store) CompleteRuntimeToolCall(
	ctx context.Context,
	input CompleteRuntimeToolCallInput,
) (ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) ||
		isNilID(input.RuntimeLockID) {
		return ToolCallRecord{}, errors.New(
			"project, agent, tool call id, and runtime lock are required",
		)
	}
	if !input.Outcome.IsTerminal() || input.Outcome == ToolResultOutcomeDenied {
		return ToolCallRecord{}, fmt.Errorf(
			"invalid runtime tool result outcome %q",
			input.Outcome,
		)
	}
	ctx, artifactScope := withArtifactRollbackScope(ctx)
	defer artifactScope.rollback(ctx)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ToolCallRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	prepared, err := s.prepareToolResultByID(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ID,
		input.Outcome,
		input.ResultContentParts,
	)
	if err != nil {
		return ToolCallRecord{}, err
	}
	input.ResultContentParts = prepared
	record, err := completeRuntimeToolCallTx(ctx, txNotifications, tx, input)
	if err != nil {
		return ToolCallRecord{}, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"complete runtime tool call",
	); err != nil {
		return ToolCallRecord{}, err
	}
	artifactScope.commit()
	return record, nil
}

func completeRuntimeToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input CompleteRuntimeToolCallInput,
) (ToolCallRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) ||
		isNilID(input.RuntimeLockID) {
		return ToolCallRecord{}, errors.New(
			"project, agent, tool call id, and runtime lock are required",
		)
	}
	if !input.Outcome.IsTerminal() || input.Outcome == ToolResultOutcomeDenied {
		return ToolCallRecord{}, fmt.Errorf(
			"invalid runtime tool result outcome %q",
			input.Outcome,
		)
	}
	if len(input.ResultContentParts) == 0 {
		input.ResultContentParts = json.RawMessage(`[]`)
	}
	blocks, err := parseToolResultContentBlocks(input.ResultContentParts)
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("invalid model-visible content parts: %w", err)
	}
	normalizedParts, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("canonicalize model-visible content parts: %w", err)
	}
	input.ResultContentParts = normalizedParts
	qtx := dbsqlc.New(tx)
	if err := lockAgentRuntimeForOwnedMutationTx(
		ctx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ToolCallRecord{}, err
	}
	row, err := qtx.CompleteRuntimeToolCall(
		ctx,
		dbsqlc.CompleteRuntimeToolCallParams{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			ID:            input.ID,
			RuntimeLockID: input.RuntimeLockID,
			Outcome:       string(input.Outcome),
		},
	)
	if err == nil {
		return finishCompletedToolCallTx(
			ctx,
			txNotifications,
			tx,
			qtx,
			toolCallRecordFromRuntimeCompleteSQLC(row),
			toolCallResultInput{
				Outcome:            input.Outcome,
				ResultContentParts: input.ResultContentParts,
			},
		)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, fmt.Errorf("complete runtime tool call: %w", err)
	}
	existing, loadErr := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if loadErr != nil {
		return ToolCallRecord{}, loadErr
	}
	if err := agentRuntimeLockActiveTx(
		ctx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return ToolCallRecord{}, err
	}
	if toolcatalog.IsPlatformManagedToolType(existing.Type) &&
		existing.State == ToolCallStateCompleted &&
		existing.Outcome == input.Outcome {
		ok, err := completedToolCallMatchesTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			input.ID,
			input.Outcome,
			input.ResultContentParts,
		)
		if err != nil {
			return ToolCallRecord{}, err
		}
		if ok {
			return existing, nil
		}
	}
	return ToolCallRecord{}, storeerr.ErrIdempotencyConflict
}

type toolCallResultInput struct {
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

func finishCompletedToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	record ToolCallRecord,
	input toolCallResultInput,
) (ToolCallRecord, error) {
	record.ResultContentParts = input.ResultContentParts
	admitted, err := appendToolResultRecordTx(
		ctx,
		txNotifications,
		tx,
		record,
	)
	if err != nil {
		return ToolCallRecord{}, err
	}
	applyAdmittedToolResult(&record, admitted)
	metadata, err := marshalJSON(
		map[string]any{
			"reason":                "tool_result",
			"tool_call_id":          record.ID,
			"model_call_context_id": record.ModelCallContextID,
			"outcome":               input.Outcome,
		},
	)
	if err != nil {
		return ToolCallRecord{}, fmt.Errorf("marshal runtime tool result wakeup metadata: %w", err)
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: record.ProjectID,
			AgentID:   record.AgentID,
			Metadata:  metadata,
		},
	); err != nil {
		return ToolCallRecord{}, fmt.Errorf("mark runtime tool result wakeup: %w", err)
	}
	return record, nil
}

func (s *Store) CompleteCustomToolCall(
	ctx context.Context,
	input CompleteCustomToolCallInput,
) (CompleteCustomToolCallResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
		return CompleteCustomToolCallResult{}, errors.New(
			"project, agent, and tool call ids are required",
		)
	}
	switch input.Outcome {
	case ToolResultOutcomeSucceeded, ToolResultOutcomeFailed:
	default:
		return CompleteCustomToolCallResult{}, errors.New(
			"custom tool result outcome must be succeeded or failed",
		)
	}
	ctx, artifactScope := withArtifactRollbackScope(ctx)
	defer artifactScope.rollback(ctx)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	prepared, err := s.prepareToolResultByID(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ID,
		input.Outcome,
		input.ContentBlocks,
	)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	input.ContentBlocks = prepared
	result, err := completeCustomToolCallTx(ctx, txNotifications, tx, input)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		"complete custom tool call",
	); err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	artifactScope.commit()
	return result, nil
}

func completeCustomToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input CompleteCustomToolCallInput,
) (CompleteCustomToolCallResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
		return CompleteCustomToolCallResult{}, errors.New(
			"project, agent, and tool call ids are required",
		)
	}
	switch input.Outcome {
	case ToolResultOutcomeSucceeded, ToolResultOutcomeFailed:
	default:
		return CompleteCustomToolCallResult{}, errors.New(
			"custom tool result outcome must be succeeded or failed",
		)
	}
	blocks, err := parseToolResultContentBlocks(input.ContentBlocks)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	resultContentParts, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return CompleteCustomToolCallResult{}, err
	}
	qtx := dbsqlc.New(tx)
	row, err := qtx.CompleteCustomToolCall(
		ctx,
		dbsqlc.CompleteCustomToolCallParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ID:        input.ID,
			Outcome:   string(input.Outcome),
		},
	)
	if err == nil {
		record := toolCallRecordFromCustomCompleteSQLC(row)
		record.ResultContentParts = resultContentParts
		admitted, err := appendToolResultRecordTx(
			ctx,
			txNotifications,
			tx,
			record,
		)
		if err != nil {
			return CompleteCustomToolCallResult{}, err
		}
		if !admitted.Inserted {
			return CompleteCustomToolCallResult{}, storeerr.ErrIdempotencyConflict
		}
		applyAdmittedToolResult(&record, admitted)
		metadata, err := marshalJSON(
			map[string]any{
				"reason":                "custom_tool_result",
				"tool_call_id":          record.ID,
				"model_call_context_id": record.ModelCallContextID,
			},
		)
		if err != nil {
			return CompleteCustomToolCallResult{}, fmt.Errorf(
				"marshal custom tool result wakeup metadata: %w",
				err,
			)
		}
		if err := qtx.MarkAgentWakeup(
			ctx,
			dbsqlc.MarkAgentWakeupParams{
				ProjectID: record.ProjectID,
				AgentID:   record.AgentID,
				Metadata:  metadata,
			},
		); err != nil {
			return CompleteCustomToolCallResult{}, fmt.Errorf(
				"mark custom tool result wakeup: %w",
				err,
			)
		}
		return CompleteCustomToolCallResult{
			ToolCall:      record,
			Event:         admitted.Event,
			ContentBlocks: admitted.ContentBlocks,
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CompleteCustomToolCallResult{}, fmt.Errorf(
			"complete custom tool call: %w",
			err,
		)
	}
	existing, loadErr := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if loadErr != nil {
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return CompleteCustomToolCallResult{}, storeerr.ErrNotFound
		}
		return CompleteCustomToolCallResult{}, loadErr
	}
	if existing.Type != toolcatalog.ToolTypeCustom {
		return CompleteCustomToolCallResult{}, storeerr.ErrNotFound
	}
	return CompleteCustomToolCallResult{}, storeerr.ErrStateTransitionConflict
}
