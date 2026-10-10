package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processresult"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type processActionCompletionInput struct {
	ProjectID          uuid.UUID
	AgentID            uuid.UUID
	ProcessID          uuid.UUID
	ID                 uuid.UUID
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
		return DaemonProcessActionReportApplication{}, false, fmt.Errorf(
			"load process action report replay: %w",
			err,
		)
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
		if record.StateReasonCode != input.StateReasonCode ||
			record.StateReasonMessage != input.StateReasonMessage {
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
				result, err = processresult.ReadFailure(
					processRecordFromSQLC(processRow).resultFacts(),
					record.resultFacts(),
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
		result, err = processresult.ActionResult(
			input.ProcessID,
			input.ID, string(state), input.StateReasonCode,
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
		record.ToolCallID != uuid.Nil {
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
		record.ToolCallID != uuid.Nil {
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
	if !resultCommitted && record.ToolCallID != uuid.Nil {
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
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ProcessID == uuid.Nil ||
		input.ID == uuid.Nil {
		return DaemonProcessActionReportApplication{}, errors.New(
			"project, agent, process, and action are required",
		)
	}
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf("begin complete process action: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()

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
	application, err := completeDaemonProcessActionTx(ctx, unit, qtx, completion, state)
	if err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	if err := unit.Commit(ctx, "complete process action"); err != nil {
		return DaemonProcessActionReportApplication{}, err
	}
	return application, nil
}

func completeDaemonProcessActionTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input processActionCompletionInput,
	state ProcessActionState,
) (DaemonProcessActionReportApplication, error) {
	_, err := unit.LockAgent(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	}, agentexecution.ExternalAuthority{})
	if err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf(
			"lock agent for process action completion: %w",
			err,
		)
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
			canonical, nextCursor, canonicalErr := processresult.Read(
				process.resultFacts(),
				action.resultFacts(),
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
			input.Result, err = processresult.ReadFailure(
				process.resultFacts(),
				action.resultFacts(),
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
		StateReasonCode:    storeutil.TextFromEmpty(input.StateReasonCode),
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
				StateReasonCode:    storeutil.TextFromEmpty(input.StateReasonCode),
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
				StateReasonCode:    storeutil.TextFromEmpty(input.StateReasonCode),
				StateReasonMessage: input.StateReasonMessage,
			},
		)
	default:
		return DaemonProcessActionReportApplication{}, fmt.Errorf(
			"unsupported process action completion state %q",
			state,
		)
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
	if err := qtx.TouchProcessActivity(ctx, dbsqlc.TouchProcessActivityParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		ProcessID: input.ProcessID,
	}); err != nil {
		return DaemonProcessActionReportApplication{}, fmt.Errorf("touch process activity: %w", err)
	}
	record := processActionRecordFromSQLC(row)
	resultCommitted := record.ToolCallID == uuid.Nil
	if record.ToolCallID != uuid.Nil {
		h, err := unit.Handle(input.ProjectID, input.AgentID)
		if err != nil {
			return DaemonProcessActionReportApplication{}, err
		}
		completed, err := h.CompleteProcessAction(
			ctx,
			agentexecution.ActionResult{ID: record.ID, Observed: input.Result},
		)
		if err != nil {
			return DaemonProcessActionReportApplication{}, err
		}
		resultCommitted = completed.Matches
	}
	if record.ToolCallID == uuid.Nil && resultCommitted &&
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

func publicResourceID(kind publicid.Kind, id uuid.UUID) string {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return ""
	}
	return encoded
}
