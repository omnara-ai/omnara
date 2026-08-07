package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ExecuteToolCallInput struct {
	ProjectID     ID
	AgentID       ID
	ToolCallID    ID
	RuntimeLockID ID
}

type ExecuteToolCallResult struct {
	Disposition   ToolCallDisposition
	Applied       bool
	CommandResult any
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
	store                      *Store
	tx                         pgx.Tx
	q                          *dbsqlc.Queries
	notifications              *notifications.TxNotifications
	input                      ExecuteToolCallInput
	disposition                ToolCallDisposition
	locked                     bool
	applied                    bool
	hasDurableCompletionOwner  bool
	requiresWaitingDisposition bool
}

func (s *Store) ExecuteToolCall(
	ctx context.Context,
	input ExecuteToolCallInput,
	plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ToolCallID) ||
		isNilID(input.RuntimeLockID) {
		return ExecuteToolCallResult{}, errors.New(
			"project, agent, tool call, and runtime lock are required",
		)
	}
	if plan == nil {
		return ExecuteToolCallResult{}, errors.New("tool call plan is required")
	}
	return storeutil.RetryTransaction(ctx, func() (ExecuteToolCallResult, error) {
		return s.executeToolCallOnce(ctx, input, plan)
	})
}

func (s *Store) executeToolCallOnce(
	ctx context.Context,
	input ExecuteToolCallInput,
	plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ExecuteToolCallResult{}, fmt.Errorf("begin tool call execution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	toolTx := &toolCallTransaction{
		store:         s,
		tx:            tx,
		q:             dbsqlc.New(tx),
		notifications: s.newTxNotifications(),
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
	if err := s.commitTxWithNotifications(ctx, tx, toolTx.notifications, "execute tool call"); err != nil {
		return ExecuteToolCallResult{}, err
	}
	return ExecuteToolCallResult{
		Disposition:   toolTx.disposition,
		Applied:       toolTx.applied,
		CommandResult: commandResult,
	}, nil
}

func (t *toolCallTransaction) lockForMutation(ctx context.Context) error {
	return t.lockToolCall(ctx, false)
}

func (t *toolCallTransaction) lockOrAcceptExisting(ctx context.Context) error {
	return t.lockToolCall(ctx, true)
}

func (t *toolCallTransaction) lockToolCall(
	ctx context.Context,
	acceptExisting bool,
) error {
	if t == nil || t.tx == nil || t.q == nil {
		return errors.New("tool call transaction is required")
	}
	if t.locked || t.disposition != 0 {
		return nil
	}
	if err := lockAgentRuntimeForOwnedMutationTx(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		t.input.RuntimeLockID,
	); err != nil {
		return err
	}
	existing, err := t.q.GetToolCallDispatchState(
		ctx,
		dbsqlc.GetToolCallDispatchStateParams{
			ProjectID: t.input.ProjectID,
			AgentID:   t.input.AgentID,
			ID:        t.input.ToolCallID,
		},
	)
	if err != nil {
		return fmt.Errorf("load tool call before execution: %w", err)
	}
	switch ToolCallState(existing.State) {
	case ToolCallStateReady:
		t.locked = true
		return nil
	case ToolCallStateAwaitingAuthorization, ToolCallStateAwaitingPermission:
		return storeerr.ErrIdempotencyConflict
	case ToolCallStateRunning:
		return storeerr.ErrToolCallInProgress
	case ToolCallStateWaiting:
		if acceptExisting {
			t.disposition = ToolCallDispositionWaiting
			return nil
		}
	case ToolCallStateCompleted:
		if acceptExisting {
			t.disposition = ToolCallDispositionCompleted
			return nil
		}
	}
	return storeerr.ErrIdempotencyConflict
}

func (t *toolCallTransaction) startToolCall(
	ctx context.Context,
	retainRuntimeOwnership bool,
) error {
	if t == nil || t.tx == nil || t.q == nil {
		return errors.New("tool call transaction is required")
	}
	if t.disposition != 0 {
		return nil
	}
	if err := t.lockForMutation(ctx); err != nil {
		return err
	}
	changed, err := t.q.StartToolCall(
		ctx,
		dbsqlc.StartToolCallParams{
			RetainRuntimeOwnership: retainRuntimeOwnership,
			ProjectID:              t.input.ProjectID,
			AgentID:                t.input.AgentID,
			ID:                     t.input.ToolCallID,
			RuntimeLockID:          t.input.RuntimeLockID,
		},
	)
	if err != nil {
		return fmt.Errorf("start tool call: %w", err)
	}
	if changed == 0 {
		if runtimeErr := agentRuntimeLockActiveTx(
			ctx,
			t.q,
			t.input.ProjectID,
			t.input.AgentID,
			t.input.RuntimeLockID,
		); runtimeErr != nil {
			return runtimeErr
		}
		return storeerr.ErrIdempotencyConflict
	}
	if retainRuntimeOwnership {
		t.disposition = ToolCallDispositionRunning
	} else {
		t.disposition = ToolCallDispositionWaiting
	}
	t.applied = true
	return nil
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
	record, err := completeToolCallTx(ctx, t.notifications, t.tx, CompleteToolCallInput{
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
	return record, nil
}

func (r *ToolCallReader) GetToolCall(ctx context.Context) (ToolCallRecord, error) {
	t := r.transaction
	return getToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
}

func (r *ToolCallReader) GetModelCallContext(
	ctx context.Context,
	id ID,
) (ModelCallContextRecord, bool, error) {
	if isNilID(id) {
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
