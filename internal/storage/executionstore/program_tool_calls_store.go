package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// Program tool calls are tools a run_command process calls through
// `omnarad call`. They are child tool_calls rows of that run_command call,
// kept out of the model context, and executed by the API under a claim
// instead of the agent runtime lock. Every write requires the process to be
// live (execution granted, starting or running) and the agent active.

// ProgramToolScope is the daemon process's authority to make program tool
// calls: its agent and the agent's current configuration, which decides the
// tools it may call and their permissions.
type ProgramToolScope struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ProcessID     uuid.UUID
	AgentConfigID uuid.UUID
}

// MaxOpenProgramToolCalls is how many unfinished program tool calls one
// command may have; CreateProgramToolCall fails a further new call with
// storeerr.ErrProgramToolCallLimitReached.
const MaxOpenProgramToolCalls = agentexecution.MaxOpenProgramTools

type ProgramToolCallPhase uint8

const (
	// ProgramToolCallPhaseAuthorize: decide permission, then call
	// AuthorizeProgramToolCall (and execute it under the same claim),
	// OpenProgramPermissionInteraction, or CompleteProgramToolCall
	// (denied/failed) with the claim.
	ProgramToolCallPhaseAuthorize = ProgramToolCallPhase(agentexecution.ProgramToolAuthorize)
	// ProgramToolCallPhaseExecute: the call is waiting under the claim; run it
	// with ExecuteProgramToolCall and/or CompleteProgramToolCall.
	ProgramToolCallPhaseExecute = ProgramToolCallPhase(agentexecution.ProgramToolExecute)
)

type ProgramToolCallRef struct {
	ProjectID  uuid.UUID
	AgentID    uuid.UUID
	ProcessID  uuid.UUID
	ToolCallID uuid.UUID
	// ClaimID is the executing claim; zero for calls that are not claimed.
	ClaimID uuid.UUID
}

func (ref ProgramToolCallRef) ref() agentexecution.ProgramToolRef {
	return agentexecution.ProgramToolRef{ID: ref.ToolCallID, ProcessID: ref.ProcessID, ClaimID: ref.ClaimID}
}

func (ref ProgramToolCallRef) validate() error {
	if ref.ProjectID == uuid.Nil || ref.AgentID == uuid.Nil || ref.ProcessID == uuid.Nil || ref.ToolCallID == uuid.Nil {
		return errors.New("project, agent, process, and tool call are required")
	}
	return nil
}

type ProgramToolCallRecord struct {
	ToolCallRecord
	ProcessID uuid.UUID
	// ClaimID is loaded by command results only.
	ClaimID uuid.UUID
	Created bool
}

type ProgramToolCallClaim struct {
	ProgramToolCallRecord
	Phase ProgramToolCallPhase
}

// Ref fences later writes on this claim.
func (claim ProgramToolCallClaim) Ref() ProgramToolCallRef {
	return ProgramToolCallRef{ProjectID: claim.ProjectID, AgentID: claim.AgentID,
		ProcessID: claim.ProcessID, ToolCallID: claim.ID, ClaimID: claim.ClaimID}
}

type CreateProgramToolCallInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	ProcessID uuid.UUID
	Key       uuid.UUID
	Name      string
	Type      string
	Input     json.RawMessage
}

type CompleteProgramToolCallInput struct {
	ProgramToolCallRef
	Outcome            ToolResultOutcome
	ResultContentParts json.RawMessage
}

