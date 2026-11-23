//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type programFixture struct {
	executionFixture
	agent, lease, parent, process uuid.UUID
}

func newProgramFixture(t *testing.T) programFixture {
	t.Helper()
	f, a, lease := commandFixture(t)
	return programFixtureFor(t, f, a, lease)
}

func programFixtureFor(t *testing.T, f executionFixture, a, lease uuid.UUID) programFixture {
	t.Helper()
	u, h := f.handle(t, a)
	ref := toolCommand(t, h, lease, "built_in")
	_, err := h.AuthorizeTool(t.Context(), ref)
	require.NoError(t, err)
	process := processOwner(t, f, u, a, lease, ref.ID, "running")
	_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref, Outcome: "succeeded",
		Content: []agentexecution.Content{{Kind: "text", Text: "started"}}})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "program fixture"))
	return programFixture{executionFixture: f, agent: a, lease: lease, parent: ref.ID, process: process}
}

func (f programFixture) tx(t *testing.T, run func(*agentexecution.Unit, *agentexecution.Handle)) {
	t.Helper()
	u, h := f.handle(t, f.agent)
	run(u, h)
	require.NoError(t, u.Commit(t.Context(), "program test"))
}

func (f programFixture) try(t *testing.T, run func(*agentexecution.Handle)) {
	t.Helper()
	u, h := f.handle(t, f.agent)
	run(h)
	require.NoError(t, u.Rollback(t.Context()))
}

func (f programFixture) create(t *testing.T, key string) agentexecution.ProgramTool {
	t.Helper()
	var tool agentexecution.ProgramTool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey(key), Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{"path":"a"}`)})
		require.NoError(t, err)
	})
	return tool
}

func (f programFixture) claim(t *testing.T, id, claim uuid.UUID) (agentexecution.ProgramToolPhase, bool) {
	t.Helper()
	var phase agentexecution.ProgramToolPhase
	var found bool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		_, phase, found, err = h.ClaimProgramTool(t.Context(),
			agentexecution.ProgramToolRef{ID: id, ProcessID: f.process, ClaimID: claim}, time.Minute)
		require.NoError(t, err)
	})
	return phase, found
}

func (f programFixture) fingerprint(t *testing.T) string {
	t.Helper()
	var fingerprint string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT concat_ws('|',
 (SELECT to_jsonb(h)::text FROM agent_execution_state h WHERE agent_id=$1),
 (SELECT next_event_sequence::text FROM agents WHERE id=$1),
 (SELECT count(*)::text FROM agent_wakeups WHERE agent_id=$1),
 (SELECT count(*)::text FROM agent_inputs WHERE agent_id=$1),
 (SELECT count(*)::text FROM agent_turns WHERE agent_id=$1))`, f.agent).Scan(&fingerprint))
	u, h := f.handle(t, f.agent)
	reloaded, err := agentexecution.ReloadExecution(t.Context(), h)
	require.NoError(t, err)
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	q := dbsqlc.New(u.DB())
	groups, err := q.ListCompactionAtomicGroups(t.Context(), dbsqlc.ListCompactionAtomicGroupsParams{
		ProjectID: f.project, AgentID: f.agent, InputEventSequence: 1_000_000_000_000})
	require.NoError(t, err)
	var output uuid.UUID
	require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT model_output_id FROM tool_calls WHERE id=$1`,
		f.parent).Scan(&output))
	_, runnableErr := q.NextRunnableToolCallForModelOutput(t.Context(), dbsqlc.NextRunnableToolCallForModelOutputParams{
		ProjectID: f.project, AgentID: f.agent, ModelOutputID: output})
	completed, err := q.ListCompletedToolCallsAtWatermark(t.Context(), dbsqlc.ListCompletedToolCallsAtWatermarkParams{
		ProjectID: f.project, AgentID: f.agent, MaxEventSequence: 1_000_000_000_000})
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(completed))
	for i, call := range completed {
		ids[i] = call.ID
	}
	var sequence int64
	require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT sequence FROM agent_events WHERE model_output_id=$1`,
		output).Scan(&sequence))
	limit, err := q.IsOutputLimitBoundary(t.Context(), dbsqlc.IsOutputLimitBoundaryParams{
		ProjectID: f.project, AgentID: f.agent, EventSequence: sequence})
	require.NoError(t, err)
	encoded, err := json.Marshal([]any{reloaded.View, reloaded.Head, rebuilt.Head, groups,
		errors.Is(runnableErr, pgx.ErrNoRows), ids, limit})
	require.NoError(t, err)
	require.NoError(t, u.Rollback(t.Context()))
	return fingerprint + "|" + string(encoded)
}

func programPermission(t *testing.T) toolpermission.Request {
	t.Helper()
	form, err := toolpermission.NewAllowDenyForm("Permission", nil)
	require.NoError(t, err)
	return toolpermission.Request{
		Permission:    toolpermission.Selection{Mode: toolpermission.ModeAlwaysAsk},
		Authorization: toolpermission.Authorization{ToolName: "storage_read", Input: json.RawMessage(`{"path":"a"}`)},
		Form:          form,
	}
}

