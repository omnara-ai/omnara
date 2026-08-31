package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type DaemonProcessOffer struct {
	Process          ProcessRecord     `json:"process"`
	Env              map[string]string `json:"env"`
	PreparationError string            `json:"preparation_error"`
	RetryError       error             `json:"-"`
}

type DaemonArtifactProcessScope struct {
	ProjectID  ID
	AgentID    ID
	ArtifactID string
}

func (s *Store) GetDaemonArtifactProcessScope(
	ctx context.Context,
	orgID, machineID, toolCallID ID,
	toolName string,
) (DaemonArtifactProcessScope, bool, error) {
	if isNilID(orgID) || isNilID(machineID) || isNilID(toolCallID) {
		return DaemonArtifactProcessScope{}, false, errors.New(
			"organization id, machine id, and tool call id are required",
		)
	}
	if toolName == "" {
		return DaemonArtifactProcessScope{}, false, errors.New("tool name is required")
	}
	record, err := s.q.GetDaemonArtifactProcessScope(ctx, dbsqlc.GetDaemonArtifactProcessScopeParams{
		OrgID:      orgID,
		MachineID:  machineID,
		ToolCallID: toolCallID,
		ToolName:   toolName,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DaemonArtifactProcessScope{}, false, nil
	}
	if err != nil {
		return DaemonArtifactProcessScope{}, false, fmt.Errorf("load daemon artifact process scope: %w", err)
	}
	return DaemonArtifactProcessScope{
		ProjectID:  record.ProjectID,
		AgentID:    record.AgentID,
		ArtifactID: record.ArtifactID,
	}, true, nil
}

func (t *toolCallTransaction) startProcess(
	ctx context.Context,
	input CreateProcessInput,
) (ProcessRecord, error) {
	if isNilID(input.AgentMachineBindingID) {
		return ProcessRecord{}, errors.New("agent machine binding is required")
	}
	if input.Command == "" {
		return ProcessRecord{}, errors.New("process command is required")
	}
	spec, err := processcmd.NormalizeShellCommand(input.Command, input.ShellSelector)
	if err != nil {
		return ProcessRecord{}, err
	}
	input.Command = spec.Command
	input.ShellSelector = spec.Shell
	input.IOMode, err = processcmd.NormalizeIOMode(input.IOMode)
	if err != nil {
		return ProcessRecord{}, err
	}
	if input.InitialWaitMS < 0 {
		return ProcessRecord{}, errors.New("process initial wait must be non-negative")
	}
	projectID := t.input.ProjectID
	agentID := t.input.AgentID
	toolCallID := t.input.ToolCallID
	runtimeLockID := t.input.RuntimeLockID
	if existing, found, err := getProcessByToolCallTx(ctx, t.tx, projectID, agentID, toolCallID); err != nil {
		return ProcessRecord{}, err
	} else if found {
		if !processReplayMatches(existing, input) {
			return ProcessRecord{}, storeerr.ErrIdempotencyConflict
		}
		t.hasDurableCompletionOwner = true
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return ProcessRecord{}, err
		}
		return existing, nil
	}
	executionConfig, err := t.q.GetProcessExecutionConfig(
		ctx,
		dbsqlc.GetProcessExecutionConfigParams{
			ProjectID:             projectID,
			AgentID:               agentID,
			AgentMachineBindingID: input.AgentMachineBindingID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessRecord{}, storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("load process execution config: %w", err)
	}
	if !executionConfig.NewManagedWorkAllowed {
		return ProcessRecord{}, storeerr.ErrManagedWorkAdmissionDenied
	}
	cwd := resolveProcessCwd(executionConfig.MachineCwd, executionConfig.BindingCwd, input.Cwd)
	bindingEnvironmentOverlay, err := machineEnvironmentOverlayFromColumns(
		executionConfig.BindingEnvOverlay,
		executionConfig.BindingSecretEnvOverlay,
	)
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("load binding environment overlay: %w", err)
	}
	processEnvironment, err := effectiveMachineEnvironment(
		executionConfig.MachineEnv,
		executionConfig.MachineSecretEnv,
		bindingEnvironmentOverlay,
	)
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("resolve process environment: %w", err)
	}
	if err := validateMachineEnvironmentSecretsTx(
		ctx,
		t.q,
		executionConfig.OrgID,
		executionConfig.ProjectID,
		processEnvironment,
	); err != nil {
		return ProcessRecord{}, fmt.Errorf("resolve process environment: %w", err)
	}
	if environmentByteSize(processEnvironment.Env) > MaxResolvedEnvironmentBytes {
		return ProcessRecord{}, errors.New("process environment exceeds size limit")
	}
	processEnv, processSecretEnv, err := machineEnvironmentToColumns(processEnvironment)
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("prepare process environment: %w", err)
	}
	if _, err := t.q.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: executionConfig.OrgID,
			ID:    executionConfig.MachineID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProcessRecord{}, storeerr.ErrMachineNotReachable
		}
		return ProcessRecord{}, fmt.Errorf("lock machine for start process: %w", err)
	}
	if err := t.lockForMutation(ctx); err != nil {
		return ProcessRecord{}, err
	}
	limits, err := resolveResourceLimits(ctx, t.q, executionConfig.OrgID)
	if err != nil {
		return ProcessRecord{}, err
	}
	nonTerminalProcesses, err := t.q.CountNonTerminalProcessesForAgent(
		ctx,
		dbsqlc.CountNonTerminalProcessesForAgentParams{
			ProjectID: projectID,
			AgentID:   agentID,
		},
	)
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("count non-terminal agent processes: %w", err)
	}
	if nonTerminalProcesses >= limits.MaxNonTerminalProcessesPerAgent {
		return ProcessRecord{}, fmt.Errorf(
			"non-terminal processes limit of %d reached: %w",
			limits.MaxNonTerminalProcessesPerAgent,
			storeerr.ErrAgentProcessLimitReached,
		)
	}
	row, err := t.q.InsertProcess(ctx, dbsqlc.InsertProcessParams{
		ToolCallID:            toolCallID,
		IoMode:                string(input.IOMode),
		Command:               input.Command,
		ShellSelector:         string(input.ShellSelector),
		Cwd:                   cwd,
		Env:                   processEnv,
		SecretEnv:             processSecretEnv,
		TimeoutSeconds:        int32(input.TimeoutSeconds),
		InitialWaitMs:         int32(input.InitialWaitMS),
		AgentMachineBindingID: input.AgentMachineBindingID,
		ProjectID:             projectID,
		AgentID:               agentID,
		RuntimeLockID:         runtimeLockID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, found, loadErr := getProcessByToolCallTx(
				ctx,
				t.tx,
				projectID,
				agentID,
				toolCallID,
			)
			if loadErr != nil {
				return ProcessRecord{}, loadErr
			}
			if found {
				if !processReplayMatches(existing, input) {
					return ProcessRecord{}, storeerr.ErrIdempotencyConflict
				}
				return existing, nil
			}
			if _, found, loadErr := machineReachableForProjectMachineTx(
				ctx,
				t.tx,
				projectID,
				executionConfig.MachineID,
			); loadErr != nil {
				return ProcessRecord{}, loadErr
			} else if !found {
				return ProcessRecord{}, storeerr.ErrMachineNotReachable
			}
			return ProcessRecord{}, storeerr.ErrRuntimeLockInactive
		}
		return ProcessRecord{}, fmt.Errorf("start process: %w", err)
	}
	process := processRecordFromInsertSQLC(row)
	t.hasDurableCompletionOwner = true
	t.requiresWaitingDisposition = true
	t.notifications.AddDaemonWork(process.MachineID)
	return process, nil
}

