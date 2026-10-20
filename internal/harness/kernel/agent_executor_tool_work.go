package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (e AgentExecutor) ExecuteToolWorkStep(
	ctx context.Context, input ToolWorkExecution,
) (executionstore.StepDisposition, error) {
	disposition := executionstore.StepRetained
	input.finishClaim = true
	input.disposition = &disposition
	err := e.ExecuteToolWork(ctx, input)
	return disposition, err
}

func (e AgentExecutor) ExecuteToolWork(ctx context.Context, input ToolWorkExecution) error {
	return e.executeToolWork(ctx, input, e.configuredToolExecutor())
}

type toolWorkExecutor interface {
	FailRunnableToolCall(context.Context, tools.Turn, model.ToolCall, error) error
	PrepareRecordedToolCallPermission(
		context.Context, tools.Turn, executionstore.ToolCallRecord,
	) (tools.ToolPermissionPreparation, error)
	DispatchRecordedToolCall(context.Context, tools.Turn, executionstore.ToolCallRecord) (tools.Result, error)
}

func (e AgentExecutor) executeToolWork(
	ctx context.Context,
	input ToolWorkExecution,
	executor toolWorkExecutor,
) error {
	if e.Store == nil {
		return errors.New("kernel store is required")
	}
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil ||
		input.TurnID == uuid.Nil || input.ModelCallContextID == uuid.Nil ||
		input.ModelOutputID == uuid.Nil || input.SourceEventID == uuid.Nil ||
		input.RuntimeLockID == uuid.Nil {
		return errors.New("tool work requires project, agent, turn, model output, source event, context, and runtime")
	}
	if input.Now.IsZero() {
		input.Now = e.now()
	}
	if input.Prepared == nil {
		return errors.New("prepared tool work is required")
	}
	contextRecord := input.Prepared.Context
	if contextRecord.ID != input.ModelCallContextID ||
		contextRecord.AgentID != input.AgentID ||
		contextRecord.ProjectID != input.ProjectID ||
		contextRecord.AgentConfigID != input.Prepared.Config.ID {
		return storeerr.ErrStateTransitionConflict
	}
	specs, err := e.toolRuntimeFromConfig(ctx, input.ProjectID, input.AgentID, input.Prepared.Config, input.Now)
	if err != nil {
		return fmt.Errorf("load tool work runtime contract: %w", err)
	}
	first := input.Prepared.FirstCall
	turn := toolWorkTurn(input, contextRecord.OrgID, specs)
	turn.RootAgentID = input.RootAgentID
	if input.finishClaim {
		turn.MergeAuthorization = true
		turn.BatchOutputID = input.ModelOutputID
		turn.Completion = input.Completion
		turn.StepDisposition = input.disposition
	}
	deferredToolCallIDs := make([]uuid.UUID, 0)
	for {
		var record executionstore.ToolCallRecord
		var found bool
		if first != nil {
			record, found = *first, true
			first = nil
		} else {
			record, found, err = e.Store.Execution().NextRunnableToolCall(
				ctx,
				input.ProjectID,
				input.AgentID,
				input.ModelOutputID,
				deferredToolCallIDs,
			)
		}
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
		turn.AuthorizeOnDispatch = false
		call := modelToolCallFromRecord(record)
		if record.State == executionstore.ToolCallStateAwaitingAuthorization {
			preparation, err := executor.PrepareRecordedToolCallPermission(ctx, turn, record)
			if err != nil {
				return fmt.Errorf("prepare permission for tool %q: %w", call.ID, err)
			}
			prepared := preparation.Record
			if prepared.ID == uuid.Nil {
				prepared, err = e.Store.Execution().GetToolCall(ctx, input.ProjectID, input.AgentID, record.ID)
				if err != nil {
					return err
				}
			}
			if preparation.Authorization == tools.AuthorizationOnDispatch {
				turn.AuthorizeOnDispatch = true
			} else if prepared.State == record.State {
				if err := executor.FailRunnableToolCall(ctx, turn, call,
					fmt.Errorf("tool coordinator returned without moving tool call out of %s", record.State)); err != nil {
					return err
				}
				continue
			}
			record = prepared
		}
		if record.State != executionstore.ToolCallStateReady && !turn.AuthorizeOnDispatch ||
			!toolcatalog.IsPlatformManagedToolType(record.Type) {
			continue
		}
		result, err := executor.DispatchRecordedToolCall(ctx, turn, record)
		if err != nil {
			return fmt.Errorf("dispatch tool %q: %w", call.ID, err)
		}
		if input.disposition != nil && *input.disposition != executionstore.StepRetained {
			return nil
		}
		switch result.Disposition {
		case tools.DispatchDeferred:
			turn.BatchOutputID = uuid.Nil
			deferredToolCallIDs = append(deferredToolCallIDs, record.ID)
		case tools.DispatchCompleted:
		default:
			return fmt.Errorf("dispatch tool %q returned invalid disposition %d", call.ID, result.Disposition)
		}
	}
}