func programState(t *testing.T, f programFixture, id uuid.UUID) (string, string) {
	t.Helper()
	var state, outcome string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT c.state,coalesce(r.outcome,'') FROM tool_calls c
 LEFT JOIN tool_call_results r ON r.agent_id=c.agent_id AND r.tool_call_id=c.id WHERE c.id=$1`, id).
		Scan(&state, &outcome))
	return state, outcome
}

type statement struct {
	sql  string
	args []any
}

func stmt(sql string, args ...any) statement { return statement{sql: sql, args: args} }

func sqlState(t *testing.T, u pgx.Tx, statements ...statement) string {
	t.Helper()
	ctx := t.Context()
	_, err := u.Exec(ctx, "SAVEPOINT program_check")
	require.NoError(t, err)
	for i, s := range statements {
		_, err = u.Exec(ctx, s.sql, s.args...)
		if i < len(statements)-1 {
			require.NoError(t, err, s.sql)
		}
	}
	if err == nil {
		_, err = u.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE")
	}
	_, rollbackErr := u.Exec(ctx, "ROLLBACK TO SAVEPOINT program_check")
	require.NoError(t, rollbackErr)
	_, setErr := u.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED")
	require.NoError(t, setErr)
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		return postgres.Code
	}
	require.NoError(t, err)
	return ""
}

func TestProgramToolSchema(t *testing.T) {
	f := newProgramFixture(t)
	child := f.create(t, "k1")
	u, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	var output uuid.UUID
	require.NoError(t, u.QueryRow(t.Context(), `SELECT model_output_id FROM tool_calls WHERE id=$1`,
		f.parent).Scan(&output))
	insert := `INSERT INTO tool_calls(project_id,agent_id,model_output_id,provider_call_id,name,input,type,state,caller,
 parent_tool_call_id,created_at) SELECT project_id,agent_id,$5::uuid,$2,'storage_read','{}','built_in',
 'awaiting_authorization',$3,$4,statement_timestamp() FROM tool_calls WHERE id=$1`
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p1", "program", f.parent, output)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p2", "program", nil, nil)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p3", "model", f.parent, output)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p4", "program", uuid.New(), nil)))
	require.Equal(t, "23505", sqlState(t, u, stmt(insert, f.parent, programKey("k1"), "program", f.parent, nil)))
	require.Empty(t, sqlState(t, u, stmt(insert, f.parent, "k2", "program", f.parent, nil)))
	require.Equal(t, "25006", sqlState(t, u, stmt(`UPDATE tool_calls SET caller='model' WHERE id=$1`, child.ID)))
	require.Equal(t, "25006", sqlState(t, u, stmt(`UPDATE tool_calls SET provider_call_id='other' WHERE id=$1`, child.ID)))
	ready := stmt(`UPDATE tool_calls SET state='ready' WHERE id=$1`, child.ID)
	require.Equal(t, "23514", sqlState(t, u, ready, stmt(`UPDATE tool_calls SET state='waiting' WHERE id=$1`, child.ID)))
	require.Equal(t, "23514", sqlState(t, u, ready,
		stmt(`UPDATE tool_calls SET state='running',runtime_lock_id=$2 WHERE id=$1`, child.ID, f.lease)))
	require.Equal(t, "23514", sqlState(t, u, stmt(`UPDATE tool_calls SET state='ready',program_claim_id=$2,
 program_claim_expires_at=statement_timestamp() WHERE id=$1`, child.ID, uuid.New())))
	require.Empty(t, sqlState(t, u, ready, stmt(`UPDATE tool_calls SET state='waiting',program_claim_id=$2,
 program_claim_expires_at=statement_timestamp() WHERE id=$1`, child.ID, uuid.New())))
	require.Equal(t, "23514", sqlState(t, u, stmt(`INSERT INTO processes(org_id,project_id,agent_id,tool_call_id,
 runtime_lock_id,agent_machine_binding_id,machine_id,execution_spec,state,execution_granted_at,source_started_at,
 state_changed_at,created_at,updated_at,last_activity_at) SELECT org_id,project_id,agent_id,$2,runtime_lock_id,
 agent_machine_binding_id,machine_id,execution_spec,state,execution_granted_at,source_started_at,state_changed_at,
 created_at,updated_at,last_activity_at FROM processes WHERE id=$1`, f.process, child.ID),
		stmt(`INSERT INTO tool_calls(agent_id,provider_call_id,name,input,type,state,caller,parent_tool_call_id,
 created_at) SELECT agent_id,'p6','storage_read','{}','built_in','awaiting_authorization','program',id,
 statement_timestamp() FROM tool_calls WHERE id=$1`, child.ID)))
	require.NoError(t, u.Rollback(t.Context()))
}

func TestProgramToolCreateIsIdempotent(t *testing.T) {
	f := newProgramFixture(t)
	before := f.fingerprint(t)
	created := f.create(t, "key")
	require.True(t, created.Created)
	require.Equal(t, "awaiting_authorization", created.State)
	require.Equal(t, f.parent, created.ParentID)
	replay := f.create(t, "key")
	require.False(t, replay.Created)
	require.Equal(t, created.ID, replay.ID)
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("key"), Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{"path":"b"}`)})
		require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	})
	var provider, caller string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT provider_call_id,caller FROM tool_calls WHERE id=$1`,
		created.ID).Scan(&provider, &caller))
	require.Equal(t, programKey("key"), provider)
	require.Equal(t, "program", caller)
	require.Equal(t, before, f.fingerprint(t))
}

func TestProgramToolFlows(t *testing.T) {
	for _, mode := range []string{"allow", "ask_allow", "ask_deny", "deny"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramFixture(t)
			before := f.fingerprint(t)
			tool := f.create(t, "key")
			claim := uuid.New()
			phase, found := f.claim(t, tool.ID, claim)
			require.True(t, found)
			require.Equal(t, agentexecution.ProgramToolAuthorize, phase)
			_, found = f.claim(t, tool.ID, uuid.New())
			require.False(t, found, "a live authorization lease is exclusive")
			ref := agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: claim}
			switch mode {
			case "allow":
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					changed, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
					require.NoError(t, err)
					require.True(t, changed)
				})
			case "deny":
				f.try(t, func(h *agentexecution.Handle) {
					_, err := h.CompleteProgramTool(t.Context(), ref, "succeeded", nil)
					require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
				})
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					_, err := h.CompleteProgramTool(t.Context(), ref, "denied",
						[]agentexecution.Content{{Kind: "structured_data", Data: json.RawMessage(`{"reason":"no"}`)}})
					require.NoError(t, err)
				})
			default:
				var opened agentexecution.Interaction
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					var err error
					opened, err = h.OpenProgramPermission(t.Context(), ref, programPermission(t), nil)
					require.NoError(t, err)
					replay, err := h.OpenProgramPermission(t.Context(), ref, programPermission(t), nil)
					require.NoError(t, err)
					require.Equal(t, opened.ID, replay.ID)
				})
				state, _ := programState(t, f, tool.ID)
				require.Equal(t, "awaiting_permission", state)
				require.Equal(t, before, f.fingerprint(t))
				actor := uuid.New()
				_, err := f.pool.Exec(t.Context(), `INSERT INTO actors(id,project_id,provider,provider_user_id,
 created_at,updated_at) VALUES($1,$2,'external','user',now(),now())`, actor, f.project)
				require.NoError(t, err)
				option := toolpermission.AllowOptionIndex
				if mode == "ask_deny" {
					option = toolpermission.DenyOptionIndex
				}
				input := agentexecution.ResolveInteractionInput{ID: opened.ID, ActorID: actor,
					Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{option}}}}}
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					resolved, err := h.ResolveInteraction(t.Context(), input)
					require.NoError(t, err)
					require.Equal(t, "resolved", resolved.State)
					require.Equal(t, uuid.Nil, resolved.InputID)
				})
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					replay, err := h.ResolveInteraction(t.Context(), input)
					require.NoError(t, err)
					require.Equal(t, actor, replay.ActorID)
				})
				f.try(t, func(h *agentexecution.Handle) {
					other := input
					other.ActorID = uuid.Nil
					_, err := h.ResolveInteraction(t.Context(), other)
					require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				})
				var interactionState string
				var inputID, actorID *uuid.UUID
				require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state,resolved_by_input_id,resolved_by_actor_id
 FROM agent_interactions WHERE id=$1`, opened.ID).Scan(&interactionState, &inputID, &actorID))
				require.Equal(t, "resolved", interactionState)
				require.Nil(t, inputID)
				require.Equal(t, actor, *actorID)
			}
			state, outcome := programState(t, f, tool.ID)
			if mode == "deny" || mode == "ask_deny" {
				require.Equal(t, "completed", state)
				require.Equal(t, "denied", outcome)
			} else {
				if mode == "allow" {
					require.Equal(t, "waiting", state)
				} else {
					require.Equal(t, "ready", state)
					execute := uuid.New()
					phase, found = f.claim(t, tool.ID, execute)
					require.True(t, found)
					require.Equal(t, agentexecution.ProgramToolExecute, phase)
					ref.ClaimID = execute
				}
				_, found = f.claim(t, tool.ID, uuid.New())
				require.False(t, found)
				f.try(t, func(h *agentexecution.Handle) {
					_, err := h.CompleteProgramTool(t.Context(),
						agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: uuid.New()}, "succeeded", nil)
					require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
				})
				f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
					dispatch, err := h.DispatchProgramTool(t.Context(), ref)
					require.NoError(t, err)
					require.Equal(t, agentexecution.ToolDispatchReady, dispatch)
					completed, err := h.CompleteProgramTool(t.Context(), ref, "succeeded", []agentexecution.Content{
						{Kind: "text", Text: "content"}, {Kind: "structured_data", Data: json.RawMessage(`{"ok":true}`)}})
					require.NoError(t, err)
					require.Equal(t, "completed", completed.State)
					replay, err := h.CompleteProgramTool(t.Context(), ref, "succeeded", nil)
					require.NoError(t, err)
					require.Equal(t, completed.ResultID, replay.ResultID)
				})
				f.try(t, func(h *agentexecution.Handle) {
					_, err := h.CompleteProgramTool(t.Context(), ref, "failed", nil)
					require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
				})
				state, outcome = programState(t, f, tool.ID)
				require.Equal(t, "completed", state)
				require.Equal(t, "succeeded", outcome)
			}
			var blocks, included, events int
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(b.id),
				count(b.id) FILTER (WHERE NOT b.exclude_from_model_context),
 (SELECT count(*) FROM agent_events e WHERE e.tool_call_result_id=r.id)
 FROM tool_call_results r LEFT JOIN content_blocks b ON b.owner_tool_call_result_id=r.id
 WHERE r.tool_call_id=$1 GROUP BY r.id`, tool.ID).Scan(&blocks, &included, &events))
			require.Positive(t, blocks)
			require.Zero(t, included)
			require.Zero(t, events)
			require.Equal(t, before, f.fingerprint(t))
		})
	}
}

func TestProgramToolProcessFence(t *testing.T) {
	f := newProgramFixture(t)
	tool := f.create(t, "key")
	var interaction agentexecution.Interaction
	claim := uuid.New()
	_, found := f.claim(t, tool.ID, claim)
	require.True(t, found)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		interaction, err = h.OpenProgramPermission(t.Context(),
			agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: claim}, programPermission(t), nil)
		require.NoError(t, err)
		reaped, err := h.ReapProgramTool(t.Context(), tool.ID)
		require.NoError(t, err)
		require.False(t, reaped, "live calls are not reaped")
	})
	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
	before := f.fingerprint(t)
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("late"), Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{}`)})
		require.ErrorIs(t, err, storeerr.ErrProcessTerminal)
	})
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.ResolveInteraction(t.Context(), agentexecution.ResolveInteractionInput{ID: interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{
				{OptionIndices: []int{toolpermission.AllowOptionIndex}}}}})
		require.ErrorIs(t, err, storeerr.ErrProcessTerminal)
	})
	cell := agentexecution.NewCell(f.pool, nil, nil)
	reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 10)
	require.NoError(t, err)
	require.Len(t, reapable, 1)
	require.False(t, reapable[0].CreatedAt.IsZero())
	require.Equal(t, agentexecution.ReapableProgramTool{ID: tool.ID, ProjectID: f.project, AgentID: f.agent,
		CreatedAt: reapable[0].CreatedAt}, reapable[0])
	behind, err := cell.ListProgramToolReapable(t.Context(), reapable[0], 10)
	require.NoError(t, err)
	require.Empty(t, behind)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), tool.ID)
		require.NoError(t, err)
		require.True(t, reaped)
	})
	state, outcome := programState(t, f, tool.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	var interactionState string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state FROM agent_interactions WHERE id=$1`,
		interaction.ID).Scan(&interactionState))
	require.Equal(t, "canceled", interactionState)
	require.Equal(t, before, f.fingerprint(t))
}

func TestProgramToolClaimLeases(t *testing.T) {
	f := newProgramFixture(t)
	tool := f.create(t, "key")
	cell := agentexecution.NewCell(f.pool, nil, nil)
	first := uuid.New()
	_, found := f.claim(t, tool.ID, first)
	require.True(t, found)
	_, found = f.claim(t, tool.ID, uuid.New())
	require.False(t, found)
	ref := agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: first}
	err := cell.RenewProgramTool(t.Context(), f.agent, ref, time.Hour)
	require.NoError(t, err)
	expire := func() {
		_, err := f.pool.Exec(t.Context(), `UPDATE tool_calls
			SET program_claim_expires_at=statement_timestamp()-interval '1 second'
 WHERE id=$1`, tool.ID)
		require.NoError(t, err)
	}
	expire()
	err = cell.RenewProgramTool(t.Context(), f.agent, ref, time.Hour)
	require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
	second := uuid.New()
	phase, found := f.claim(t, tool.ID, second)
	require.True(t, found)
	require.Equal(t, agentexecution.ProgramToolAuthorize, phase)
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
	})
	ref.ClaimID = second
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.NoError(t, err)
	})
	execute := second
	_, found = f.claim(t, tool.ID, uuid.New())
	require.False(t, found)
	ref.ClaimID = uuid.New()
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.DispatchProgramTool(t.Context(), ref)
		require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
	})
	f.try(t, func(h *agentexecution.Handle) {
		require.ErrorIs(t, h.ReleaseProgramTool(t.Context(), ref), storeerr.ErrProgramToolCallClaimLost)
	})
	ref.ClaimID = execute
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		require.NoError(t, h.ReleaseProgramTool(t.Context(), ref))
	})
	state, _ := programState(t, f, tool.ID)
	require.Equal(t, "ready", state)
	ref.ClaimID = uuid.New()
	phase, found = f.claim(t, tool.ID, ref.ClaimID)
	require.True(t, found)
	require.Equal(t, agentexecution.ProgramToolExecute, phase)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.CompleteProgramTool(t.Context(), ref, "succeeded", nil)
		require.NoError(t, err)
	})

	builtIn := f.create(t, "built-in-lost")
	builtInRef := agentexecution.ProgramToolRef{ID: builtIn.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found = f.claim(t, builtIn.ID, builtInRef.ClaimID)
	require.True(t, found)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), builtInRef, time.Minute)
		require.NoError(t, err)
	})
	requireProgramToolLostByServer(t, f, builtIn.ID)

	var mcp agentexecution.ProgramTool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		mcp, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{ProcessID: f.process,
			Key: programKey("mcp"), Name: "mcp__server__tool", Type: "mcp", Input: json.RawMessage(`{}`)})
		require.NoError(t, err)
	})
	mcpRef := agentexecution.ProgramToolRef{ID: mcp.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found = f.claim(t, mcp.ID, mcpRef.ClaimID)
	require.True(t, found)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), mcpRef, time.Minute)
		require.NoError(t, err)
	})
	requireProgramToolLostByServer(t, f, mcp.ID)
}

func requireProgramToolLostByServer(t *testing.T, f programFixture, id uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `UPDATE tool_calls
		SET program_claim_expires_at=statement_timestamp()-interval '1 second' WHERE id=$1`, id)
	require.NoError(t, err)
	_, found := f.claim(t, id, uuid.New())
	require.False(t, found, "a call that had started is not run again")
	cell := agentexecution.NewCell(f.pool, nil, nil)
	reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 100)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(reapable, func(tool agentexecution.ReapableProgramTool) bool {
		return tool.ID == id
	}), "the reaper finds a call whose server stopped running it")
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), id)
		require.NoError(t, err)
		require.True(t, reaped)
	})
	state, outcome := programState(t, f, id)
	require.Equal(t, []string{"completed", "failed"}, []string{state, outcome})
	var code string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT b.structured_data->>'error_code' FROM content_blocks b
 JOIN tool_call_results r ON r.id=b.owner_tool_call_result_id WHERE r.tool_call_id=$1`, id).Scan(&code))
	require.Equal(t, "outcome_unknown", code, "a call that had started may have taken effect")
}

