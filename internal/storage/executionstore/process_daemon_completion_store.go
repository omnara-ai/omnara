package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) MarkProcessStarted(
	ctx context.Context,
	input MarkProcessStartedInput,
) (DaemonProcessReportApplication, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ID == uuid.Nil {
		return DaemonProcessReportApplication{}, errors.New("project, agent, and process are required")
	}
	if input.SourceStartedAt.IsZero() {
		return DaemonProcessReportApplication{}, errors.New(
			"process physical start time is required",
		)
	}
	input.SourceStartedAt = canonicalSourceTime(input.SourceStartedAt)
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("begin mark process started: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()
	qtx := dbsqlc.New(tx)
	if err := requireReportableDaemonRuntimeAuthorityTx(ctx, qtx, input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	_, err = unit.LockAgent(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	}, agentexecution.ExternalAuthority{})
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf(
			"lock agent for process start observation: %w",
			err,
		)
	}
	row, err := qtx.MarkProcessStarted(
		ctx,
		dbsqlc.MarkProcessStartedParams{
			ProjectID:       input.ProjectID,
			AgentID:         input.AgentID,
			ID:              input.ID,
			MachineID:       input.Authority.MachineID,
			SourceStartedAt: input.SourceStartedAt,
		},
	)
	var record ProcessRecord
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = daemonProcessForReportTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			input.Authority,
			input.ID,
			false,
		)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		if !isProcessTerminal(record.State) {
			return DaemonProcessReportApplication{}, storeerr.ErrDaemonRuntimeUnregistered
		}
	} else if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("mark process started: %w", err)
	} else {
		record = processRecordFromStartedSQLC(row)
	}
	resultCommitted := false
	if record.ToolCallID != uuid.Nil && record.ExecutionSpec.FileTransfer == nil {
		h, err := unit.Handle(input.ProjectID, input.AgentID)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		completed, err := h.CompleteProcess(
			ctx,
			agentexecution.ProcessResult{ID: record.ID, Started: true, Observed: input.Result},
		)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		resultCommitted = completed.Matches
		row, err := qtx.GetProcess(
			ctx,
			dbsqlc.GetProcessParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: record.ID},
		)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		record = processRecordFromSQLC(row)
	}
	if err := unit.Commit(ctx, "mark process started"); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	return DaemonProcessReportApplication{
		Process:             record,
		ToolResultCommitted: resultCommitted,
	}, nil
}

