//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

const customProgramInput = `{"title":"Broken build"}`

func (f programFixture) createCustom(t *testing.T, key string) agentexecution.ProgramTool {
	t.Helper()
	var tool agentexecution.ProgramTool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey(key), Name: "create_ticket", Type: "custom",
			Input: json.RawMessage(customProgramInput)})
		require.NoError(t, err)
	})
	return tool
}

func (f programFixture) resumeCustom(t *testing.T, key string) (agentexecution.ProgramTool, bool) {
	t.Helper()
	var tool agentexecution.ProgramTool
	var found bool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		tool, found, err = h.ResumeProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey(key), Named: true, Name: "create_ticket",
			Input: json.RawMessage(customProgramInput)})
		require.NoError(t, err)
	})
	return tool, found
}

// authorizeCustom takes a custom call through authorization, which leaves it waiting for its result.
func (f programFixture) authorizeCustom(
	t *testing.T, tool agentexecution.ProgramTool,
) []notifications.ToolCallUpdatedCommitted {
	t.Helper()
	claim := uuid.New()
	phase, found := f.claim(t, tool.ID, claim)
	require.True(t, found)
	require.Equal(t, agentexecution.ProgramToolAuthorize, phase)
	ref := agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: claim}
	var updates []notifications.ToolCallUpdatedCommitted
	f.tx(t, func(u *agentexecution.Unit, h *agentexecution.Handle) {
		changed, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.NoError(t, err)
		require.True(t, changed)
		updates = u.Notifications().ToolCallUpdates()
		changed, err = h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.NoError(t, err)
		require.False(t, changed, "authorizing again is a no-op")
	})
	return updates
}

func endProgramProcess(t *testing.T, f programFixture) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
}

func programResult(t *testing.T, f programFixture, id uuid.UUID) (string, int) {
	t.Helper()
	var data string
	var events int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(b.structured_data::text,''),''),
 (SELECT count(*) FROM agent_events e WHERE e.tool_call_result_id=r.id)
 FROM tool_call_results r LEFT JOIN content_blocks b ON b.owner_tool_call_result_id=r.id
 WHERE r.tool_call_id=$1 GROUP BY r.id`, id).Scan(&data, &events))
	return data, events
}

// A custom call from a command waits for the result its runner submits. The command timing out,
// losing its connection or exiting leaves it pending, and another command picks it up by its key.
func TestProgramCustomToolWaitsForItsResult(t *testing.T) {
	f := newProgramFixture(t)
	second := programFixtureFor(t, f.executionFixture, f.agent, f.lease)
	before := f.fingerprint(t)
	tool := f.createCustom(t, "ticket")
	require.True(t, tool.Created)

	updates := f.authorizeCustom(t, tool)
	require.Equal(t, []notifications.ToolCallUpdatedCommitted{{AgentID: f.agent, ToolCallID: tool.ID,
		ParentToolCallID: f.parent, State: "ready", Name: "create_ticket",
		Input: notifications.ToolCallUpdateInput(json.RawMessage(customProgramInput))}}, updates,
		"the ready update carries what the runner needs")
	state, _ := programState(t, f, tool.ID)
	require.Equal(t, "ready", state)
	var claimed bool
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT program_claim_id IS NOT NULL FROM tool_calls WHERE id=$1`, tool.ID).Scan(&claimed))
	require.False(t, claimed, "nothing on the server holds a claim on a call its runner completes")
	_, found := f.claim(t, tool.ID, uuid.New())
	require.False(t, found, "a server never runs a custom call")

	// The command stops waiting, by key or by the call it was serving.
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		require.NoError(t, h.CancelProgramToolByKey(t.Context(), f.process, programKey("ticket")))
		over, err := h.CancelProgramTool(t.Context(),
			agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process})
		require.NoError(t, err)
		require.True(t, over, "the waiter is released")
	})
	state, _ = programState(t, f, tool.ID)
	require.Equal(t, "ready", state, "a command that stops waiting leaves the call pending")

	// The command exits; the call is not reaped, even past a named call's lifetime.
	endProgramProcess(t, f)
	defer agentexecution.SetProgramToolNamedLifetime(0)()
	cell := agentexecution.NewCell(f.pool, nil, nil)
	reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 10)
	require.NoError(t, err)
	require.Empty(t, reapable)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), tool.ID)
		require.NoError(t, err)
		require.False(t, reaped)
	})

	picked, found := second.resumeCustom(t, "ticket")
	require.True(t, found, "another command picks the pending call up by its key")
	require.Equal(t, []any{tool.ID, f.process, "ready"}, []any{picked.ID, picked.ProcessID, picked.State})

	second.try(t, func(h *agentexecution.Handle) {
		_, err := h.CompleteProgramCustomTool(t.Context(), tool.ID, "denied", nil)
		require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict, "a runner reports success or failure")
	})
	result := json.RawMessage(`{"ticket":"T-1"}`)
	f.tx(t, func(u *agentexecution.Unit, h *agentexecution.Handle) {
		completed, err := h.CompleteProgramCustomTool(t.Context(), tool.ID, "succeeded",
			[]agentexecution.Content{{Kind: "structured_data", Data: result}})
		require.NoError(t, err)
		require.Equal(t, "completed", completed.State)
		require.Equal(t, []notifications.ToolCallUpdatedCommitted{{AgentID: f.agent, ToolCallID: tool.ID,
			ParentToolCallID: f.parent, State: "completed"}}, u.Notifications().ToolCallUpdates(),
			"the waiting command wakes on the completion")
	})
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CompleteProgramCustomTool(t.Context(), tool.ID, "succeeded", nil)
		require.ErrorIs(t, err, storeerr.ErrToolCallCompleted)
	})
	state, outcome := programState(t, f, tool.ID)
	require.Equal(t, []string{"completed", "succeeded"}, []string{state, outcome})
	data, events := programResult(t, f, tool.ID)
	require.JSONEq(t, string(result), data)
	require.Zero(t, events, "the result goes to the command, not the agent's events")

	picked, found = second.resumeCustom(t, "ticket")
	require.True(t, found, "the key keeps returning the submitted result")
	require.Equal(t, []any{tool.ID, "completed"}, []any{picked.ID, picked.State})
	require.Equal(t, before, f.fingerprint(t), "custom calls from commands never touch the agent's turn")
}