func TestProgramToolCreateLimitsOpenCallsPerCommand(t *testing.T) {
	f := newProgramFixture(t)
	first := f.create(t, "key-0")
	for i := 1; i < agentexecution.MaxOpenProgramTools; i++ {
		f.create(t, fmt.Sprintf("key-%d", i))
	}
	create := func(h *agentexecution.Handle, key string) (agentexecution.ProgramTool, error) {
		return h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{ProcessID: f.process,
			Key: programKey(key), Name: "storage_read", Type: "built_in", Input: json.RawMessage(`{"path":"a"}`)})
	}
	f.try(t, func(h *agentexecution.Handle) {
		_, err := create(h, "one-too-many")
		require.ErrorIs(t, err, storeerr.ErrProgramToolCallLimitReached)
	})
	replay := f.create(t, "key-0")
	require.False(t, replay.Created)
	require.Equal(t, first.ID, replay.ID)

	claim := uuid.New()
	_, found := f.claim(t, first.ID, claim)
	require.True(t, found)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.CompleteProgramTool(t.Context(),
			agentexecution.ProgramToolRef{ID: first.ID, ProcessID: f.process, ClaimID: claim}, "failed", nil)
		require.NoError(t, err)
	})
	require.True(t, f.create(t, "one-too-many").Created)
}

