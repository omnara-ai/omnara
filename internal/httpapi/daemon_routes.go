package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

var errDaemonReportValidation = errors.New("daemon report validation failed")

type daemonReportedEvent = daemonprotocol.ReportedEvent

func validateDaemonReportedEvent(event daemonReportedEvent) error {
	if event.Type == "" || event.ProcessID == "" {
		return fmt.Errorf("%w: type and process are required", errDaemonReportValidation)
	}
	if err := validateDaemonReportedResult(event.Result); err != nil {
		return err
	}
	switch event.Type {
	case daemonprotocol.EventProcessStarted:
		if event.StartedAt.IsZero() || event.ObservedAt.IsZero() {
			return fmt.Errorf(
				"%w: process_started requires started_at and observed_at",
				errDaemonReportValidation,
			)
		}
		if event.StartedAt.After(event.ObservedAt) {
			return fmt.Errorf(
				"%w: process_started observed_at cannot precede started_at",
				errDaemonReportValidation,
			)
		}
	case daemonprotocol.EventProcessFinished:
		if !event.ObservedAt.IsZero() {
			return fmt.Errorf(
				"%w: process_finished does not support observed_at",
				errDaemonReportValidation,
			)
		}
		if event.State == "" {
			return fmt.Errorf(
				"%w: process_finished requires state",
				errDaemonReportValidation,
			)
		}
		switch event.State {
		case daemonprotocol.ProcessStateExited,
			daemonprotocol.ProcessStateFailed,
			daemonprotocol.ProcessStateKilled,
			daemonprotocol.ProcessStateUnknown:
		default:
			return fmt.Errorf(
				"%w: process_finished state must be terminal",
				errDaemonReportValidation,
			)
		}
		if !daemonprotocol.ValidProcessTerminalSourceTimes(
			event.State,
			event.StartedAt,
			event.EndedAt,
		) {
			return fmt.Errorf(
				"%w: process_finished source times do not match its terminal state",
				errDaemonReportValidation,
			)
		}
		if (event.State == daemonprotocol.ProcessStateFailed ||
			event.State == daemonprotocol.ProcessStateUnknown) &&
			event.StateReasonCode == "" {
			return fmt.Errorf(
				"%w: process_finished %s requires state_reason_code",
				errDaemonReportValidation,
				event.State,
			)
		}
	case daemonprotocol.EventProcessActionApplied,
		daemonprotocol.EventProcessActionFailed,
		daemonprotocol.EventProcessActionUnknown:
		if !event.ObservedAt.IsZero() {
			return fmt.Errorf(
				"%w: process action completion does not support observed_at",
				errDaemonReportValidation,
			)
		}
		if event.ProcessActionID == "" {
			return fmt.Errorf(
				"%w: process action completion requires process_action_id",
				errDaemonReportValidation,
			)
		}
		if event.Type != daemonprotocol.EventProcessActionApplied &&
			event.StateReasonCode == "" {
			return fmt.Errorf(
				"%w: %s requires state_reason_code",
				errDaemonReportValidation,
				event.Type,
			)
		}
	default:
		return fmt.Errorf("%w: unsupported daemon reported event type", errDaemonReportValidation)
	}
	return nil
}

