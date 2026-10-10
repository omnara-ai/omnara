package agentexecution

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (h *Handle) cancelTurnProcesses(ctx context.Context, turn uuid.UUID) error {
	q := dbsqlc.New(h.unit.DB())
	rows, err := q.CancelUnresolvedProcessesForAgentTurn(
		ctx,
		dbsqlc.CancelUnresolvedProcessesForAgentTurnParams{
			ProjectID: h.route.ProjectID,
			AgentID:   h.route.AgentID,
			TurnID:    turn,
		},
	)
	if err != nil {
		return err
	}
	if _,
		err = q.CancelQueuedProcessActionsForAgentTurn(ctx,
		dbsqlc.CancelQueuedProcessActionsForAgentTurnParams{ProjectID: h.route.ProjectID,
			AgentID: h.route.AgentID,
			TurnID:  turn}); err != nil {
		return err
	}
	if _,
		err = q.CancelAcceptedProcessActionsForAgentTurn(ctx,
		dbsqlc.CancelAcceptedProcessActionsForAgentTurnParams{ProjectID: h.route.ProjectID,
			AgentID: h.route.AgentID,
			TurnID:  turn}); err != nil {
		return err
	}
	for _, row := range rows {
		if err = h.interruptActions(ctx,
			row.OrgID,
			row.ID,
			"agent_canceled_before_grant",
			"agent_canceled_after_grant"); err != nil {
			return err
		}
		if row.State == "unknown" {
			h.unit.Notifications().AddDaemonProcessTermination(row.MachineID, row.ID)
		}
	}
	return nil
}

