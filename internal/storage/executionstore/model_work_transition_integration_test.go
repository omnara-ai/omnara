//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func fusedModelFixture(
	t *testing.T, ctx context.Context,
) (processDaemonFixture, executionstore.AcceptModelOutputAndAdvanceInput) {
	t.Helper()
	f := newProcessDaemonFixture(t, ctx, "fused_output")
	ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	advance := ownedAdvanceInput(f, true)
	advance.PrepareModel = true
	work, continued, err := f.Store.Execution().AdvanceOwnedAgentWork(ctx, advance)
	require.NoError(t, err)
	require.True(t, continued)
	require.NotNil(t, work.Model.Prepared)
	require.True(t, work.Model.Prepared.Claim.Claimed)
	model := work.Model.Prepared.Claim.Context
	slug := modelProviderSlugForContext(t, ctx, f.Store, testProjectID, f.AgentID, model.ID)
	return f, executionstore.AcceptModelOutputAndAdvanceInput{
		AllowModelWork: true, PrepareModel: true,
		Output: executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID, ModelCallContextID: model.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: slug, ServedProviderModelSlug: slug,
				APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: "default",
				Normalized: modelenvelope.ResponseNormalized{ID: "fused_response", StopReason: modelenvelope.StopReasonEndTurn},
			},
		},
	}
}

func TestAcceptModelOutputAndAdvancePrecedenceAndPreparation(t *testing.T) {
	for _, mode := range []string{"idle", "queued", "steering", "output limit", "budget", "tools before steering"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f, input := fusedModelFixture(t, ctx)
			var queued, steering executionstore.AgentInputRecord
			if mode != "idle" {
				queued = ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "queued")
			}
			if mode == "steering" || mode == "tools before steering" {
				steering = ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "steering")
			}
			if mode == "output limit" || mode == "steering" {
				input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
			}
			if mode == "budget" {
				input.AllowModelWork = false
			}
			if mode == "tools before steering" {
				input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonToolUse
				input.Output.ProviderResponse.Normalized.Content = []modelenvelope.ResponsePart{{
					Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "call",
					ToolName: "read_process", ToolInput: []byte(`{}`),
				}}
				input.ToolCallBindings = []executionstore.ToolCallBindingInput{{ProviderCallID: "call", Type: "built_in"}}
			}
			transition, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
			require.NoError(t, err)
			wantContinued := mode != "idle" && mode != "budget"
			require.Equal(t, wantContinued, transition.Continued)
			requireOwnedLease(t, ctx, f, wantContinued)
			completed, found, err := f.Store.Execution().GetModelCallContext(
				ctx, testProjectID, f.AgentID, input.Output.ModelCallContextID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.ModelCallContextSucceeded, completed.State)
			if !wantContinued {
				return
			}
			work := transition.Work
			require.Equal(t, f.Lock.ID, work.RuntimeLock.ID)
			require.Zero(t, countAgentWakeups(t, ctx, f.Store, f.AgentID))
			if mode == "tools before steering" {
				require.Equal(t, executionstore.AgentWorkTool, work.Kind)
				require.NotNil(t, work.Tool.Prepared)
				require.NotNil(t, work.Tool.Prepared.FirstCall)
				require.Equal(t, "call", work.Tool.Prepared.FirstCall.ProviderCallID)
				require.Equal(t, executionstore.ToolCallStateAwaitingAuthorization, work.Tool.Prepared.FirstCall.State)
				return
			}
			require.Equal(t, executionstore.AgentWorkModel, work.Kind)
			prepared := work.Model.Prepared
			require.NotNil(t, prepared)
			require.True(t, prepared.Claim.Claimed)
			require.NotNil(t, prepared.ContextData)
			require.Equal(t, prepared.Claim.Context.InputEventSequence, prepared.ContextData.InputEventSequence)
			require.NotEmpty(t, prepared.ContextData.Events)
			switch mode {
			case "queued":
				require.Equal(t, []uuid.UUID{queued.ID}, work.Model.InputIDs)
			case "steering":
				require.Equal(t, []uuid.UUID{steering.ID}, work.Model.InputIDs)
			case "output limit":
				require.Equal(t, executionstore.ModelWorkContinue, work.Model.Kind)
				require.Equal(t, input.Output.ModelCallContextID, work.Model.SourceModelCallContextID)
			}
		})
	}
}

