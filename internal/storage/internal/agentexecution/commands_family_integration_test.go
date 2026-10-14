//go:build integration

package agentexecution_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestExecutionCreatedAgentPublication(t *testing.T) {
	f := migratedExecutionFixture(t)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	u, err := agentexecution.BeginTestUnit(t.Context(), cell)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(context.Background()) }()
	h, err := u.CreateAgent(
		t.Context(),
		agentexecution.CreateAgentInput{ID: uuid.New(), ProjectID: f.project, ConfigID: f.config},
	)
	require.NoError(t, err)
	receiveCommand(t, h, "queued", "created")
	assertCommandState(t, u, h)
	var heads int
	require.NoError(
		t,
		u.DB().QueryRow(t.Context(), `SELECT count(*) FROM agent_execution_state`).Scan(&heads),
	)
	require.Zero(t, heads)
	statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
	require.NoError(t, err)
	require.Equal(t, 1, statements)
	require.Equal(t, 1, roundTrips)
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_execution_state`).Scan(&heads),
	)
	require.Equal(t, 1, heads)
}

func familyFixture(
	t *testing.T,
	childFirst bool,
) (executionFixture, *agentexecution.Unit, *agentexecution.Handle, *agentexecution.Handle, uuid.UUID) {
	t.Helper()
	f := migratedExecutionFixture(t)
	parent := f.create(t, uuid.New(), uuid.Nil)
	child := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	if childFirst {
		child = uuid.MustParse("00000000-0000-4000-8000-000000000001")
	}
	f.create(t, child, parent)
	lease := uuid.New()
	_, err := f.pool.Exec(
		t.Context(),
		`INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,$3,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '90 seconds')`,
		lease,
		child,
		uuid.New(),
	)
	require.NoError(t, err)
	cell := agentexecution.NewCell(f.pool, nil, nil)
	u, err := agentexecution.BeginTestUnit(t.Context(), cell)
	require.NoError(t, err)
	t.Cleanup(func() { _ = u.Rollback(context.Background()) })
	require.NoError(
		t,
		u.LockAgentRefs(
			t.Context(),
			[]lifecyclelock.AgentRef{
				{ProjectID: f.project, AgentID: parent},
				{ProjectID: f.project, AgentID: child},
			},
			nil,
		),
	)
	p, err := u.Agent(
		agentexecution.AgentRoute{ProjectID: f.project, AgentID: parent, RootAgentID: parent},
	)
	require.NoError(t, err)
	c, err := u.Agent(
		agentexecution.AgentRoute{ProjectID: f.project, AgentID: child, RootAgentID: parent},
	)
	require.NoError(t, err)
	return f, u, p, c, lease
}

func TestExecutionParentEffects(t *testing.T) {
	for _, order := range []bool{false, true} {
		for _, kind := range []string{"answer", "question", "failure"} {
			t.Run(
				map[bool]string{false: "parent_first", true: "child_first"}[order]+"/"+kind,
				func(t *testing.T) {
					f, u, p, c, lease := familyFixture(t, order)
					for range 50 {
						receiveCommand(t, p, "queued", "pending")
					}
					if kind == "question" {
						ref := toolCommand(t, c, lease, "built_in")
						_, err := c.AuthorizeTool(t.Context(), ref)
						require.NoError(t, err)
						form := interactionform.Form{
							Title: "Parent question",
							Questions: []interactionform.Question{
								{Prompt: "Which?", Options: []interactionform.Option{{Label: "yes"}}},
							},
						}
						_, err = c.OpenInteraction(
							t.Context(),
							agentexecution.OpenInteractionInput{ToolRef: ref, Question: &form},
						)
						require.NoError(t, err)
					} else {
						receiveCommand(t, c, "queued", "input")
						_, err := c.AdmitInputs(t.Context())
						require.NoError(t, err)
						prepared := prepareCommand(t, c, lease)
						if kind == "answer" {
							input := agentexecution.AcceptOutputInput{RuntimeLockID: lease,
								ContextID: prepared.Context.ID,
								Response:  commandResponse(modelenvelope.StopReasonEndTurn)}
							_, err = c.AcceptOutput(t.Context(), input)
							require.NoError(t, err)
							replay, err := c.AcceptOutput(t.Context(), input)
							require.NoError(t, err)
							require.False(t, replay.Created)
						} else {
							_,
								err = c.FailModel(t.Context(),
								agentexecution.ModelFailure{ContextID: prepared.Context.ID,
									RuntimeLockID: lease,
									ErrorKind:     "runtime",
									ErrorMessage:  "failure"})
							require.NoError(t, err)
						}
					}
					pr, err := p.Route()
					require.NoError(t, err)
					parentState := assertCommandState(t, u, p)
					require.Equal(t, agentexecution.AdmitAllSteering, parentState.Selection.Admission)
					assertCommandState(t, u, c)
					var inputs int
					require.NoError(
						t,
						u.DB().
							QueryRow(t.Context(), `SELECT count(*) FROM agent_inputs WHERE agent_id=$1`, pr.AgentID).
							Scan(&inputs),
					)
					require.Equal(t, 51, inputs)
					statements, roundTrips, err := agentexecution.CountCommit(t.Context(), u)
					require.NoError(t, err)
					require.Equal(t, 2, statements)
					require.Equal(t, 1, roundTrips)
					_, parent := f.handle(t, pr.AgentID)
					err = parent.CheckContentCapacity(t.Context())
					require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
				},
			)
		}
	}
}

func TestExecutionArchiveFamily(t *testing.T) {
	f, u, p, c, lease := familyFixture(t, true)
	toolCommand(t, c, lease, "custom")
	receiveCommand(t, p, "queued", "parent")
	receiveCommand(t, c, "queued", "child")
	for _, h := range []*agentexecution.Handle{p, c} {
		_, err := h.Archive(t.Context(), uuid.Nil)
		require.NoError(t, err)
	}
	for _, h := range []*agentexecution.Handle{p, c} {
		snapshot := assertCommandState(t, u, h)
		require.Equal(t, agentexecution.WaitArchived, snapshot.Selection.Wait)
		replay, err := h.Archive(t.Context(), uuid.Nil)
		require.NoError(t, err)
		require.False(t, replay)
	}
	require.NoError(t, u.Commit(t.Context(), "archive family"))
	var wakeups int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_wakeups`).Scan(&wakeups))
	require.Zero(t, wakeups)
}

