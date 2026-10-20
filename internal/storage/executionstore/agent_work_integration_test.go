//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	require.NotNil(t, work.Model.Prepared)
	require.True(t, work.Model.Prepared.Claim.Claimed)
	return work.Model.Prepared.Claim
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
	for _, complete := range []bool{false, true} {
		name := "runnable tools before steering"
		if complete {
			name = "steering before model continuation"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "claim_precedence")
			toolID := createToolCallForProcessTest(t, ctx, f, "claim_precedence", "read_process")
			if complete {
				ownedCompleteTool(t, ctx, f, toolID)
			}
			steering := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "steering")
			queued := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "queued")
			work := reclaimAgentWork(t, ctx, &f)
			require.Zero(t, countAgentWakeups(t, ctx, f.Store, f.AgentID))
			if complete {
				require.Equal(t, executionstore.AgentWorkModel, work.Kind)
				require.Equal(t, executionstore.ModelWorkStart, work.Model.Kind)
				require.Equal(t, []uuid.UUID{steering.ID}, work.Model.InputIDs)
			} else {
				require.Equal(t, executionstore.AgentWorkTool, work.Kind)
			}
			var state string
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id=$1`, queued.ID).Scan(&state))
			require.Equal(t, "received", state)
		})
	}
}

func TestClaimNextAgentWorkCrashRecovery(t *testing.T) {
	for _, name := range []string{"before provider request", "after provider request", "release"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "claim_crash")

			if name == "release" {
				ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
			} else {
				id := createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
				ownedCompleteTool(t, ctx, f, id)
			}
			work := reclaimAgentWork(t, ctx, &f)
			attempt := ownedModelClaim(t, ctx, f, work)
			if name == "release" {
				require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
				requireAgentWakeupCoverage(t, ctx, f.Store, testProjectID, f.AgentID)
				require.Equal(t, 1, countAgentWakeups(t, ctx, f.Store, f.AgentID))
			} else {
				expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
				n, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
				require.NoError(t, err)
				require.EqualValues(t, 1, n)
			}
			replacement, found, err := f.Store.Execution().
				ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			require.NoError(t, err)
			require.True(t, found)
			require.NotEqual(t, f.Lock.ID, replacement.RuntimeLock.ID)
			require.Equal(t, work.Model.TurnID, replacement.Model.TurnID)
			require.Equal(t, work.Model.InputIDs, replacement.Model.InputIDs)
			require.Equal(t, executionstore.ModelWorkResume, replacement.Model.Kind)
			require.Equal(t, attempt.Context.ID, replacement.Model.ModelCallContextID)
		})
	}
}

func TestClaimNextAgentWorkReturnsDurablePreparedModel(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepared_claim")
	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "prepared")
	work := reclaimAgentWork(t, ctx, &f)
	require.NotNil(t, work.Model.Prepared)
	prepared := work.Model.Prepared
	require.True(t, prepared.Claim.Claimed)
	require.True(t, prepared.Claim.Created)
	require.Equal(t, f.Lock.ID, prepared.Claim.Context.RuntimeLockID)
	require.Equal(t, work.OrgID, prepared.Claim.Context.OrgID)
	require.Equal(t, f.AgentID, work.RootAgentID)
	require.Equal(t, prepared.Snapshot.AgentConfig.ID, prepared.Claim.Context.AgentConfigID)
	require.Equal(t, prepared.Snapshot.InputEventSequence, prepared.Claim.Context.InputEventSequence)
	require.Equal(t, []uuid.UUID{input.ID}, work.Model.InputIDs)
	saved, found, err := f.Store.Execution().GetModelCallContext(ctx, testProjectID, f.AgentID, prepared.Claim.Context.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, prepared.Claim.Context, saved)
}
