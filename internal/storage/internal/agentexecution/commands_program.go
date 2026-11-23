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
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type ProgramToolPhase uint8

const (
	ProgramToolAuthorize ProgramToolPhase = iota + 1
	ProgramToolExecute
)

const (
	MaxOpenProgramTools    = 128
	MaxProgramToolKeyBytes = 128
)

// programToolNamedLifetime is how long a named call stays open once the command that made it has
// ended and no server is running it. Until then a rerun with its key picks it up.
var programToolNamedLifetime = 24 * time.Hour

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
	// Key identifies the call across the agent's commands. Reusing it returns the earlier call.
	Key string
	// Named is set when the command chose the key. A named call outlives the command that made it,
	// so a rerun gets the same execution, approval or result.
	Named bool
	Name  string
	Type  string
	Input json.RawMessage
	// OfferID names the daemon offer this request answers. The call keeps the latest offer to make or
	// pick it up, so a late cancel of an earlier offer of its key cannot end it.
	OfferID string
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

func (h *Handle) lockProgram(ctx context.Context, processID uuid.UUID) (executiondb.LockProgramProcessRow, error) {
	row, err := executiondb.New().LockProgramProcess(ctx, h.unit.DB(),
		executiondb.LockProgramProcessParams{AgentID: h.route.AgentID, ID: processID})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, storeerr.ErrNotFound
	}
	return row, err
}

// fenceProgram requires a running command: only one can make calls.
func (h *Handle) fenceProgram(ctx context.Context, processID uuid.UUID) (executiondb.LockProgramProcessRow, error) {
	row, err := h.lockProgram(ctx, processID)
	if err != nil {
		return row, err
	}
	if !row.Live || h.identity.State != "active" {
		return row, storeerr.ErrProcessTerminal
	}
	return row, nil
}

// programToolOpen reports whether a call can still make progress. A named call, or a custom call
// waiting for its result, needs only its agent to be active; any other call ends with the command
// that made it.
func (h *Handle) programToolOpen(process executiondb.LockProgramProcessRow, row executiondb.ReadProgramToolRow) bool {
	return h.identity.State == "active" && (process.Live || row.ProgramNamed || programToolAwaitingResult(row))
}

// programToolAwaitingResult reports whether a custom call has been authorized and waits for the
// result its runner submits. Like a named call it then outlives the command that made it, but it
// never expires: a command's timeout, lost connection or exit leaves it pending, and only its
// result, a cancel of its agent, or its agent ending settles it.
func programToolAwaitingResult(row executiondb.ReadProgramToolRow) bool {
	return row.Type == toolcatalog.ToolTypeCustom && row.State == "ready"
}

func (h *Handle) programTool(ctx context.Context, id, processID uuid.UUID) (executiondb.ReadProgramToolRow, error) {
	row, err := executiondb.New().ReadProgramTool(ctx, h.unit.DB(),
		executiondb.ReadProgramToolParams{AgentID: h.route.AgentID, ID: id})
	if err == nil && processID != uuid.Nil && row.ProcessID != processID {
		err = pgx.ErrNoRows
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return row, storeerr.ErrNotFound
	}
	return row, err
}

