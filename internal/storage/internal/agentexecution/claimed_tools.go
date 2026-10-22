package agentexecution

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (h *Handle) ClaimedToolContext(id uuid.UUID) (dbsqlc.GetModelCallContextRow, error) {
	if h.mutation == nil || h.mutation.loaded == nil {
		return dbsqlc.GetModelCallContextRow{}, ErrInvalidState
	}
	for _, row := range h.mutation.loaded.contexts {
		if row.ID != id {
			continue
		}
		if row.OperationKind != "normal" || row.State != "succeeded" || row.AgentConfigID == uuid.Nil {
			return dbsqlc.GetModelCallContextRow{}, ErrInvalidState
		}
		return dbsqlc.GetModelCallContextRow{
			ID:                        row.ID,
			OrgID:                     row.OrgID,
			ProjectID:                 row.ProjectID,
			AgentID:                   row.AgentID,
			OperationKind:             row.OperationKind,
			AttemptNumber:             row.AttemptNumber,
			AgentConfigID:             row.AgentConfigID,
			ConfiguredModelRevisionID: row.ConfiguredModelRevisionID,
			InputEventSequence:        row.InputEventSequence,
			SourceEventSequenceEnd:    row.SourceEventSequenceEnd,
			RuntimeLockID:             row.RuntimeLockID,
			State:                     row.State,
			RecoveryKind:              row.RecoveryKind,
			ApiFormat:                 row.ApiFormat,
			ApiVariant:                row.ApiVariant,
			ProviderRequestID:         row.ProviderRequestID,
			ProviderResponseID:        row.ProviderResponseID,
			ErrorKind:                 row.ErrorKind,
			ErrorCode:                 row.ErrorCode,
			ErrorMessage:              row.ErrorMessage,
			ErrorDetails:              row.ErrorDetails,
			InputFingerprint:          row.InputFingerprint,
			RetryAt:                   row.RetryAt,
			InputTokensTotal:          row.InputTokensTotal,
			UncachedInputTokens:       row.UncachedInputTokens,
			CacheReadInputTokens:      row.CacheReadInputTokens,
			CacheWriteInputTokens:     row.CacheWriteInputTokens,
			OutputTokensTotal:         row.OutputTokensTotal,
			ReasoningOutputTokens:     row.ReasoningOutputTokens,
			CreatedAt:                 row.CreatedAt,
			CompletedAt:               row.CompletedAt,
			ProviderReportedCostUsd:   row.ProviderReportedCostUsd,
			ProviderMetadata:          row.ProviderMetadata,
		}, nil
	}
	return dbsqlc.GetModelCallContextRow{}, ErrInvalidState
}

type claimedToolBatch struct {
	rows         map[uuid.UUID]executiondb.ReadExecutionToolRow
	completed    []CompletedTool
	turnAdvanced bool
}

func (h *Handle) CompleteClaimedToolBatch(ctx context.Context, runtimeID, outputID uuid.UUID,
	rows []dbsqlc.ListClaimToolCallsRow, complete func() error) error {
	_, err := executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		if h.unit.claimedAgent != h.route.AgentID || runtimeID == uuid.Nil || h.lockedRuntimeID != runtimeID ||
			h.claimTools != nil || m.loaded == nil || m.loaded.Selection.Work != WorkTool ||
			m.head.PendingToolOutputID != outputID || len(rows) == 0 {
			return false, storeerr.ErrStateTransitionConflict
		}
		batch := &claimedToolBatch{rows: make(map[uuid.UUID]executiondb.ReadExecutionToolRow, len(rows))}
		for _, row := range rows {
			if !row.WholeBatch || row.BatchCount != int64(len(rows)) || row.AgentID != h.route.AgentID ||
				row.ProjectID != h.route.ProjectID || row.ModelOutputID != outputID || row.RuntimeLockID != nil ||
				row.HasPermission || row.Type != "built_in" || row.State != "awaiting_authorization" && row.State != "ready" {
				return false, storeerr.ErrStateTransitionConflict
			}
			if _, exists := batch.rows[row.ID]; exists {
				return false, storeerr.ErrStateTransitionConflict
			}
			batch.rows[row.ID] = executiondb.ReadExecutionToolRow{
				ID: row.ID, AgentID: row.AgentID, CreatedAt: row.CreatedAt, ModelOutputID: row.ModelOutputID,
				ProviderCallID: row.ProviderCallID,
				Name:           row.Name, Input: row.Input, Type: row.Type, State: row.State, RuntimeLockID: row.RuntimeLockID,
				TurnID: row.TurnID, ContextID: row.ModelCallContextID, OperationKind: row.OperationKind,
				AttemptNumber:      row.AttemptNumber,
				InputEventSequence: row.InputEventSequence, SourceEventSequenceEnd: row.SourceEventSequenceEnd,
				ContextState: row.ContextState, RecoveryKind: row.RecoveryKind, RetryAt: row.RetryAt,
				OpeningInputIds: row.OpeningInputIds, OpeningEventSequence: row.OpeningEventSequence,
				StopReason: row.StopReason, OutputCreatedAt: row.OutputCreatedAt, SourceEventID: row.SourceEventID,
				SourceSequence: row.SourceSequence,
			}
		}
		h.claimTools = batch
		defer func() { h.claimTools = nil }()
		if err := complete(); err != nil {
			return false, err
		}
		if len(batch.completed) != len(rows) || len(batch.rows) != 0 || m.loaded == nil ||
			m.loaded.View.ToolBatch == nil || !batch.turnAdvanced {
			return false, ErrInvalidState
		}
		var last ExecutionEvent
		var ready time.Time
		for _, completed := range batch.completed {
			if completed.Event.Sequence > last.Sequence {
				last = completed.Event
			}
			if completed.Event.Time.After(ready) {
				ready = completed.Event.Time
			}
		}
		tools := m.loaded.View.ToolBatch
		tools.HasIncomplete = false
		tools.HasRunnable = false
		tools.Completion = &BatchCompletion{LastResultSequence: last.Sequence, ReadyAt: ready}
		m.toolBatchChanged = false
		if err := m.selectLoaded(); err != nil {
			return false, err
		}
		if err := h.FenceRuntime(ctx, runtimeID); err != nil {
			return false, err
		}
		if err := h.finishRuntime(ctx, m, runtimeID); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}
