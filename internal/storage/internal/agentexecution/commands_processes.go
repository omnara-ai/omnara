package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ProcessResult struct {
	ID       uuid.UUID
	Started  bool
	Observed json.RawMessage
	Content  []Content
	Outcome  string
}

type ActionResult struct {
	ID       uuid.UUID
	Observed json.RawMessage
}

type ExternalCompletion struct {
	Tool      CompletedTool
	Matches   bool
	Published bool
}

func processFacts(row executiondb.ReadExecutionProcessOwnerRow) processresult.Process {
	var exit *int
	if row.ExitCode != nil {
		n := int(*row.ExitCode)
		exit = &n
	}
	return processresult.Process{ID: row.ID, State: row.State, DefaultOutputCursor: row.DefaultOutputCursor,
		SourceStartedAt: row.SourceStartedAt,
		SourceEndedAt:   row.SourceEndedAt,
		ExitCode:        exit,
		ExitSignal:      row.ExitSignal,
		StateReasonCode: storeutil.ValueOrZero(
			row.StateReasonCode,
		), StateReasonMessage: row.StateReasonMessage, ExecutionSpec: row.ExecutionSpec}
}

func (h *Handle) CompleteProcess(ctx context.Context, input ProcessResult) (ExternalCompletion, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (ExternalCompletion, error) {
		row, err := executiondb.New().
			ReadExecutionProcessOwner(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionProcessOwnerParams{AgentID: h.route.AgentID,
					ID: input.ID})
		if err != nil {
			return ExternalCompletion{}, err
		}
		call, err := h.tool(ctx, row.ToolCallID)
		if err != nil {
			return ExternalCompletion{}, err
		}
		outcome := "succeeded"
		var result json.RawMessage
		if input.Started {
			if row.ExecutionSpec.HasFileResult() {
				return ExternalCompletion{}, storeerr.ErrInvalidToolCallDisposition
			}
			if row.State != "running" && call.State != "completed" {
				return ExternalCompletion{}, storeerr.ErrStateTransitionConflict
			}
			facts := processFacts(row)
			facts.State = "running"
			result, err = processresult.Started(facts, input.Observed)
		} else {
			if !processresult.TerminalState(row.State) {
				return ExternalCompletion{}, storeerr.ErrStateTransitionConflict
			}
			input.Observed = processresult.NormalizeObservation(input.Observed)
			outcome, result, err = processresult.Terminal(processFacts(row))
			if err == nil && len(input.Observed) > 0 {
				var body map[string]any
				err = json.Unmarshal(input.Observed, &body)
				if err == nil {
					var base map[string]any
					err = json.Unmarshal(result, &base)
					if err == nil {
						body["process_id"] = base["process_id"]
						result, err = json.Marshal(body)
					}
				}
			}
		}
		if err != nil {
			return ExternalCompletion{}, err
		}
		parts := []Content{{Kind: "structured_data", Data: result}}
		if input.Content != nil {
			if !row.ExecutionSpec.HasFileResult() || input.Started ||
				input.Outcome != "succeeded" && input.Outcome != "failed" {
				return ExternalCompletion{}, storeerr.ErrInvalidToolCallDisposition
			}
			outcome = input.Outcome
			parts = input.Content
		}
		published, err := h.completeExternalTool(ctx, m, call, outcome, parts)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if !published.Matches {
			return published, nil
		}
		next, found, err := processresult.ObservationNextCursor(result)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if published.Matches && found {
			_, err = dbsqlc.New(h.unit.DB()).
				AdvanceProcessDefaultOutputCursor(ctx,
					dbsqlc.AdvanceProcessDefaultOutputCursorParams{ProjectID: h.route.ProjectID,
						AgentID:    h.route.AgentID,
						ID:         row.ID,
						NextCursor: next})
		}
		return published, err
	})
}

func (h *Handle) CompleteProcessAction(ctx context.Context, input ActionResult) (ExternalCompletion, error) {
	return h.completeProcessAction(ctx, input, false)
}

func (h *Handle) SettleInterruptedAction(ctx context.Context, id uuid.UUID) (ExternalCompletion, error) {
	return h.completeProcessAction(ctx, ActionResult{ID: id}, true)
}

