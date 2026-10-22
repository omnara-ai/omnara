package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ExecuteToolCallInput struct {
	Completion        *StepCompletion
	RequireCompletion bool
	ResolveRecorded   bool
	ObservedState     ToolCallState
	RootAgentID       uuid.UUID
	Authorize         bool
	BatchOutputID     uuid.UUID
	ProjectID         uuid.UUID
	AgentID           uuid.UUID
	ToolCallID        uuid.UUID
	RuntimeLockID     uuid.UUID
}

type ExecuteToolCallResult struct {
	StepDisposition StepDisposition
	Disposition     ToolCallDisposition
	Applied         bool
	CommandResult   any
	Completed       *ToolCallRecord
}

type ToolCallDisposition uint8

const (
	ToolCallDispositionRunning ToolCallDisposition = iota + 1
	ToolCallDispositionWaiting
	ToolCallDispositionCompleted
)

type ToolCallCommand interface {
	apply(context.Context, *toolCallTransaction) (any, error)
	resolve(context.Context, *toolCallTransaction) (any, bool, error)
}

type ToolCallPlan func(*ToolCallReader) (ToolCallCommand, error)

type ToolCallReader struct {
	transaction *toolCallTransaction
}

type toolCallTransaction struct {
	unit                       *agentexecution.Unit
	store                      *Store
	tx                         dbsqlc.DBTX
	q                          *dbsqlc.Queries
	notifications              *notifications.TxNotifications
	input                      ExecuteToolCallInput
	disposition                ToolCallDisposition
	locked                     bool
	applied                    bool
	hasDurableCompletionOwner  bool
	requiresWaitingDisposition bool
	owner                      agentexecution.ToolOwner
	completed                  *ToolCallRecord
	completionID               uuid.UUID
	observedState              ToolCallState
}

func (s *Store) ExecuteToolCall(
	ctx context.Context,
	input ExecuteToolCallInput,
	plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ToolCallID == uuid.Nil ||
		input.RuntimeLockID == uuid.Nil {
		return ExecuteToolCallResult{}, errors.New(
			"project, agent, tool call, and runtime lock are required",
		)
	}
	if plan == nil {
		return ExecuteToolCallResult{}, errors.New("tool call plan is required")
	}
	return agentexecution.Retry(ctx,
		"execute_tool_call",
		func(ctx context.Context) (ExecuteToolCallResult, error) {
			return s.executeToolCallOnce(ctx, input, plan)
		})
}

func (s *Store) executeToolCallOnce(
	ctx context.Context,
	input ExecuteToolCallInput,
	plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	return s.executeToolCallCommand(ctx, input, plan, nil)
}

