//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/cleanupcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPrepareNormalModelCallUsesSnapshotAfterLockWait(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepare_snapshot")
	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	blocker, err := f.Store.Execution().IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(ctx) }()
	_, err = agentexecution.LockTestAgent(ctx, blocker, testProjectID, f.AgentID, nil)
	require.NoError(t, err)
	var pid int32
	require.NoError(t, blocker.DB().QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	type result struct {
		prepared executionstore.PreparedNormalModelCall
		err      error
	}
	done := make(chan result, 1)
	go func() {
		prepared, err := f.Store.Execution().
			PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
				ProjectID:       testProjectID,
				AgentID:         f.AgentID,
				RuntimeLockID:   f.Lock.ID,
				OpeningInputIDs: []uuid.UUID{input.ID},
			})
		done <- result{prepared, err}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.Store.pool, "", pid)
	admitted, err := executionstore.IntegrationAdmitInputs(ctx, blocker, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Len(t, admitted.Events, 1)
	sequence := admitted.Events[0].Sequence
	require.NoError(t, blocker.Commit(ctx, "admit after wait"))
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.prepared.Claim.Claimed)
	require.Equal(t, sequence, got.prepared.Snapshot.InputEventSequence)
	require.Equal(t, sequence, got.prepared.Claim.Context.InputEventSequence)
	require.Equal(t, got.prepared.Snapshot.AgentConfig.ID, got.prepared.Claim.Context.AgentConfigID)
}