func TestProgramToolsIgnoredByTurnControl(t *testing.T) {
	for _, mode := range []string{"supersede", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newProgramFixture(t)
			tool := f.create(t, "key")
			claim := uuid.New()
			_, found := f.claim(t, tool.ID, claim)
			require.True(t, found)
			var opened agentexecution.Interaction
			f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
				var err error
				opened, err = h.OpenProgramPermission(t.Context(),
					agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: claim}, programPermission(t), nil)
				require.NoError(t, err)
			})
			f.tx(t, func(u *agentexecution.Unit, h *agentexecution.Handle) {
				if mode == "supersede" {
					received := receiveCommand(t, h, "steering", "supersede")
					ids, err := h.SupersedeInteractions(t.Context(), received.ID)
					require.NoError(t, err)
					require.NotContains(t, ids, opened.ID)
				} else {
					_, err := h.Cancel(t.Context(), agentexecution.CancelInput{Reason: "agent_canceled", Message: "canceled"})
					require.NoError(t, err)
				}
				assertCommandState(t, u, h)
			})
			var interactionState string
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state FROM agent_interactions WHERE id=$1`,
				opened.ID).Scan(&interactionState))
			require.Equal(t, "open", interactionState)
			state, _ := programState(t, f, tool.ID)
			require.Equal(t, "awaiting_permission", state)
		})
	}
}

func programKey(label string) string { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)).String() }

func TestProgramToolKeyResumesAcrossCommands(t *testing.T) {
	f := newProgramFixture(t)
	second := programFixtureFor(t, f.executionFixture, f.agent, f.lease)
	resume := func(h *agentexecution.Handle, path string) (agentexecution.ProgramTool, bool, error) {
		return h.ResumeProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: second.process, Key: programKey("job"), Name: "storage_read",
			Input: json.RawMessage(`{"path":"` + path + `"}`)})
	}
	second.try(t, func(h *agentexecution.Handle) {
		_, found, err := resume(h, "a")
		require.NoError(t, err)
		require.False(t, found, "an unused key names no call")
	})
	first := f.create(t, "job")
	second.try(t, func(h *agentexecution.Handle) {
		tool, found, err := resume(h, "a")
		require.NoError(t, err)
		require.True(t, found, "another running command attaches to the open call")
		require.Equal(t, []uuid.UUID{first.ID, f.process}, []uuid.UUID{tool.ID, tool.ProcessID})
		_, _, err = resume(h, "b")
		require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict, "a key cannot be reused for a different input")
	})

	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, found, err := resume(h, "a")
		require.NoError(t, err)
		require.False(t, found, "a call canceled before it ran can be started again")
	})
	state, outcome := programState(t, f, first.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})

	retry := second.create(t, "job")
	require.True(t, retry.Created)
	require.NotEqual(t, first.ID, retry.ID)
	claim := uuid.New()
	_, found := second.claim(t, retry.ID, claim)
	require.True(t, found)
	ref := agentexecution.ProgramToolRef{ID: retry.ID, ProcessID: second.process, ClaimID: claim}
	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.NoError(t, err)
		_, err = h.CompleteProgramTool(t.Context(), ref, "succeeded",
			[]agentexecution.Content{{Kind: "text", Text: "done"}})
		require.NoError(t, err)
	})
	second.try(t, func(h *agentexecution.Handle) {
		tool, found, err := resume(h, "a")
		require.NoError(t, err)
		require.True(t, found, "a finished call keeps returning its result")
		require.Equal(t, []any{retry.ID, "completed"}, []any{tool.ID, tool.State})
	})
	replay := second.create(t, "job")
	require.False(t, replay.Created)
	require.Equal(t, retry.ID, replay.ID)
}

func TestProgramToolNamedCallOutlivesItsCommand(t *testing.T) {
	f := newProgramFixture(t)
	second := programFixtureFor(t, f.executionFixture, f.agent, f.lease)
	named := func(owner programFixture, key, toolType string) agentexecution.ProgramTool {
		t.Helper()
		var tool agentexecution.ProgramTool
		owner.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
			var err error
			tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
				ProcessID: owner.process, Key: programKey(key), Named: true, Name: "storage_read", Type: toolType,
				Input: json.RawMessage(`{"path":"a"}`)})
			require.NoError(t, err)
		})
		return tool
	}
	resume := func(h *agentexecution.Handle, key string) (agentexecution.ProgramTool, bool) {
		t.Helper()
		tool, found, err := h.ResumeProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: second.process, Key: programKey(key), Named: true, Name: "storage_read",
			Input: json.RawMessage(`{"path":"a"}`)})
		require.NoError(t, err)
		return tool, found
	}

	running := named(f, "running", "mcp")
	runClaim := uuid.New()
	runRef := agentexecution.ProgramToolRef{ID: running.ID, ProcessID: f.process, ClaimID: runClaim}
	_, found := f.claim(t, running.ID, runClaim)
	require.True(t, found)
	asking := named(f, "asking", "built_in")
	askClaim := uuid.New()
	_, found = f.claim(t, asking.ID, askClaim)
	require.True(t, found)
	idle := named(f, "idle", "built_in")
	var interaction agentexecution.Interaction
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		started, err := h.AuthorizeProgramTool(t.Context(), runRef, time.Minute)
		require.NoError(t, err)
		require.True(t, started)
		interaction, err = h.OpenProgramPermission(t.Context(),
			agentexecution.ProgramToolRef{ID: asking.ID, ProcessID: f.process, ClaimID: askClaim},
			programPermission(t), nil)
		require.NoError(t, err)
	})

	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("late"), Named: true, Name: "storage_read", Type: "built_in",
			Input: json.RawMessage(`{}`)})
		require.ErrorIs(t, err, storeerr.ErrProcessTerminal, "an ended command makes no new calls")
	})
	cell := agentexecution.NewCell(f.pool, nil, nil)
	reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 10)
	require.NoError(t, err)
	require.Empty(t, reapable, "named calls are not reaped with their command")
	require.NoError(t, cell.RenewProgramTool(t.Context(), f.agent, runRef, time.Minute),
		"the server running a named call keeps its claim after the command ends")

	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		tool, found := resume(h, "running")
		require.True(t, found)
		require.Equal(t, []any{running.ID, f.process, "waiting"}, []any{tool.ID, tool.ProcessID, tool.State})
	})
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.CompleteProgramTool(t.Context(), runRef, "succeeded",
			[]agentexecution.Content{{Kind: "text", Text: "done"}})
		require.NoError(t, err, "the original execution still records its result")
	})
	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		tool, found := resume(h, "running")
		require.True(t, found)
		require.Equal(t, []any{running.ID, "completed"}, []any{tool.ID, tool.State})
	})
	state, outcome := programState(t, f, running.ID)
	require.Equal(t, []string{"completed", "succeeded"}, []string{state, outcome})

	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		tool, found := resume(h, "asking")
		require.True(t, found)
		require.Equal(t, []any{asking.ID, "awaiting_permission"}, []any{tool.ID, tool.State})
		_, err := h.ResolveInteraction(t.Context(), agentexecution.ResolveInteractionInput{ID: interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{
				{OptionIndices: []int{toolpermission.AllowOptionIndex}}}}})
		require.NoError(t, err)
	})
	var interactions int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agent_interactions WHERE agent_id=$1`, f.agent).Scan(&interactions))
	require.Equal(t, 1, interactions, "the rerun did not open another approval")
	state, _ = programState(t, f, asking.ID)
	require.Equal(t, "ready", state)
	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, phase, found, err := h.ClaimProgramTool(t.Context(),
			agentexecution.ProgramToolRef{ID: asking.ID, ProcessID: f.process, ClaimID: uuid.New()}, time.Minute)
		require.NoError(t, err)
		require.True(t, found, "the approved call can be run after its command ended")
		require.Equal(t, agentexecution.ProgramToolExecute, phase)
	})

	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), idle.ID)
		require.NoError(t, err)
		require.False(t, reaped)
	})
	defer agentexecution.SetProgramToolNamedLifetime(0)()
	reapable, err = cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 10)
	require.NoError(t, err)
	require.Len(t, reapable, 1, "a claimed call is left to the server running it")
	require.Equal(t, idle.ID, reapable[0].ID)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), idle.ID)
		require.NoError(t, err)
		require.True(t, reaped)
	})
	state, outcome = programState(t, f, idle.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	retry := named(second, "idle", "built_in")
	require.True(t, retry.Created)
}

