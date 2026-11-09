//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"errors"
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

// newProgramFixture completes a run_command call whose shell process keeps
// running, the usual state when the process makes program tool calls.
func newProgramFixture(t *testing.T) programFixture {
	t.Helper()
	f, a, lease := commandFixture(t)
	return programFixtureFor(t, f, a, lease)
}

// sibling is the same state for another agent in the fixture's database.
func (f programFixture) sibling(t *testing.T) programFixture {
	t.Helper()
	a, lease := f.executionFixture.create(t, uuid.New(), uuid.Nil), uuid.New()
	_, err := f.pool.Exec(t.Context(),
		`INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,$3,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '90 seconds')`,
		lease, a, uuid.New())
	require.NoError(t, err)
	return programFixtureFor(t, f.executionFixture, a, lease)
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

// try runs commands expected to fail and rolls them back.
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

// fingerprint captures what program calls must never change: the execution
// head, wakeups, events, inputs, and the model-visible tool batch.
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

// sqlState runs statements in a savepoint and returns the SQLSTATE of the
// last one, checking deferred constraints too. Earlier statements must pass.
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
	insert := `INSERT INTO tool_calls(agent_id,model_output_id,provider_call_id,name,input,type,state,caller,
 parent_tool_call_id,created_at) SELECT agent_id,$5::uuid,$2,'storage_read','{}','built_in',
 'awaiting_authorization',$3,$4,statement_timestamp() FROM tool_calls WHERE id=$1`
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p1", "program", f.parent, output)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p2", "program", nil, nil)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p3", "model", f.parent, output)))
	require.Equal(t, "23514", sqlState(t, u, stmt(insert, f.parent, "p4", "program", uuid.New(), nil)))
	require.Equal(t, "23505", sqlState(t, u, stmt(insert, f.parent, programKey("k1").String(), "program", f.parent, nil)))
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
	// A program call cannot own children even if it owned a process.
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
	require.Equal(t, programKey("key").String(), provider)
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
					// Allowed in the authorize phase: waiting under the same claim.
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
	reapable, err := cell.ListProgramToolReapable(t.Context(), 10)
	require.NoError(t, err)
	require.Equal(t,
		[]agentexecution.ProgramToolCandidate{{ID: tool.ID, ProjectID: f.project, AgentID: f.agent}}, reapable)
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
	candidates, err := cell.ListProgramToolCandidates(t.Context(), 10)
	require.NoError(t, err)
	require.Equal(t, []agentexecution.ProgramToolCandidate{
		{ID: tool.ID, ProjectID: f.project, AgentID: f.agent, ProcessID: f.process}}, candidates)
	first := uuid.New()
	_, found := f.claim(t, tool.ID, first)
	require.True(t, found)
	ref := agentexecution.ProgramToolRef{ID: tool.ID, ProcessID: f.process, ClaimID: first}
	err = cell.RenewProgramTool(t.Context(), f.agent, ref, time.Hour)
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
	// An expired authorization lease is claimable again; the old holder loses it.
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
	// The authorized call waits under the authorization claim.
	execute := second
	candidates, err = cell.ListProgramToolCandidates(t.Context(), 10)
	require.NoError(t, err)
	require.Empty(t, candidates)
	expire()
	// An expired execution claim is never re-run: it fails as worker_lost.
	_, found = f.claim(t, tool.ID, uuid.New())
	require.False(t, found)
	ref.ClaimID = execute
	f.try(t, func(h *agentexecution.Handle) {
		_, err := h.DispatchProgramTool(t.Context(), ref)
		require.ErrorIs(t, err, storeerr.ErrProgramToolCallClaimLost)
	})
	f.tx(t, func(_ *agentexecution.Unit, h *agentexecution.Handle) {
		reaped, err := h.ReapProgramTool(t.Context(), tool.ID)
		require.NoError(t, err)
		require.True(t, reaped)
	})
	state, outcome := programState(t, f, tool.ID)
	require.Equal(t, []string{"completed", "failed"}, []string{state, outcome})
	var code string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT b.structured_data->>'error_code' FROM content_blocks b
 JOIN tool_call_results r ON r.id=b.owner_tool_call_result_id WHERE r.tool_call_id=$1`, tool.ID).Scan(&code))
	require.Equal(t, "worker_lost", code)
}

// An agent's claimed calls count against its claimable ones, so another
// agent's later call is offered before the rest of its backlog.
func TestProgramToolCandidatesShareWorkersAcrossAgents(t *testing.T) {
	busy := newProgramFixture(t)
	running, second, third := busy.create(t, "running"), busy.create(t, "second"), busy.create(t, "third")
	_, found := busy.claim(t, running.ID, uuid.New())
	require.True(t, found)
	other := busy.sibling(t)
	late := other.create(t, "late")
	candidates, err := agentexecution.NewCell(busy.pool, nil, nil).ListProgramToolCandidates(t.Context(), 10)
	require.NoError(t, err)
	ids := make([]uuid.UUID, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ID
	}
	require.Equal(t, []uuid.UUID{late.ID, second.ID, third.ID}, ids)
	require.Equal(t, other.agent, candidates[0].AgentID)
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

// programKey is a stable idempotency key for a test label.
func programKey(label string) uuid.UUID { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(label)) }
