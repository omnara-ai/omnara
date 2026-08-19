package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) MarkProcessStarted(
	ctx context.Context,
	input MarkProcessStartedInput,
) (DaemonProcessReportApplication, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
		return DaemonProcessReportApplication{}, errors.New("project, agent, and process are required")
	}
	if input.SourceStartedAt.IsZero() {
		return DaemonProcessReportApplication{}, errors.New(
			"process physical start time is required",
		)
	}
	input.SourceStartedAt = canonicalSourceTime(input.SourceStartedAt)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("begin mark process started: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := requireReportableDaemonRuntimeAuthorityTx(ctx, qtx, input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	_, err = qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: input.ProjectID,
			ID:        input.AgentID,
		},
	)
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("lock agent for process start observation: %w", err)
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
	var committedResult json.RawMessage
	if !isNilID(record.ToolCallID) {
		startedRecord := record
		startedRecord.State = ProcessStateRunning
		result, err := startedProcessToolResult(startedRecord, input.Result)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		contentParts, err := ToolResultContentParts(result)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		outcome := ToolResultOutcomeSucceeded
		toolRow, err := qtx.CompleteToolCallFromStartedProcess(
			ctx,
			dbsqlc.CompleteToolCallFromStartedProcessParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        record.ToolCallID,
				ProcessID: record.ID,
				Outcome:   string(outcome),
			},
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				publication, checkErr := inspectPublishedToolCallResultTx(
					ctx,
					qtx,
					input.ProjectID,
					input.AgentID,
					record.ToolCallID,
					outcome,
					contentParts,
				)
				if checkErr != nil {
					return DaemonProcessReportApplication{}, checkErr
				}
				if publication.Matches {
					resultCommitted = true
					committedResult = result
				} else if !publication.Published {
					return DaemonProcessReportApplication{}, fmt.Errorf(
						"linked tool call %s has no durable result for started process %s",
						record.ToolCallID,
						record.ID,
					)
				}
			} else {
				return DaemonProcessReportApplication{}, fmt.Errorf("complete tool call from started process: %w", err)
			}
		} else {
			resultCommitted = true
			committedResult = result
			resultRecord := toolCallRecordFromStartedProcessCompleteSQLC(toolRow)
			resultRecord.ResultContentParts = contentParts
			if _, err := appendToolResultEventTx(
				ctx,
				txNotifications,
				tx,
				resultRecord,
			); err != nil {
				return DaemonProcessReportApplication{}, err
			}
			metadata, err := marshalJSON(
				map[string]any{
					"reason":       "process_started",
					"process_id":   record.ID,
					"tool_call_id": record.ToolCallID,
				},
			)
			if err != nil {
				return DaemonProcessReportApplication{}, fmt.Errorf("marshal process started wakeup metadata: %w", err)
			}
			if err := qtx.MarkAgentWakeup(
				ctx,
				dbsqlc.MarkAgentWakeupParams{
					ProjectID: input.ProjectID,
					AgentID:   input.AgentID,
					Metadata:  metadata,
				},
			); err != nil {
				return DaemonProcessReportApplication{}, fmt.Errorf("mark started process tool result wakeup: %w", err)
			}
		}
	}
	if resultCommitted {
		if err := advanceProcessDefaultOutputCursorFromResult(
			ctx,
			qtx,
			&record,
			committedResult,
		); err != nil {
			return DaemonProcessReportApplication{}, fmt.Errorf(
				"advance started process output cursor: %w",
				err,
			)
		}
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "mark process started"); err != nil {
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
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
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
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("begin complete daemon process: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := requireReportableDaemonRuntimeAuthorityTx(ctx, qtx, input.Authority); err != nil {
		return DaemonProcessReportApplication{}, err
	}
	_, err = qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: input.ProjectID,
			ID:        input.AgentID,
		},
	)
	if err != nil {
		return DaemonProcessReportApplication{}, fmt.Errorf("lock agent for daemon process completion: %w", err)
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
			ExitCode:           sqlcInt32Ptr(input.ExitCode),
			ExitSignal:         input.ExitSignal,
			StateReasonCode:    sqlcTextFromEmpty(input.StateReasonCode),
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
	var committedResult json.RawMessage
	if reportMatchesProcess && !isNilID(record.ToolCallID) {
		outcome, result, resultErr := processToolResult(record)
		if resultErr != nil {
			return DaemonProcessReportApplication{}, resultErr
		}
		if len(input.Result) > 0 && string(input.Result) != "null" {
			result, err = commandTerminalToolResult(record.ID, input.Result)
			if err != nil {
				return DaemonProcessReportApplication{}, err
			}
		}
		contentParts, err := ToolResultContentParts(result)
		if err != nil {
			return DaemonProcessReportApplication{}, err
		}
		toolRow, err := qtx.CompleteToolCallFromProcess(
			ctx,
			dbsqlc.CompleteToolCallFromProcessParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        record.ToolCallID,
				ProcessID: record.ID,
				Outcome:   string(outcome),
			},
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				publication, checkErr := inspectPublishedToolCallResultTx(
					ctx,
					qtx,
					input.ProjectID,
					input.AgentID,
					record.ToolCallID,
					outcome,
					contentParts,
				)
				if checkErr != nil {
					return DaemonProcessReportApplication{}, checkErr
				}
				if publication.Matches {
					resultCommitted = true
					committedResult = result
				} else if !publication.Published {
					return DaemonProcessReportApplication{}, fmt.Errorf(
						"linked tool call %s has no durable result for process %s",
						record.ToolCallID,
						record.ID,
					)
				}
			} else {
				return DaemonProcessReportApplication{}, fmt.Errorf("complete tool call from daemon process: %w", err)
			}
		} else {
			resultCommitted = true
			committedResult = result
			resultRecord := toolCallRecordFromProcessCompleteSQLC(toolRow)
			resultRecord.ResultContentParts = contentParts
			if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
				return DaemonProcessReportApplication{}, err
			}
			metadata, err := marshalJSON(
				map[string]any{
					"reason":       "process_result",
					"process_id":   record.ID,
					"tool_call_id": record.ToolCallID,
				},
			)
			if err != nil {
				return DaemonProcessReportApplication{}, fmt.Errorf("marshal process result wakeup metadata: %w", err)
			}
			if err := qtx.MarkAgentWakeup(
				ctx,
				dbsqlc.MarkAgentWakeupParams{
					ProjectID: input.ProjectID,
					AgentID:   input.AgentID,
					Metadata:  metadata,
				},
			); err != nil {
				return DaemonProcessReportApplication{}, fmt.Errorf("mark process tool result wakeup: %w", err)
			}
		}
	} else if !isNilID(record.ToolCallID) {
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
	if resultCommitted {
		if err := advanceProcessDefaultOutputCursorFromResult(
			ctx,
			qtx,
			&record,
			committedResult,
		); err != nil {
			return DaemonProcessReportApplication{}, fmt.Errorf(
				"advance completed process output cursor: %w",
				err,
			)
		}
	}
	if input.StorageExhausted {
		if err := completeUnresolvedProcessActionsForClosedProcessTx(
			ctx,
			txNotifications,
			tx,
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
			txNotifications,
			tx,
			qtx,
			record,
		); err != nil {
			return DaemonProcessReportApplication{}, err
		}
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete daemon process"); err != nil {
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
	projectID, agentID ID,
	authority DaemonRuntimeAuthority,
	processID ID,
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
	projectID, agentID ID,
	toolCallID ID,
) (ProcessRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(toolCallID) {
		return ProcessRecord{}, false, errors.New("project, agent, and tool call are required")
	}
	return getProcessByToolCallTx(ctx, s.pool, projectID, agentID, toolCallID)
}

func (s *Store) ListActiveProcessesForContext(
	ctx context.Context,
	projectID, agentID ID,
) ([]ActiveProcessRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	return listActiveProcessesForContext(ctx, s.q, projectID, agentID)
}

func (r *ToolCallReader) ListActiveProcessesForContext(
	ctx context.Context,
) ([]ActiveProcessRecord, error) {
	t := r.transaction
	return listActiveProcessesForContext(ctx, t.q, t.input.ProjectID, t.input.AgentID)
}

func listActiveProcessesForContext(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
) ([]ActiveProcessRecord, error) {
	rows, err := q.ListActiveProcessesForContext(
		ctx,
		dbsqlc.ListActiveProcessesForContextParams{ProjectID: projectID, AgentID: agentID},
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
	projectID, agentID, toolCallID ID,
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
		dbsqlc.GetToolCallResultByToolCallParams{ProjectID: projectID, AgentID: agentID, ToolCallID: toolCallID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load linked tool result: %w", err)
	}
	eventExists, err := qtx.ToolCallResultHasTypedEvent(
		ctx,
		dbsqlc.ToolCallResultHasTypedEventParams{ProjectID: projectID, AgentID: agentID, ToolCallResultID: &result.ID},
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
	projectID, agentID, toolCallID ID,
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
	storedParts, err := toolCallResultContentBlocksTx(
		ctx,
		qtx,
		projectID,
		agentID,
		result.ID,
	)
	if err != nil {
		return toolCallResultPublication{}, err
	}
	return toolCallResultPublication{
		Published: true,
		Matches: result.Outcome == string(outcome) &&
			sameJSON(storedParts, normalizedJSON(contentParts)),
	}, nil
}

func completedToolCallMissIsBenignTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID ID,
	allowStartedProcessResult bool,
) (bool, error) {
	existing, err := qtx.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: toolCallID},
	)
	if err != nil {
		return false, fmt.Errorf("load linked tool call after completion miss: %w", err)
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
		return false, fmt.Errorf("load linked tool result after completion miss: %w", err)
	}
	contentParts, err := toolCallResultContentBlocksTx(
		ctx,
		qtx,
		projectID,
		agentID,
		result.ID,
	)
	if err != nil {
		return false, err
	}
	canceled := result.Outcome == string(ToolResultOutcomeCanceled)
	machineUnreachable := structuredContentStringFieldEquals(
		contentParts,
		"error_code",
		ProcessToolReasonMachineUnreachable,
	)
	startedProcess := allowStartedProcessResult &&
		result.Outcome == string(ToolResultOutcomeSucceeded) &&
		structuredContentStringFieldEquals(
			contentParts,
			"state",
			string(ProcessStateRunning),
		)
	if !canceled && !machineUnreachable && !startedProcess {
		return false, nil
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
		return false, fmt.Errorf(
			"check linked tool result event after completion miss: %w",
			err,
		)
	}
	return eventExists, nil
}

func structuredContentStringFieldEquals(
	raw json.RawMessage,
	field string,
	want string,
) bool {
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if part.Type == "structured_data" &&
			jsonObjectStringFieldEquals(part.Value, field, want) {
			return true
		}
	}
	return false
}

func jsonObjectStringFieldEquals(
	raw json.RawMessage,
	field string,
	want string,
) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	value, ok := object[field]
	if !ok {
		return false
	}
	var got string
	return json.Unmarshal(value, &got) == nil && got == want
}