func TestProgramToolCancelByKeyEndsOnlyItsOffer(t *testing.T) {
	f := newProgramFixture(t)
	offer := func(offerID string) agentexecution.ProgramTool {
		t.Helper()
		var tool agentexecution.ProgramTool
		f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
			var err error
			tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
				ProcessID: f.process, Key: programKey("job"), Name: "storage_read", Type: "built_in",
				Input: json.RawMessage(`{"path":"a"}`), OfferID: offerID})
			require.NoError(t, err)
		})
		return tool
	}
	cancel := func(offerID string) {
		t.Helper()
		f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
			require.NoError(t, h.CancelProgramToolByKey(t.Context(), f.process, programKey("job"), offerID))
		})
	}

	first := offer("a")
	cancel("a")
	state, outcome := programState(t, f, first.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	retry := offer("b")
	require.True(t, retry.Created)
	cancel("a")
	state, _ = programState(t, f, retry.ID)
	require.Equal(t, "awaiting_authorization", state, "a late cancel of the first attempt must not end the retry")

	again := offer("c")
	require.Equal(t, retry.ID, again.ID)
	require.False(t, again.Created)
	cancel("b")
	state, _ = programState(t, f, retry.ID)
	require.Equal(t, "awaiting_authorization", state)
	cancel("c")
	state, outcome = programState(t, f, retry.ID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})

	unnamed := offer("d")
	cancel("")
	state, _ = programState(t, f, unnamed.ID)
	require.Equal(t, "completed", state)

	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("bad-offer"), Name: "storage_read", Type: "built_in",
			Input: json.RawMessage(`{}`), OfferID: fmt.Sprintf("%0129d", 0)})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	})
}

