package agentexecution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type executionMutation struct {
	head             ExecutionHead
	insertHead       bool
	dirty            bool
	selectionChanged bool
	loaded           *ExecutionSnapshot
}

func (h *Handle) executionMutation(ctx context.Context) (*executionMutation, error) {
	if _, err := h.Route(); err != nil {
		return nil, err
	}
	if h.mutation != nil {
		return h.mutation, nil
	}
	base, err := executiondb.New().LoadExecutionBase(ctx, h.unit.DB(), executiondb.LoadExecutionBaseParams{
		ProjectID: h.route.ProjectID, AgentID: h.route.AgentID,
	})
	if err != nil {
		return nil, err
	}
	if !base.HeadExists {
		return nil, fmt.Errorf("load execution head: %w", pgx.ErrNoRows)
	}
	m := &executionMutation{head: executionHead(base)}
	h.mutation = m
	return m, nil
}

func (m *executionMutation) publicationParams(route AgentRoute) executiondb.PublishExecutionParams {
	h := m.head
	return executiondb.PublishExecutionParams{
		AgentID:                 route.AgentID,
		InsertHead:              m.insertHead,
		CurrentTurnID:           storeutil.IDFromNil(h.CurrentTurnID),
		StopSequence:            h.StopSequence,
		MaxNormalInputSequence:  h.MaxNormalInputSequence,
		MaxContextInputSequence: h.MaxContextInputSequence,
		NormalContextID:         storeutil.IDFromNil(h.NormalContextID),
		CompactionContextID:     storeutil.IDFromNil(h.CompactionContextID),
		PendingToolOutputID:     storeutil.IDFromNil(h.PendingToolOutputID),
		PendingOutputLimitID:    storeutil.IDFromNil(h.PendingOutputLimitID),
		PendingConfigInputID:    storeutil.IDFromNil(h.PendingConfigInputID),
		PendingCheckpointID:     storeutil.IDFromNil(h.PendingCheckpointID),
		TurnContinuable:         h.TurnContinuable,
		IncompleteTools:         h.IncompleteTools,
		LogicalReadyAt:          h.LogicalReadyAt,
	}
}

func (u *Unit) publishExecution(ctx context.Context) error {
	var handles []*Handle
	loads := factsBatch{DBTX: u.DB()}
	var facts []*executionFacts
	var reloaded []*Handle
	for _, h := range u.orderedHandles() {
		if h.mutation == nil || !h.mutation.dirty {
			continue
		}
		handles = append(handles, h)
		if h.mutation.loaded == nil && h.mutation.selectionChanged {
			reloaded = append(reloaded, h)
			facts = append(facts, loads.queue(ctx, h.route, h.mutation.head))
		}
	}
	if len(handles) == 0 {
		return nil
	}
	var failure error
	q := executiondb.New()
	if err := loads.read(ctx); err != nil {
		return err
	}
	for i, h := range reloaded {
		snapshot, err := executionSnapshot(h.route, h.mutation.head, *facts[i])
		if err != nil {
			return err
		}
		h.mutation.head = snapshot.Head
	}
	writes := make([]executiondb.PublishExecutionParams, len(handles))
	for i, h := range handles {
		writes[i] = h.mutation.publicationParams(h.route)
	}
	published := q.PublishExecution(ctx, u.DB(), writes)
	published.QueryRow(func(i int, agentID uuid.UUID, err error) {
		if failure != nil {
			return
		}
		if err != nil {
			failure = err
			return
		}
		if agentID != handles[i].route.AgentID {
			failure = fmt.Errorf("%w: missing execution head", ErrInvalidState)
		}
	})
	return errors.Join(failure, published.Close())
}

func executeCommand[T any](
	ctx context.Context,
	h *Handle,
	run func(*executionMutation) (T, error),
) (result T, err error) {
	m, err := h.executionMutation(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			h.unit.aborted = true
		}
	}()
	return run(m)
}

func (m *executionMutation) changed() {
	m.dirty = true
	m.selectionChanged = true
	m.loaded = nil
}

func (m *executionMutation) selectLoaded() error {
	snapshot := m.loaded
	snapshot.View.StopSequence = m.head.StopSequence
	snapshot.View.MaxNormalInputSequence = m.head.MaxNormalInputSequence
	snapshot.View.MaxContextInputSequence = m.head.MaxContextInputSequence
	selected, err := SelectNext(snapshot.View, snapshot.databaseNow)
	if err != nil {
		return err
	}
	snapshot.Selection = selected
	m.head.TurnContinuable = snapshot.Selection.TurnContinuable
	m.head.IncompleteTools = snapshot.Selection.IncompleteTools
	m.head.LogicalReadyAt = copyPointer(snapshot.Selection.LogicalReadyAt)
	snapshot.Head = m.head
	m.selectionChanged = false
	return nil
}

func (m *executionMutation) updated(snapshot ExecutionSnapshot) error {
	m.dirty = true
	m.loaded = &snapshot
	return m.selectLoaded()
}
