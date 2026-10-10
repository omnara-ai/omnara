//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func ownedAdvanceInput(f processDaemonFixture, allowModel bool) executionstore.AdvanceOwnedAgentWorkInput {
	return executionstore.AdvanceOwnedAgentWorkInput{
		ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID, AllowModelWork: allowModel,
	}
}

func ownedInput(
	t *testing.T,
	ctx context.Context,
	f processDaemonFixture,
	mode executionstore.AgentInputDeliveryMode,
	key string,
) executionstore.AgentInputRecord {
	t.Helper()
	input, _, _, err := f.Store.Execution().
		CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"next input"}]`),
			DeliveryMode:  mode, IdempotencyKey: key,
		})
	require.NoError(t, err)
	return input
}

func ownedCompleteTool(t *testing.T, ctx context.Context, f processDaemonFixture, id uuid.UUID) {
	t.Helper()
	_, err := f.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: id, RuntimeLockID: f.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"done"}]`),
	})
	require.NoError(t, err)
}

func ownedModelClaim(
	t *testing.T,
	ctx context.Context,
	f processDaemonFixture,
	work executionstore.ClaimedAgentWork,
) executionstore.ModelCallClaim {
	t.Helper()
	_, err := f.Store.Execution().CaptureAgentConfigForModelContext(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	prepared1, err := f.Store.Execution().
		PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
			ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
			OpeningInputIDs:          work.Model.InputIDs,
			SourceModelCallContextID: work.Model.SourceModelCallContextID,
			SourceModelOutputID:      work.Model.SourceModelOutputID,
		})
	claim := prepared1.Claim

	require.NoError(t, err)
	require.True(t, claim.Claimed)
	return claim
}

