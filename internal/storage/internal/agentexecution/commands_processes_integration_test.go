//go:build integration

package agentexecution_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func processOwner(
	t *testing.T,
	f executionFixture,
	u *agentexecution.Unit,
	agent, lease, tool uuid.UUID,
	state string,
) uuid.UUID {
	t.Helper()
	machine, binding, id := uuid.New(), uuid.New(), uuid.New()
	_, err := u.DB().
		Exec(t.Context(),
			`INSERT INTO machines(id,org_id,display_name,source_kind,provider,lifecycle_state,lifecycle_changed_at,created_at,updated_at)
 VALUES($1,$2,'test','byo','byo','active',statement_timestamp(),statement_timestamp(),statement_timestamp())`,
			machine, f.org)
	require.NoError(t, err)
	_, err = u.DB().
		Exec(t.Context(),
			`INSERT INTO agent_machine_bindings(id,org_id,project_id,agent_id,machine_id,machine_ref,binding_kind,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,'mchr-abcdef','explicit',statement_timestamp(),statement_timestamp())`,
			binding, f.org, f.project, agent, machine)
	require.NoError(t, err)
	spec, err := json.Marshal(processcmd.ForShell("echo done", "", ""))
	require.NoError(t, err)
	_, err = u.DB().
		Exec(t.Context(),
			`INSERT INTO processes(id,org_id,project_id,agent_id,tool_call_id,runtime_lock_id,agent_machine_binding_id,machine_id,
 execution_spec,state,execution_granted_at,source_started_at,state_changed_at,created_at,updated_at,last_activity_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,CASE WHEN $10='queued' THEN NULL ELSE statement_timestamp() END,
 CASE WHEN $10='running' THEN statement_timestamp() ELSE NULL END,
 statement_timestamp(),statement_timestamp(),statement_timestamp(),statement_timestamp())`,
			id, f.org, f.project, agent, tool, lease, binding, machine, spec, state)
	require.NoError(t, err)
	return id
}

func TestExecutionDurableProcessCustody(t *testing.T) {
	for _, mode := range []string{"started", "terminal", "release", "reap", "cancel", "archive", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			ref := toolCommand(t, h, lease, "built_in")
			_, err := h.AuthorizeTool(t.Context(), ref)
			require.NoError(t, err)
			state := "queued"
			if mode == "started" || mode == "terminal" {
				state = "running"
			}
			process := processOwner(t, f, u, a, lease, ref.ID, state)
			changed, err := h.WaitForTool(t.Context(), ref, agentexecution.ToolOwner{ProcessID: process})
			require.NoError(t, err)
			require.True(t, changed)
			changed, err = h.WaitForTool(t.Context(), ref, agentexecution.ToolOwner{ProcessID: process})
			require.NoError(t, err)
			require.False(t, changed)
			assertCommandState(t, u, h)
			switch mode {
			case "started":
				input := agentexecution.ProcessResult{
					ID:       process,
					Started:  true,
					Observed: json.RawMessage(`{"output":"start","cursor":0,"next_cursor":5}`),
				}
				published, err := h.CompleteProcess(t.Context(), input)
				require.NoError(t, err)
				require.True(t, published.Matches)
				require.True(t, published.Tool.Changed)
				replay, err := h.CompleteProcess(t.Context(), input)
				require.NoError(t, err)
				require.True(t, replay.Matches)
				require.False(t, replay.Tool.Changed)
				_, err = u.DB().
					Exec(t.Context(),
						`UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),exit_code=0 WHERE id=$1`,
						process)
				require.NoError(t, err)
				late, err := h.CompleteProcess(t.Context(), agentexecution.ProcessResult{ID: process})
				require.NoError(t, err)
				require.False(t, late.Matches)
				require.True(t, late.Published)
			case "terminal":
				work, err := h.Advance(t.Context(), lease, true, true)
				require.NoError(t, err)
				require.True(t, work.Released)
				_, err = u.DB().
					Exec(t.Context(),
						`UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),exit_code=0 WHERE id=$1`,
						process)
				require.NoError(t, err)
				published, err := h.CompleteProcess(t.Context(), agentexecution.ProcessResult{ID: process})
				require.NoError(t, err)
				require.True(t, published.Matches)
			case "release":
				require.NoError(t, h.Release(t.Context(), lease))
				var state string
				require.NoError(
					t,
					u.DB().
						QueryRow(t.Context(), `SELECT state FROM processes WHERE id=$1`, process).
						Scan(&state),
				)
				require.Equal(t, "queued", state)
			case "reap":
				_, err = u.DB().
					Exec(t.Context(),
						`UPDATE agent_runtime_locks SET started_at=statement_timestamp()-interval '3 minutes',renewed_at=statement_timestamp()-interval '2 minutes',lease_expires_at=statement_timestamp()-interval '1 minute' WHERE id=$1`,
						lease)
				require.NoError(t, err)
				reaped, err := h.Reap(t.Context(), lease)
				require.NoError(t, err)
				require.True(t, reaped)
			case "cancel":
				_, err = h.Cancel(
					t.Context(),
					agentexecution.CancelInput{Reason: "agent_canceled", Message: "canceled"},
				)
				require.NoError(t, err)
			case "archive":
				archived, err := h.Archive(t.Context(), uuid.Nil)
				require.NoError(t, err)
				require.True(t, archived)
			case "revoke":
				route, err := h.Route()
				require.NoError(t, err)
				require.NoError(
					t,
					u.InterruptProcesses(
						t.Context(),
						map[agentexecution.AgentRoute][]uuid.UUID{route: {process}},
						"grant_revoked",
					),
				)
			}
			assertCommandState(t, u, h)
			require.NoError(t, u.Commit(t.Context(), "process custody"))
		})
	}
}

