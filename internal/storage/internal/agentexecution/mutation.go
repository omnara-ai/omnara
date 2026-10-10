package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type executionMutation struct {
	head             ExecutionHead
	insertHead       bool
	dirty            bool
	selectionChanged bool
	loaded           *ExecutionSnapshot
}

func (m *executionMutation) clone() *executionMutation {
	cloned := *m
	if m.loaded != nil {
		snapshot := cloneExecutionSnapshot(*m.loaded)
		cloned.loaded = &snapshot
	}
	if m.head.LogicalReadyAt != nil {
		ready := *m.head.LogicalReadyAt
		cloned.head.LogicalReadyAt = &ready
	}
	return &cloned
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
		CurrentTurnID:           nullableID(h.CurrentTurnID),
		StopSequence:            h.StopSequence,
		AnsweredThroughSequence: h.AnsweredThroughSequence,
		MaxNormalInputSequence:  h.MaxNormalInputSequence,
		MaxContextInputSequence: h.MaxContextInputSequence,
		NormalContextID:         nullableID(h.NormalContextID),
		CompactionContextID:     nullableID(h.CompactionContextID),
		PendingToolOutputID:     nullableID(h.PendingToolOutputID),
		PendingOutputLimitID:    nullableID(h.PendingOutputLimitID),
		PendingConfigInputID:    nullableID(h.PendingConfigInputID),
		PendingCheckpointID:     nullableID(h.PendingCheckpointID),
		TurnContinuable:         h.TurnContinuable,
		IncompleteTools:         h.IncompleteTools,
		LogicalReadyAt:          h.LogicalReadyAt,
	}
}

func (u *Unit) publishExecution(ctx context.Context) error {
	var handles []*Handle
	var loads []executiondb.LoadExecutionFactsParams
	var reloaded []*Handle
	for _, h := range u.orderedHandles() {
		if h.mutation == nil || !h.mutation.dirty {
			continue
		}
		handles = append(handles, h)
		if h.mutation.loaded == nil && h.mutation.selectionChanged {
			reloaded = append(reloaded, h)
			loads = append(loads, executionFactsParams(h.route, h.mutation.head))
		}
	}
	if len(handles) == 0 {
		return nil
	}
	var failure error
	q := executiondb.New()
	if len(loads) > 0 {
		batch := q.LoadExecutionFacts(ctx, u.DB(), loads)
		batch.Query(func(i int, rows []executiondb.LoadExecutionFactsRow, err error) {
			if failure != nil {
				return
			}
			if err != nil {
				failure = err
				return
			}
			h := reloaded[i]
			snapshot, err := executionSnapshot(h.route, h.mutation.head, rows)
			if err != nil {
				failure = err
				return
			}
			h.mutation.head = snapshot.Head
		})
		if err := errors.Join(failure, batch.Close()); err != nil {
			return err
		}
	}
	writes := make([]executiondb.PublishExecutionParams, len(handles))
	for i, h := range handles {
		writes[i] = h.mutation.publicationParams(h.route)
		_, captured := u.webhooks[h.route.AgentID]
		writes[i].CaptureWebhook = !captured
	}
	published := q.PublishExecution(ctx, u.DB(), writes)
	published.QueryRow(func(i int, row executiondb.PublishExecutionRow, err error) {
		if failure != nil {
			return
		}
		if err != nil {
			failure = err
			return
		}
		if row.AgentID != handles[i].route.AgentID {
			failure = fmt.Errorf("%w: missing execution head", ErrInvalidState)
			return
		}
		if writes[i].CaptureWebhook {
			target := webhookTarget{orgID: row.WebhookOrgID}
			if err := json.Unmarshal(row.WebhookEvents, &target.events); err != nil {
				failure = err
				return
			}
			u.webhooks[row.AgentID] = target
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
