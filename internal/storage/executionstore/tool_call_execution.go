package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ExecuteToolCallInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ToolCallID    uuid.UUID
	RuntimeLockID uuid.UUID
}

type ExecuteToolCallResult struct {
	Disposition   ToolCallDisposition
	Applied       bool
	CommandResult any
	Completed     *ToolCallRecord
}

type ToolCallDisposition uint8

const (
	ToolCallDispositionRunning ToolCallDisposition = iota + 1
	ToolCallDispositionWaiting
	ToolCallDispositionCompleted
)

type ToolCallCommand interface {
	apply(context.Context, *toolCallTransaction) (any, error)
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
	return storeutil.RetryTransaction(ctx, "execute_tool_call", func() (ExecuteToolCallResult, error) {
		return s.executeToolCallOnce(ctx, input, plan)
	})
}

func (s *Store) executeToolCallOnce(
	ctx context.Context,
	input ExecuteToolCallInput,
	plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return ExecuteToolCallResult{}, fmt.Errorf("begin tool call execution: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()
	toolTx := &toolCallTransaction{
		unit:          unit,
		store:         s,
		tx:            tx,
		q:             dbsqlc.New(tx),
		notifications: unit.Notifications(),
		input:         input,
	}
	command, err := plan(&ToolCallReader{transaction: toolTx})
	if err != nil {
		return ExecuteToolCallResult{}, err
	}
	if command == nil {
		return ExecuteToolCallResult{}, errors.New("tool call plan returned no command")
	}
	commandResult, err := command.apply(ctx, toolTx)
	if err != nil {
		return ExecuteToolCallResult{}, err
	}
	if toolTx.disposition == 0 {
		return ExecuteToolCallResult{}, errors.New("tool call command did not choose a disposition")
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
	if err := unit.Commit(ctx, "execute tool call"); err != nil {
		return ExecuteToolCallResult{}, err
	}
	return ExecuteToolCallResult{
		Disposition:   toolTx.disposition,
		Applied:       toolTx.applied,
		CommandResult: commandResult,
		Completed:     toolTx.completed,
	}, nil
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
	err := t.unit.LockAgentRefs(ctx,
		[]lifecyclelock.AgentRef{{ProjectID: t.input.ProjectID, AgentID: t.input.AgentID}},
		agentexecution.IngressAuthority{},
	)
	if errors.Is(err, storeerr.ErrNotFound) {
		return storeerr.ErrRuntimeLockInactive
	}
	if err != nil {
		return err
	}
	h, err := t.unit.Handle(t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return err
	}
	dispatch, err := h.DispatchTool(ctx,
		agentexecution.ToolRef{ID: t.input.ToolCallID, RuntimeLockID: t.input.RuntimeLockID}, acceptExisting,
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
	if t.disposition == ToolCallDispositionCompleted {
		return getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
	}
	if t.disposition != 0 {
		return ToolCallRecord{}, storeerr.ErrStateTransitionConflict
	}
	record, err := completeExecutionTool(ctx, t.unit, CompleteToolCallInput{
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
	t.applied = true
	t.completed = &record
	return record, nil
}

func (r *ToolCallReader) GetToolCall(ctx context.Context) (ToolCallRecord, error) {
	t := r.transaction
	return getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
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