func requireOwnedLease(t *testing.T, ctx context.Context, f processDaemonFixture, exists bool) {
	t.Helper()
	var found bool
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_runtime_locks WHERE agent_id=$1 AND id=$2)`, f.AgentID, f.Lock.ID,
	).Scan(&found))
	require.Equal(t, exists, found)
}

func TestAdvanceOwnedAgentWorkPrecedenceMatchesGlobalClaim(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                                       string
		toolType                                   string
		blocked, complete, steering, queued, ended bool
		kind                                       executionstore.AgentWorkKind
		modelKind                                  executionstore.ModelWorkKind
	}{
		{name: "runnable tools before steering", steering: true, queued: true, kind: executionstore.AgentWorkTool},
		{name: "permission blocks steering", blocked: true, steering: true},
		{name: "custom tool blocks steering", toolType: "custom", steering: true},
		{
			name:      "steering before model continuation",
			complete:  true,
			steering:  true,
			queued:    true,
			kind:      executionstore.AgentWorkModel,
			modelKind: executionstore.ModelWorkStart,
		},
		{
			name:      "model continuation before queued input",
			complete:  true,
			queued:    true,
			kind:      executionstore.AgentWorkModel,
			modelKind: executionstore.ModelWorkContinue,
		},
		{
			name:      "completed tools continue",
			complete:  true,
			kind:      executionstore.AgentWorkModel,
			modelKind: executionstore.ModelWorkContinue,
		},
		{
			name:      "queued input after completed turn",
			complete:  true,
			ended:     true,
			queued:    true,
			kind:      executionstore.AgentWorkModel,
			modelKind: executionstore.ModelWorkStart,
		},
		{name: "idle after completed turn", complete: true, ended: true},
	} {
		for _, owned := range []bool{false, true} {
			name := test.name + "/global"
			if owned {
				name = test.name + "/owned"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newProcessDaemonFixture(t, ctx, "owned_precedence")

				item := builtInProcessToolCallBatchItem("first", "read_process")
				if test.toolType != "" {
					item.ToolType = test.toolType
				}
				item.Allowed = !test.blocked
				toolID := createToolCallBatchForProcessTest(t,
					ctx,
					f,
					"owned_precedence",
					[]processToolCallBatchItem{item})[0]
				contextID := modelContextIDForProcessToolCallTest(t, ctx, f, toolID)
				outputID := modelOutputIDForToolCall(t, ctx, f.Store, f.AgentID, toolID)
				if test.blocked {
					createPermissionInteractionForTest(
						t,
						ctx,
						f,
						toolID,
						permissionRequestForStorageTest(t, "read_process"),
					)
				}
				if test.complete {
					ownedCompleteTool(t, ctx, f, toolID)
				}
				if test.ended {
					work, continued, err := f.Store.Execution().
						AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
					require.NoError(t, err)
					require.True(t, continued)
					claim := ownedModelClaim(t, ctx, f, work)
					createModelOutputEventForTurnTest(
						t, ctx, f, work.Model.TurnID, claim.Context.ID, "owned_end", "end_turn", "", f.Now,
					)
				}
				var steering, queued executionstore.AgentInputRecord
				if test.steering {
					steering = ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "steering")
				}
				if test.queued {
					queued = ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "queued")
				}
				var work executionstore.ClaimedAgentWork
				var err error
				if owned {
					var continued bool
					work, continued, err = f.Store.Execution().
						AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
					require.NoError(t, err)
					require.Equal(t, test.kind != executionstore.AgentWorkNone, continued)
					requireOwnedLease(t, ctx, f, continued)
					if continued {
						require.Equal(t, f.Lock.ID, work.RuntimeLock.ID)
					}
				} else {
					require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
					work, _, err = f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
					if test.kind == executionstore.AgentWorkNone {
						require.ErrorIs(t, err, storeerr.ErrNoClaimableAgentWakeup)
					} else {
						require.NoError(t, err)
					}
				}
				require.Equal(t, test.kind, work.Kind)
				if work.Kind != executionstore.AgentWorkNone {
					require.Zero(t, countAgentWakeups(t, ctx, f.Store, f.AgentID))
				}
				if work.Kind == executionstore.AgentWorkModel {
					require.Equal(t, test.modelKind, work.Model.Kind)
					if test.modelKind == executionstore.ModelWorkContinue {
						require.Equal(t, contextID, work.Model.SourceModelCallContextID)
						require.Equal(t, outputID, work.Model.SourceModelOutputID)
					} else if test.steering {
						require.Equal(t, []uuid.UUID{steering.ID}, work.Model.InputIDs)
					} else {
						require.Equal(t, []uuid.UUID{queued.ID}, work.Model.InputIDs)
					}
				}
				if test.queued && !test.ended {
					var state string
					require.NoError(t, f.Store.pool.QueryRow(ctx,
						`SELECT state FROM agent_inputs WHERE id=$1`, queued.ID,
					).Scan(&state))
					require.Equal(t, "received", state)
				}
			})
		}
	}
}

func TestAdvanceOwnedAgentWorkRunnableSiblingAndModelBudget(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "owned_siblings")

	blocked := builtInProcessToolCallBatchItem("blocked", "read_process")
	blocked.Allowed = false
	ids := createToolCallBatchForProcessTest(t, ctx, f, "siblings", []processToolCallBatchItem{
		builtInProcessToolCallBatchItem("ready", "read_process"), blocked,
	})
	interaction := createPermissionInteractionForTest(
		t, ctx, f, ids[1], permissionRequestForStorageTest(t, "read_process"),
	)
	work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, false))
	require.NoError(t, err)
	require.True(t, continued)
	require.Equal(t, executionstore.AgentWorkTool, work.Kind)
	ownedCompleteTool(t, ctx, f, ids[0])
	_, err = f.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: interaction.ID,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}}},
		Actor: mustOmnaraActorParams(t, f.UserID),
	})
	require.NoError(t, err)
	ownedCompleteTool(t, ctx, f, ids[1])
	steering := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "budget-steering")
	_, continued, err = f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, false))
	require.NoError(t, err)
	require.False(t, continued)
	requireOwnedLease(t, ctx, f, false)
	var state string
	require.NoError(
		t,
		f.Store.pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id=$1`, steering.ID).Scan(&state),
	)
	require.Equal(t, "received", state)
	work, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []uuid.UUID{steering.ID}, work.Model.InputIDs)
}