func (h *Handle) fencedProgramTool(ctx context.Context, ref ProgramToolRef) (executiondb.ReadProgramToolRow, error) {
	process, err := h.lockProgram(ctx, ref.ProcessID)
	if err != nil {
		return executiondb.ReadProgramToolRow{}, err
	}
	row, err := h.programTool(ctx, ref.ID, ref.ProcessID)
	if err != nil {
		return row, err
	}
	if !h.programToolOpen(process, row) {
		return row, storeerr.ErrProcessTerminal
	}
	return row, nil
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

// readyProgramToolNotification carries a custom call's name and input, as for a model's call, so
// its runner can pick the call up from the update alone.
func (h *Handle) readyProgramToolNotification(
	row executiondb.ReadProgramToolRow, interaction *notifications.AgentInteractionUpdate,
) {
	if row.Type != toolcatalog.ToolTypeCustom {
		h.programToolNotification(row, "ready", interaction)
		return
	}
	h.unit.Notifications().AddReadyCustomToolCallUpdate(h.route.AgentID, row.ID,
		storeutil.ValueOrZero(row.ParentToolCallID), row.Name, row.Input, interaction)
}

func validProgramToolKey(key string) bool {
	return key != "" && len(key) <= MaxProgramToolKeyBytes && dbsafe.Text(key) == nil
}

// resumeProgramTool finds the call a key already names. It reports false when the key is new, or when
// its last call was canceled before it could take effect, so the caller may start another attempt.
// input.Type is checked only when the caller has resolved it. An open call it finds now answers
// input.OfferID.
func (h *Handle) resumeProgramTool(
	ctx context.Context, input CreateProgramToolInput, callInput json.RawMessage,
) (ProgramTool, bool, error) {
	processID, name, toolType := input.ProcessID, input.Name, input.Type
	q := executiondb.New()
	found, err := q.ReadProgramToolByKey(ctx, h.unit.DB(),
		executiondb.ReadProgramToolByKeyParams{AgentID: h.route.AgentID, Key: input.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProgramTool{}, false, nil
	}
	if err != nil {
		return ProgramTool{}, false, err
	}
	row := executiondb.ReadProgramToolRow(found)
	if row.Name != name || (toolType != "" && row.Type != toolType) || !equalJSON(row.Input, callInput) {
		return ProgramTool{}, false, storeerr.ErrIdempotencyConflict
	}
	if row.State != "completed" && row.ProcessID != processID {
		// Another command made this call. A named call is picked up as it is, running or waiting for
		// approval. Any other call ended with its command, so settle it now rather than attaching to
		// one the reaper is about to end.
		reaped, err := h.reapProgramTool(ctx, row)
		if err != nil {
			return ProgramTool{}, false, err
		}
		if reaped {
			if row, err = h.programTool(ctx, row.ID, uuid.Nil); err != nil {
				return ProgramTool{}, false, err
			}
		}
	}
	if row.State == "completed" && storeutil.ValueOrZero(row.Outcome) == "canceled" {
		return ProgramTool{}, false, nil
	}
	if row.State != "completed" && input.OfferID != "" && storeutil.ValueOrZero(row.ProgramOfferID) != input.OfferID {
		if _, err := q.SetProgramToolOffer(ctx, h.unit.DB(), executiondb.SetProgramToolOfferParams{
			OfferID: input.OfferID, AgentID: h.route.AgentID, ID: row.ID}); err != nil {
			return ProgramTool{}, false, err
		}
	}
	return programToolRecord(row), true, nil
}

func programToolInput(input CreateProgramToolInput) (json.RawMessage, error) {
	if !validProgramToolKey(input.Key) || input.Name == "" || dbsafe.Text(input.Name) != nil ||
		(input.OfferID != "" && !validProgramToolKey(input.OfferID)) {
		return nil, storeerr.ErrInvalidRequest
	}
	callInput, err := objectJSON(input.Input)
	if err != nil || dbsafe.JSONStrings(callInput) != nil {
		return nil, storeerr.ErrInvalidRequest
	}
	return callInput, nil
}

// ResumeProgramTool checks only that the calling process may make calls, so a finished call is
// returned even when a new one could no longer be started.
func (h *Handle) ResumeProgramTool(ctx context.Context, input CreateProgramToolInput) (ProgramTool, bool, error) {
	type resumed struct {
		tool  ProgramTool
		found bool
	}
	result, err := programCommand(h, func() (resumed, error) {
		callInput, err := programToolInput(input)
		if err != nil {
			return resumed{}, err
		}
		if _, err := h.fenceProgram(ctx, input.ProcessID); err != nil {
			return resumed{}, err
		}
		input.Type = ""
		tool, found, err := h.resumeProgramTool(ctx, input, callInput)
		return resumed{tool: tool, found: found}, err
	})
	return result.tool, result.found, err
}

func (h *Handle) CreateProgramTool(ctx context.Context, input CreateProgramToolInput) (ProgramTool, error) {
	return programCommand(h, func() (ProgramTool, error) {
		if !toolcatalog.Type(input.Type).Valid() {
			return ProgramTool{}, storeerr.ErrInvalidRequest
		}
		callInput, err := programToolInput(input)
		if err != nil {
			return ProgramTool{}, err
		}
		process, err := h.fenceProgram(ctx, input.ProcessID)
		if err != nil {
			return ProgramTool{}, err
		}
		q := executiondb.New()
		existing := func() (ProgramTool, bool, error) {
			return h.resumeProgramTool(ctx, input, callInput)
		}
		if record, found, err := existing(); err != nil || found {
			return record, err
		}
		open, err := q.CountOpenProgramTools(ctx, h.unit.DB(), executiondb.CountOpenProgramToolsParams{
			AgentID: h.route.AgentID, ParentID: process.ToolCallID})
		if err != nil {
			return ProgramTool{}, err
		}
		if open >= MaxOpenProgramTools {
			return ProgramTool{}, storeerr.ErrProgramToolCallLimitReached
		}
		id, err := uuid.NewV7()
		if err != nil {
			return ProgramTool{}, err
		}
		if _, err = q.InsertProgramTool(ctx, h.unit.DB(), executiondb.InsertProgramToolParams{
			ID: id, AgentID: h.route.AgentID, ParentID: process.ToolCallID, Key: input.Key, Named: input.Named,
			Name: input.Name, Type: input.Type, Input: callInput, OfferID: storeutil.TextFromEmpty(input.OfferID),
		}); errors.Is(err, pgx.ErrNoRows) {
			record, found, err := existing()
			if err == nil && !found {
				err = storeerr.ErrStateTransitionConflict
			}
			return record, err
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

func (h *Handle) AuthorizeProgramTool(ctx context.Context, ref ProgramToolRef, lease time.Duration) (bool, error) {
	return programCommand(h, func() (bool, error) {
		row, err := h.authorizingProgramTool(ctx, ref)
		if err != nil {
			return false, err
		}
		switch {
		case row.State == "waiting" && storeutil.ValueOrZero(row.ProgramClaimID) == ref.ClaimID,
			programToolAwaitingResult(row):
			return false, nil
		case row.State != "awaiting_authorization":
			return false, storeerr.ErrStateTransitionConflict
		}
		if err = h.transitionProgramTool(ctx, row, "ready", uuid.Nil, 0); err != nil {
			return false, err
		}
		row.State, row.ProgramClaimID = "ready", nil
		if row.Type == toolcatalog.ToolTypeCustom {
			// Nothing here runs a custom tool: the call waits for the result its runner submits.
			h.readyProgramToolNotification(row, nil)
			return true, nil
		}
		if err = h.transitionProgramTool(ctx, row, "waiting", ref.ClaimID, lease); err != nil {
			return false, err
		}
		h.programToolNotification(row, "waiting", nil)
		return true, nil
	})
}

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
			ProjectID: h.route.ProjectID, AgentID: h.route.AgentID, ToolID: row.ID, Kind: kind, Request: request,
			Destination: target})
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
		case row.State == "ready" && row.Type != toolcatalog.ToolTypeCustom:
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

func (h *Handle) ReleaseProgramTool(ctx context.Context, ref ProgramToolRef) error {
	_, err := programCommand(h, func() (struct{}, error) {
		row, err := h.fencedProgramTool(ctx, ref)
		if err != nil {
			return struct{}{}, err
		}
		if row.State != "waiting" || storeutil.ValueOrZero(row.ProgramClaimID) != ref.ClaimID {
			return struct{}{}, storeerr.ErrProgramToolCallClaimLost
		}
		if err = h.transitionProgramTool(ctx, row, "ready", uuid.Nil, 0); err != nil {
			return struct{}{}, err
		}
		h.readyProgramToolNotification(row, nil)
		return struct{}{}, nil
	})
	return err
}

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

// CompleteProgramCustomTool records the result a custom tool's runner submits for a call a command
// made. Whoever is waiting on the call, in that command or a later one with its key, gets it.
func (h *Handle) CompleteProgramCustomTool(
	ctx context.Context, id uuid.UUID, outcome string, content []Content,
) (ProgramTool, error) {
	return programCommand(h, func() (ProgramTool, error) {
		row, err := h.programTool(ctx, id, uuid.Nil)
		if err != nil {
			return ProgramTool{}, err
		}
		if row.Type == toolcatalog.ToolTypeCustom && row.State == "completed" {
			return ProgramTool{}, storeerr.ErrToolCallCompleted
		}
		if !programToolAwaitingResult(row) || (outcome != "succeeded" && outcome != "failed") {
			return ProgramTool{}, storeerr.ErrStateTransitionConflict
		}
		return h.completeProgramTool(ctx, row, outcome, content, nil)
	})
}

// cancelProgramToolsAwaitingResult ends the agent's custom calls still waiting for their results
// when the agent is canceled. Their runners may have acted on them, so their keys keep returning
// outcome_unknown rather than calling the tool again.
func (h *Handle) cancelProgramToolsAwaitingResult(ctx context.Context) error {
	ids, err := executiondb.New().ListProgramToolsAwaitingResult(ctx, h.unit.DB(),
		executiondb.ListProgramToolsAwaitingResultParams{AgentID: h.route.AgentID})
	if err != nil {
		return err
	}
	for _, id := range ids {
		row, err := h.programTool(ctx, id, uuid.Nil)
		if err != nil {
			return err
		}
		if !programToolAwaitingResult(row) {
			continue
		}
		outcome, body, err := programToolStopped(row, "agent_canceled",
			"The agent was canceled before this call's result was submitted.")
		if err != nil {
			return err
		}
		if _, err = h.completeProgramTool(ctx, row, outcome,
			[]Content{{Kind: "structured_data", Data: body}}, nil); err != nil {
			return err
		}
	}
	return nil
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

// programToolStopped is the result of a call that is ended without finishing. A call that never
// started is canceled, so its key can run it again. A call that had started, of any tool type, or a
// custom call handed to its runner, is failed as outcome_unknown, because it may have taken effect,
// and its key keeps returning that result, as a model's call would.
func programToolStopped(row executiondb.ReadProgramToolRow, code, message string) (string, json.RawMessage, error) {
	outcome := "canceled"
	if row.State == "waiting" || programToolAwaitingResult(row) {
		outcome, code = "failed", "outcome_unknown"
		message += " The call had started, so it may or may not have taken effect."
	}
	body, err := json.Marshal(map[string]string{"error_code": code, "message": message})
	return outcome, body, err
}

func (h *Handle) reapProgramTool(ctx context.Context, row executiondb.ReadProgramToolRow) (bool, error) {
	process, err := executiondb.New().LockProgramProcess(ctx, h.unit.DB(),
		executiondb.LockProgramProcessParams{AgentID: h.route.AgentID, ID: row.ProcessID})
	if err != nil {
		return false, err
	}
	code, message := "process_ended", "The command that made this call is no longer running."
	switch {
	case h.identity.State != "active":
		code, message = "agent_inactive", "The agent is no longer active."
	case (process.Live || row.ProgramNamed) && row.State == "waiting" && row.ClaimExpired:
		code, message = "server_lost", "The server running this call stopped before it completed."
	case process.Live, programToolAwaitingResult(row):
		return false, nil
	case row.ProgramNamed:
		// A named call outlives its command so a rerun can pick it up. It ends only when none did.
		if (row.ProgramClaimID != nil && !row.ClaimExpired) || time.Since(row.CreatedAt) < programToolNamedLifetime {
			return false, nil
		}
		code, message = "expired", "No command picked this call up after the one that made it ended."
	}
	outcome, body, err := programToolStopped(row, code, message)
	if err != nil {
		return false, err
	}
	_, err = h.completeProgramTool(ctx, row, outcome, []Content{{Kind: "structured_data", Data: body}}, nil)
	return err == nil, err
}

func (h *Handle) ReapProgramTool(ctx context.Context, id uuid.UUID) (bool, error) {
	return programCommand(h, func() (bool, error) {
		row, err := h.programTool(ctx, id, uuid.Nil)
		if err != nil || row.State == "completed" {
			return false, err
		}
		return h.reapProgramTool(ctx, row)
	})
}

// CancelProgramTool also ends a call a server is still running: that server loses its claim and stops.
// A custom call waiting for its result stays pending: only the command stopped waiting for it.
func (h *Handle) CancelProgramTool(ctx context.Context, ref ProgramToolRef) (bool, error) {
	return programCommand(h, func() (bool, error) {
		row, err := h.fencedProgramTool(ctx, ref)
		if err != nil {
			return false, err
		}
		return true, h.cancelProgramTool(ctx, row)
	})
}

// CancelProgramToolByKey ends the call a key names for a cancel of offerID, unless a later offer has
// made or picked it up since. An empty offerID ends it whatever offer it answers. It leaves a named
// call alone: it outlives the command that stopped waiting.
func (h *Handle) CancelProgramToolByKey(ctx context.Context, processID uuid.UUID, key, offerID string) error {
	_, err := programCommand(h, func() (struct{}, error) {
		process, err := h.lockProgram(ctx, processID)
		if err != nil {
			return struct{}{}, err
		}
		found, err := executiondb.New().ReadProgramToolByKey(ctx, h.unit.DB(),
			executiondb.ReadProgramToolByKeyParams{AgentID: h.route.AgentID, Key: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return struct{}{}, nil
		}
		if err != nil {
			return struct{}{}, err
		}
		row := executiondb.ReadProgramToolRow(found)
		if row.ProcessID != processID || row.ProgramNamed || !h.programToolOpen(process, row) ||
			(offerID != "" && storeutil.ValueOrZero(row.ProgramOfferID) != offerID) {
			return struct{}{}, nil
		}
		return struct{}{}, h.cancelProgramTool(ctx, row)
	})
	return err
}

func (h *Handle) cancelProgramTool(ctx context.Context, row executiondb.ReadProgramToolRow) error {
	if row.State == "completed" || programToolAwaitingResult(row) {
		return nil
	}
	outcome, body, err := programToolStopped(row, "caller_gone", "The command stopped waiting for this call.")
	if err != nil {
		return err
	}
	_, err = h.completeProgramTool(ctx, row, outcome, []Content{{Kind: "structured_data", Data: body}}, nil)
	return err
}

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
		process, err := h.lockProgram(ctx, call.ProcessID)
		if err != nil {
			return Interaction{}, err
		}
		if !h.programToolOpen(process, call) {
			return Interaction{}, storeerr.ErrProcessTerminal
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
			h.readyProgramToolNotification(call, result.notification())
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

type ReapableProgramTool struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	CreatedAt time.Time
}

func (c *AgentCell) ListProgramToolReapable(
	ctx context.Context, after ReapableProgramTool, limit int32,
) ([]ReapableProgramTool, error) {
	var rows []executiondb.ListProgramToolReapableRow
	if err := c.short(ctx, func(tx pgx.Tx) (err error) {
		rows, err = executiondb.New().ListProgramToolReapable(ctx, tx,
			executiondb.ListProgramToolReapableParams{
				NamedLifetimeMicroseconds: programToolNamedLifetime.Microseconds(),
				AfterCreatedAt:            after.CreatedAt, AfterID: after.ID, LimitCount: limit})
		return err
	}); err != nil {
		return nil, err
	}
	result := make([]ReapableProgramTool, len(rows))
	for i, row := range rows {
		result[i] = ReapableProgramTool{ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID,
			CreatedAt: row.CreatedAt}
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
