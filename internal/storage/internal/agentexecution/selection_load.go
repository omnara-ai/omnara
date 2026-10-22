package agentexecution

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ExecutionHead struct {
	AgentID                 uuid.UUID
	CurrentTurnID           uuid.UUID
	StopSequence            int64
	MaxNormalInputSequence  int64
	MaxContextInputSequence int64
	NormalContextID         uuid.UUID
	CompactionContextID     uuid.UUID
	PendingToolOutputID     uuid.UUID
	PendingOutputLimitID    uuid.UUID
	PendingConfigInputID    uuid.UUID
	PendingCheckpointID     uuid.UUID
	IncompleteTools         bool
	LogicalReadyAt          *time.Time
}

type ExecutionSnapshot struct {
	contexts    []executiondb.LoadExecutionContextsRow
	Head        ExecutionHead
	View        ExecutionView
	Selection   Selection
	databaseNow time.Time
}

func (h *Handle) LoadExecution(ctx context.Context) (ExecutionSnapshot, error) {
	route, err := h.Route()
	if err != nil {
		return ExecutionSnapshot{}, err
	}
	var head *ExecutionHead
	m := h.mutation
	if m != nil {
		head = &m.head
		if m.loaded != nil {
			if m.toolBatchChanged {
				if err := h.refreshToolBatch(ctx, m); err != nil {
					return ExecutionSnapshot{}, err
				}
			}
			if m.loaded.Selection.Wait == WaitModelDeadline {
				now, err := executiondb.New().ExecutionDatabaseTime(ctx, h.unit.DB())
				if err != nil {
					return ExecutionSnapshot{}, err
				}
				m.loaded.databaseNow = now
				if err := m.selectLoaded(); err != nil {
					return ExecutionSnapshot{}, err
				}
			}
			return cloneExecutionSnapshot(*m.loaded), nil
		}
	}
	var known []executiondb.LoadExecutionContextsRow
	if m != nil {
		known = m.knownContexts
	}
	snapshot, err := loadExecution(ctx, h.unit.DB(), route, head, known...)
	if err == nil {
		if m == nil {
			m = &executionMutation{}
			h.mutation = m
		}
		m.head = snapshot.Head
		m.loaded = &snapshot
		m.selectionChanged = false
	}
	return cloneExecutionSnapshot(snapshot), err
}

func executionHead(row executiondb.LoadExecutionBaseRow) ExecutionHead {
	return ExecutionHead{
		AgentID:                 row.AgentID,
		CurrentTurnID:           storeutil.ValueOrZero(row.CurrentTurnID),
		StopSequence:            row.StopSequence,
		MaxNormalInputSequence:  row.MaxNormalInputSequence,
		MaxContextInputSequence: row.MaxContextInputSequence, NormalContextID: storeutil.ValueOrZero(row.NormalContextID),
		CompactionContextID: storeutil.ValueOrZero(
			row.CompactionContextID,
		), PendingToolOutputID: storeutil.ValueOrZero(row.PendingToolOutputID),
		PendingOutputLimitID: storeutil.ValueOrZero(
			row.PendingOutputLimitID,
		), PendingConfigInputID: storeutil.ValueOrZero(row.PendingConfigInputID),
		PendingCheckpointID: storeutil.ValueOrZero(row.PendingCheckpointID),
		IncompleteTools:     row.IncompleteTools, LogicalReadyAt: row.LogicalReadyAt,
	}
}

func (h *Handle) refreshToolBatch(ctx context.Context, m *executionMutation) error {
	row, err := executiondb.New().LoadExecutionToolBatch(ctx, h.unit.DB(), executiondb.LoadExecutionToolBatchParams{
		AgentID: h.route.AgentID, ToolOutputID: storeutil.IDFromNil(m.head.PendingToolOutputID),
	})
	if err != nil {
		return err
	}
	if !row.HasTools {
		return fmt.Errorf("%w: pending output has no tools", ErrInvalidState)
	}
	batch := m.loaded.View.ToolBatch
	batch.HasIncomplete, batch.HasRunnable = row.Incomplete, row.Runnable
	batch.Completion = nil
	if !row.Incomplete {
		if row.Calls == 0 || row.Calls != row.Results || row.Calls != row.Events {
			return fmt.Errorf("%w: incomplete result authority", ErrInvalidState)
		}
		batch.Completion = &BatchCompletion{LastResultSequence: row.LastSequence, ReadyAt: row.ReadyAt}
	}
	m.toolBatchChanged = false
	m.loaded.databaseNow = row.DatabaseNow
	return m.selectLoaded()
}