func (s *Store) CompleteDaemonProcess(
	ctx context.Context,
	input CompleteDaemonProcessInput,
) (DaemonProcessReportApplication, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ID == uuid.Nil {
		return DaemonProcessReportApplication{}, errors.New("project, agent, and process are required")
	}
	if !isProcessTerminal(input.State) {
		return DaemonProcessReportApplication{}, errors.New(
			"process completion requires a terminal state",
		)
	}
	if !daemonprotocol.ValidProcessTerminalSourceTimes(
		daemonprotocol.ProcessState(input.State),
		input.SourceStartedAt,
		input.SourceEndedAt,
	) {
		return DaemonProcessReportApplication{}, errors.New(
			"process terminal source times do not match its state",
		)
	}
	if !input.SourceStartedAt.IsZero() {
		input.SourceStartedAt = canonicalSourceTime(input.SourceStartedAt)
	}
	if !input.SourceEndedAt.IsZero() {
		input.SourceEndedAt = canonicalSourceTime(input.SourceEndedAt)
	}
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("begin complete daemon process: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()

	qtx := dbsqlc.New(tx)
	if err := requireReportableDaemonRuntimeAuthorityTx(ctx, qtx, input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	_, err = unit.LockAgent(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	}, agentexecution.ExternalAuthority{})
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf(
			"lock agent for daemon process completion: %w",
			err,
		)
	}
	var startedAt *time.Time
	if !input.SourceStartedAt.IsZero() {
		startedAt = &input.SourceStartedAt
	}
	var endedAt *time.Time
	if !input.SourceEndedAt.IsZero() {
		endedAt = &input.SourceEndedAt
	}
	row, err := qtx.CompleteDaemonObservedProcess(
		ctx,
		dbsqlc.CompleteDaemonObservedProcessParams{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			ID:                 input.ID,
			MachineID:          input.Authority.MachineID,
			State:              string(input.State),
			SourceStartedAt:    startedAt,
			SourceEndedAt:      endedAt,
			ExitCode:           storeutil.Int32Ptr(input.ExitCode),
			ExitSignal:         input.ExitSignal,
			StateReasonCode:    storeutil.TextFromEmpty(input.StateReasonCode),
			StateReasonMessage: input.StateReasonMessage,
			StorageExhausted:   input.StorageExhausted,
		},
	)
	processUpdated := err == nil
	var record ProcessRecord
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = daemonProcessForReportTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			input.Authority,
			input.ID,
			input.StorageExhausted,
		)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		if !isProcessTerminal(record.State) {
			return DaemonProcessReportApplication{}, storeerr.ErrDaemonRuntimeUnregistered
		}
	} else if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("complete daemon process: %w", err)
	} else {
		record = processRecordFromCompleteSQLC(row)
	}
	reportMatchesProcess := processUpdated || daemonTerminalReportMatchesRecord(record, input)
	resultCommitted := false
	if reportMatchesProcess && record.ToolCallID != uuid.Nil {
		request := agentexecution.ProcessResult{ID: record.ID, Observed: input.Result}
		if record.ExecutionSpec.FileTransfer != nil {
			_, result, err := processresult.Terminal(record.resultFacts())
			if err != nil {
				return DaemonProcessReportApplication{}, err
			}
			if len(input.Result) > 0 && string(input.Result) != "null" {
				result, err = commandTerminalToolResult(record.ID, input.Result)
				if err != nil {
					return DaemonProcessReportApplication{}, err
				}
			}
			outcome, raw, err := fileTransferToolResultContentParts(ctx, qtx, record, result)
			if err != nil {
				return DaemonProcessReportApplication{}, err
			}
			parts, err := parseToolResultContentBlocks(raw)
			if err != nil {
				return DaemonProcessReportApplication{}, err
			}
			request.Content, err = executionContent(parts)
			if err != nil {
				return DaemonProcessReportApplication{}, err
			}
			request.Outcome = string(outcome)
		}
		h, err := unit.Handle(input.ProjectID, input.AgentID)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		completed, err := h.CompleteProcess(ctx, request)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		resultCommitted = completed.Matches
		row, err := qtx.GetProcess(
			ctx,
			dbsqlc.GetProcessParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: record.ID},
		)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		record = processRecordFromSQLC(row)
	} else if record.ToolCallID != uuid.Nil {
		published, checkErr := publishedToolCallResultExistsTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			record.ToolCallID,
		)
		if checkErr != nil {
			return DaemonProcessReportApplication{}, checkErr
		}
		if !published {
			return DaemonProcessReportApplication{}, fmt.Errorf(
				"terminal process %s has no durable tool result",
				record.ID,
			)
		}
	}
	if input.StorageExhausted {
		if err := completeUnresolvedProcessActionsForClosedProcessTx(
			ctx,
			unit,
			qtx,
			record.OrgID,
			record.ID,
			daemonprotocol.ProcessReasonMachineStorageExhausted,
		); err != nil {
			return DaemonProcessReportApplication{}, err
		}
	} else if processUpdated {
		if err := completeQueuedProcessActionsForTerminalProcessTx(
			ctx,
			unit,
			qtx,
			record,
		); err != nil {
			return DaemonProcessReportApplication{}, err
		}
	}
	if err := unit.Commit(ctx, "complete daemon process"); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	return DaemonProcessReportApplication{
		Process:             record,
		ToolResultCommitted: resultCommitted,
	}, nil
}

func daemonProcessForReportTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	authority DaemonRuntimeAuthority,
	processID uuid.UUID,
	allowUngranted bool,
) (ProcessRecord, error) {
	row, err := qtx.GetDaemonProcessForProjectReport(
		ctx,
		dbsqlc.GetDaemonProcessForProjectReportParams{
			ProjectID: projectID,
			MachineID: authority.MachineID,
			ID:        processID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessRecord{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("load daemon process report: %w", err)
	}
	record := processRecordFromSQLC(row)
	if record.AgentID != agentID {
		return ProcessRecord{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if record.ExecutionGrantedAt == nil && !allowUngranted {
		return ProcessRecord{}, storeerr.ErrProcessExecutionNotGranted
	}
	return record, nil
}

func daemonTerminalReportMatchesRecord(
	record ProcessRecord,
	input CompleteDaemonProcessInput,
) bool {
	return record.State == input.State &&
		sameOptionalInt(record.ExitCode, input.ExitCode) &&
		record.ExitSignal == input.ExitSignal &&
		record.StateReasonCode == input.StateReasonCode &&
		record.StateReasonMessage == input.StateReasonMessage &&
		matchesProvidedSourceTime(record.SourceStartedAt, input.SourceStartedAt) &&
		(input.StorageExhausted ||
			matchesTerminalSourceTime(record.SourceEndedAt, input.SourceEndedAt))
}

func matchesProvidedSourceTime(stored *time.Time, provided time.Time) bool {
	return provided.IsZero() || (stored != nil && stored.Equal(provided))
}

func matchesTerminalSourceTime(stored *time.Time, provided time.Time) bool {
	if provided.IsZero() {
		return stored == nil
	}
	return stored != nil && stored.Equal(provided)
}

func (s *Store) GetProcessByToolCall(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	toolCallID uuid.UUID,
) (ProcessRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || toolCallID == uuid.Nil {
		return ProcessRecord{}, false, errors.New("project, agent, and tool call are required")
	}
	return getProcessByToolCallTx(ctx, s.pool, projectID, agentID, toolCallID)
}

func (r *ToolCallReader) ListActiveProcesses(
	ctx context.Context,
) ([]ActiveProcessRecord, error) {
	t := r.transaction
	return listActiveProcesses(ctx, t.q, t.input.ProjectID, t.input.AgentID)
}

func listActiveProcesses(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) ([]ActiveProcessRecord, error) {
	rows, err := q.ListActiveProcesses(
		ctx,
		dbsqlc.ListActiveProcessesParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list active processes: %w", err)
	}
	out := make([]ActiveProcessRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, activeProcessRecordFromSQLC(row))
	}
	return out, nil
}

func isProcessActionTerminal(state ProcessActionState) bool {
	switch state {
	case ProcessActionStateApplied, ProcessActionStateFailed, ProcessActionStateUnknown:
		return true
	default:
		return false
	}
}

func sameOptionalInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func publishedToolCallResultExistsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID uuid.UUID,
) (bool, error) {
	existing, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: toolCallID},
	)
	if err != nil {
		return false, fmt.Errorf("load linked tool call result: %w", err)
	}
	if existing.State != "completed" || existing.CompletedAt == nil {
		return false, nil
	}
	result, err := qtx.GetToolCallResultByToolCall(
		ctx,
		dbsqlc.GetToolCallResultByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load linked tool result: %w", err)
	}
	eventExists, err := qtx.ToolCallResultHasTypedEvent(
		ctx,
		dbsqlc.ToolCallResultHasTypedEventParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			ToolCallResultID: &result.ID,
		},
	)
	if err != nil {
		return false, fmt.Errorf("check linked tool result event: %w", err)
	}
	return eventExists, nil
}

type toolCallResultPublication struct {
	Published bool
	Matches   bool
}

func inspectPublishedToolCallResultTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID uuid.UUID,
	outcome ToolResultOutcome,
	contentParts json.RawMessage,
) (toolCallResultPublication, error) {
	existing, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        toolCallID,
		},
	)
	if err != nil {
		return toolCallResultPublication{}, fmt.Errorf(
			"load linked tool call publication: %w",
			err,
		)
	}
	if existing.State != "completed" || existing.CompletedAt == nil {
		return toolCallResultPublication{}, nil
	}
	result, err := qtx.GetToolCallResultByToolCall(
		ctx,
		dbsqlc.GetToolCallResultByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return toolCallResultPublication{}, nil
	}
	if err != nil {
		return toolCallResultPublication{}, fmt.Errorf(
			"load linked tool result publication: %w",
			err,
		)
	}
	eventExists, err := qtx.ToolCallResultHasTypedEvent(
		ctx,
		dbsqlc.ToolCallResultHasTypedEventParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			ToolCallResultID: &result.ID,
		},
	)
	if err != nil {
		return toolCallResultPublication{}, fmt.Errorf(
			"check linked tool result publication: %w",
			err,
		)
	}
	if !eventExists {
		return toolCallResultPublication{}, nil
	}
	tool, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: toolCallID},
	)
	storedParts := tool.ResultContentParts
	if err != nil {
		return toolCallResultPublication{}, err
	}
	return toolCallResultPublication{
		Published: true,
		Matches: result.Outcome == string(outcome) &&
			sameJSON(storedParts, normalizedJSON(contentParts)),
	}, nil
}