func TestExecutionRejectsMissingDurableOwner(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	ref := toolCommand(t, h, lease, "built_in")
	_, err := h.AuthorizeTool(t.Context(), ref)
	require.NoError(t, err)
	require.ErrorIs(t, u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
		_, err := h.WaitForTool(t.Context(), ref, agentexecution.ToolOwner{ProcessID: uuid.New()})
		return err
	}), storeerr.ErrInvalidToolCallDisposition)
	snapshot := assertCommandState(t, u, h)
	require.Equal(t, agentexecution.WorkTool, snapshot.Selection.Work)
	require.NoError(t, u.Commit(t.Context(), "missing owner rollback"))
}

func TestExecutionActionReports(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "actions")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, h, lease)
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = nil
	var bindings []agentexecution.ToolProposal
	for _, id := range []string{"start", "one", "two"} {
		response.Normalized.Content = append(
			response.Normalized.Content,
			modelenvelope.ResponsePart{
				Type:           "tool_call",
				ProviderCallID: id,
				ToolName:       "tool",
				ToolInput:      json.RawMessage(`{}`),
			},
		)
		bindings = append(bindings, agentexecution.ToolProposal{ProviderCallID: id, Type: "built_in"})
	}
	output, err := h.AcceptOutput(
		t.Context(),
		agentexecution.AcceptOutputInput{
			RuntimeLockID: lease,
			ContextID:     prepared.Context.ID,
			Response:      response,
			Tools:         bindings,
		},
	)
	require.NoError(t, err)
	refs := make([]agentexecution.ToolRef, 3)
	for i, tool := range output.Tools {
		refs[i] = agentexecution.ToolRef{ID: tool.ID, RuntimeLockID: lease}
		_, err = h.AuthorizeTool(t.Context(), refs[i])
		require.NoError(t, err)
	}
	process := processOwner(t, f, u, a, lease, refs[0].ID, "running")
	_, err = h.WaitForTool(t.Context(), refs[0], agentexecution.ToolOwner{ProcessID: process})
	require.NoError(t, err)
	_, err = h.CompleteProcess(t.Context(), agentexecution.ProcessResult{ID: process, Started: true})
	require.NoError(t, err)
	actions := []uuid.UUID{uuid.New(), uuid.New()}
	for i, id := range actions {
		_, err = u.DB().
			Exec(t.Context(),
				`INSERT INTO process_actions(id,org_id,project_id,agent_id,process_id,tool_call_id,runtime_lock_id,action_kind,seq,payload,state,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,'read',$8,'{}','queued',statement_timestamp(),statement_timestamp())`,
				id, f.org, f.project, a, process, refs[i+1].ID, lease, i+1)
		require.NoError(t, err)
		_, err = h.WaitForTool(t.Context(), refs[i+1], agentexecution.ToolOwner{ActionID: id})
		require.NoError(t, err)
	}
	_, err = u.DB().Exec(t.Context(), `UPDATE process_actions SET state='applied' WHERE id=$1`, actions[1])
	require.NoError(t, err)
	pid, err := publicid.Encode(publicid.KindProcess, process)
	require.NoError(t, err)
	observed := json.RawMessage(
		`{"process_id":"` + pid + `","output":"ab","cursor":0,"next_cursor":2,"truncated":false}`,
	)
	require.ErrorIs(t, u.Savepoint(t.Context(), func(_ *agentexecution.Unit) error {
		_, err := h.CompleteProcessAction(
			t.Context(),
			agentexecution.ActionResult{ID: actions[1], Observed: observed},
		)
		return err
	}), storeerr.ErrProcessActionReportBlocked)
	_, err = u.DB().Exec(t.Context(), `UPDATE process_actions SET state='applied' WHERE id=$1`, actions[0])
	require.NoError(t, err)
	first, err := h.CompleteProcessAction(
		t.Context(),
		agentexecution.ActionResult{ID: actions[0], Observed: observed},
	)
	require.NoError(t, err)
	require.True(t, first.Tool.Changed)
	assertCommandState(t, u, h)
	var cursor int64
	require.NoError(
		t,
		u.DB().
			QueryRow(t.Context(), `SELECT default_output_cursor FROM processes WHERE id=$1`, process).
			Scan(&cursor),
	)
	require.EqualValues(t, 2, cursor)
	observedTwo := json.RawMessage(
		`{"process_id":"` + pid + `","output":"cd","cursor":2,"next_cursor":4,"truncated":false}`,
	)
	second, err := h.CompleteProcessAction(
		t.Context(),
		agentexecution.ActionResult{ID: actions[1], Observed: observedTwo},
	)
	require.NoError(t, err)
	require.True(t, second.Matches)
	_, err = u.DB().
		Exec(t.Context(),
			`UPDATE processes SET state='exited',source_ended_at=statement_timestamp(),exit_code=0 WHERE id=$1`,
			process)
	require.NoError(t, err)
	replay, err := h.CompleteProcessAction(
		t.Context(),
		agentexecution.ActionResult{ID: actions[0], Observed: observed},
	)
	require.NoError(t, err)
	require.True(t, replay.Matches)
	require.False(t, replay.Tool.Changed)
	conflict, err := h.CompleteProcessAction(
		t.Context(),
		agentexecution.ActionResult{ID: actions[0], Observed: observedTwo},
	)
	require.NoError(t, err)
	require.False(t, conflict.Matches)
	assertCommandState(t, u, h)

	for i, state := range []string{"failed", "unknown"} {
		prepared = prepareCommand(t, h, lease)
		response.Normalized.Content = response.Normalized.Content[:1]
		failedOutput,
			err := h.AcceptOutput(t.Context(),
			agentexecution.AcceptOutputInput{RuntimeLockID: lease,
				ContextID: prepared.Context.ID,
				Response:  response,
				Tools:     bindings[:1]})
		require.NoError(t, err)
		ref := agentexecution.ToolRef{ID: failedOutput.Tools[0].ID, RuntimeLockID: lease}
		_, err = h.AuthorizeTool(t.Context(), ref)
		require.NoError(t, err)
		id := uuid.New()
		kind := "read"
		if state == "unknown" {
			kind = "write"
		}
		_, err = u.DB().
			Exec(t.Context(), `INSERT INTO process_actions(id,org_id,project_id,agent_id,process_id,tool_call_id,
 runtime_lock_id,action_kind,seq,payload,state,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}','queued',statement_timestamp(),statement_timestamp())`,
				id, f.org, f.project, a, process, ref.ID, lease, kind, i+3)
		require.NoError(t, err)
		_, err = h.WaitForTool(t.Context(), ref, agentexecution.ToolOwner{ActionID: id})
		require.NoError(t, err)
		_, err = u.DB().Exec(t.Context(), `UPDATE process_actions SET state=$2,state_reason_code='lost',
 state_reason_message='connection lost' WHERE id=$1`, id, state)
		require.NoError(t, err)
		completion, err := h.CompleteProcessAction(t.Context(), agentexecution.ActionResult{ID: id})
		require.NoError(t, err)
		require.True(t, completion.Matches)
		require.True(t, completion.Tool.Changed)
		repeated, err := h.CompleteProcessAction(t.Context(), agentexecution.ActionResult{ID: id})
		require.NoError(t, err)
		require.True(t, repeated.Matches)
		require.False(t, repeated.Tool.Changed)
		var outcome string
		require.NoError(t,
			u.DB().QueryRow(t.Context(),
				`SELECT outcome FROM tool_call_results WHERE id=$1`,
				completion.Tool.ResultID).Scan(&outcome))
		require.Equal(t, "failed", outcome)
		assertCommandState(t, u, h)
	}
	require.NoError(t, u.Commit(t.Context(), "ordered actions"))
}