func (s *Store) executeToolCallCommand(
	ctx context.Context, input ExecuteToolCallInput, plan ToolCallPlan, replay ToolCallCommand,
) (ExecuteToolCallResult, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return ExecuteToolCallResult{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if input.RootAgentID != uuid.Nil {
		unit.ReuseAgentRoute(agentexecution.AgentRoute{
			ProjectID: input.ProjectID, AgentID: input.AgentID, RootAgentID: input.RootAgentID,
		})
	}
	tx := unit.DB()
	toolTx := &toolCallTransaction{
		unit:          unit,
		store:         s,
		tx:            tx,
		q:             dbsqlc.New(tx),
		notifications: unit.Notifications(),
		input:         input,
		observedState: input.ObservedState,
	}
	command := replay
	var commandResult any
	var resolved bool
	if command == nil && input.ResolveRecorded && toolTx.hasRecordedOutcome() {
		commandResult, resolved, err = recordedToolReplay().resolve(ctx, toolTx)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
	}
	if resolved {
		return toolTx.replayResult(ctx, commandResult)
	}
	if command == nil {
		command, err = plan(&ToolCallReader{transaction: toolTx})
		if err != nil {
			if input.ResolveRecorded {
				if rollbackErr := unit.Rollback(ctx); rollbackErr != nil {
					return ExecuteToolCallResult{}, errors.Join(err, rollbackErr)
				}
				return s.resolveToolCallAfterError(ctx, input, recordedToolReplay(), err)
			}
			return ExecuteToolCallResult{}, err
		}
	}
	if command == nil {
		return ExecuteToolCallResult{}, errors.New("tool call plan returned no command")
	}
	if replay != nil || toolTx.hasRecordedOutcome() {
		commandResult, resolved, err = command.resolve(ctx, toolTx)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
	}
	if !resolved {
		if replay != nil {
			return ExecuteToolCallResult{}, storeerr.ErrNotFound
		}
		commandResult, err = command.apply(ctx, toolTx)
		if err != nil {
			if rollbackErr := unit.Rollback(ctx); rollbackErr != nil {
				return ExecuteToolCallResult{}, errors.Join(err, rollbackErr)
			}
			return s.resolveToolCallAfterError(ctx, input, command, err)
		}
	}
	if !resolved && !toolTx.applied &&
		(toolTx.disposition == ToolCallDispositionCompleted || toolTx.disposition == ToolCallDispositionWaiting) {
		commandResult, resolved, err = command.resolve(ctx, toolTx)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		if !resolved {
			return ExecuteToolCallResult{}, storeerr.ErrIdempotencyConflict
		}
	}
	if toolTx.disposition == 0 {
		return ExecuteToolCallResult{}, errors.New("tool call command did not choose a disposition")
	}
	if input.RequireCompletion && toolTx.disposition != ToolCallDispositionCompleted {
		return ExecuteToolCallResult{}, storeerr.ErrInvalidToolCallDisposition
	}
	if toolTx.requiresWaitingDisposition && toolTx.disposition != ToolCallDispositionWaiting {
		return ExecuteToolCallResult{}, fmt.Errorf(
			"%w: durable completion owner requires a waiting tool call",
			storeerr.ErrInvalidToolCallDisposition,
		)
	}
	if toolTx.disposition == ToolCallDispositionWaiting && !toolTx.hasDurableCompletionOwner {
		return ExecuteToolCallResult{}, fmt.Errorf(
			"%w: waiting tool call requires a durable completion owner",
			storeerr.ErrInvalidToolCallDisposition,
		)
	}
	if toolTx.disposition == ToolCallDispositionCompleted && toolTx.completed == nil {
		record, err := getToolCallTx(ctx, tx, input.ProjectID, input.AgentID, input.ToolCallID)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		toolTx.completed = &record
	}
	if toolTx.disposition != ToolCallDispositionRunning && !toolTx.applied {
		return toolTx.replayResult(ctx, commandResult)
	}
	step := StepRetained
	if input.BatchOutputID != uuid.Nil && toolTx.disposition != ToolCallDispositionRunning {
		h, err := unit.Handle(input.ProjectID, input.AgentID)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		finished, transferred, err := h.FinishToolBatch(ctx, input.RuntimeLockID, input.BatchOutputID)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		if finished {
			step = StepFinished
			if transferred {
				step = StepTransferred
			}
		}
	}
	finish := input.Completion.Begin(ctx, step != StepRetained)
	finished := false
	defer func() { finish(finished) }()
	if err := unit.Commit(ctx, "execute tool call"); err != nil {
		if errors.Is(err, storeerr.ErrCommitOutcomeUnknown) && toolTx.applied {
			resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			committed, resolveErr := toolTx.resolveCommitted(resolveCtx)
			if resolveErr == nil && committed {
				finished = step != StepRetained
				return ExecuteToolCallResult{StepDisposition: step, Disposition: toolTx.disposition, Applied: true,
					CommandResult: commandResult, Completed: toolTx.completed}, nil
			}
			return ExecuteToolCallResult{}, errors.Join(err, resolveErr)
		}
		return ExecuteToolCallResult{}, err
	}
	finished = step != StepRetained
	return ExecuteToolCallResult{
		StepDisposition: step,
		Disposition:     toolTx.disposition,
		Applied:         toolTx.applied,
		CommandResult:   commandResult,
		Completed:       toolTx.completed,
	}, nil
}

func (t *toolCallTransaction) resolveCommitted(ctx context.Context) (bool, error) {
	if t.completed != nil {
		receipt, err := t.store.q.GetCommittedToolResult(ctx, dbsqlc.GetCommittedToolResultParams{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
		})
		return err == nil && receipt.ID == t.completionID && receipt.Outcome == string(t.completed.Outcome), err
	}
	if t.disposition != ToolCallDispositionWaiting ||
		t.owner.ProcessID == uuid.Nil && t.owner.ActionID == uuid.Nil && t.owner.InteractionID == uuid.Nil {
		return false, nil
	}
	call, err := t.store.q.GetToolCall(ctx, dbsqlc.GetToolCallParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ID: t.input.ToolCallID,
	})
	if err != nil || call.State != "waiting" && call.State != "completed" {
		return false, err
	}
	if t.owner.InteractionID != uuid.Nil {
		interaction, err := t.store.q.GetAgentInteractionByToolCallKind(ctx, dbsqlc.GetAgentInteractionByToolCallKindParams{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
			InteractionKind: string(AgentInteractionKindQuestion),
		})
		return err == nil && interaction.ID == t.owner.InteractionID, err
	}
	if t.owner.ProcessID != uuid.Nil {
		process, err := t.store.q.GetProcessByToolCall(ctx, dbsqlc.GetProcessByToolCallParams{
			ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
		})
		return err == nil && process.ID == t.owner.ProcessID, err
	}
	action, err := t.store.q.GetProcessActionByToolCall(ctx, dbsqlc.GetProcessActionByToolCallParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, ToolCallID: t.input.ToolCallID,
	})
	return err == nil && action.ID == t.owner.ActionID, err
}

func (t *toolCallTransaction) lockForMutation(ctx context.Context) error {
	return t.lockToolCall(ctx, false)
}

func (t *toolCallTransaction) lockOrAcceptExisting(ctx context.Context) error {
	return t.lockToolCall(ctx, true)
}