func TestProgramToolNamedLifetimeUsesDatabaseTime(t *testing.T) {
	f := newProgramFixture(t)
	second := programFixtureFor(t, f.executionFixture, f.agent, f.lease)
	aged := func(key, age string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		require.NoError(t, f.pool.QueryRow(t.Context(), `INSERT INTO tool_calls(project_id,agent_id,provider_call_id,
 name,input,type,state,caller,parent_tool_call_id,program_named,created_at)
 SELECT project_id,agent_id,$2,'storage_read','{"path":"a"}','built_in','awaiting_authorization','program',id,true,
 statement_timestamp()-$3::interval FROM tool_calls WHERE id=$1 RETURNING id`,
			f.parent, programKey(key), age).Scan(&id))
		return id
	}
	young := aged("young", "23 hours 58 minutes")
	old := aged("old", "24 hours 1 second")
	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)

	reapable, err := agentexecution.NewCell(f.pool, nil, nil).ListProgramToolReapable(t.Context(),
		agentexecution.ReapableProgramTool{}, 1000)
	require.NoError(t, err)
	var ids []uuid.UUID
	for _, row := range reapable {
		if row.AgentID == f.agent {
			ids = append(ids, row.ID)
		}
	}
	require.Equal(t, []uuid.UUID{old}, ids, "maintenance ends only the call past its lifetime")
	second.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		resume := func(key string) (agentexecution.ProgramTool, bool) {
			tool, found, err := h.ResumeProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
				ProcessID: second.process, Key: programKey(key), Named: true, Name: "storage_read",
				Input: json.RawMessage(`{"path":"a"}`)})
			require.NoError(t, err)
			return tool, found
		}
		tool, found := resume("young")
		require.True(t, found, "a rerun picks up a call still inside its lifetime")
		require.Equal(t, []any{young, "awaiting_authorization"}, []any{tool.ID, tool.State})
		_, found = resume("old")
		require.False(t, found, "a rerun agrees with maintenance that the old call has expired")
		reaped, err := h.ReapProgramTool(t.Context(), young)
		require.NoError(t, err)
		require.False(t, reaped)
	})
	state, outcome := programState(t, f, young)
	require.Equal(t, []string{"awaiting_authorization", ""}, []string{state, outcome})
	state, outcome = programState(t, f, old)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	var code string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT b.structured_data->>'error_code' FROM content_blocks b
 JOIN tool_call_results r ON r.id=b.owner_tool_call_result_id WHERE r.tool_call_id=$1`, old).Scan(&code))
	require.Equal(t, "expired", code)
}

func TestProgramToolArchiveEndsOpenCalls(t *testing.T) {
	f := newProgramFixture(t)
	named := func(key string) agentexecution.ProgramTool {
		t.Helper()
		var tool agentexecution.ProgramTool
		f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
			var err error
			tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
				ProcessID: f.process, Key: programKey(key), Named: true, Name: "storage_read", Type: "mcp",
				Input: json.RawMessage(`{"path":"a"}`)})
			require.NoError(t, err)
		})
		return tool
	}
	running, asking, ready := named("running"), named("asking"), named("ready")
	pending := f.create(t, "pending")
	runRef := agentexecution.ProgramToolRef{ID: running.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found := f.claim(t, running.ID, runRef.ClaimID)
	require.True(t, found)
	askRef := agentexecution.ProgramToolRef{ID: asking.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found = f.claim(t, asking.ID, askRef.ClaimID)
	require.True(t, found)
	var interaction agentexecution.Interaction
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), runRef, time.Minute)
		require.NoError(t, err)
		interaction, err = h.OpenProgramPermission(t.Context(), askRef, programPermission(t), nil)
		require.NoError(t, err)
	})
	readyRef := agentexecution.ProgramToolRef{ID: ready.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found = f.claim(t, ready.ID, readyRef.ClaimID)
	require.True(t, found)
	_, err := f.pool.Exec(t.Context(), `UPDATE tool_calls SET state='ready',program_claim_id=NULL,
 program_claim_expires_at=NULL WHERE id=$1`, ready.ID)
	require.NoError(t, err)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	require.NoError(t, cell.RenewProgramTool(t.Context(), f.agent, runRef, time.Minute))

	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		archived, err := h.Archive(t.Context(), uuid.Nil)
		require.NoError(t, err)
		require.True(t, archived)
	})
	require.ErrorIs(t, cell.RenewProgramTool(t.Context(), f.agent, runRef, time.Minute),
		storeerr.ErrProgramToolCallClaimLost, "the server running a call of an archived agent loses its claim")
	for _, want := range []struct {
		id      uuid.UUID
		outcome string
	}{
		{running.ID, "failed"}, {asking.ID, "canceled"}, {ready.ID, "canceled"}, {pending.ID, "canceled"},
	} {
		state, outcome := programState(t, f, want.id)
		require.Equal(t, []string{"completed", want.outcome}, []string{state, outcome})
	}
	var interactionState string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state FROM agent_interactions WHERE id=$1`,
		interaction.ID).Scan(&interactionState))
	require.NotEqual(t, "open", interactionState, "the approval closes with its call")
	reapable, err := cell.ListProgramToolReapable(t.Context(), agentexecution.ReapableProgramTool{}, 1000)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(reapable, func(tool agentexecution.ReapableProgramTool) bool {
		return tool.AgentID == f.agent
	}), "nothing is left for maintenance to end")
}

