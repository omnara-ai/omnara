package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

// Program tool calls are children of a run_command call whose process calls
// tools through `omnarad call`. They never touch the execution head, agent
// events, or the runtime lock: every write holds the agent row lock and a
// FOR SHARE lock on the owning live process instead.

type ProgramToolPhase uint8

const (
	// ProgramToolAuthorize leases an awaiting_authorization call for the
	// permission step; an expired authorization lease may be claimed again.
	ProgramToolAuthorize ProgramToolPhase = iota + 1
	// ProgramToolExecute moves a ready call to waiting under the claim; an
	// expired execution claim is failed by the reaper, never re-run.
	ProgramToolExecute
)

type ProgramTool struct {
	ID        uuid.UUID
	ParentID  uuid.UUID
	ProcessID uuid.UUID
	State     string
	ClaimID   uuid.UUID
	ResultID  uuid.UUID
	Created   bool
}

type ProgramToolRef struct {
	ID        uuid.UUID
	ProcessID uuid.UUID
	ClaimID   uuid.UUID
}

type CreateProgramToolInput struct {
	ProcessID uuid.UUID
	Key       uuid.UUID
	Name      string
	Type      string
	Input     json.RawMessage
}

func programToolRecord(row executiondb.ReadProgramToolRow) ProgramTool {
	return ProgramTool{ID: row.ID, ParentID: storeutil.ValueOrZero(row.ParentToolCallID), ProcessID: row.ProcessID,
		State: row.State, ClaimID: storeutil.ValueOrZero(row.ProgramClaimID), ResultID: storeutil.ValueOrZero(row.ResultID)}
}

func programCommand[T any](h *Handle, run func() (T, error)) (result T, err error) {
	if _, err = h.Route(); err != nil {
		return result, err
	}
	if h.identity == nil {
		return result, errors.New("program tool commands require a locked agent")
	}
	defer func() {
		if err != nil {
			h.unit.aborted = true
		}
	}()
	return run()
}

