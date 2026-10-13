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
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// Machine operations are agent-free daemon processes started through the
// public machine API (exec, file upload, file download). They use the same
// process protocol as agent processes, so released daemons run them
// unchanged, but they live in their own table because they have no project,
// agent, tool call, or machine binding.

type MachineOperationState string

const (
	MachineOperationStateQueued   MachineOperationState = "queued"
	MachineOperationStateStarting MachineOperationState = "starting"
	MachineOperationStateRunning  MachineOperationState = "running"
	MachineOperationStateExited   MachineOperationState = "exited"
	MachineOperationStateFailed   MachineOperationState = "failed"
	MachineOperationStateKilled   MachineOperationState = "killed"
	MachineOperationStateUnknown  MachineOperationState = "unknown"
)

func (s MachineOperationState) Terminal() bool {
	switch s {
	case MachineOperationStateExited, MachineOperationStateFailed,
		MachineOperationStateKilled, MachineOperationStateUnknown:
		return true
	default:
		return false
	}
}

type MachineOperationRecord struct {
	ID                 uuid.UUID
	OrgID              uuid.UUID
	MachineID          uuid.UUID
	ExecutionSpec      processcmd.ExecutionSpec
	TimeoutSeconds     int
	State              MachineOperationState
	DaemonRuntimeID    *uuid.UUID
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	Result             json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateMachineOperationInput struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	MachineID      uuid.UUID
	ExecutionSpec  processcmd.ExecutionSpec
	TimeoutSeconds int
}

type CompleteMachineOperationInput struct {
	OrgID              uuid.UUID
	MachineID          uuid.UUID
	ID                 uuid.UUID
	DaemonRuntimeID    uuid.UUID
	State              MachineOperationState
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	Result             json.RawMessage
}

const (
	MachineOperationReasonDaemonReconnect = LocalProcessMissingAfterDaemonReconnectReason
	MachineOperationReasonNotAccepted     = "not_accepted"
	MachineOperationReasonCanceled        = "canceled"
)

func machineOperationFromSQLC(row dbsqlc.MachineOperation) MachineOperationRecord {
	record := MachineOperationRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		MachineID:          row.MachineID,
		ExecutionSpec:      row.ExecutionSpec,
		TimeoutSeconds:     int(row.TimeoutSeconds),
		State:              MachineOperationState(row.State),
		DaemonRuntimeID:    row.DaemonRuntimeID,
		ExitSignal:         row.ExitSignal,
		StateReasonCode:    row.StateReasonCode,
		StateReasonMessage: row.StateReasonMessage,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
	if row.ExitCode != nil {
		exitCode := int(*row.ExitCode)
		record.ExitCode = &exitCode
	}
	if row.Result != nil {
		record.Result = *row.Result
	}
	return record
}

func (s *Store) CreateMachineOperation(
	ctx context.Context,
	input CreateMachineOperationInput,
) (MachineOperationRecord, error) {
	if input.ID == uuid.Nil || input.OrgID == uuid.Nil || input.MachineID == uuid.Nil {
		return MachineOperationRecord{}, errors.New("machine operation, organization, and machine are required")
	}
	if input.TimeoutSeconds <= 0 {
		return MachineOperationRecord{}, errors.New("machine operation timeout must be positive")
	}
	if err := input.ExecutionSpec.Validate(); err != nil {
		return MachineOperationRecord{}, err
	}
	row, err := s.q.InsertMachineOperation(ctx, dbsqlc.InsertMachineOperationParams{
		ID:             input.ID,
		OrgID:          input.OrgID,
		MachineID:      input.MachineID,
		ExecutionSpec:  input.ExecutionSpec,
		TimeoutSeconds: int32(input.TimeoutSeconds),
	})
	if err != nil {
		return MachineOperationRecord{}, fmt.Errorf("create machine operation: %w", err)
	}
	return machineOperationFromSQLC(row), nil
}

