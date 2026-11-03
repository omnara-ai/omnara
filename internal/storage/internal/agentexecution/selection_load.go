package agentexecution

import (
	"context"
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
	snapshot, err := loadExecution(ctx, h.unit.DB(), route, head)
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