func TestExecutionOldTurnLateToolResult(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "old turn")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	old := prepareCommand(t, h, lease)
	receiveCommand(t, h, "steering", "new turn")
	admitted, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, old.Context.TurnID, admitted.TurnID)
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = []modelenvelope.ResponsePart{
		{Type: "tool_call", ProviderCallID: "late", ToolName: "tool", ToolInput: json.RawMessage(`{}`)},
	}
	output, err := h.AcceptOutput(
		t.Context(),
		agentexecution.AcceptOutputInput{
			RuntimeLockID: lease,
			ContextID:     old.Context.ID,
			Response:      response,
			Tools:         []agentexecution.ToolProposal{{ProviderCallID: "late", Type: "custom"}},
		},
	)
	require.NoError(t, err)
	ref := agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease}
	_, err = h.AuthorizeTool(t.Context(), ref)
	require.NoError(t, err)
	before := assertCommandState(t, u, h)
	completed, err := h.CompleteCustomTool(
		t.Context(),
		ref.ID,
		"succeeded",
		[]agentexecution.Content{{Kind: "text", Text: "late"}},
	)
	require.NoError(t, err)
	require.Equal(t, old.Context.TurnID, completed.Event.TurnID)
	after := assertCommandState(t, u, h)
	require.Equal(t, before.Head, after.Head)
	require.Equal(t, before.Selection, after.Selection)
	require.NoError(t, u.Commit(t.Context(), "late result"))
}