func TestProgramToolRenewRequiresActiveAgent(t *testing.T) {
	f := newProgramFixture(t)
	var tool agentexecution.ProgramTool
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
			ProcessID: f.process, Key: programKey("named"), Named: true, Name: "storage_read", Type: "mcp",
			Input: json.RawMessage(`{"path":"a"}`)})
		require.NoError(t, err)
	})
	ref := agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: uuid.New()}
	_, found := f.claim(t, tool.ID, ref.ClaimID)
	require.True(t, found)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), ref, time.Minute)
		require.NoError(t, err)
	})
	_, err := f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	require.NoError(t, cell.RenewProgramTool(t.Context(), f.agent, ref, time.Minute),
		"a named call keeps its claim after its command ends")
	_, err = f.pool.Exec(t.Context(), `UPDATE agents SET state='archived',archived_at=statement_timestamp()
 WHERE id=$1`, f.agent)
	require.NoError(t, err)
	require.ErrorIs(t, cell.RenewProgramTool(t.Context(), f.agent, ref, time.Minute),
		storeerr.ErrProgramToolCallClaimLost)
}

func TestProgramToolRunnableListsNamedCallsNoServerHolds(t *testing.T) {
	f := newProgramFixture(t)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	create := func(key, toolType string, named bool) uuid.UUID {
		t.Helper()
		var tool agentexecution.ProgramTool
		f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
			var err error
			tool, err = h.CreateProgramTool(t.Context(), agentexecution.CreateProgramToolInput{
				ProcessID: f.process, Key: programKey(key), Named: named, Name: "storage_read", Type: toolType,
				Input: json.RawMessage(`{"path":"a"}`)})
			require.NoError(t, err)
		})
		return tool.ID
	}
	runnable := func() []uuid.UUID {
		t.Helper()
		rows, err := cell.ListProgramToolRunnable(t.Context(), 1000)
		require.NoError(t, err)
		var ids []uuid.UUID
		for _, row := range rows {
			if row.AgentID == f.agent {
				require.Equal(t, f.process, row.ProcessID)
				ids = append(ids, row.ID)
			}
		}
		return ids
	}
	authorize := func(id uuid.UUID) agentexecution.ProgramToolRef {
		t.Helper()
		ref := agentexecution.ProgramToolRef{ID: id, ProcessID: f.process, ClaimID: uuid.New()}
		_, found := f.claim(t, id, ref.ClaimID)
		require.True(t, found)
		return ref
	}

	pending := create("pending", "built_in", true)
	create("unnamed", "built_in", false)
	claimed := create("claimed", "built_in", true)
	authorize(claimed)
	custom := create("custom", "custom", true)
	customRef := authorize(custom)
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.AuthorizeProgramTool(t.Context(), customRef, time.Minute)
		require.NoError(t, err)
	})
	approved := create("approved", "built_in", true)
	approveRef := authorize(approved)
	var interaction agentexecution.Interaction
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		var err error
		interaction, err = h.OpenProgramPermission(t.Context(), approveRef, programPermission(t), nil)
		require.NoError(t, err)
	})
	require.Equal(t, []uuid.UUID{pending}, runnable())
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		_, err := h.ResolveInteraction(t.Context(), agentexecution.ResolveInteractionInput{ID: interaction.ID,
			Resolution: interactionform.Resolution{Answers: []interactionform.Answer{
				{OptionIndices: []int{toolpermission.AllowOptionIndex}}}}})
		require.NoError(t, err)
	})
	require.Equal(t, []uuid.UUID{pending, approved}, runnable(), "an approved call waits for a server")
	_, err := f.pool.Exec(t.Context(), `UPDATE tool_calls SET program_claim_expires_at=statement_timestamp()
 WHERE id=$1`, claimed)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{pending, claimed, approved}, runnable(),
		"a call whose authorizer stopped can be authorized again")

	_, err = f.pool.Exec(t.Context(), `UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),
 exit_code=0 WHERE id=$1`, f.process)
	require.NoError(t, err)
	require.Len(t, runnable(), 3, "named calls outlive their command")
	defer agentexecution.SetProgramToolNamedLifetime(0)()
	require.Empty(t, runnable(), "a named call past its lifetime is left for maintenance to end")
}