func TestPrepareNormalModelCallRollbackAndReplay(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "prepare_rollback")
	input := ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
	reclaimUnpreparedAgentWork(t, ctx, &f)
	prepare := executionstore.PrepareNormalModelCallInput{
		ProjectID:       testProjectID,
		AgentID:         f.AgentID,
		RuntimeLockID:   f.Lock.ID,
		OpeningInputIDs: []uuid.UUID{input.ID},
	}
	_, err := f.Store.pool.Exec(
		ctx,
		`CREATE FUNCTION reject_prepared_context() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'reject'; END $$;
CREATE CONSTRAINT TRIGGER reject_prepared_context AFTER INSERT ON model_call_contexts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_prepared_context();`,
	)
	require.NoError(t, err)
	_, err = f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.ErrorContains(t, err, "commit prepare model call")
	var count int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1`, f.AgentID,
	).Scan(&count))
	require.Zero(t, count)
	_, err = f.Store.pool.Exec(ctx, `DROP TRIGGER reject_prepared_context ON model_call_contexts`)
	require.NoError(t, err)
	prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	require.Equal(t, prepared.Snapshot.AgentConfig.ID, prepared.Claim.Context.AgentConfigID)
	require.Equal(t, prepared.Snapshot.InputEventSequence, prepared.Claim.Context.InputEventSequence)
	replay, err := f.Store.Execution().PrepareNormalModelCall(ctx, prepare)
	require.NoError(t, err)
	require.False(t, replay.Claim.Created)
	require.False(t, replay.Claim.Claimed)
	require.Equal(t, prepared.Claim.Context.ID, replay.Claim.Context.ID)
}

func TestPrepareNormalModelCallValidatesOwnershipAndSelectedWork(t *testing.T) {
	for _, mode := range []string{
		"outside opening", "subset", "duplicate", "opening inputs", "missing source", "wrong source", "wrong context",
	} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "prepare_validation")
			initial := mode == "outside opening" || mode == "subset" || mode == "duplicate"
			var work executionstore.ClaimedAgentWork
			if initial {
				first := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "first")
				second := ownedInput(t, ctx, f, executionstore.DeliveryModeSteering, "second")
				work = reclaimUnpreparedAgentWork(t, ctx, &f)
				require.Equal(t, []uuid.UUID{first.ID, second.ID}, work.Model.InputIDs)
				admitted := work.Model.AdmittedInputTurn
				require.Len(t, admitted.Events, 2)
				turns, err := f.Store.Execution().ListAgentTurnsForRead(ctx, testProjectID, f.AgentID, 0, 1)
				require.NoError(t, err)
				require.Len(t, turns, 1)
				require.Equal(t, admitted.Turn.ID, turns[0].ID)
				require.True(t, turns[0].StartedAt.Equal(admitted.Events[0].At))
				require.True(t, turns[0].UpdatedAt.Equal(admitted.Events[1].At))
			} else {
				id := createToolCallForProcessTest(t, ctx, f, "tool", "read_process")
				ownedCompleteTool(t, ctx, f, id)
				work = reclaimUnpreparedAgentWork(t, ctx, &f)
			}
			input := executionstore.PrepareNormalModelCallInput{
				ProjectID: testProjectID, AgentID: f.AgentID, RuntimeLockID: f.Lock.ID,
				OpeningInputIDs:          work.Model.InputIDs,
				SourceModelCallContextID: work.Model.SourceModelCallContextID,
				SourceModelOutputID:      work.Model.SourceModelOutputID,
			}
			invalid := input
			switch mode {
			case "outside opening":
				var configInput uuid.UUID
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT id FROM agent_inputs WHERE agent_id=$1 AND input_kind='config_change' LIMIT 1`, f.AgentID).Scan(&configInput))
				invalid.OpeningInputIDs = []uuid.UUID{configInput}
			case "subset":
				invalid.OpeningInputIDs = input.OpeningInputIDs[:1]
			case "duplicate":
				invalid.OpeningInputIDs = []uuid.UUID{input.OpeningInputIDs[0], input.OpeningInputIDs[0]}
			case "opening inputs":
				invalid.OpeningInputIDs = []uuid.UUID{uuid.New()}
			case "missing source":
				invalid.SourceModelCallContextID, invalid.SourceModelOutputID = uuid.Nil, uuid.Nil
			case "wrong source":
				invalid.SourceModelOutputID = uuid.New()
			case "wrong context":
				invalid.SourceModelCallContextID = uuid.New()
			}
			_, err := f.Store.Execution().PrepareNormalModelCall(ctx, invalid)
			require.ErrorIs(t, err, storeerr.ErrAgentNotAdvanceable)
			var count int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND state='started'`, f.AgentID).Scan(&count))
			require.Zero(t, count)
			watermark, err := f.Store.Execution().MaxEventSequence(ctx, testProjectID, f.AgentID)
			require.NoError(t, err)
			if initial {
				require.Equal(t, work.Model.AdmittedInputTurn.Events[1].Sequence, watermark)
			}
			prepared, err := f.Store.Execution().PrepareNormalModelCall(ctx, input)
			require.NoError(t, err)
			require.True(t, prepared.Claim.Created)
			require.True(t, prepared.Claim.Claimed)
			require.Equal(t, watermark, prepared.Claim.Context.InputEventSequence)
			replay, err := f.Store.Execution().PrepareNormalModelCall(ctx, input)
			require.NoError(t, err)
			require.Equal(t, prepared.Claim.Context.ID, replay.Claim.Context.ID)
			require.False(t, replay.Claim.Created)
			require.False(t, replay.Claim.Claimed)
		})
	}
}

func reclaimUnpreparedAgentWork(
	t *testing.T, ctx context.Context, f *processDaemonFixture,
) executionstore.ClaimedAgentWork {
	t.Helper()
	require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
	work, found, err := f.Store.Execution().IntegrationClaimUnprepared(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	f.Lock = work.RuntimeLock
	return work
}

type resolutionFaultTx struct {
	pgx.Tx
	resolve func(context.Context) error
}

func (tx resolutionFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := tx.resolve(ctx); err != nil {
		return stepErrorRow{err}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestClaimStoreLostAcknowledgementDoesNotReturnExecutableWork(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[committed], func(t *testing.T) {
			ctx := t.Context()
			f := newProcessDaemonFixture(t, ctx, "claim_store_ack")
			ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
			require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
			armed := &atomic.Bool{}
			armed.Store(true)
			f.Store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
				return stepFaultTx{Tx: tx, armed: armed, committed: committed}
			})
			claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			require.ErrorIs(t, err, storeerr.ErrCommitOutcomeUnknown)
			require.False(t, found)
			require.Equal(t, executionstore.ClaimedAgentWork{}, claim)
			var attempts, leases int
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM model_call_contexts WHERE agent_id=$1 AND state='started'),
 (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$1)`, f.AgentID).Scan(&attempts, &leases))
			want := 0
			if committed {
				want = 1
			}
			require.Equal(t, want, attempts)
			require.Equal(t, want, leases)
		})
	}
}