func processReplayMatches(existing ProcessRecord, input CreateProcessInput) bool {
	return existing.AgentMachineBindingID == input.AgentMachineBindingID &&
		existing.IOMode == input.IOMode &&
		existing.Command == input.Command &&
		existing.ShellSelector == input.ShellSelector &&
		existing.TimeoutSeconds == input.TimeoutSeconds &&
		existing.InitialWaitMS == input.InitialWaitMS
}

func getProcessByToolCallTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	projectID, agentID ID,
	toolCallID ID,
) (ProcessRecord, bool, error) {
	row, err := dbsqlc.New(tx).
		GetProcessByToolCall(
			ctx,
			dbsqlc.GetProcessByToolCallParams{
				ProjectID:  projectID,
				AgentID:    agentID,
				ToolCallID: toolCallID,
			},
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessRecord{}, false, nil
	}
	if err != nil {
		return ProcessRecord{}, false, fmt.Errorf("get process by tool call: %w", err)
	}
	return processRecordFromSQLC(row), true, nil
}

func (s *Store) GetProcess(ctx context.Context, projectID, agentID, id ID) (ProcessRecord, error) {
	row, err := s.q.GetProcess(ctx, dbsqlc.GetProcessParams{ProjectID: projectID, AgentID: agentID, ID: id})
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("get process: %w", err)
	}
	return processRecordFromSQLC(row), nil
}

func machineReachableForProjectMachineTx(
	ctx context.Context,
	q dbsqlc.DBTX,
	projectID, machineID ID,
) (ID, bool, error) {
	row, err := dbsqlc.New(q).
		MachineReachableForProjectMachine(
			ctx,
			dbsqlc.MachineReachableForProjectMachineParams{
				ProjectID: projectID,
				MachineID: machineID,
			},
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return NilID, false, nil
	}
	if err != nil {
		return NilID, false, fmt.Errorf("machine reachable for project machine: %w", err)
	}
	return row, true, nil
}

func (s *Store) CompleteProcess(ctx context.Context, input CompleteProcessInput) (ProcessRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) || isNilID(input.RuntimeLockID) {
		return ProcessRecord{}, errors.New("project, agent, process, and runtime lock are required")
	}
	if input.SourceEndedAt.IsZero() {
		return ProcessRecord{}, errors.New("source ended at is required")
	}
	input.SourceEndedAt = canonicalSourceTime(input.SourceEndedAt)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("begin complete process: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return ProcessRecord{}, fmt.Errorf("lock agent for process completion: %w", err)
	}
	row, err := qtx.CompleteProcess(
		ctx,
		dbsqlc.CompleteProcessParams{
			ProjectID:          input.ProjectID,
			AgentID:            input.AgentID,
			ID:                 input.ID,
			RuntimeLockID:      input.RuntimeLockID,
			State:              string(input.State),
			SourceEndedAt:      input.SourceEndedAt,
			ExitCode:           sqlcInt32Ptr(input.ExitCode),
			ExitSignal:         input.ExitSignal,
			StateReasonCode:    sqlcTextFromEmpty(input.StateReasonCode),
			StateReasonMessage: input.StateReasonMessage,
		},
	)
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("complete process: %w", err)
	}
	process := processRecordFromCompleteSQLC(row)
	if err := completeQueuedProcessActionsForTerminalProcessTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		process,
	); err != nil {
		return ProcessRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "complete process"); err != nil {
		return ProcessRecord{}, err
	}
	return process, nil
}