// fenceProgram requires an active agent and a live process, held FOR SHARE so
// the process cannot end before the write commits.
func (h *Handle) fenceProgram(ctx context.Context, processID uuid.UUID) (executiondb.LockProgramProcessRow, error) {
	row, err := executiondb.New().LockProgramProcess(ctx, h.unit.DB(),
		executiondb.LockProgramProcessParams{AgentID: h.route.AgentID, ID: processID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, storeerr.ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if !row.Live || h.identity.State != "active" {
		return row, storeerr.ErrProcessTerminal
	}
	return row, nil
}

func (h *Handle) programTool(ctx context.Context, id, processID uuid.UUID) (executiondb.ReadProgramToolRow, error) {
	row, err := executiondb.New().ReadProgramTool(ctx, h.unit.DB(),
		executiondb.ReadProgramToolParams{AgentID: h.route.AgentID, ID: &id})
	if err == nil && processID != uuid.Nil && row.ProcessID != processID {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return row, storeerr.ErrNotFound
	}
	return row, err
}

func (h *Handle) fencedProgramTool(ctx context.Context, ref ProgramToolRef) (executiondb.ReadProgramToolRow, error) {
	if _, err := h.fenceProgram(ctx, ref.ProcessID); err != nil {
		return executiondb.ReadProgramToolRow{}, err
	}
	return h.programTool(ctx, ref.ID, ref.ProcessID)
}

func (h *Handle) transitionProgramTool(
	ctx context.Context, row executiondb.ReadProgramToolRow, state string, claim uuid.UUID, lease time.Duration,
) error {
	n, err := executiondb.New().TransitionProgramTool(ctx, h.unit.DB(), executiondb.TransitionProgramToolParams{
		AgentID: h.route.AgentID, ID: row.ID, PreviousState: row.State, PreviousClaimID: row.ProgramClaimID,
		NextState: state, NextClaimID: storeutil.IDFromNil(claim), LeaseMicroseconds: lease.Microseconds()})
	if err == nil && n != 1 {
		err = storeerr.ErrStateTransitionConflict
	}
	return err
}

func (h *Handle) programToolNotification(
	row executiondb.ReadProgramToolRow, state string, interaction *notifications.AgentInteractionUpdate,
) {
	h.unit.Notifications().AddProgramToolCallUpdate(h.route.AgentID, row.ID,
		storeutil.ValueOrZero(row.ParentToolCallID), state, interaction)
}

// CreateProgramTool inserts an awaiting_authorization child of the process's
// run_command call, idempotently on (process call, key).
func (h *Handle) CreateProgramTool(ctx context.Context, input CreateProgramToolInput) (ProgramTool, error) {
	return programCommand(h, func() (ProgramTool, error) {
		if input.Key == uuid.Nil || input.Name == "" || (input.Type != "built_in" && input.Type != "mcp") {
			return ProgramTool{}, storeerr.ErrInvalidRequest
		}
		key := input.Key.String()
		if err := dbsafe.Text(input.Name); err != nil {
			return ProgramTool{}, storeerr.ErrInvalidRequest
		}
		callInput, err := objectJSON(input.Input)
		if err != nil || dbsafe.JSONStrings(callInput) != nil {
			return ProgramTool{}, storeerr.ErrInvalidRequest
		}
		process, err := h.fenceProgram(ctx, input.ProcessID)
		if err != nil {
			return ProgramTool{}, err
		}
		q := executiondb.New()
		existing := func() (ProgramTool, error) {
			row, err := q.ReadProgramTool(ctx, h.unit.DB(), executiondb.ReadProgramToolParams{
				AgentID: h.route.AgentID, ParentID: &process.ToolCallID, Key: &key})
			if err != nil {
				return ProgramTool{}, err
			}
			if row.Name != input.Name || row.Type != input.Type || !equalJSON(row.Input, callInput) {
				return ProgramTool{}, storeerr.ErrIdempotencyConflict
			}
			return programToolRecord(row), nil
		}
		record, err := existing()
		if !errors.Is(err, pgx.ErrNoRows) {
			return record, err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return ProgramTool{}, err
		}
		if _, err = q.InsertProgramTool(ctx, h.unit.DB(), executiondb.InsertProgramToolParams{
			ID: id, AgentID: h.route.AgentID, ParentID: process.ToolCallID, Key: key,
			Name: input.Name, Type: input.Type, Input: callInput}); errors.Is(err, pgx.ErrNoRows) {
			return existing()
		} else if err != nil {
			return ProgramTool{}, err
		}
		h.unit.Notifications().AddProgramToolCallUpdate(h.route.AgentID, id, process.ToolCallID,
			"awaiting_authorization", nil)
		return ProgramTool{ID: id, ParentID: process.ToolCallID, ProcessID: input.ProcessID,
			State: "awaiting_authorization", Created: true}, nil
	})
}

func (h *Handle) authorizingProgramTool(
	ctx context.Context, ref ProgramToolRef,
) (executiondb.ReadProgramToolRow, error) {
	row, err := h.fencedProgramTool(ctx, ref)
	if err != nil {
		return row, err
	}
	if row.State == "awaiting_authorization" && storeutil.ValueOrZero(row.ProgramClaimID) != ref.ClaimID {
		return row, storeerr.ErrProgramToolCallClaimLost
	}
	return row, nil
}

// AuthorizeProgramTool moves an awaiting_authorization call to waiting under
// the authorization claim, so its holder executes it next (at most once). A
// call already waiting under the claim is a no-op.
func (h *Handle) AuthorizeProgramTool(ctx context.Context, ref ProgramToolRef, lease time.Duration) (bool, error) {
	return programCommand(h, func() (bool, error) {
		row, err := h.authorizingProgramTool(ctx, ref)
		if err != nil {
			return false, err
		}
		switch {
		case row.State == "waiting" && storeutil.ValueOrZero(row.ProgramClaimID) == ref.ClaimID:
			return false, nil
		case row.State != "awaiting_authorization":
			return false, storeerr.ErrStateTransitionConflict
		}
		// Through ready, as the tool call transition graph requires.
		if err = h.transitionProgramTool(ctx, row, "ready", uuid.Nil, 0); err != nil {
			return false, err
		}
		row.State, row.ProgramClaimID = "ready", nil
		if err = h.transitionProgramTool(ctx, row, "waiting", ref.ClaimID, lease); err != nil {
			return false, err
		}
		h.programToolNotification(row, "waiting", nil)
		return true, nil
	})
}

// OpenProgramPermission opens the permission interaction the user resolves
// through the normal interaction API and moves the call to awaiting_permission.
func (h *Handle) OpenProgramPermission(
	ctx context.Context, ref ProgramToolRef, permission toolpermission.Request, destination json.RawMessage,
) (Interaction, error) {
	return programCommand(h, func() (Interaction, error) {
		if err := permission.Validate(); err != nil {
			return Interaction{}, err
		}
		request, err := json.Marshal(permission)
		if err != nil {
			return Interaction{}, err
		}
		row, err := h.authorizingProgramTool(ctx, ref)
		if err != nil {
			return Interaction{}, err
		}
		q := executiondb.New()
		kind := "permission"
		existing, err := q.ReadExecutionInteraction(ctx, h.unit.DB(),
			executiondb.ReadExecutionInteractionParams{AgentID: h.route.AgentID, ToolID: &row.ID, Kind: &kind})
		if err == nil {
			if !equalJSON(existing.Request, request) {
				return Interaction{}, storeerr.ErrIdempotencyConflict
			}
			return interactionRecord(existing), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Interaction{}, err
		}
		if row.State != "awaiting_authorization" {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		var target *json.RawMessage
		if len(destination) > 0 {
			if !json.Valid(destination) {
				return Interaction{}, errors.New("invalid interaction destination")
			}
			target = &destination
		}
		id, err := q.InsertExecutionInteraction(ctx, h.unit.DB(), executiondb.InsertExecutionInteractionParams{
			AgentID: h.route.AgentID, ToolID: row.ID, Kind: kind, Request: request, Destination: target})
		if err != nil {
			return Interaction{}, err
		}
		if err = h.transitionProgramTool(ctx, row, "awaiting_permission", uuid.Nil, 0); err != nil {
			return Interaction{}, err
		}
		result := Interaction{ID: id, ToolID: row.ID, Kind: kind, State: "open", Request: request,
			Resolution: json.RawMessage(`{}`), Created: true}
		h.programToolNotification(row, "awaiting_permission", result.notification())
		return result, nil
	})
}

// ClaimProgramTool leases one call for its next phase. It reports false when
// the call is not claimable, including when its process has ended.
func (h *Handle) ClaimProgramTool(
	ctx context.Context, ref ProgramToolRef, lease time.Duration,
) (ProgramTool, ProgramToolPhase, bool, error) {
	type claimed struct {
		tool  ProgramTool
		phase ProgramToolPhase
		found bool
	}
	result, err := programCommand(h, func() (claimed, error) {
		if ref.ClaimID == uuid.Nil || lease <= 0 {
			return claimed{}, errors.New("program tool claim and lease are required")
		}
		row, err := h.fencedProgramTool(ctx, ref)
		if errors.Is(err, storeerr.ErrProcessTerminal) || errors.Is(err, storeerr.ErrNotFound) {
			return claimed{}, nil
		}
		if err != nil {
			return claimed{}, err
		}
		next, phase := "waiting", ProgramToolExecute
		switch {
		case row.State == "ready":
		case row.State == "awaiting_authorization" && (row.ProgramClaimID == nil || row.ClaimExpired):
			next, phase = row.State, ProgramToolAuthorize
		default:
			return claimed{}, nil
		}
		if err = h.transitionProgramTool(ctx, row, next, ref.ClaimID, lease); err != nil {
			return claimed{}, err
		}
		if next != row.State {
			h.programToolNotification(row, next, nil)
		}
		tool := programToolRecord(row)
		tool.State, tool.ClaimID = next, ref.ClaimID
		return claimed{tool: tool, phase: phase, found: true}, nil
	})
	if err != nil || !result.found {
		return ProgramTool{}, 0, false, err
	}
	return result.tool, result.phase, true, nil
}

// DispatchProgramTool fences a transactional execution phase on the claim.
func (h *Handle) DispatchProgramTool(ctx context.Context, ref ProgramToolRef) (ToolDispatch, error) {
	return programCommand(h, func() (ToolDispatch, error) {
		row, err := h.fencedProgramTool(ctx, ref)
		if err != nil {
			return 0, err
		}
		switch {
		case row.State == "completed":
			return ToolDispatchCompleted, nil
		case row.State != "waiting":
			return 0, storeerr.ErrStateTransitionConflict
		case storeutil.ValueOrZero(row.ProgramClaimID) != ref.ClaimID || row.ClaimExpired:
			return 0, storeerr.ErrProgramToolCallClaimLost
		}
		return ToolDispatchReady, nil
	})
}

// CompleteProgramTool records the call's result. A claimed (waiting) call
// completes with any outcome under its claim; unexecuted calls may only be
// failed, denied, or canceled.
func (h *Handle) CompleteProgramTool(
	ctx context.Context, ref ProgramToolRef, outcome string, content []Content,
) (ProgramTool, error) {
	return programCommand(h, func() (ProgramTool, error) {
		row, err := h.fencedProgramTool(ctx, ref)
		if err != nil {
			return ProgramTool{}, err
		}
		claimed := storeutil.ValueOrZero(row.ProgramClaimID) == ref.ClaimID
		switch row.State {
		case "completed":
		case "waiting", "awaiting_authorization", "awaiting_permission", "ready":
			if !claimed {
				return ProgramTool{}, storeerr.ErrProgramToolCallClaimLost
			}
			if row.State != "waiting" && outcome == "succeeded" {
				return ProgramTool{}, storeerr.ErrStateTransitionConflict
			}
		default:
			return ProgramTool{}, storeerr.ErrStateTransitionConflict
		}
		return h.completeProgramTool(ctx, row, outcome, content, nil)
	})
}

func (h *Handle) completeProgramTool(
	ctx context.Context,
	row executiondb.ReadProgramToolRow,
	outcome string,
	content []Content,
	interaction *notifications.AgentInteractionUpdate,
) (ProgramTool, error) {
	switch outcome {
	case "succeeded", "failed", "denied", "canceled":
	default:
		return ProgramTool{}, errors.New("invalid tool outcome")
	}
	if row.State == "completed" {
		if storeutil.ValueOrZero(row.Outcome) != outcome {
			return ProgramTool{}, storeerr.ErrIdempotencyConflict
		}
		return programToolRecord(row), nil
	}
	parts := make([]Content, len(content))
	for i, part := range content {
		if part.Kind != "text" && part.Kind != "structured_data" && part.Kind != "artifact" {
			return ProgramTool{}, errors.New("invalid tool result content")
		}
		part.Exclude = true
		parts[i] = part
	}
	if err := h.transitionProgramTool(ctx, row, "completed", uuid.Nil, 0); err != nil {
		return ProgramTool{}, err
	}
	q := executiondb.New()
	id, err := q.InsertExecutionToolResult(ctx, h.unit.DB(), executiondb.InsertExecutionToolResultParams{
		AgentID: h.route.AgentID, ID: row.ID, Outcome: outcome})
	if err != nil {
		return ProgramTool{}, err
	}
	if err = h.writeContent(ctx, "tool_call_result", id, parts); err != nil {
		return ProgramTool{}, err
	}
	closed, err := q.CloseExecutionToolInteractions(ctx, h.unit.DB(),
		executiondb.CloseExecutionToolInteractionsParams{AgentID: h.route.AgentID, ToolID: row.ID})
	if err != nil {
		return ProgramTool{}, err
	}
	for _, i := range closed {
		interaction = &notifications.AgentInteractionUpdate{ID: i.ID, InteractionKind: i.InteractionKind, State: i.State}
	}
	h.programToolNotification(row, "completed", interaction)
	record := programToolRecord(row)
	record.State, record.ClaimID, record.ResultID = "completed", uuid.Nil, id
	return record, nil
}

// ReapProgramTool cancels a call whose agent is inactive or whose process has
// ended, and fails a call whose execution claim expired. It reports whether
// the call was completed.
func (h *Handle) ReapProgramTool(ctx context.Context, id uuid.UUID) (bool, error) {
	return programCommand(h, func() (bool, error) {
		row, err := h.programTool(ctx, id, uuid.Nil)
		if err != nil || row.State == "completed" {
			return false, err
		}
		process, err := executiondb.New().LockProgramProcess(ctx, h.unit.DB(),
			executiondb.LockProgramProcessParams{AgentID: h.route.AgentID, ID: row.ProcessID})
		if err != nil {
			return false, err
		}
		outcome, code, message := "canceled", "process_ended", "The command that made this call is no longer running."
		switch {
		case h.identity.State != "active":
			code, message = "agent_inactive", "The agent is no longer active."
		case process.Live && row.State == "waiting" && row.ClaimExpired:
			outcome, code, message = "failed", "worker_lost", "The worker running this call stopped before it completed."
		case process.Live:
			return false, nil
		}
		body, err := json.Marshal(map[string]string{"error_code": code, "message": message})
		if err != nil {
			return false, err
		}
		_, err = h.completeProgramTool(ctx, row, outcome, []Content{{Kind: "structured_data", Data: body}}, nil)
		return err == nil, err
	})
}

// resolveProgramInteraction records a user's decision on a program call's
// permission without an agent input, event, or execution-head change.
func (h *Handle) resolveProgramInteraction(
	ctx context.Context, row executiondb.ReadExecutionInteractionRow, input ResolveInteractionInput,
) (Interaction, error) {
	return programCommand(h, func() (Interaction, error) {
		resolution, err := normalizedResolution(row, input.Resolution)
		if err != nil {
			return Interaction{}, err
		}
		if row.State != "open" {
			if row.State != "resolved" || !equalJSON(row.Resolution, resolution) ||
				storeutil.ValueOrZero(row.ResolvedByActorID) != input.ActorID {
				return Interaction{}, storeerr.ErrIdempotencyConflict
			}
			return interactionRecord(row), nil
		}
		if row.InteractionKind != "permission" {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		call, err := h.programTool(ctx, row.ToolCallID, uuid.Nil)
		if err != nil {
			return Interaction{}, err
		}
		if _, err = h.fenceProgram(ctx, call.ProcessID); err != nil {
			return Interaction{}, err
		}
		if call.State != "awaiting_permission" {
			return Interaction{}, storeerr.ErrStateTransitionConflict
		}
		q := executiondb.New()
		n, err := q.ResolveExecutionInteraction(ctx, h.unit.DB(), executiondb.ResolveExecutionInteractionParams{
			AgentID: h.route.AgentID, ID: row.ID, State: "resolved", Resolution: resolution,
			ActorID: storeutil.IDFromNil(input.ActorID)})
		if err == nil && n != 1 {
			err = storeerr.ErrStateTransitionConflict
		}
		if err != nil {
			return Interaction{}, err
		}
		result := interactionRecord(row)
		result.State = "resolved"
		result.Resolution = resolution
		result.ActorID = input.ActorID
		request, err := toolpermission.ParseRequest(row.Request)
		if err != nil {
			return Interaction{}, err
		}
		answers, err := interactionform.ParseResolution(request.Form, resolution)
		if err != nil {
			return Interaction{}, err
		}
		decision, err := toolpermission.Resolve(request, answers)
		if err != nil {
			return Interaction{}, err
		}
		if decision.Decision == toolpermission.DecisionAllow {
			if err = h.transitionProgramTool(ctx, call, "ready", uuid.Nil, 0); err != nil {
				return Interaction{}, err
			}
			h.programToolNotification(call, "ready", result.notification())
			return result, nil
		}
		body, err := json.Marshal(map[string]string{"reason": decision.Reason})
		if err != nil {
			return Interaction{}, err
		}
		_, err = h.completeProgramTool(ctx, call, "denied", []Content{{Kind: "structured_data", Data: body}},
			result.notification())
		return result, err
	})
}

// RenewProgramTool extends a live claim. It holds only the call's row lock,
// so it needs no agent lock.
func (c *AgentCell) RenewProgramTool(
	ctx context.Context, agentID uuid.UUID, ref ProgramToolRef, lease time.Duration,
) error {
	if agentID == uuid.Nil || ref.ID == uuid.Nil || ref.ProcessID == uuid.Nil || ref.ClaimID == uuid.Nil || lease <= 0 {
		return errors.New("agent, program tool, process, claim, and lease are required")
	}
	err := c.short(ctx, func(tx pgx.Tx) (err error) {
		_, err = executiondb.New().RenewProgramToolClaim(ctx, tx, executiondb.RenewProgramToolClaimParams{
			AgentID: agentID, ID: ref.ID, ProcessID: ref.ProcessID, ClaimID: &ref.ClaimID,
			LeaseMicroseconds: lease.Microseconds()})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrProgramToolCallClaimLost
	}
	return err
}

type ProgramToolCandidate struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	ProcessID uuid.UUID
}

// ListProgramToolCandidates lists calls that may be claimable without locking
// them; ClaimProgramTool re-checks under the agent lock. Agents with the fewest
// calls in flight come first, then older calls.
func (c *AgentCell) ListProgramToolCandidates(ctx context.Context, limit int32) ([]ProgramToolCandidate, error) {
	var rows []executiondb.ListProgramToolCandidatesRow
	if err := c.short(ctx, func(tx pgx.Tx) (err error) {
		rows, err = executiondb.New().ListProgramToolCandidates(ctx, tx,
			executiondb.ListProgramToolCandidatesParams{LimitCount: limit})
		return err
	}); err != nil {
		return nil, err
	}
	result := make([]ProgramToolCandidate, len(rows))
	for i, row := range rows {
		result[i] = ProgramToolCandidate{ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, ProcessID: row.ProcessID}
	}
	return result, nil
}

// ListProgramToolReapable lists open calls of inactive agents or ended
// processes and waiting calls with expired claims.
func (c *AgentCell) ListProgramToolReapable(ctx context.Context, limit int32) ([]ProgramToolCandidate, error) {
	var rows []executiondb.ListProgramToolReapableRow
	if err := c.short(ctx, func(tx pgx.Tx) (err error) {
		rows, err = executiondb.New().ListProgramToolReapable(ctx, tx,
			executiondb.ListProgramToolReapableParams{LimitCount: limit})
		return err
	}); err != nil {
		return nil, err
	}
	result := make([]ProgramToolCandidate, len(rows))
	for i, row := range rows {
		result[i] = ProgramToolCandidate{ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID}
	}
	return result, nil
}

func (c *AgentCell) short(ctx context.Context, run func(pgx.Tx) error) error {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = run(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