func TestExecutionParentReportPreservesInteraction(t *testing.T) {
	_, u, p, c, lease := familyFixture(t, true)
	parentLease := familyParentRuntime(t, u, p)
	ref := toolCommand(t, p, parentLease, "built_in")
	_, err := p.AuthorizeTool(t.Context(), ref)
	require.NoError(t, err)
	form := interactionform.Form{Title: "Question",
		Questions: []interactionform.Question{{Prompt: "Choose",
			Options: []interactionform.Option{{Label: "yes"}}}}}
	opened, err := p.OpenInteraction(
		t.Context(),
		agentexecution.OpenInteractionInput{ToolRef: ref, Question: &form},
	)
	require.NoError(t, err)
	receiveCommand(t, c, "queued", "answer")
	_, err = c.AdmitInputs(t.Context())
	require.NoError(t, err)
	prepared := prepareCommand(t, c, lease)
	_,
		err = c.AcceptOutput(t.Context(),
		agentexecution.AcceptOutputInput{RuntimeLockID: lease,
			ContextID: prepared.Context.ID,
			Response:  commandResponse(modelenvelope.StopReasonEndTurn)})
	require.NoError(t, err)
	var state string
	require.NoError(t,
		u.DB().QueryRow(t.Context(),
			`SELECT state FROM agent_interactions WHERE id=$1`,
			opened.ID).Scan(&state))
	require.Equal(t, "open", state)
	snapshot := assertCommandState(t, u, p)
	require.Equal(t, agentexecution.WaitToolBatch, snapshot.Selection.Wait)
	require.NoError(t, u.Commit(t.Context(), "parent report"))
}

func TestSiblingPreparationDoesNotLockParent(t *testing.T) {
	f := migratedExecutionFixture(t)
	parent := f.create(t, uuid.New(), uuid.Nil)
	store := storage.NewStore(f.pool)
	var inputs []executionstore.PrepareNormalModelCallInput
	for range 8 {
		child := f.create(t, uuid.New(), parent)
		u, h := f.handle(t, child)
		received := receiveCommand(t, h, "queued", "work")
		claimed, err := h.Claim(t.Context(), uuid.New(), 90*time.Second)
		require.NoError(t, err)
		require.NoError(t, u.Commit(t.Context(), "claim sibling"))
		inputs = append(inputs,
			executionstore.PrepareNormalModelCallInput{ProjectID: f.project,
				AgentID:         child,
				RuntimeLockID:   claimed.RuntimeID,
				OpeningInputIDs: []uuid.UUID{received.ID}})
	}
	blocker, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(t.Context()) }()
	_, err = blocker.Exec(t.Context(), `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, parent)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, len(inputs))
	for i, input := range inputs {
		go func() {
			prepared, err := store.Execution().PrepareNormalModelCall(ctx, input)
			if err != nil {
				done <- err
				return
			}
			reason := modelenvelope.StopReasonToolUse
			if i%2 == 0 {
				reason = modelenvelope.StopReasonEndTurn
			}
			response := commandResponse(reason)
			response.Normalized.Content = []modelenvelope.ResponsePart{{Type: "tool_call", ProviderCallID: "call",
				ToolName: "tool", ToolInput: json.RawMessage(`{}`)}}
			bindings := []executionstore.ToolCallBindingInput{{ProviderCallID: "call", Type: "built_in"}}
			if i%4 < 2 {
				_, err = store.Execution().AcceptModelOutputAndAdvance(ctx, executionstore.AcceptModelOutputAndAdvanceInput{
					Output: executionstore.RecordModelOutputAndCompleteContextInput{
						ProjectID: f.project, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
						ModelCallContextID: prepared.Claim.Context.ID, ProviderResponse: response,
					}, ToolCallBindings: bindings,
				})
			} else {
				_, _, err = store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
					executionstore.RecordToolCallSourceAndCompleteContextInput{
						ProjectID: f.project, AgentID: input.AgentID, RuntimeLockID: input.RuntimeLockID,
						ModelCallContextID: prepared.Claim.Context.ID, ProviderResponse: response, ToolCallBindings: bindings,
					})
			}
			done <- err
		}()
	}
	for range inputs {
		require.NoError(t, <-done)
	}
}

func familyParentRuntime(t *testing.T, u *agentexecution.Unit, p *agentexecution.Handle) uuid.UUID {
	t.Helper()
	route, err := p.Route()
	require.NoError(t, err)
	lease := uuid.New()
	_,
		err = u.DB().Exec(t.Context(),
		`INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,$3,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '90 seconds')`,
		lease, route.AgentID, uuid.New())
	require.NoError(t, err)
	return lease
}
