package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/notifications"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CancelInput struct {
	ActorID      uuid.UUID
	Reason       string
	Message      string
	ForceRuntime bool
}

type CanceledExecution struct {
	Event     ExecutionEvent
	RuntimeID uuid.UUID
	Changed   bool
}

func (h *Handle) Cancel(ctx context.Context, input CancelInput) (CanceledExecution, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (CanceledExecution, error) {
		if input.Reason == "" || input.Message == "" {
			return CanceledExecution{}, errors.New("cancellation reason and message are required")
		}
		q := executiondb.New()
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return CanceledExecution{}, err
		}
		live, err := q.ListExecutionLiveContexts(
			ctx,
			h.unit.DB(),
			executiondb.ListExecutionLiveContextsParams{AgentID: h.route.AgentID},
		)
		if err != nil {
			return CanceledExecution{}, err
		}
		if snapshot.View.State == AgentArchived && input.ForceRuntime {
			view := snapshot.View
			view.State = AgentActive
			snapshot.Selection, err = SelectNext(view, time.Time{})
			if err != nil {
				return CanceledExecution{}, err
			}
		}
		updates := make(map[uuid.UUID]*notifications.AgentInteractionUpdate)
		turn := uuid.Nil
		if snapshot.Selection.TurnContinuable {
			turn = m.head.CurrentTurnID
		}
		affected := turn != uuid.Nil || len(live) > 0
		result := CanceledExecution{}
		runtime, err := q.ReadExecutionRuntime(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionRuntimeParams{AgentID: h.route.AgentID},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		if err == nil {
			if affected || input.ForceRuntime {
				canceled, err := q.RequestExecutionCancel(
					ctx,
					h.unit.DB(),
					executiondb.RequestExecutionCancelParams{AgentID: h.route.AgentID},
				)
				if err != nil {
					return result, err
				}
				result.RuntimeID = canceled.ID
				h.unit.Notifications().
					AddWorkerControlCancel(canceled.WorkerProcessID, h.route.AgentID, canceled.ID)
			} else if runtime.CancelRequestedAt != nil {
				result.RuntimeID = runtime.ID
				h.unit.Notifications().AddWorkerControlCancel(runtime.WorkerProcessID, h.route.AgentID, runtime.ID)
			}
		}
		for _, c := range live {
			if runtime.ID == uuid.Nil || c.RuntimeLockID != runtime.ID {
				return result, storeerr.ErrStateTransitionConflict
			}
			n, err := q.TeardownExecutionContext(ctx, h.unit.DB(), executiondb.TeardownExecutionContextParams{
				AgentID:      h.route.AgentID,
				ID:           c.ID,
				RuntimeID:    runtime.ID,
				State:        "canceled",
				ErrorKind:    "canceled",
				ErrorCode:    input.Reason,
				ErrorMessage: input.Message, ErrorDetails: json.RawMessage(`{}`)})
			if err != nil {
				return result, err
			}
			if n != 1 {
				return result, storeerr.ErrStateTransitionConflict
			}
			m.changed()
		}
		if !affected {
			return result, nil
		}
		_, err = q.CancelExecutionInputs(
			ctx,
			h.unit.DB(),
			executiondb.CancelExecutionInputsParams{AgentID: h.route.AgentID},
		)
		if err != nil {
			return result, err
		}
		id, err := q.InsertExecutionStop(
			ctx,
			h.unit.DB(),
			executiondb.InsertExecutionStopParams{
				AgentID: h.route.AgentID,
				ActorID: nullableID(input.ActorID),
			},
		)
		if err != nil {
			return result, err
		}
		result.Event, err = h.appendEvent(ctx, turn, "agent_input", id, uuid.Nil, uuid.Nil, false)
		if err != nil {
			return result, err
		}
		n, err := q.ResolveExecutionInput(
			ctx,
			h.unit.DB(),
			executiondb.ResolveExecutionInputParams{
				AgentID: h.route.AgentID,
				ID:      id,
				EventID: &result.Event.ID,
			},
		)
		if err != nil {
			return result, err
		}
		if n != 1 {
			return result, storeerr.ErrStateTransitionConflict
		}
		if turn != uuid.Nil {
			if err = h.advanceTurn(ctx, result.Event, false); err != nil {
				return result, err
			}
			ids, err := q.ListExecutionInteractions(
				ctx,
				h.unit.DB(),
				executiondb.ListExecutionInteractionsParams{AgentID: h.route.AgentID, TurnID: turn},
			)
			if err != nil {
				return result, err
			}
			resolution, err := json.Marshal(map[string]string{"reason": input.Reason})
			if err != nil {
				return result, err
			}
			for _, interactionID := range ids {
				row, err := q.ReadExecutionInteraction(
					ctx,
					h.unit.DB(),
					executiondb.ReadExecutionInteractionParams{AgentID: h.route.AgentID, ID: interactionID},
				)
				if err != nil {
					return result, err
				}
				n, err := q.ResolveExecutionInteraction(
					ctx,
					h.unit.DB(),
					executiondb.ResolveExecutionInteractionParams{
						AgentID:    h.route.AgentID,
						ID:         interactionID,
						State:      "canceled",
						InputID:    &id,
						Resolution: resolution,
					},
				)
				if err != nil {
					return result, err
				}
				if n != 1 {
					return result, storeerr.ErrStateTransitionConflict
				}
				i := interactionRecord(row)
				i.State = "canceled"
				updates[i.ToolID] = &notifications.AgentInteractionUpdate{
					ID:              i.ID,
					InteractionKind: i.Kind,
					State:           i.State,
				}
			}
			if err = h.cancelTurnProcesses(ctx, turn); err != nil {
				return result, err
			}
			calls, err := q.ListExecutionUnfinishedTools(
				ctx,
				h.unit.DB(),
				executiondb.ListExecutionUnfinishedToolsParams{AgentID: h.route.AgentID, TurnID: &turn},
			)
			if err != nil {
				return result, err
			}
			for _, callID := range calls {
				call, err := h.tool(ctx, callID)
				if err != nil {
					return result, err
				}
				if _,
					err = h.completeTool(ctx,
					m,
					call,
					"canceled",
					[]Content{{Kind: "structured_data",
						Data: json.RawMessage(`{"reason":"Agent canceled before this tool call completed."}`)}},
					uuid.Nil, updates[call.ID]); err != nil {
					return result, err
				}
			}
		}
		m.head.StopSequence = result.Event.Sequence
		m.head.AnsweredThroughSequence = max(m.head.AnsweredThroughSequence, result.Event.Sequence)
		m.head.PendingToolOutputID = uuid.Nil
		m.head.PendingOutputLimitID = uuid.Nil
		m.head.PendingConfigInputID = uuid.Nil
		m.head.PendingCheckpointID = uuid.Nil
		m.changed()
		result.Changed = true
		return result, nil
	})
}