func validateDaemonReportedResult(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if len(raw) > tooloutput.MaxStoredToolResultBytes {
		return fmt.Errorf(
			"%w: result exceeds %d serialized bytes",
			errDaemonReportValidation,
			tooloutput.MaxStoredToolResultBytes,
		)
	}
	var result struct {
		Output     *string `json:"output"`
		Cursor     *int64  `json:"cursor"`
		NextCursor *int64  `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("%w: result must be a JSON object", errDaemonReportValidation)
	}
	if result.Output != nil && len([]byte(*result.Output)) > processaction.MaxObservationBytes {
		return fmt.Errorf(
			"%w: result output exceeds %d bytes",
			errDaemonReportValidation,
			processaction.MaxObservationBytes,
		)
	}
	if result.Cursor != nil && result.NextCursor != nil &&
		*result.NextCursor-*result.Cursor > int64(processaction.MaxObservationBytes) {
		return fmt.Errorf(
			"%w: result cursor range exceeds %d bytes",
			errDaemonReportValidation,
			processaction.MaxObservationBytes,
		)
	}
	return nil
}

func (s *Server) applyDaemonReportedEventForProcess(
	ctx context.Context,
	authority executionstore.DaemonRuntimeAuthority,
	process executionstore.ProcessRecord,
	body daemonReportedEvent,
	missingProcessErr error,
) (bool, error) {
	if process.ExecutionGrantedAt == nil {
		return false, fmt.Errorf(
			"%w: process was not accepted",
			errDaemonReportValidation,
		)
	}
	switch body.Type {
	case daemonprotocol.EventProcessStarted:
		application, err := s.store.Execution().MarkProcessStarted(
			ctx,
			executionstore.MarkProcessStartedInput{
				Authority:       authority,
				ProjectID:       process.ProjectID,
				AgentID:         process.AgentID,
				ID:              process.ID,
				Result:          body.Result,
				SourceStartedAt: body.StartedAt,
			},
		)
		if err != nil {
			return false, err
		}
		return !application.ToolResultCommitted, nil
	case daemonprotocol.EventProcessFinished:
		application, err := s.store.Execution().CompleteDaemonProcess(
			ctx,
			executionstore.CompleteDaemonProcessInput{
				Authority:          authority,
				ProjectID:          process.ProjectID,
				AgentID:            process.AgentID,
				ID:                 process.ID,
				State:              executionstore.ProcessState(body.State),
				ExitCode:           body.ExitCode,
				ExitSignal:         body.ExitSignal,
				StateReasonCode:    body.StateReasonCode,
				StateReasonMessage: body.StateReasonMessage,
				Result:             body.Result,
				SourceStartedAt:    body.StartedAt,
				SourceEndedAt:      body.EndedAt,
			},
		)
		if err != nil {
			return false, err
		}
		return !application.ToolResultCommitted, nil
	case daemonprotocol.EventProcessActionApplied,
		daemonprotocol.EventProcessActionFailed,
		daemonprotocol.EventProcessActionUnknown:
		actionID, err := parsePublicID(publicid.KindProcessAction, body.ProcessActionID)
		if err != nil {
			return false, fmt.Errorf(
				"%w: invalid process_action_id: %w",
				errDaemonReportValidation,
				err,
			)
		}
		action, found, err := s.store.Execution().GetProcessActionForDaemonReport(
			ctx,
			authority,
			process.ID,
			actionID,
		)
		if err != nil {
			return false, err
		}
		if !found {
			return false, missingProcessErr
		}
		if action.State == executionstore.ProcessActionStateQueued {
			return false, fmt.Errorf(
				"%w: process action was not accepted",
				errDaemonReportValidation,
			)
		}
		input := executionstore.CompleteDaemonProcessActionInput{
			Authority:          authority,
			ProjectID:          process.ProjectID,
			AgentID:            process.AgentID,
			ProcessID:          process.ID,
			ID:                 action.ID,
			StateReasonCode:    body.StateReasonCode,
			StateReasonMessage: body.StateReasonMessage,
			Result:             body.Result,
		}
		var application executionstore.DaemonProcessActionReportApplication
		switch body.Type {
		case daemonprotocol.EventProcessActionApplied:
			application, err = s.store.Execution().ApplyDaemonProcessAction(ctx, input)
		case daemonprotocol.EventProcessActionFailed:
			application, err = s.store.Execution().FailDaemonProcessAction(ctx, input)
		case daemonprotocol.EventProcessActionUnknown:
			application, err = s.store.Execution().MarkDaemonProcessActionUnknown(ctx, input)
		default:
			return false, errors.New("unsupported process action report type")
		}
		if err != nil {
			return false, err
		}
		return !application.ToolResultCommitted, nil
	default:
		return false, errors.New("unsupported daemon reported event type")
	}
}

func (s *Server) applyDaemonReportedEventForMachineWithContext(
	ctx context.Context,
	authority executionstore.DaemonRuntimeAuthority,
	body daemonReportedEvent,
	missingProcessErr error,
) (bool, error) {
	processID, err := parsePublicID(publicid.KindProcess, body.ProcessID)
	if err != nil {
		return false, fmt.Errorf("%w: invalid process_id: %w", errDaemonReportValidation, err)
	}
	process, found, err := s.store.Execution().GetDaemonProcessForMachineReport(
		ctx,
		authority,
		processID,
	)
	if err != nil {
		return false, err
	}
	if !found {
		return false, missingProcessErr
	}
	return s.applyDaemonReportedEventForProcess(
		ctx,
		authority,
		process,
		body,
		missingProcessErr,
	)
}