func TestAcceptModelOutputAndAdvanceFencing(t *testing.T) {
	for _, mode := range []string{"cancel", "expired", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f, input := fusedModelFixture(t, ctx)
			if mode == "cancel" {
				_, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
			} else {
				expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
				if mode == "replaced" {
					n, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
					require.NoError(t, err)
					require.EqualValues(t, 1, n)
					_, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
					require.NoError(t, err)
					require.True(t, found)
				}
			}
			_, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
			_, found, err := f.Store.Execution().GetModelOutputForContext(
				ctx, testProjectID, f.AgentID, input.Output.ModelCallContextID,
			)
			require.NoError(t, err)
			require.False(t, found)
		})
	}
}

func TestAcceptModelOutputAndAdvanceRollbackAndCrashRecovery(t *testing.T) {
	ctx := t.Context()
	f, input := fusedModelFixture(t, ctx)
	input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
	_, err := f.Store.pool.Exec(ctx, `CREATE FUNCTION reject_fused_preparation() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'reject fused preparation'; END $$;
 CREATE CONSTRAINT TRIGGER reject_fused_preparation AFTER INSERT ON model_call_contexts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_fused_preparation();`)
	require.NoError(t, err)
	_, err = f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
	require.ErrorContains(t, err, "commit accept model output and advance")
	_, found, err := f.Store.Execution().GetModelOutputForContext(
		ctx, testProjectID, f.AgentID, input.Output.ModelCallContextID,
	)
	require.NoError(t, err)
	require.False(t, found)
	original, found, err := f.Store.Execution().GetModelCallContext(
		ctx, testProjectID, f.AgentID, input.Output.ModelCallContextID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelCallContextStarted, original.State)
	requireOwnedLease(t, ctx, f, true)
	_, err = f.Store.pool.Exec(ctx, `DROP TRIGGER reject_fused_preparation ON model_call_contexts`)
	require.NoError(t, err)
	transition, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
	require.NoError(t, err)
	require.True(t, transition.Continued)
	next := transition.Work.Model.Prepared.Claim.Context
	expireAgentRuntimeLockForTest(t, ctx, f.Store, f.Lock.ID)
	n, err := f.Store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	recovered, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelWorkResume, recovered.Model.Kind)
	require.Equal(t, next.ID, recovered.Model.ModelCallContextID)
	require.NotEqual(t, f.Lock.ID, recovered.RuntimeLock.ID)
	input.Output.ModelCallContextID = next.ID
	_, err = f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
}

func TestAcceptModelOutputAndAdvanceReplayRecoversPreparedWorkWithoutDuplicatingOutput(t *testing.T) {
	ctx := t.Context()
	f, input := fusedModelFixture(t, ctx)
	input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
	transition, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
	require.NoError(t, err)
	require.True(t, transition.Continued)
	next := transition.Work.Model.Prepared.Claim.Context
	replay, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
	require.NoError(t, err)
	require.False(t, replay.Continued)
	requireOwnedLease(t, ctx, f, false)
	recovered, found, err := f.Store.Execution().GetModelCallContext(ctx, testProjectID, f.AgentID, next.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelCallContextFailed, recovered.State)
	require.Equal(t, executionstore.ModelCallRecoveryRetry, recovered.RecoveryKind)
	var outputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM model_outputs WHERE agent_id=$1`, f.AgentID,
	).Scan(&outputs))
	require.Equal(t, 1, outputs)
}
