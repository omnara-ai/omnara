package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type processActionCompletionInput struct {
	ProjectID          ID
	AgentID            ID
	ProcessID          ID
	ID                 ID
	StateReasonCode    string
	StateReasonMessage string
	Result             json.RawMessage
}

func replayDaemonProcessActionReportTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input processActionCompletionInput,
	state ProcessActionState,
) (DaemonProcessActionReportApplication, bool, error) {
	row, err := qtx.GetProcessActionForReport(
		ctx,
		dbsqlc.GetProcessActionForReportParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ProcessID: input.ProcessID,
			ID:        input.ID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DaemonProcessActionReportApplication{}, false, nil
	}
	if err != nil {
		return DaemonProcessActionReportApplication{}, false, fmt.Errorf("load process action report replay: %w", err)
	}
	record := processActionRecordFromSQLC(row)
	if record.ID != input.ID || !isProcessActionTerminal(record.State) {
		return DaemonProcessActionReportApplication{}, false, nil
	}
	var result json.RawMessage
	outcome := ToolResultOutcomeSucceeded
	errText := ""
	reportMatchesAction := record.State == state
	switch state {
	case ProcessActionStateApplied:
		if record.StateReasonCode != input.StateReasonCode ||
			record.StateReasonMessage != input.StateReasonMessage {
			reportMatchesAction = false
		}
	case ProcessActionStateFailed, ProcessActionStateUnknown:
		if record.StateReasonCode != input.StateReasonCode || record.StateReasonMessage != input.StateReasonMessage {
			reportMatchesAction = false
		}
		outcome = ToolResultOutcomeFailed
		errText = input.StateReasonMessage
		if errText == "" {
			errText = input.StateReasonCode
		}
	default:
		return DaemonProcessActionReportApplication{}, false, nil
	}
	result = input.Result
	readObservationReplay := false
	if reportMatchesAction && record.ActionKind == ProcessActionKindRead {
		if state == ProcessActionStateApplied {
			readObservationReplay = true
		} else if state == ProcessActionStateFailed {
			if len(input.Result) != 0 && string(input.Result) != "null" {
				reportMatchesAction = false
			} else {
				processRow, processErr := qtx.GetProcessForUpdate(
					ctx,
					dbsqlc.GetProcessForUpdateParams{
						ProjectID: input.ProjectID,
						AgentID:   input.AgentID,
						ID:        input.ProcessID,
					},
				)
				if processErr != nil {
					return DaemonProcessActionReportApplication{}, false, fmt.Errorf(
						"lock process for failed read report replay: %w",
						processErr,
					)
				}
				result, err = canonicalProcessReadFailureResult(
					processRecordFromSQLC(processRow),
					record,
					input.StateReasonCode,
					input.StateReasonMessage,
				)
				if err != nil {
					return DaemonProcessActionReportApplication{}, false, err
				}
			}
		}
	}
	if len(result) == 0 || string(result) == "null" {
		result, err = processActionToolResult(
			input.ProcessID,
			input.ID,
			state,
			input.StateReasonCode,
			errText,
		)
		if err != nil {
			return DaemonProcessActionReportApplication{}, false, err
		}
	}
	resultCommitted := false
	published := false
	publicationChecked := false
	if reportMatchesAction &&
		readObservationReplay &&
		!isNilID(record.ToolCallID) {
		publication, err := inspectPublishedProcessReadObservationTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			record.ToolCallID,
			input.Result,
		)
		if err != nil {
			return DaemonProcessActionReportApplication{}, false, err
		}
		resultCommitted = publication.Matches
		published = publication.Published
		publicationChecked = true
	}
	if reportMatchesAction &&
		!publicationChecked &&
		!isNilID(record.ToolCallID) {
		contentParts, err := ToolResultContentParts(result)
		if err != nil {
			return DaemonProcessActionReportApplication{}, false, err
		}
		publication, err := inspectPublishedToolCallResultTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			record.ToolCallID,
			outcome,
			contentParts,
		)
		if err != nil {
			return DaemonProcessActionReportApplication{}, false, err
		}
		resultCommitted = publication.Matches
		published = publication.Published
		publicationChecked = true
	}
	if !resultCommitted && !isNilID(record.ToolCallID) {
		if !publicationChecked {
			var err error
			published, err = publishedToolCallResultExistsTx(
				ctx,
				qtx,
				input.ProjectID,
				input.AgentID,
				record.ToolCallID,
			)
			if err != nil {
				return DaemonProcessActionReportApplication{}, false, err
			}
		}
		if !published {
			return DaemonProcessActionReportApplication{}, false, fmt.Errorf(
				"terminal process action %s has no durable tool result",
				record.ID,
			)
		}
	}
	return DaemonProcessActionReportApplication{
		Action:              record,
		ToolResultCommitted: resultCommitted,
	}, true, nil
}

