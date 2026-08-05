package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (e AgentExecutor) ExecuteToolWork(ctx context.Context, input ToolWorkExecution) error {
	return e.executeToolWork(ctx, input, e.configuredToolExecutor())
}

type toolWorkExecutor interface {
	PrepareToolCallPermission(context.Context, tools.Turn, model.ToolCall) error
	Dispatch(context.Context, tools.Turn, model.ToolCall) (tools.Result, error)
	FailRunnableToolCall(context.Context, tools.Turn, model.ToolCall, error) error
}

func (e AgentExecutor) executeToolWork(
	ctx context.Context,
	input ToolWorkExecution,
	executor toolWorkExecutor,
) error {
	if e.Store == nil {
		return errors.New("kernel store is required")
	}
	if input.ProjectID == storage.NilID || input.AgentID == storage.NilID ||
		input.TurnID == storage.NilID || input.ModelCallContextID == storage.NilID ||
		input.ModelOutputID == storage.NilID || input.SourceEventID == storage.NilID ||
		input.RuntimeLockID == storage.NilID {
		return errors.New("tool work requires project, agent, turn, model output, source event, context, and runtime")
	}
	if input.Now.IsZero() {
		input.Now = e.now()
	}
	contextRecord, found, err := e.Store.Execution().GetModelCallContext(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return fmt.Errorf("load tool work model context: %w", err)
	}
	if !found {
		return fmt.Errorf("tool work model context %s not found", input.ModelCallContextID)
	}
	output, found, err := e.Store.Execution().GetModelOutputForContext(
		ctx,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return fmt.Errorf("load tool work model output: %w", err)
	}
	if contextRecord.State != executionstore.ModelCallContextSucceeded ||
		!found ||
		output.ID != input.ModelOutputID {
		return fmt.Errorf(
			"tool work does not match accepted model output for context %s: %w",
			input.ModelCallContextID,
			storeerr.ErrStateTransitionConflict,
		)
	}
	specs, err := e.modelContextToolRuntime(
		ctx,
		input.ProjectID,
		input.AgentID,
		contextRecord,
		input.Now,
	)
	if err != nil {
		return fmt.Errorf("load tool work runtime contract: %w", err)
	}
	turn := toolWorkTurn(input, contextRecord.OrgID, specs)
	deferredToolCallIDs := make([]storage.ID, 0)
	for {
		record, found, err := e.Store.Execution().NextRunnableToolCall(
			ctx,
			input.ProjectID,
			input.AgentID,
			input.ModelOutputID,
			deferredToolCallIDs,
		)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if record.TurnID != input.TurnID ||
			record.ModelCallContextID != input.ModelCallContextID ||
			record.SourceEventID != input.SourceEventID {
			return fmt.Errorf(
				"runnable tool %s does not belong to claimed tool work: %w",
				record.ID,
				storeerr.ErrStateTransitionConflict,
			)
		}
		call := modelToolCallFromRecord(record)
		switch record.State {
		case executionstore.ToolCallStateAwaitingAuthorization:
			if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
				return fmt.Errorf("prepare permission for tool %q: %w", call.ID, err)
			}
		case executionstore.ToolCallStateReady:
			result, err := executor.Dispatch(ctx, turn, call)
			if err != nil {
				return fmt.Errorf("dispatch tool %q: %w", call.ID, err)
			}
			switch result.Disposition {
			case tools.DispatchDeferred:
				deferredToolCallIDs = append(deferredToolCallIDs, record.ID)
				continue
			case tools.DispatchCompleted:
			default:
				return fmt.Errorf(
					"dispatch tool %q returned invalid disposition %d",
					call.ID,
					result.Disposition,
				)
			}
		default:
			return fmt.Errorf(
				"tool work selected non-runnable state %q: %w",
				record.State,
				storeerr.ErrStateTransitionConflict,
			)
		}
		current, err := e.Store.Execution().GetToolCall(
			ctx,
			input.ProjectID,
			input.AgentID,
			record.ID,
		)
		if err != nil {
			return fmt.Errorf("reload tool %q after work: %w", call.ID, err)
		}
		if current.State != record.State {
			continue
		}
		cause := fmt.Errorf(
			"tool coordinator returned without moving tool call out of %s",
			record.State,
		)
		if err := executor.FailRunnableToolCall(ctx, turn, call, cause); err != nil {
			return fmt.Errorf("fail stalled tool %q: %w", call.ID, err)
		}
	}
}