func (t *toolCallTransaction) lockToolCall(ctx context.Context, acceptExisting bool) error {
	if t == nil || t.tx == nil || t.q == nil {
		return errors.New("tool call transaction is required")
	}
	if t.locked || t.disposition != 0 {
		return nil
	}
	h, err := lockExecution(ctx, t.unit, t.input.ProjectID, t.input.AgentID, false, t.input.RuntimeLockID)
	if err != nil {
		return err
	}
	dispatch, err := h.DispatchTool(ctx,
		agentexecution.ToolRef{ID: t.input.ToolCallID, RuntimeLockID: t.input.RuntimeLockID},
		acceptExisting, t.input.Authorize,
	)
	if err != nil {
		return err
	}
	switch dispatch {
	case agentexecution.ToolDispatchReady:
		t.locked = true
	case agentexecution.ToolDispatchWaiting:
		t.disposition = ToolCallDispositionWaiting
	case agentexecution.ToolDispatchCompleted:
		t.disposition = ToolCallDispositionCompleted
	}
	return nil
}

func (t *toolCallTransaction) startToolCall(ctx context.Context, retainRuntimeOwnership bool) error {
	if t.disposition != 0 {
		return nil
	}
	if err := t.lockForMutation(ctx); err != nil {
		return err
	}
	h, err := t.unit.Handle(t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return err
	}
	ref := agentexecution.ToolRef{ID: t.input.ToolCallID, RuntimeLockID: t.input.RuntimeLockID}
	if retainRuntimeOwnership {
		t.applied, err = h.RunTool(ctx, ref)
		t.disposition = ToolCallDispositionRunning
	} else {
		t.applied, err = h.WaitForTool(ctx, ref, t.owner)
		t.disposition = ToolCallDispositionWaiting
	}
	return err
}

func (t *toolCallTransaction) completeToolCall(
	ctx context.Context,
	input ToolCallCompletionInput,
) (ToolCallRecord, error) {
	if t.disposition != 0 && t.disposition != ToolCallDispositionCompleted {
		return ToolCallRecord{}, storeerr.ErrStateTransitionConflict
	}
	record, completed, err := completeExecutionTool(ctx, t.unit, CompleteToolCallInput{
		Authorize:          t.input.Authorize,
		ProjectID:          t.input.ProjectID,
		AgentID:            t.input.AgentID,
		ID:                 t.input.ToolCallID,
		Outcome:            input.Outcome,
		RuntimeLockID:      t.input.RuntimeLockID,
		ResultContentParts: input.ResultContentParts,
	})
	if err != nil {
		return ToolCallRecord{}, err
	}
	t.locked = true
	t.disposition = ToolCallDispositionCompleted
	t.applied = completed.Changed
	t.completionID = completed.ResultID
	t.completed = &record
	return record, nil
}

func (r *ToolCallReader) GetToolCall(ctx context.Context) (ToolCallRecord, error) {
	t := r.transaction
	record, err := getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
	if err == nil {
		t.observedState = record.State
	}
	return record, err
}

func (r *ToolCallReader) GetModelCallContext(
	ctx context.Context,
	id uuid.UUID,
) (ModelCallContextRecord, bool, error) {
	if id == uuid.Nil {
		return ModelCallContextRecord{}, false, errors.New("model context id is required")
	}
	t := r.transaction
	record, err := loadModelCallContextByIDTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelCallContextRecord{}, false, nil
	}
	if err != nil {
		return ModelCallContextRecord{}, false, err
	}
	return record, true, nil
}

func (t *toolCallTransaction) replayResult(ctx context.Context, commandResult any) (ExecuteToolCallResult, error) {
	if t.input.RequireCompletion && t.disposition != ToolCallDispositionCompleted {
		return ExecuteToolCallResult{}, storeerr.ErrInvalidToolCallDisposition
	}
	step := StepRetained
	if t.input.BatchOutputID != uuid.Nil {
		h, err := t.unit.Handle(t.input.ProjectID, t.input.AgentID)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		finished, transferred, err := h.ResolveToolBatch(ctx, t.input.BatchOutputID)
		if err != nil {
			return ExecuteToolCallResult{}, err
		}
		if finished {
			step = StepFinished
			if transferred {
				step = StepTransferred
			}
		}
	}
	finish := t.input.Completion.Begin(ctx, step != StepRetained)
	finish(step != StepRetained)
	return ExecuteToolCallResult{StepDisposition: step, Disposition: t.disposition, CommandResult: commandResult,
		Completed: t.completed}, nil
}

func (s *Store) resolveToolCallAfterError(
	ctx context.Context, input ExecuteToolCallInput, command ToolCallCommand, cause error,
) (ExecuteToolCallResult, error) {
	record, err := getToolCallTx(ctx, s.pool, input.ProjectID, input.AgentID, input.ToolCallID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return ExecuteToolCallResult{}, cause
	}
	if err != nil {
		return ExecuteToolCallResult{}, errors.Join(cause, err)
	}
	if record.State != ToolCallStateCompleted && record.State != ToolCallStateWaiting {
		return ExecuteToolCallResult{}, cause
	}
	result, err := s.executeToolCallCommand(ctx, input, nil, command)
	if err != nil {
		return ExecuteToolCallResult{}, errors.Join(cause, err)
	}
	return result, nil
}
