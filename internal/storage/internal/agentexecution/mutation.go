package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type executionMutation struct {
	head       ExecutionHead
	insertHead bool
	dirty      bool
	loaded     *ExecutionSnapshot
}

func (m *executionMutation) clone() *executionMutation {
	cloned := *m
	cloned.loaded = nil
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
	snapshot, err := h.LoadExecution(ctx)
	if err != nil {
		return nil, err
	}
	m := &executionMutation{head: snapshot.Head, loaded: &snapshot}
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
	for _, h := range u.orderedHandles() {
		if h.mutation == nil || !h.mutation.dirty {
			continue
		}
		handles = append(handles, h)
		args := executionFactsParams(h.route, h.mutation.head)
		_, captured := u.webhooks[h.route.AgentID]
		args.CaptureWebhook = !captured
		loads = append(loads, args)
	}
	if len(handles) == 0 {
		return nil
	}
	var failure error
	q := executiondb.New()
	batch := q.LoadExecutionFacts(ctx, u.DB(), loads)
	batch.Query(func(i int, rows []executiondb.LoadExecutionFactsRow, err error) {
		if failure != nil {
			return
		}
		if err != nil {
			failure = err
			return
		}
		h := handles[i]
		if loads[i].CaptureWebhook {
			for _, row := range rows {
				if row.Kind != "scope" {
					continue
				}
				target := webhookTarget{orgID: row.WebhookOrgID}
				if err := json.Unmarshal(row.WebhookEvents, &target.events); err != nil {
					failure = err
					return
				}
				u.webhooks[h.route.AgentID] = target
			}
		}
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
	writes := make([]executiondb.PublishExecutionParams, len(handles))
	for i, h := range handles {
		writes[i] = h.mutation.publicationParams(h.route)
	}
	published := q.PublishExecution(ctx, u.DB(), writes)
	published.QueryRow(func(i int, id uuid.UUID, err error) {
		if failure != nil {
			return
		}
		if err != nil {
			failure = err
			return
		}
		if id != handles[i].route.AgentID {
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
	m.loaded = nil
}
