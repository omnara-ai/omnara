//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
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

func TestAcceptModelOutputAndAdvanceCrashRecovery(t *testing.T) {
	ctx := t.Context()
	f, input := fusedModelFixture(t, ctx)
	input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
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

func TestAcceptModelOutputFallbackRechecksFence(t *testing.T) {
	for _, mode := range []string{"cancel", "expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f, input := fusedModelFixture(t, ctx)
			input.Output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonMaxTokens
			gate, err := f.Store.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = gate.Rollback(ctx) }()
			var gatePID int32
			require.NoError(t, gate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID))
			_, err = gate.Exec(ctx, `SELECT pg_advisory_xact_lock(63109)`)
			require.NoError(t, err)
			_, err = f.Store.pool.Exec(ctx, `
CREATE FUNCTION reject_successor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM model_outputs WHERE agent_id=NEW.agent_id) THEN
  PERFORM pg_advisory_xact_lock(63109);
  RAISE EXCEPTION 'reject successor';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER reject_successor AFTER INSERT ON model_call_contexts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_successor()`)
			require.NoError(t, err)
			type acceptance struct {
				transition executionstore.OwnedAgentWorkTransition
				err        error
			}
			accepted := make(chan acceptance, 1)
			go func() {
				transition, err := f.Store.Execution().AcceptModelOutputAndAdvance(ctx, input)
				accepted <- acceptance{transition, err}
			}()
			integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "commit", gatePID)
			var acceptPID int32
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity
WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))`, gatePID).Scan(&acceptPID))
			fenced := make(chan error, 1)
			actor := mustOmnaraActorParams(t, f.UserID)
			go func() {
				if mode == "cancel" {
					_, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
						ProjectID: testProjectID, AgentID: f.AgentID, Actor: actor,
					})
					fenced <- err
				} else {
					_, err := f.Store.pool.Exec(ctx, `UPDATE agent_runtime_locks
SET renewed_at=started_at,lease_expires_at=statement_timestamp()-interval '1 microsecond' WHERE id=$1`, f.Lock.ID)
					fenced <- err
				}
			}()
			integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "", acceptPID)
			require.NoError(t, gate.Rollback(ctx))
			require.NoError(t, <-fenced)
			result := <-accepted
			require.ErrorIs(t, result.err, storeerr.ErrRuntimeLockInactive)
			var cause *pgconn.PgError
			require.ErrorAs(t, result.transition.Fallback, &cause)
			require.Equal(t, "reject successor", cause.Message)
			var outputs int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM model_outputs WHERE agent_id=$1`, f.AgentID).Scan(&outputs))
			require.Zero(t, outputs)
		})
	}
}