func TestProgramCustomToolResultChecksTheCall(t *testing.T) {
	f := newProgramFixture(t)
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("bogus"), Name: "create_ticket", Type: "bogus",
			Input: json.RawMessage(`{}`)})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	})
	builtIn := f.create(t, "built-in")
	pending := f.createCustom(t, "unauthorized")
	for _, id := range []uuid.UUID{builtIn.ID, pending.ID} {
		f.try(t, func(h *agentexecution.Handle) {
			_, err := h.CompleteProgramCustomTool(t.Context(), id, "succeeded", nil)
			require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict,
				"only an authorized custom call takes a submitted result")
		})
	}
	// Before authorization a custom call is ended with its command, like any other unnamed call.
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		require.NoError(t, h.CancelProgramToolByKey(t.Context(), f.process, programKey("unauthorized")))
	})
	state, outcome := programState(t, f, pending.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
}

func TestProgramCustomToolPermission(t *testing.T) {
	for _, mode := range []string{"allow", "deny"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramFixture(t)
			before := f.fingerprint(t)
			tool := f.createCustom(t, "ticket")
			claim := uuid.New()
			_, found := f.claim(t, tool.ID, claim)
			require.True(t, found)
			var opened agentexecution.Interaction
			f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
				var err error
				opened, err = h.OpenProgramPermission(t.Context(),
					agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: claim},
					programPermission(t), nil)
				require.NoError(t, err)
			})
			state, _ := programState(t, f, tool.ID)
			require.Equal(t, "awaiting_permission", state)
			option := toolpermission.AllowOptionIndex
			if mode == "deny" {
				option = toolpermission.DenyOptionIndex
			}
			var updates []notifications.ToolCallUpdatedCommitted
			f.tx(t, func(u *agentexecution.Unit, h *agentexecution.Handle) {
				_, err := h.ResolveInteraction(t.Context(), agentexecution.ResolveInteractionInput{ID: opened.ID,
					Resolution: interactionform.Resolution{Answers: []interactionform.Answer{
						{OptionIndices: []int{option}}}}})
				require.NoError(t, err)
				updates = u.Notifications().ToolCallUpdates()
			})
			require.Len(t, updates, 1)
			state, outcome := programState(t, f, tool.ID)
			if mode == "deny" {
				require.Equal(t, []string{"completed", "denied"}, []string{state, outcome})
				require.Equal(t, "completed", updates[0].State)
			} else {
				require.Equal(t, "ready", state, "an approved custom call waits for its result")
				require.Equal(t, "create_ticket", updates[0].Name)
				require.Equal(t, f.parent, updates[0].ParentToolCallID)
				require.NotNil(t, updates[0].InteractionUpdate)
				_, found = f.claim(t, tool.ID, uuid.New())
				require.False(t, found, "approval does not hand a custom call to a server")
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					_, err := h.CompleteProgramCustomTool(t.Context(), tool.ID, "failed",
						[]agentexecution.Content{{Kind: "text", Text: "no tracker"}})
					require.NoError(t, err)
				})
				state, outcome = programState(t, f, tool.ID)
				require.Equal(t, []string{"completed", "failed"}, []string{state, outcome})
			}
			require.Equal(t, before, f.fingerprint(t))
		})
	}
}