func (h *Handle) interruptActions(
	ctx context.Context,
	orgID, processID uuid.UUID,
	queuedReason, acceptedReason string,
) error {
	q := dbsqlc.New(h.unit.DB())
	queued, err := q.MarkQueuedProcessActionsFailedForProcess(
		ctx,
		dbsqlc.MarkQueuedProcessActionsFailedForProcessParams{
			OrgID:           orgID,
			ProcessID:       processID,
			StateReasonCode: optionalText(queuedReason),
		},
	)
	if err != nil {
		return err
	}
	accepted, err := q.ResolveAcceptedProcessActionsWithoutEvidence(
		ctx,
		dbsqlc.ResolveAcceptedProcessActionsWithoutEvidenceParams{
			OrgID:           orgID,
			ProcessID:       processID,
			StateReasonCode: optionalText(acceptedReason),
		},
	)
	if err != nil {
		return err
	}
	if len(accepted) > 0 {
		if err = q.TouchProcessActivity(ctx,
			dbsqlc.TouchProcessActivityParams{ProjectID: h.route.ProjectID,
				AgentID:   h.route.AgentID,
				ProcessID: processID}); err != nil {
			return err
		}
	}
	for _, action := range append(queued, accepted...) {
		if _, err = h.completeProcessAction(ctx, ActionResult{ID: action.ID}, true); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handle) failQueuedRuntime(ctx context.Context, id uuid.UUID, reason string) error {
	q := dbsqlc.New(h.unit.DB())
	rows, err := q.FailQueuedProcessesForRuntimeEnd(
		ctx,
		dbsqlc.FailQueuedProcessesForRuntimeEndParams{
			ProjectID:       h.route.ProjectID,
			AgentID:         h.route.AgentID,
			RuntimeLockID:   id,
			StateReasonCode: optionalText(reason),
		},
	)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if _, err = h.CompleteProcess(ctx, ProcessResult{ID: row.ID}); err != nil {
			return err
		}
	}
	actions, err := q.FailQueuedProcessActionsForRuntimeEnd(
		ctx,
		dbsqlc.FailQueuedProcessActionsForRuntimeEndParams{
			ProjectID:       h.route.ProjectID,
			AgentID:         h.route.AgentID,
			RuntimeLockID:   id,
			StateReasonCode: optionalText(reason),
		},
	)
	if err != nil {
		return err
	}
	for _, row := range actions {
		if _, err = h.completeProcessAction(ctx, ActionResult{ID: row.ID}, true); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handle) InterruptProcess(ctx context.Context, id uuid.UUID, reason string) (bool, error) {
	return executeCommand(ctx, h, func(_ *executionMutation) (bool, error) {
		if reason == "" {
			return false, errors.New("process interruption reason is required")
		}
		row, err := executiondb.New().
			ReadExecutionProcessOwner(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionProcessOwnerParams{AgentID: h.route.AgentID,
					ID: id})
		if err != nil {
			return false, err
		}
		q := dbsqlc.New(h.unit.DB())
		changed := false
		switch row.State {
		case "queued":
			_, err = q.MarkQueuedProcessFailedByMachine(
				ctx,
				dbsqlc.MarkQueuedProcessFailedByMachineParams{
					OrgID:           row.OrgID,
					MachineID:       row.MachineID,
					ProjectID:       h.route.ProjectID,
					AgentID:         h.route.AgentID,
					ID:              id,
					StateReasonCode: optionalText(reason),
				},
			)
			if err != nil {
				return false, err
			}
			changed = true
		case "starting", "running":
			_, err = q.MarkActiveProcessUnknownByMachine(
				ctx,
				dbsqlc.MarkActiveProcessUnknownByMachineParams{
					OrgID:           row.OrgID,
					MachineID:       row.MachineID,
					ID:              id,
					StateReasonCode: optionalText(reason),
				},
			)
			if err != nil {
				return false, err
			}
			changed = true
			h.unit.Notifications().AddDaemonProcessTermination(row.MachineID, id)
		}
		if changed && row.ToolCallID != uuid.Nil {
			if _, err = h.CompleteProcess(ctx, ProcessResult{ID: id}); err != nil {
				return false, err
			}
		}
		return changed, h.interruptActions(ctx, row.OrgID, id, reason, reason)
	})
}

func (h *Handle) Archive(ctx context.Context, actorID uuid.UUID) (bool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (bool, error) {
		identity, err := executiondb.New().
			ReadExecutionAgentIdentity(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionAgentIdentityParams{ProjectID: h.route.ProjectID,
					ID: h.route.AgentID})
		if err != nil {
			return false, err
		}
		if identity.State == "archived" {
			return false, nil
		}
		if err = h.unit.CaptureWebhookEligibility(ctx, h.route.AgentID); err != nil {
			return false, err
		}
		n, err := executiondb.New().
			ArchiveExecutionAgent(ctx, h.unit.DB(), executiondb.ArchiveExecutionAgentParams{ID: h.route.AgentID})
		if err != nil {
			return false, err
		}
		if n != 1 {
			return false, storeerr.ErrStateTransitionConflict
		}
		q := dbsqlc.New(h.unit.DB())
		if err = q.DeleteAgentIntegrationSubscriptions(ctx,
			dbsqlc.DeleteAgentIntegrationSubscriptionsParams{ProjectID: h.route.ProjectID,
				AgentID: h.route.AgentID}); err != nil {
			return false, err
		}
		if _,
			err = q.DeleteCronTriggersForAgent(ctx,
			dbsqlc.DeleteCronTriggersForAgentParams{ProjectID: h.route.ProjectID,
				AgentID: &h.route.AgentID}); err != nil {
			return false, err
		}
		rows, err := q.ListProcessesForExecutionRevoked(
			ctx,
			dbsqlc.ListProcessesForExecutionRevokedParams{
				ProjectID: h.route.ProjectID,
				AgentID:   &h.route.AgentID,
			},
		)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if _, err = h.InterruptProcess(ctx, row.ID, "agent_archived"); err != nil {
				return false, err
			}
		}
		if _,
			err = h.Cancel(ctx,
			CancelInput{ActorID: actorID,
				Reason:       "agent_archived",
				Message:      "The model call was stopped because the agent was archived.",
				ForceRuntime: true}); err != nil {
			return false, err
		}
		if _,
			err = executiondb.New().CancelExecutionInputs(ctx,
			h.unit.DB(),
			executiondb.CancelExecutionInputsParams{AgentID: h.route.AgentID,
				IncludeQueued: true}); err != nil {
			return false, err
		}

		m.changed()
		return true, nil
	})
}

func (u *Unit) ArchiveAgents(ctx context.Context, routes []AgentRoute, actorID uuid.UUID) error {
	for _, route := range routes {
		if _, err := u.Agent(route); err != nil {
			return err
		}
	}
	for _, route := range routes {
		h := u.handles[route.AgentID]
		if _, err := h.Archive(ctx, actorID); err != nil {
			return err
		}
	}
	return nil
}

func (u *Unit) InterruptProcesses(
	ctx context.Context,
	agents map[AgentRoute][]uuid.UUID,
	reason string,
) error {
	for route := range agents {
		if _, err := u.Agent(route); err != nil {
			return err
		}
	}
	for _, h := range u.orderedHandles() {
		for _, id := range agents[h.route] {
			_, err := h.InterruptProcess(ctx, id, reason)
			if errors.Is(err, pgx.ErrNoRows) {
				return storeerr.ErrNotFound
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