func TestAdvanceOwnedAgentWorkRetryAndUnfinishedRecovery(t *testing.T) {
	for _, unfinished := range []bool{false, true} {
		name := "future retry"
		if unfinished {
			name = "unfinished model"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "owned_retry")

			ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
			work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
			require.NoError(t, err)
			require.True(t, continued)
			claim := ownedModelClaim(t, ctx, f, work)
			if !unfinished {
				_, err := f.Store.Execution().RecordRetryableModelCallFailure(
					ctx, executionstore.RecordRecoverableModelCallFailureInput{
						ProjectID:          testProjectID,
						AgentID:            f.AgentID,
						RuntimeLockID:      f.Lock.ID,
						ModelCallContextID: claim.Context.ID,
						ErrorKind:          "transient", ErrorMessage: "retry later", RetryDelay: time.Hour,
					},
				)
				require.NoError(t, err)
			}
			ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "waiting")
			_, continued, err = f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
			require.NoError(t, err)
			require.False(t, continued)
			requireOwnedLease(t, ctx, f, false)
			record, found, err := f.Store.Execution().
				GetModelCallContext(ctx, testProjectID, f.AgentID, claim.Context.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.ModelCallContextFailed, record.State)
			require.NotNil(t, record.RetryAt)
			require.Equal(t, *record.RetryAt, agentWakeupReadyAt(t, ctx, f.Store, f.AgentID))
			if unfinished {
				require.Equal(t, "runtime_released_before_model_result_acceptance", record.ErrorCode)
			}
		})
	}
}

func TestAdvanceOwnedAgentWorkFencesInactiveRuntime(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "expired", "replaced", "wrong project", "wrong runtime"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "owned_fence")

			createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
			input := ownedAdvanceInput(f, true)
			switch mode {
			case "cancel":
				_, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
			case "expired", "replaced":
				expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
				if mode == "replaced" {
					n, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
					require.NoError(t, err)
					require.EqualValues(t, 1, n)
					claim, found, err := f.Store.Execution().
						ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
					require.NoError(t, err)
					require.True(t, found)
					f.Lock = claim.RuntimeLock
				}
			case "wrong project":
				input.ProjectID = uuid.New()
			case "wrong runtime":
				input.RuntimeLockID = uuid.New()
			}
			_, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
			require.False(t, continued)
			requireOwnedLease(t, ctx, f, true)
		})
	}
}

func TestAdvanceOwnedAgentWorkReleasesExternalWaitsAndRecoversUnfinishedTools(t *testing.T) {
	for _, mode := range []string{"question", "process", "unfinished tool"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "owned_external_wait")

			toolName := "run_command"
			if mode == "question" {
				toolName = "ask_question"
			}
			id := createToolCallForProcessTest(t, ctx, f, "external", toolName)
			execute := executionstore.ExecuteToolCallInput{
				ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ToolCallID: id,
			}
			switch mode {
			case "question":
				createQuestionInteractionForTest(t, ctx, f, id)
			case "process":
				_, err := startProcessForTest(ctx, f.Store, execute, executionstore.CreateProcessInput{
					ExecutionSpec:         processcmd.ForShell("sleep 1", "sh", ""),
					AgentMachineBindingID: f.BindingID, Cwd: "/work",
				})
				require.NoError(t, err)
			case "unfinished tool":
				_, err := startAsyncToolCallForTest(ctx, f.Store, execute)
				require.NoError(t, err)
			}
			_, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
			require.NoError(t, err)
			require.False(t, continued)
			requireOwnedLease(t, ctx, f, false)
			tool, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, id)
			require.NoError(t, err)
			if mode == "unfinished tool" {
				require.Equal(t, executionstore.ToolCallStateCompleted, tool.State)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, tool.Outcome)
				require.Equal(t, 1, countAgentWakeups(t, ctx, f.Store, f.AgentID))
			} else {
				require.Equal(t, executionstore.ToolCallStateWaiting, tool.State)
				require.JSONEq(t, `[]`, string(tool.ResultContentParts))
				require.Zero(t, countAgentWakeups(t, ctx, f.Store, f.AgentID))
			}
		})
	}
}