// Canceling or archiving the agent ends its pending custom calls. Their runners may already have
// acted, so the key keeps returning outcome_unknown instead of calling the tool again.
func TestProgramCustomToolSettledByItsAgent(t *testing.T) {
	for _, mode := range []string{"cancel", "archive"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramFixture(t)
			tool := f.createCustom(t, "ticket")
			f.authorizeCustom(t, tool)
			asking := f.createCustom(t, "asking")
			claim := uuid.New()
			_, found := f.claim(t, asking.ID, claim)
			require.True(t, found)
			f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
				_, err := h.OpenProgramPermission(t.Context(),
					agentexecution.ProgramToolRef{ID: asking.ID, ProcessID: f.process, ClaimID: claim},
					programPermission(t), nil)
				require.NoError(t, err)
			})
			reason := "The agent was canceled before this call's result was submitted."
			if mode == "cancel" {
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					_, err := h.Cancel(t.Context(), agentexecution.CancelInput{Reason: "agent_canceled",
						Message: "canceled"})
					require.NoError(t, err)
				})
				state, _ := programState(t, f, asking.ID)
				require.Equal(t, "awaiting_permission", state, "an approval keeps waiting as before")
			} else {
				reason = "The agent is no longer active."
				_, err := f.pool.Exec(t.Context(), `UPDATE agents SET state='archived',archived_at=now() WHERE id=$1`,
					f.agent)
				require.NoError(t, err)
				cell := agentexecution.NewCell(f.pool, nil, nil)
				reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 10)
				require.NoError(t, err)
				require.Len(t, reapable, 2)
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					reaped, err := h.ReapProgramTool(t.Context(), tool.ID)
					require.NoError(t, err)
					require.True(t, reaped)
				})
			}
			state, outcome := programState(t, f, tool.ID)
			require.Equal(t, []string{"completed", "failed"}, []string{state, outcome})
			data, _ := programResult(t, f, tool.ID)
			var body map[string]string
			require.NoError(t, json.Unmarshal([]byte(data), &body))
			require.Equal(t, "outcome_unknown", body["error_code"])
			require.Contains(t, body["message"], reason)
			f.try(t, func(h *agentexecution.Handle) {
				_, err := h.CompleteProgramCustomTool(t.Context(), tool.ID, "succeeded", nil)
				require.ErrorIs(t, err, storeerr.ErrToolCallCompleted, "a late result is refused")
			})
		})
	}
}

// Pending custom calls outlive their commands, so they count against every command's open calls.
func TestProgramCustomToolsCountAgainstLaterCommands(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.pool.Exec(t.Context(), `INSERT INTO tool_calls(id,project_id,agent_id,provider_call_id,name,input,
 type,state,caller,parent_tool_call_id,created_at)
 SELECT gen_random_uuid(),c.project_id,c.agent_id,'pending-'||g,'create_ticket','{}'::jsonb,'custom',
 'awaiting_authorization','program',c.id,statement_timestamp()
 FROM tool_calls c, generate_series(1,$2::int) g WHERE c.id=$1`, f.parent, agentexecution.MaxOpenProgramTools)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE tool_calls SET state='ready'
 WHERE agent_id=$1 AND caller='program' AND type='custom'`, f.agent)
	require.NoError(t, err)
	endProgramProcess(t, f)
	second := programFixtureFor(t, f.executionFixture, f.agent, f.lease)
	second.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: second.process, Key: programKey("next"), Name: "storage_read", Type: "built_in",
			Input: json.RawMessage(`{}`)})
		require.ErrorIs(t, err, storeerr.ErrProgramToolCallLimitReached)
	})
}