func (s *Store) GetMachineOperation(
	ctx context.Context,
	orgID, machineID, id uuid.UUID,
) (MachineOperationRecord, bool, error) {
	row, err := s.q.GetMachineOperation(ctx, dbsqlc.GetMachineOperationParams{
		OrgID: orgID, MachineID: machineID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineOperationRecord{}, false, nil
	}
	if err != nil {
		return MachineOperationRecord{}, false, fmt.Errorf("get machine operation: %w", err)
	}
	return machineOperationFromSQLC(row), true, nil
}

// AcceptMachineOperation grants execution to the daemon runtime that accepted
// the offer. It returns false when the operation is no longer queued.
func (s *Store) AcceptMachineOperation(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
	id uuid.UUID,
) (MachineOperationRecord, bool, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return MachineOperationRecord{}, false, err
	}
	row, err := s.q.AcceptMachineOperation(ctx, dbsqlc.AcceptMachineOperationParams{
		DaemonRuntimeID: authority.DaemonRuntimeID,
		DaemonTokenID:   authority.DaemonTokenID,
		OrgID:           authority.OrgID,
		MachineID:       authority.MachineID,
		ID:              id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MachineOperationRecord{}, false, nil
	}
	if err != nil {
		return MachineOperationRecord{}, false, fmt.Errorf("accept machine operation: %w", err)
	}
	return machineOperationFromSQLC(row), true, nil
}

func (s *Store) MarkMachineOperationRunning(ctx context.Context, orgID, machineID, id uuid.UUID) error {
	if _, err := s.q.MarkMachineOperationRunning(ctx, dbsqlc.MarkMachineOperationRunningParams{
		OrgID: orgID, MachineID: machineID, ID: id,
	}); err != nil {
		return fmt.Errorf("mark machine operation running: %w", err)
	}
	return nil
}

// CompleteMachineOperation records a terminal state. It reports false when the
// operation was already terminal, which makes repeated daemon reports
// idempotent.
func (s *Store) CompleteMachineOperation(
	ctx context.Context,
	input CompleteMachineOperationInput,
) (bool, error) {
	if !input.State.Terminal() {
		return false, fmt.Errorf("machine operation state %q is not terminal", input.State)
	}
	var exitCode *int32
	if input.ExitCode != nil && input.State == MachineOperationStateExited ||
		input.ExitCode != nil && input.State == MachineOperationStateFailed {
		value := int32(*input.ExitCode)
		exitCode = &value
	}
	var result *json.RawMessage
	if len(input.Result) > 0 {
		result = &input.Result
	}
	var runtimeID *uuid.UUID
	if input.DaemonRuntimeID != uuid.Nil {
		runtimeID = &input.DaemonRuntimeID
	}
	rows, err := s.q.CompleteMachineOperation(ctx, dbsqlc.CompleteMachineOperationParams{
		State:              string(input.State),
		ExitCode:           exitCode,
		ExitSignal:         input.ExitSignal,
		StateReasonCode:    input.StateReasonCode,
		StateReasonMessage: input.StateReasonMessage,
		Result:             result,
		DaemonRuntimeID:    runtimeID,
		OrgID:              input.OrgID,
		MachineID:          input.MachineID,
		ID:                 input.ID,
	})
	if err != nil {
		return false, fmt.Errorf("complete machine operation: %w", err)
	}
	return rows > 0, nil
}

// reconcileMachineOperationClaimsTx answers reconnecting daemons for the
// claims that name machine operations and removes those claims from the
// agent-process reconciliation. Operations the daemon no longer claims are
// marked unknown.
func reconcileMachineOperationClaimsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input RegisterDaemonRuntimeInput,
	claims map[uuid.UUID]ProcessReconciliationClaim,
) ([]ProcessReconciliationDirective, error) {
	directives := []ProcessReconciliationDirective{}
	claimed := make([]uuid.UUID, 0, len(claims))
	for processID, claim := range claims {
		row, err := qtx.GetMachineOperation(ctx, dbsqlc.GetMachineOperationParams{
			OrgID: input.OrgID, MachineID: input.MachineID, ID: processID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load claimed machine operation: %w", err)
		}
		delete(claims, processID)
		claimed = append(claimed, processID)
		operation := machineOperationFromSQLC(row)
		disposition := machineOperationDisposition(operation, claim)
		if disposition == daemonprotocol.ProcessDispositionRelease && !operation.State.Terminal() {
			state := MachineOperationStateUnknown
			if operation.State == MachineOperationStateQueued {
				state = MachineOperationStateFailed
			}
			if _, err := qtx.CompleteMachineOperation(ctx, dbsqlc.CompleteMachineOperationParams{
				State:           string(state),
				StateReasonCode: MachineOperationReasonDaemonReconnect,
				OrgID:           input.OrgID,
				MachineID:       input.MachineID,
				ID:              processID,
			}); err != nil {
				return nil, fmt.Errorf("mark machine operation unknown: %w", err)
			}
		}
		directives = append(directives, ProcessReconciliationDirective{
			ProcessID:            processID,
			SupervisorInstanceID: claim.SupervisorInstanceID,
			Disposition:          disposition,
		})
	}
	if _, err := qtx.MarkUnclaimedMachineOperationsUnknown(ctx, dbsqlc.MarkUnclaimedMachineOperationsUnknownParams{
		StateReasonCode: MachineOperationReasonDaemonReconnect,
		OrgID:           input.OrgID,
		MachineID:       input.MachineID,
		ClaimedIds:      claimed,
	}); err != nil {
		return nil, fmt.Errorf("mark unclaimed machine operations unknown: %w", err)
	}
	return directives, nil
}

func machineOperationDisposition(
	operation MachineOperationRecord,
	claim ProcessReconciliationClaim,
) daemonprotocol.ProcessDisposition {
	uncommitted := !claim.ExecutionCommitted &&
		(claim.Phase == daemonprotocol.ProcessPhasePreparing || claim.Phase == daemonprotocol.ProcessPhasePrepared)
	switch {
	case operation.State == MachineOperationStateQueued && uncommitted:
		return daemonprotocol.ProcessDispositionClosePreparation
	case operation.State.Terminal() && uncommitted:
		return daemonprotocol.ProcessDispositionClosePreparation
	case operation.State.Terminal():
		return daemonprotocol.ProcessDispositionRelease
	case claim.Phase == daemonprotocol.ProcessPhaseTerminal:
		// The daemon still holds the terminal report; keep it so the report
		// is resent and completes the operation.
		return daemonprotocol.ProcessDispositionRetain
	case claim.ExecutionCommitted && claim.SupervisorLive:
		return daemonprotocol.ProcessDispositionRetain
	case operation.State == MachineOperationStateStarting && !claim.ExecutionCommitted && claim.SupervisorLive:
		return daemonprotocol.ProcessDispositionStart
	default:
		return daemonprotocol.ProcessDispositionRelease
	}
}