func TestModelOutputStoreAutomaticallyResolvesUncertainCommit(t *testing.T) {
	for _, kind := range []string{"model", "built_in", "custom", "mcp"} {
		tool := kind != "model"
		for _, mode := range []string{"before commit", "resolved", "resolution unavailable", "drain canceled"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx := t.Context()
				f := newProcessDaemonFixture(t, ctx, "output_store_ack")
				ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
				require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
				claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
				require.NoError(t, err)
				require.True(t, found)
				require.NotNil(t, claim.Model.Prepared)
				require.Equal(t, f.AgentID, claim.RootAgentID)
				input := executionstore.RecordModelOutputAndCompleteContextInput{
					RootAgentID: claim.RootAgentID, FinishClaim: true, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
					RuntimeLockID: claim.RuntimeLock.ID, ModelCallContextID: claim.Model.Prepared.Claim.Context.ID,
					ProviderRequestID: "ack-request",
					ProviderResponse: modelenvelope.ResponseEnvelope{
						InputFingerprint: strings.Repeat("a", 64),
						RequestedProviderModelSlug: modelProviderSlugForContext(
							t, ctx, f.Store, testProjectID, f.AgentID, claim.Model.Prepared.Claim.Context.ID),
						APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
						Normalized: modelenvelope.ResponseNormalized{ID: "ack-response", StopReason: modelenvelope.StopReasonEndTurn,
							Content: []modelenvelope.ResponsePart{{Type: modelenvelope.ResponsePartTypeText, Text: "done"}}},
					},
				}
				bindings := []executionstore.ToolCallBindingInput{{ID: uuid.New(), ProviderCallID: "ack-call", Type: kind}}
				if tool {
					input.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonToolUse
					input.ProviderResponse.Normalized.Content = []modelenvelope.ResponsePart{{
						Type:           modelenvelope.ResponsePartTypeToolCall,
						ProviderCallID: "ack-call", ToolName: "list_agents", ToolInput: json.RawMessage(`{}`)}}
				}
				complete := func(ctx context.Context) (uuid.UUID, error) {
					if tool {
						event, _, err := f.Store.Execution().RecordToolCallSourceAndCompleteContext(
							ctx, executionstore.RecordToolCallSourceAndCompleteContextInput{
								RootAgentID: input.RootAgentID, FinishClaim: true, ProjectID: input.ProjectID, AgentID: input.AgentID,
								RuntimeLockID: input.RuntimeLockID, ModelCallContextID: input.ModelCallContextID,
								ProviderRequestID: input.ProviderRequestID, ProviderResponse: input.ProviderResponse,
								ToolCallBindings: bindings,
							})
						return event.ID, err
					}
					event, err := f.Store.Execution().RecordModelOutputAndCompleteContext(ctx, input)
					return event.ID, err
				}
				armed := &atomic.Bool{}
				armed.Store(true)
				executionCtx, cancelExecution := context.WithCancel(ctx)
				defer cancelExecution()
				drainCtx, endDrain := context.WithCancel(ctx)
				defer endDrain()
				executionCtx = cleanupcontext.WithDrain(executionCtx, drainCtx)
				resolving := make(chan struct{}, 1)
				var resolveCalled atomic.Bool
				f.Store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
					if armed.Load() {
						return stepFaultTx{Tx: tx, armed: armed, committed: mode != "before commit", after: cancelExecution}
					}
					return resolutionFaultTx{Tx: tx, resolve: func(ctx context.Context) error {
						if !resolveCalled.CompareAndSwap(false, true) {
							return nil
						}
						resolving <- struct{}{}
						if mode == "resolution unavailable" {
							return io.ErrClosedPipe
						}
						if mode == "drain canceled" {
							<-ctx.Done()
							return ctx.Err()
						}
						return ctx.Err()
					}}
				})
				done := integrationdb.RunAsync(func() (uuid.UUID, error) { return complete(executionCtx) })
				select {
				case <-resolving:
				case <-time.After(3 * time.Second):
					t.Fatal("store did not resolve uncertain model completion")
				}
				if mode == "drain canceled" {
					endDrain()
				}
				outcome := integrationdb.Await(t, done, "model output resolution")
				if mode == "resolved" {
					require.NoError(t, outcome.Err)
					require.NotEqual(t, uuid.Nil, outcome.Value)
				} else {
					require.ErrorIs(t, outcome.Err, storeerr.ErrCommitOutcomeUnknown)
				}
				if mode == "drain canceled" {
					require.ErrorIs(t, outcome.Err, context.Canceled)
				}
				var outputs, leases int
				require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM model_outputs WHERE agent_id=$1),
 (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$1)`, f.AgentID).Scan(&outputs, &leases))
				if mode == "before commit" {
					require.Zero(t, outputs)
					require.Equal(t, 1, leases)
					return
				}
				require.Equal(t, 1, outputs)
				require.Zero(t, leases)
				if !tool {
					ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "next")
				}
				newer, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
				require.NoError(t, err)
				require.True(t, found)
				replayed, err := complete(ctx)
				require.NoError(t, err)
				require.NotEqual(t, uuid.Nil, replayed)
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT count(*) FROM agent_runtime_locks WHERE id=$1`, newer.RuntimeLock.ID).Scan(&leases))
				require.Equal(t, 1, leases)
			})
		}
	}
}