func (s *Store) GetDaemonProgramToolScope(
	ctx context.Context,
	orgID, machineID, processID uuid.UUID,
) (ProgramToolScope, bool, error) {
	if orgID == uuid.Nil || machineID == uuid.Nil || processID == uuid.Nil {
		return ProgramToolScope{}, false, errors.New("organization, machine, and process are required")
	}
	row, err := s.q.GetDaemonProgramToolScope(ctx, dbsqlc.GetDaemonProgramToolScopeParams{
		OrgID: orgID, MachineID: machineID, ProcessID: processID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgramToolScope{}, false, nil
	}
	if err != nil {
		return ProgramToolScope{}, false, fmt.Errorf("load daemon program tool scope: %w", err)
	}
	return ProgramToolScope{ProjectID: row.ProjectID, AgentID: row.AgentID, ProcessID: row.ProcessID,
		AgentConfigID: row.AgentConfigID}, true, nil
}

// programUnit runs one program command under the agent's lifecycle lock,
// without a runtime authority, and commits it.
func programUnit[T any](
	ctx context.Context, s *Store, operation string, projectID, agentID uuid.UUID,
	run func(*agentexecution.Unit, *agentexecution.Handle) (T, error),
) (T, error) {
	return agentexecution.Retry(ctx, operation, func(ctx context.Context) (T, error) {
		var zero T
		unit, h, err := beginExecution(ctx, s, projectID, agentID, false, uuid.Nil)
		if err != nil {
			return zero, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		result, err := run(unit, h)
		if err != nil {
			return zero, err
		}
		if err := unit.Commit(ctx, operation); err != nil {
			return zero, err
		}
		return result, nil
	})
}

func programToolCallRecordTx(
	ctx context.Context, tx dbsqlc.DBTX, projectID, agentID uuid.UUID, tool agentexecution.ProgramTool,
) (ProgramToolCallRecord, error) {
	record, err := getToolCallTx(ctx, tx, projectID, agentID, tool.ID)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	return ProgramToolCallRecord{ToolCallRecord: record, ProcessID: tool.ProcessID,
		ClaimID: tool.ClaimID, Created: tool.Created}, nil
}

// CreateProgramToolCall records an awaiting_authorization call, idempotently
// on (process, key). A replay with a different name, type, or input fails with
// storeerr.ErrIdempotencyConflict.
func (s *Store) CreateProgramToolCall(
	ctx context.Context, input CreateProgramToolCallInput,
) (ProgramToolCallRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ProcessID == uuid.Nil {
		return ProgramToolCallRecord{}, errors.New("project, agent, and process are required")
	}
	return programUnit(ctx, s, "create_program_tool_call", input.ProjectID, input.AgentID,
		func(unit *agentexecution.Unit, h *agentexecution.Handle) (ProgramToolCallRecord, error) {
			tool, err := h.CreateProgramTool(ctx, agentexecution.CreateProgramToolInput{ProcessID: input.ProcessID,
				Key: input.Key, Name: input.Name, Type: input.Type, Input: input.Input})
			if err != nil {
				return ProgramToolCallRecord{}, err
			}
			return programToolCallRecordTx(ctx, unit.DB(), input.ProjectID, input.AgentID, tool)
		})
}

// AuthorizeProgramToolCall authorizes a call claimed in the authorize phase,
// leaving it waiting under the claim for the holder to execute.
func (s *Store) AuthorizeProgramToolCall(
	ctx context.Context, ref ProgramToolCallRef, lease time.Duration,
) (ProgramToolCallRecord, error) {
	if err := ref.validate(); err != nil {
		return ProgramToolCallRecord{}, err
	}
	if lease <= 0 {
		return ProgramToolCallRecord{}, errors.New("lease is required")
	}
	return programUnit(ctx, s, "authorize_program_tool_call", ref.ProjectID, ref.AgentID,
		func(unit *agentexecution.Unit, h *agentexecution.Handle) (ProgramToolCallRecord, error) {
			if _, err := h.AuthorizeProgramTool(ctx, ref.ref(), lease); err != nil {
				return ProgramToolCallRecord{}, err
			}
			// The command already checked the call belongs to the process.
			record, err := getToolCallTx(ctx, unit.DB(), ref.ProjectID, ref.AgentID, ref.ToolCallID)
			return ProgramToolCallRecord{ToolCallRecord: record, ProcessID: ref.ProcessID}, err
		})
}

// OpenProgramPermissionInteraction asks the user to approve a call claimed in
// the authorize phase. The user resolves it with ResolveAgentInteraction.
func (s *Store) OpenProgramPermissionInteraction(
	ctx context.Context, ref ProgramToolCallRef, request toolpermission.Request,
) (AgentInteractionRecord, error) {
	if err := ref.validate(); err != nil {
		return AgentInteractionRecord{}, err
	}
	return programUnit(ctx, s, "open_program_permission", ref.ProjectID, ref.AgentID,
		func(unit *agentexecution.Unit, h *agentexecution.Handle) (AgentInteractionRecord, error) {
			destination, err := captureInteractionDestinationTx(ctx, unit.DB(), ref.ProjectID, ref.AgentID)
			if err != nil {
				return AgentInteractionRecord{}, err
			}
			result, err := h.OpenProgramPermission(ctx, ref.ref(), request, destination)
			if err != nil {
				return AgentInteractionRecord{}, err
			}
			row, err := dbsqlc.New(unit.DB()).GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
				ProjectID: ref.ProjectID, AgentID: ref.AgentID, ID: result.ID})
			if err != nil {
				return AgentInteractionRecord{}, err
			}
			return agentInteractionRecordFromSQLC(row), nil
		})
}

