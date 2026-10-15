//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

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

func reclaimAgentWork(t *testing.T, ctx context.Context, f *processDaemonFixture) executionstore.ClaimedAgentWork {
	t.Helper()
	require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
	work, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, f.AgentID, work.AgentID)
	f.Lock = work.RuntimeLock
	return work
}

func TestClaimNextAgentWorkPrecedence(t *testing.T) {
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
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "claim_precedence")

			item := builtInProcessToolCallBatchItem("first", "read_process")
			if test.toolType != "" {
				item.ToolType = test.toolType
			}
			item.Allowed = !test.blocked
			toolID := createToolCallBatchForProcessTest(t,
				ctx,
				f,
				"claim_precedence",
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
				work := reclaimAgentWork(t, ctx, &f)
				claim := ownedModelClaim(t, ctx, f, work)
				createModelOutputEventForTurnTest(
					t, ctx, f, work.Model.TurnID, claim.Context.ID, "claim_end", "end_turn", "", f.Now,
				)
			}
			var steering, queued executionstore.AgentInputRecord
			if test.steering {
				steering = ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "steering")
			}
			if test.queued {
				queued = ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "queued")
			}
			require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
			work, _, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			if test.kind == executionstore.AgentWorkNone {
				require.ErrorIs(t, err, storeerr.ErrNoClaimableAgentWakeup)
			} else {
				require.NoError(t, err)
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

func TestClaimNextAgentWorkCrashRecovery(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "before model preparation"
		if started {
			name = "after model preparation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "claim_crash")

			id := createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
			ownedCompleteTool(t, ctx, f, id)
			work := reclaimAgentWork(t, ctx, &f)
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
		})
	}
}