func (h *Handle) completeProcessAction(
	ctx context.Context,
	input ActionResult,
	interrupted bool,
) (ExternalCompletion, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (ExternalCompletion, error) {
		q := executiondb.New()
		action, err := q.ReadExecutionActionOwner(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionActionOwnerParams{AgentID: h.route.AgentID, ID: input.ID},
		)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if action.State != "applied" && action.State != "failed" && action.State != "unknown" {
			return ExternalCompletion{}, storeerr.ErrStateTransitionConflict
		}
		process, err := q.ReadExecutionProcessOwner(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionProcessOwnerParams{AgentID: h.route.AgentID, ID: action.ProcessID},
		)
		if err != nil {
			return ExternalCompletion{}, err
		}
		call, err := h.tool(ctx, action.ToolCallID)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if !interrupted && action.EarlierPending && call.State != "completed" && action.State == "applied" {
			return ExternalCompletion{}, storeerr.ErrProcessActionReportBlocked
		}
		outcome := "succeeded"
		if action.State != "applied" {
			outcome = "failed"
		}
		result := input.Observed
		var advance *int64
		if action.ActionKind == "read" {
			facts := processFacts(process)
			request := processresult.Action{ID: action.ID, Payload: action.Payload}
			if action.State == "applied" {
				if call.State == "completed" {
					return h.replayProcessRead(ctx, call, result)
				}
				result, advance, err = processresult.Read(facts, request, result)
			} else {
				message := action.StateReasonMessage
				if interrupted {
					message = storeutil.ValueOrZero(action.StateReasonCode)
				}
				result, err = processresult.ReadFailure(facts, request, storeutil.ValueOrZero(action.StateReasonCode), message)
			}
		} else if len(result) == 0 || string(result) == "null" {
			message := action.StateReasonMessage
			if message == "" && action.State != "applied" {
				message = storeutil.ValueOrZero(action.StateReasonCode)
			}
			result,
				err = processresult.ActionResult(process.ID,
				action.ID,
				action.State,
				storeutil.ValueOrZero(action.StateReasonCode),
				message)
		}
		if err != nil {
			return ExternalCompletion{}, err
		}
		published, err := h.completeExternalTool(
			ctx,
			m,
			call,
			outcome,
			[]Content{{Kind: "structured_data", Data: result}},
		)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if advance != nil && published.Matches {
			_, err = dbsqlc.New(h.unit.DB()).
				AdvanceProcessDefaultOutputCursor(ctx,
					dbsqlc.AdvanceProcessDefaultOutputCursorParams{ProjectID: h.route.ProjectID,
						AgentID:    h.route.AgentID,
						ID:         process.ID,
						NextCursor: *advance})
		}
		return published, err
	})
}

func (h *Handle) replayProcessRead(
	ctx context.Context,
	call executiondb.ReadExecutionToolRow,
	raw json.RawMessage,
) (ExternalCompletion, error) {
	result := ExternalCompletion{
		Tool:      CompletedTool{ID: call.ID, ResultID: storeutil.ValueOrZero(call.ResultID)},
		Published: true,
	}
	if storeutil.ValueOrZero(call.Outcome) != "succeeded" {
		return result, nil
	}
	rows, err := executiondb.New().
		ReadExecutionContent(ctx,
			h.unit.DB(),
			executiondb.ReadExecutionContentParams{AgentID: h.route.AgentID,
				ResultID: call.ResultID})
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		if row.BlockKind != "structured_data" || row.StructuredData == nil {
			continue
		}
		result.Matches, err = processresult.ReadMatches(raw, *row.StructuredData)
		return result, err
	}
	return result, nil
}

func (h *Handle) completeExternalTool(
	ctx context.Context,
	m *executionMutation,
	call executiondb.ReadExecutionToolRow,
	outcome string,
	parts []Content,
) (ExternalCompletion, error) {
	if call.Type != "built_in" || call.State != "waiting" && call.State != "completed" {
		return ExternalCompletion{}, storeerr.ErrInvalidToolCallDisposition
	}
	if call.State == "completed" {
		matches, err := h.externalContentMatches(ctx, call.ResultID, parts)
		if err != nil {
			return ExternalCompletion{}, err
		}
		if !matches {
			return ExternalCompletion{Tool: CompletedTool{ID: call.ID, ResultID: storeutil.ValueOrZero(call.ResultID)},
				Published: true}, nil
		}
	}
	completed, err := h.completeTool(ctx, m, call, outcome, parts, uuid.Nil, nil)
	if errors.Is(err, storeerr.ErrIdempotencyConflict) {
		return ExternalCompletion{
			Tool:      CompletedTool{ID: call.ID, ResultID: storeutil.ValueOrZero(call.ResultID)},
			Published: call.State == "completed",
		}, nil
	}
	return ExternalCompletion{Tool: completed, Matches: err == nil, Published: err == nil}, err
}

func (h *Handle) FailProcessTool(
	ctx context.Context,
	processID uuid.UUID,
	content []Content,
) (CompletedTool, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (CompletedTool, error) {
		process, err := executiondb.New().
			ReadExecutionProcessOwner(ctx,
				h.unit.DB(),
				executiondb.ReadExecutionProcessOwnerParams{AgentID: h.route.AgentID,
					ID: processID})
		if err != nil {
			return CompletedTool{}, err
		}
		call, err := h.tool(ctx, process.ToolCallID)
		if err != nil {
			return CompletedTool{}, err
		}
		if call.State != "waiting" || call.Type != "built_in" {
			return CompletedTool{ID: call.ID}, nil
		}
		result, err := h.completeExternalTool(ctx, m, call, "failed", content)
		return result.Tool, err
	})
}

func (h *Handle) externalContentMatches(ctx context.Context, resultID *uuid.UUID, parts []Content) (bool, error) {
	rows, err := executiondb.New().ReadExecutionContent(ctx, h.unit.DB(), executiondb.ReadExecutionContentParams{
		AgentID: h.route.AgentID, ResultID: resultID})
	if err != nil {
		return false, err
	}
	if len(rows) != len(parts) {
		return false, nil
	}
	for i, row := range rows {
		part := parts[i]
		metadata, err := objectJSON(part.Metadata)
		if err != nil {
			return false, err
		}
		if row.Ordinal != int32(i) || row.BlockKind != part.Kind || storeutil.TextOrEmpty(row.TextContent) != part.Text ||
			storeutil.IDFromPtr(row.ArtifactID) != part.ArtifactID || storeutil.IDFromPtr(row.ToolCallID) != part.ToolID ||
			row.ExcludeFromModelContext != part.Exclude || !equalJSON(row.Metadata, metadata) ||
			((len(part.Data) > 0 || row.StructuredData != nil) &&
				!equalJSON(storeutil.ValueOrZero(row.StructuredData), part.Data)) {
			return false, nil
		}
	}
	return true, nil
}