// CompleteProgramToolCall stores the call's result outside the model context.
// A waiting call completes under its claim; unclaimed or authorize-phase calls
// may only fail, be denied, or be canceled.
func (s *Store) CompleteProgramToolCall(
	ctx context.Context, input CompleteProgramToolCallInput,
) (ProgramToolCallRecord, error) {
	if err := input.validate(); err != nil {
		return ProgramToolCallRecord{}, err
	}
	return programUnit(ctx, s, "complete_program_tool_call", input.ProjectID, input.AgentID,
		func(unit *agentexecution.Unit, _ *agentexecution.Handle) (ProgramToolCallRecord, error) {
			return completeProgramTool(ctx, unit, input.ProgramToolCallRef, ToolCallCompletionInput{
				Outcome: input.Outcome, ResultContentParts: input.ResultContentParts})
		})
}

func completeProgramTool(
	ctx context.Context, unit *agentexecution.Unit, ref ProgramToolCallRef, input ToolCallCompletionInput,
) (ProgramToolCallRecord, error) {
	h, err := unit.Handle(ref.ProjectID, ref.AgentID)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	blocks, err := parseToolResultContentBlocks(input.ResultContentParts)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	parts, err := executionContent(blocks)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	tool, err := h.CompleteProgramTool(ctx, ref.ref(), string(input.Outcome), parts)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	return programToolCallRecordTx(ctx, unit.DB(), ref.ProjectID, ref.AgentID, tool)
}

// ClaimProgramToolCall leases the call for its next phase under ref.ClaimID.
// A ready call moves to waiting under the claim (ProgramToolCallPhaseExecute,
// at most once); an awaiting_authorization call gets an authorization lease
// that may be claimed again once it expires (ProgramToolCallPhaseAuthorize).
// It reports false when the call is not claimable: it is claimed, waiting for
// approval, completed, or its process has ended.
func (s *Store) ClaimProgramToolCall(
	ctx context.Context, ref ProgramToolCallRef, lease time.Duration,
) (ProgramToolCallClaim, bool, error) {
	if err := ref.validate(); err != nil {
		return ProgramToolCallClaim{}, false, err
	}
	type result struct {
		claim ProgramToolCallClaim
		found bool
	}
	got, err := programUnit(ctx, s, "claim_program_tool_call", ref.ProjectID, ref.AgentID,
		func(unit *agentexecution.Unit, h *agentexecution.Handle) (result, error) {
			tool, phase, found, err := h.ClaimProgramTool(ctx, ref.ref(), lease)
			if err != nil || !found {
				return result{}, err
			}
			record, err := programToolCallRecordTx(ctx, unit.DB(), ref.ProjectID, ref.AgentID, tool)
			return result{claim: ProgramToolCallClaim{ProgramToolCallRecord: record, Phase: ProgramToolCallPhase(phase)},
				found: true}, err
		})
	return got.claim, got.found, err
}

// RenewProgramToolCallClaim extends an unexpired claim while the process is
// live. It fails with storeerr.ErrProgramToolCallClaimLost otherwise.
func (s *Store) RenewProgramToolCallClaim(
	ctx context.Context, ref ProgramToolCallRef, lease time.Duration,
) error {
	if err := ref.validate(); err != nil {
		return err
	}
	return s.cell.RenewProgramTool(ctx, ref.AgentID, ref.ref(), lease)
}

// GetProgramToolCall reads a call made by the process, including its result
// content once completed. Key and claim fields are not loaded.
func (s *Store) GetProgramToolCall(
	ctx context.Context, projectID, agentID, processID, toolCallID uuid.UUID,
) (ProgramToolCallRecord, bool, error) {
	ref := ProgramToolCallRef{ProjectID: projectID, AgentID: agentID, ProcessID: processID, ToolCallID: toolCallID}
	if err := ref.validate(); err != nil {
		return ProgramToolCallRecord{}, false, err
	}
	record, err := getProgramToolCallTx(ctx, s.pool, ref)
	if errors.Is(err, storeerr.ErrNotFound) {
		return ProgramToolCallRecord{}, false, nil
	}
	return record, err == nil, err
}