func TestModelOutputOverlappingReplayAudit(t *testing.T) {
	for _, kind := range []string{"model", "built_in", "custom", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			f := newProcessDaemonFixture(t, ctx, "model-overlap-audit")
			ownedInput(t, ctx, f, executionstore.DeliveryModeQueued, "initial")
			require.NoError(t, f.Store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, f.AgentID, f.Lock.ID))
			claim, found, err := f.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
			require.NoError(t, err)
			require.True(t, found)
			input := executionstore.RecordModelOutputAndCompleteContextInput{
				RootAgentID: claim.RootAgentID, FinishClaim: true, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
				RuntimeLockID: claim.RuntimeLock.ID, ModelCallContextID: claim.Model.Prepared.Claim.Context.ID,
				ProviderRequestID: "overlap-request",
				ProviderResponse: modelenvelope.ResponseEnvelope{
					RequestedProviderModelSlug: modelProviderSlugForContext(t, ctx, f.Store, testProjectID,
						f.AgentID, claim.Model.Prepared.Claim.Context.ID),
					APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
					Normalized: modelenvelope.ResponseNormalized{ID: "overlap-response", StopReason: modelenvelope.StopReasonEndTurn,
						Content: []modelenvelope.ResponsePart{{Type: modelenvelope.ResponsePartTypeText, Text: "done"}}},
				},
			}
			if kind != "model" {
				input.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonToolUse
				input.ProviderResponse.Normalized.Content = []modelenvelope.ResponsePart{{
					Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "overlap-call",
					ToolName: "list_agents", ToolInput: json.RawMessage(`{}`),
				}}
			}
			bindings := []executionstore.ToolCallBindingInput{{ID: uuid.New(), ProviderCallID: "overlap-call", Type: kind}}
			complete := func() (uuid.UUID, error) {
				if kind == "model" {
					event, err := f.Store.Execution().RecordModelOutputAndCompleteContext(ctx, input)
					return event.ID, err
				}
				event, _, err := f.Store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
					executionstore.RecordToolCallSourceAndCompleteContextInput{
						RootAgentID: input.RootAgentID, FinishClaim: true, ProjectID: input.ProjectID, AgentID: input.AgentID,
						RuntimeLockID: input.RuntimeLockID, ModelCallContextID: input.ModelCallContextID,
						ProviderRequestID: input.ProviderRequestID, ProviderResponse: input.ProviderResponse, ToolCallBindings: bindings,
					})
				return event.ID, err
			}
			entered, proceed := make(chan struct{}), make(chan struct{})
			armed := &atomic.Bool{}
			armed.Store(true)
			f.Store.Execution().IntegrationWrapTransactions(func(tx pgx.Tx) pgx.Tx {
				return lifecycleBarrierTx{Tx: tx, entered: entered, proceed: proceed, armed: armed}
			})
			done := integrationdb.RunAsync(complete)
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("model output did not reach agent lock")
			}
			original, err := complete()
			require.NoError(t, err)
			newer, err := f.Store.Execution().AcquireAgentRuntimeLock(ctx, testProjectID, f.AgentID,
				testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
			require.NoError(t, err)
			close(proceed)
			replayed := integrationdb.Await(t, done, "model output replay")
			require.NoError(t, replayed.Err)
			require.Equal(t, original, replayed.Value)
			var outputs, leases int
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM model_outputs WHERE model_call_context_id=$1),
 (SELECT count(*) FROM agent_runtime_locks WHERE id=$2)`, input.ModelCallContextID, newer.ID).Scan(&outputs, &leases))
			require.Equal(t, 1, outputs)
			require.Equal(t, 1, leases)
		})
	}
}