func (s *Store) ApplyDaemonProcessAction(
	ctx context.Context,
	input CompleteDaemonProcessActionInput,
) (DaemonProcessActionReportApplication, error) {
	return s.completeDaemonProcessAction(ctx, input, ProcessActionStateApplied)
}

func (s *Store) FailDaemonProcessAction(
	ctx context.Context,
	input CompleteDaemonProcessActionInput,
) (DaemonProcessActionReportApplication, error) {
	return s.completeDaemonProcessAction(ctx, input, ProcessActionStateFailed)
}

func (s *Store) MarkDaemonProcessActionUnknown(
	ctx context.Context,
	input CompleteDaemonProcessActionInput,
) (DaemonProcessActionReportApplication, error) {
	return s.completeDaemonProcessAction(ctx, input, ProcessActionStateUnknown)
}

func (s *Store) completeDaemonProcessAction(
	ctx context.Context,
	input CompleteDaemonProcessActionInput,
	state ProcessActionState,
) (DaemonProcessActionReportApplication, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ProcessID) || isNilID(input.ID) {
		return DaemonProcessActionReportApplication{}, errors.New("project, agent, process, and action are required")
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf("begin complete process action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := requireReportableDaemonRuntimeAuthorityTx(ctx, qtx, input.Authority); err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	completion := processActionCompletionInput{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ProcessID:          input.ProcessID,
		ID:                 input.ID,
		StateReasonCode:    input.StateReasonCode,
		StateReasonMessage: input.StateReasonMessage,
		Result:             input.Result,
	}
	application, err := completeDaemonProcessActionTx(ctx, txNotifications, tx, qtx, completion, state)
	if err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete process action"); err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	return application, nil
}

func completeDaemonProcessActionTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input processActionCompletionInput,
	state ProcessActionState,
) (DaemonProcessActionReportApplication, error) {
	_, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{
			ProjectID: input.ProjectID,
			ID:        input.AgentID,
		},
	)
	if err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf("lock agent for process action completion: %w", err)
	}
	actionRow, err := qtx.GetProcessActionForReport(
		ctx,
		dbsqlc.GetProcessActionForReportParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ProcessID: input.ProcessID,
			ID:        input.ID,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DaemonProcessActionReportApplication{}, fmt.Errorf(
			"load process action before completion: %w",
			err,
		)
	}
	reportAccepted := true
	var readNextCursor *int64
	if err == nil && state == ProcessActionStateApplied {
		kind := ProcessActionKind(actionRow.ActionKind)
		ordinaryApplied := input.StateReasonCode == "" &&
			input.StateReasonMessage == ""
		alreadyStopped := kind == ProcessActionKindTerminate &&
			input.StateReasonCode == daemonprotocol.ProcessActionReasonAlreadyStopped &&
			input.StateReasonMessage == ""
		if !ordinaryApplied && !alreadyStopped {
			return DaemonProcessActionReportApplication{}, fmt.Errorf(
				"applied %s process action has invalid reason %q",
				kind,
				input.StateReasonCode,
			)
		}
	}
	if err == nil &&
		ProcessActionKind(actionRow.ActionKind) == ProcessActionKindRead &&
		ProcessActionState(actionRow.State) == ProcessActionStateAccepted {
		processRow, processErr := qtx.GetProcessForUpdate(
			ctx,
			dbsqlc.GetProcessForUpdateParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ID:        input.ProcessID,
			},
		)
		if processErr != nil {
			return DaemonProcessActionReportApplication{}, fmt.Errorf(
				"lock process for read completion: %w",
				processErr,
			)
		}
		process := processRecordFromSQLC(processRow)
		action := processActionRecordFromSQLC(actionRow)
		if state == ProcessActionStateApplied {
			canonical, nextCursor, canonicalErr := canonicalProcessReadResult(
				process,
				action,
				input.Result,
			)
			if canonicalErr != nil {
				state = ProcessActionStateFailed
				input.StateReasonCode = daemonprotocol.ProcessActionReasonInvalidReadObservation
				input.StateReasonMessage = fmt.Sprintf(
					"daemon returned an invalid process read observation: %v",
					canonicalErr,
				)
				input.Result = nil
				reportAccepted = false
			} else {
				input.Result = canonical
				readNextCursor = nextCursor
			}
		} else if state == ProcessActionStateUnknown {
			state = ProcessActionStateFailed
			input.StateReasonCode = daemonprotocol.ProcessActionReasonReadInterrupted
			input.StateReasonMessage = "the process read ended without a usable observation"
			input.Result = nil
			reportAccepted = false
		}
		if state != ProcessActionStateApplied {
			input.Result, err = canonicalProcessReadFailureResult(
				process,
				action,
				input.StateReasonCode,
				input.StateReasonMessage,
			)
			if err != nil {
				return DaemonProcessActionReportApplication{}, err
			}
		}
	}
	var row dbsqlc.ProcessAction
	params := dbsqlc.MarkProcessActionAppliedParams{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ProcessID:          input.ProcessID,
		ID:                 input.ID,
		StateReasonCode:    sqlcTextFromEmpty(input.StateReasonCode),
		StateReasonMessage: input.StateReasonMessage,
	}
	switch state {
	case ProcessActionStateApplied:
		row, err = qtx.MarkProcessActionApplied(ctx, params)
	case ProcessActionStateFailed:
		row, err = qtx.MarkProcessActionFailed(
			ctx,
			dbsqlc.MarkProcessActionFailedParams{
				ProjectID:          input.ProjectID,
				AgentID:            input.AgentID,
				ProcessID:          input.ProcessID,
				ID:                 input.ID,
				StateReasonCode:    sqlcTextFromEmpty(input.StateReasonCode),
				StateReasonMessage: input.StateReasonMessage,
			},
		)
	case ProcessActionStateUnknown:
		row, err = qtx.MarkProcessActionUnknown(
			ctx,
			dbsqlc.MarkProcessActionUnknownParams{
				ProjectID:          input.ProjectID,
				AgentID:            input.AgentID,
				ProcessID:          input.ProcessID,
				ID:                 input.ID,
				StateReasonCode:    sqlcTextFromEmpty(input.StateReasonCode),
				StateReasonMessage: input.StateReasonMessage,
			},
		)
	default:
		return DaemonProcessActionReportApplication{}, fmt.Errorf("unsupported process action completion state %q", state)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		application, resolved, replayErr := replayDaemonProcessActionReportTx(ctx, qtx, input, state)
		if replayErr != nil {
			return DaemonProcessActionReportApplication{}, replayErr
		}
		if resolved {
			if !reportAccepted {
				application.ToolResultCommitted = false
			}
			return application, nil
		}
		blocked, blockedErr := qtx.EarlierNonTerminalProcessActionExists(
			ctx,
			dbsqlc.EarlierNonTerminalProcessActionExistsParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
				ProcessID: input.ProcessID,
				ID:        input.ID,
			},
		)
		if blockedErr != nil {
			return DaemonProcessActionReportApplication{}, fmt.Errorf(
				"check earlier process action before report: %w",
				blockedErr,
			)
		}
		if blocked {
			return DaemonProcessActionReportApplication{}, storeerr.ErrProcessActionReportBlocked
		}
		return DaemonProcessActionReportApplication{}, storeerr.ErrDaemonRuntimeUnregistered
	}
	if err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf("complete process action: %w", err)
	}
	if state == ProcessActionStateApplied {
		if err := qtx.TouchProcessActivity(ctx, dbsqlc.TouchProcessActivityParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ProcessID: input.ProcessID,
		}); err != nil {
			return DaemonProcessActionReportApplication{}, fmt.Errorf("touch process activity: %w", err)
		}
	}
	record := processActionRecordFromSQLC(row)
	resultCommitted := isNilID(record.ToolCallID)
	if !isNilID(record.ToolCallID) {
		outcome := ToolResultOutcomeSucceeded
		errText := ""
		if state != ProcessActionStateApplied {
			outcome = ToolResultOutcomeFailed
			errText = input.StateReasonMessage
			if errText == "" {
				errText = input.StateReasonCode
			}
		}
		result := input.Result
		if len(result) == 0 || string(result) == "null" {
			result, err = processActionToolResult(
				input.ProcessID,
				input.ID,
				state,
				input.StateReasonCode,
				errText,
			)
			if err != nil {
				return DaemonProcessActionReportApplication{}, err
			}
		}
		contentParts, err := ToolResultContentParts(result)
		if err != nil {
			return DaemonProcessActionReportApplication{}, err
		}
		completedToolCall := false
		toolRow, err := qtx.CompleteToolCallFromProcessAction(
			ctx,
			dbsqlc.CompleteToolCallFromProcessActionParams{
				ProjectID:       input.ProjectID,
				AgentID:         input.AgentID,
				ToolCallID:      record.ToolCallID,
				ProcessID:       input.ProcessID,
				ProcessActionID: input.ID,
				Outcome:         string(outcome),
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
					return DaemonProcessActionReportApplication{}, checkErr
				}
				if !publication.Published {
					return DaemonProcessActionReportApplication{}, fmt.Errorf(
						"terminal process action %s has no durable result for linked tool call %s",
						record.ID,
						record.ToolCallID,
					)
				}
				resultCommitted = publication.Matches
			} else {
				return DaemonProcessActionReportApplication{}, fmt.Errorf(
					"complete tool call from process action: %w",
					err,
				)
			}
		} else {
			resultCommitted = true
			completedToolCall = true
			resultRecord := toolCallRecordFromProcessActionCompleteSQLC(toolRow)
			resultRecord.ResultContentParts = contentParts
			if _, err := appendToolResultEventTx(
				ctx,
				txNotifications,
				tx,
				resultRecord,
			); err != nil {
				return DaemonProcessActionReportApplication{}, err
			}
		}
		if completedToolCall {
			metadata, err := marshalJSON(struct {
				Reason          string `json:"reason"`
				ProcessID       ID     `json:"process_id"`
				ProcessActionID ID     `json:"process_action_id"`
				ToolCallID      ID     `json:"tool_call_id"`
			}{
				Reason:          "process_action_result",
				ProcessID:       input.ProcessID,
				ProcessActionID: input.ID,
				ToolCallID:      record.ToolCallID,
			})
			if err != nil {
				return DaemonProcessActionReportApplication{}, fmt.Errorf(
					"marshal process action wakeup metadata: %w",
					err,
				)
			}
			if err := qtx.MarkAgentWakeup(
				ctx,
				dbsqlc.MarkAgentWakeupParams{
					ProjectID: input.ProjectID,
					AgentID:   input.AgentID,
					Metadata:  metadata,
				},
			); err != nil {
				return DaemonProcessActionReportApplication{}, fmt.Errorf(
					"mark process action tool result wakeup: %w",
					err,
				)
			}
		}
	}
	if resultCommitted &&
		readNextCursor != nil &&
		state == ProcessActionStateApplied {
		if _, err := qtx.AdvanceProcessDefaultOutputCursor(
			ctx,
			dbsqlc.AdvanceProcessDefaultOutputCursorParams{
				ProjectID:  input.ProjectID,
				AgentID:    input.AgentID,
				ID:         input.ProcessID,
				NextCursor: *readNextCursor,
			},
		); err != nil {
			return DaemonProcessActionReportApplication{}, fmt.Errorf(
				"advance process read cursor: %w",
				err,
			)
		}
	}
	return DaemonProcessActionReportApplication{
		Action:              record,
		ToolResultCommitted: reportAccepted && resultCommitted,
	}, nil
}

func processActionToolResult(
	processID ID,
	actionID ID,
	state ProcessActionState,
	reasonCode string,
	errText string,
) (json.RawMessage, error) {
	body, err := marshalJSON(struct {
		ProcessID       string             `json:"process_id"`
		ProcessActionID string             `json:"process_action_id"`
		State           ProcessActionState `json:"state"`
		StateReasonCode string             `json:"state_reason_code"`
		Error           string             `json:"error"`
	}{
		ProcessID:       publicResourceID(publicid.KindProcess, processID),
		ProcessActionID: publicResourceID(publicid.KindProcessAction, actionID),
		State:           state,
		StateReasonCode: reasonCode,
		Error:           errText,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal process action tool result: %w", err)
	}
	return body, nil
}

func publicResourceID(kind publicid.Kind, id ID) string {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return ""
	}
	return encoded
}