func getProgramToolCallTx(ctx context.Context, tx dbsqlc.DBTX, ref ProgramToolCallRef) (ProgramToolCallRecord, error) {
	record, err := getToolCallTx(ctx, tx, ref.ProjectID, ref.AgentID, ref.ToolCallID)
	if err != nil {
		return ProgramToolCallRecord{}, err
	}
	process, err := dbsqlc.New(tx).GetProcess(ctx, dbsqlc.GetProcessParams{
		ProjectID: ref.ProjectID, AgentID: ref.AgentID, ID: ref.ProcessID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgramToolCallRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProgramToolCallRecord{}, fmt.Errorf("load program tool call process: %w", err)
	}
	if record.Caller != "program" || record.ParentToolCallID != process.ToolCallID {
		return ProgramToolCallRecord{}, storeerr.ErrNotFound
	}
	return ProgramToolCallRecord{ToolCallRecord: record, ProcessID: ref.ProcessID}, nil
}

// ReapProgramToolCalls cancels open calls of inactive agents or ended
// processes (closing their interactions) and fails waiting calls whose claim
// expired. It returns the number of calls completed, and the errors of the
// calls it could not reap.
func (s *Store) ReapProgramToolCalls(ctx context.Context, batch int) (int, error) {
	if batch <= 0 {
		return 0, errors.New("batch must be positive")
	}
	rows, err := s.cell.ListProgramToolReapable(ctx, int32(min(batch, 1000)))
	if err != nil {
		return 0, fmt.Errorf("list reapable program tool calls: %w", err)
	}
	// A call that fails to reap does not hold back the ones behind it.
	reaped := 0
	var failed error
	for _, row := range rows {
		done, err := programUnit(ctx, s, "reap_program_tool_call", row.ProjectID, row.AgentID,
			func(_ *agentexecution.Unit, h *agentexecution.Handle) (bool, error) {
				return h.ReapProgramTool(ctx, row.ID)
			})
		switch {
		case errors.Is(err, storeerr.ErrNotFound):
		case ctx.Err() != nil:
			return reaped, errors.Join(failed, err)
		case err != nil:
			failed = errors.Join(failed, fmt.Errorf("reap program tool call %s: %w", row.ID, err))
		case done:
			reaped++
		}
	}
	return reaped, failed
}

// ExecuteProgramToolCall is ExecuteToolCall for a program call claimed in the
// execute phase: the plan's command runs fenced on the claim and live process
// instead of a runtime lock. CompleteToolCallInExecution completes the call;
// StartToolCallAsync leaves it waiting under the claim (Disposition Running)
// for a later CompleteProgramToolCall. Commands that need the runtime lock
// (processes, questions, machines, subagents) fail with
// storeerr.ErrInvalidToolCallDisposition.
func (s *Store) ExecuteProgramToolCall(
	ctx context.Context, ref ProgramToolCallRef, plan ToolCallPlan,
) (ExecuteToolCallResult, error) {
	if err := ref.validate(); err != nil {
		return ExecuteToolCallResult{}, err
	}
	if ref.ClaimID == uuid.Nil {
		return ExecuteToolCallResult{}, errors.New("program tool call claim is required")
	}
	if plan == nil {
		return ExecuteToolCallResult{}, errors.New("tool call plan is required")
	}
	return agentexecution.Retry(ctx, "execute_program_tool_call",
		func(ctx context.Context) (ExecuteToolCallResult, error) {
			return s.executeToolCallOnce(ctx, ExecuteToolCallInput{
				ProjectID: ref.ProjectID, AgentID: ref.AgentID, ToolCallID: ref.ToolCallID}, &ref, plan)
		})
}

func (input CompleteProgramToolCallInput) validate() error {
	if err := input.ProgramToolCallRef.validate(); err != nil {
		return err
	}
	if !input.Outcome.IsTerminal() {
		return errors.New("tool outcome is invalid")
	}
	return nil
}