func TestAdvanceOwnedAgentWorkChecksLeaseAfterLockWait(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "owned_expiry_wait")

	createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
	blocker, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	var pid int32
	require.NoError(t, blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	_, err = blocker.Exec(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, f.AgentID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, _, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "", pid)
	_, err = blocker.Exec(ctx, `UPDATE agent_runtime_locks
SET renewed_at=started_at, lease_expires_at=statement_timestamp()-interval '1 microsecond' WHERE id=$1`,
		f.Lock.ID)
	require.NoError(t, err)
	require.NoError(t, blocker.Commit(ctx))
	require.ErrorIs(t, <-done, storeerr.ErrRuntimeLockInactive)
	requireOwnedLease(t, ctx, f, true)
}

func TestAdvanceOwnedAgentWorkCrashRecovery(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "before model preparation"
		if started {
			name = "after model preparation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "owned_crash")

			id := createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
			ownedCompleteTool(t, ctx, f, id)
			work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
			require.NoError(t, err)
			require.True(t, continued)
			var attempt executionstore.ModelCallClaim
			if started {
				attempt = ownedModelClaim(t, ctx, f, work)
			}
			expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
			n, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			replacement, found, err := f.Store.Execution().
				ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			require.NoError(t, err)
			require.True(t, found)
			require.NotEqual(t, f.Lock.ID, replacement.RuntimeLock.ID)
			require.Equal(t, work.Model.TurnID, replacement.Model.TurnID)
			require.Equal(t, work.Model.InputIDs, replacement.Model.InputIDs)
			if started {
				require.Equal(t, executionstore.ModelWorkResume, replacement.Model.Kind)
				require.Equal(t, attempt.Context.ID, replacement.Model.ModelCallContextID)
			} else {
				require.Equal(t, executionstore.ModelWorkContinue, replacement.Model.Kind)
				require.Equal(t, work.Model.SourceModelOutputID, replacement.Model.SourceModelOutputID)
			}
			_, _, err = f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
			require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
		})
	}
}

func TestAdvanceOwnedAgentWorkCommitFailureRollsBackAdmission(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "owned_rollback")

	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	var initialTurns int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_turns WHERE agent_id=$1`, f.AgentID,
	).Scan(&initialTurns))
	_, err := f.Store.pool.Exec(
		ctx,
		`CREATE FUNCTION reject_owned_publication() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'owned commit rejected'; END $$;
CREATE CONSTRAINT TRIGGER reject_owned_publication AFTER UPDATE ON agent_execution_state
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_owned_publication();`,
	)
	require.NoError(t, err)
	_, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
	require.ErrorContains(t, err, "commit advance owned agent work")
	require.False(t, continued)
	requireOwnedLease(t, ctx, f, true)
	var state string
	var turns int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `
SELECT state, (SELECT count(*) FROM agent_turns WHERE agent_id=$2) FROM agent_inputs WHERE id=$1`,
		input.ID, f.AgentID,
	).Scan(&state, &turns))
	require.Equal(t, "received", state)
	require.Equal(t, initialTurns, turns)
	require.Zero(t, countAgentWakeups(t, ctx, f.Store, f.AgentID))
	_, err = f.Store.pool.Exec(ctx, `DROP TRIGGER reject_owned_publication ON agent_execution_state`)
	require.NoError(t, err)
	work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, ownedAdvanceInput(f, true))
	require.NoError(t, err)
	require.True(t, continued)
	require.Equal(t, []uuid.UUID{input.ID}, work.Model.InputIDs)
}
