//go:build integration

package agentexecution_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func stepFixture(t *testing.T) (executionFixture, uuid.UUID) {
	t.Helper()
	f := migratedExecutionFixture(t)
	config := storagefixture.SeedAgentConfig(t, t.Context(), storage.NewStore(f.pool).Models(), storage.NewStore(f.pool).Execution(), f.org, f.project,
		"instruction: test\nmodel:\n  provider_config: openai-prod\n  name: test\nevent_webhook:\n  url: https://example.com/events\n  events: [model_output, tool_call_update, tool_result]\n")
	f.config = config.ID
	a := f.create(t, uuid.New(), uuid.Nil)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "work")
	require.NoError(t, u.Commit(t.Context(), "input"))
	return f, a
}

func claimPreparedStep(t *testing.T, f executionFixture) (*agentexecution.Unit, *agentexecution.Handle, agentexecution.ClaimedWork, agentexecution.PreparedModel) {
	t.Helper()
	u, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
	require.NoError(t, err)
	route, err := u.ClaimAgent(t.Context())
	require.NoError(t, err)
	h, err := u.Agent(route)
	require.NoError(t, err)
	work, err := h.Claim(t.Context(), uuid.New(), time.Minute)
	require.NoError(t, err)
	prepared, err := h.PrepareModel(t.Context(), agentexecution.PrepareModelInput{RuntimeLockID: work.RuntimeID, Selected: *work.Selection.Model})
	require.NoError(t, err)
	return u, h, work, prepared
}

func stepToolOutput(prepared agentexecution.PreparedModel, lease uuid.UUID, count int) agentexecution.AcceptOutputInput {
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = nil
	input := agentexecution.AcceptOutputInput{ContextID: prepared.Context.ID, RuntimeLockID: lease, FinishClaim: true}
	for i := range count {
		id := strconv.Itoa(i)
		response.Normalized.Content = append(response.Normalized.Content, modelenvelope.ResponsePart{Type: "tool_call", ProviderCallID: id, ToolName: "list_agents", ToolInput: json.RawMessage(`{}`)})
		input.Tools = append(input.Tools, agentexecution.ToolProposal{ID: uuid.New(), ProviderCallID: id, Type: "built_in"})
	}
	input.Response = response
	return input
}

func stepCounts(t *testing.T, f executionFixture, a uuid.UUID) (int, int, int) {
	t.Helper()
	var outputs, leases, wakeups int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM model_outputs WHERE agent_id=$1),
 (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$1),
 (SELECT count(*) FROM agent_wakeups WHERE agent_id=$1)`, a).Scan(&outputs, &leases, &wakeups))
	return outputs, leases, wakeups
}

func expireStep(t *testing.T, f executionFixture, lease uuid.UUID) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `UPDATE agent_runtime_locks SET started_at=statement_timestamp()-interval '3 minutes',renewed_at=statement_timestamp()-interval '2 minutes',lease_expires_at=statement_timestamp()-interval '1 minute' WHERE id=$1`, lease)
	require.NoError(t, err)
}

func TestPreparedClaimLostAcknowledgementAndCrashRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(strconv.FormatBool(committed), func(t *testing.T) {
			f, a := stepFixture(t)
			u, _, work, prepared := claimPreparedStep(t, f)
			agentexecution.LoseTestCommitAcknowledgement(u, committed)
			require.ErrorIs(t, u.Commit(t.Context(), "claim prepare"), storeerr.ErrCommitOutcomeUnknown)
			var count int
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM model_call_contexts WHERE id=$1 AND state='started'`, prepared.Context.ID).Scan(&count))
			if !committed {
				require.Zero(t, count)
				_, leases, wakes := stepCounts(t, f, a)
				require.Zero(t, leases)
				require.Equal(t, 1, wakes)
				return
			}
			require.Equal(t, 1, count)
			expireStep(t, f, work.RuntimeID)
			u, h := f.handle(t, a)
			reaped, err := h.Reap(t.Context(), work.RuntimeID)
			require.NoError(t, err)
			require.True(t, reaped)
			require.NoError(t, u.Commit(t.Context(), "reap abandoned intent"))
			var state string
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state FROM model_call_contexts WHERE id=$1`, prepared.Context.ID).Scan(&state))
			require.Equal(t, "failed", state)
			var ambiguous bool
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT (error_details->>'outcome_ambiguous')::boolean FROM model_call_contexts WHERE id=$1`, prepared.Context.ID).Scan(&ambiguous))
			require.True(t, ambiguous)
			_, leases, wakes := stepCounts(t, f, a)
			require.Zero(t, leases)
			require.Equal(t, 1, wakes)
		})
	}
}

func TestResultReleaseLostAcknowledgementAndNewerClaim(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(strconv.FormatBool(committed), func(t *testing.T) {
			f, a := stepFixture(t)
			u, _, work, prepared := claimPreparedStep(t, f)
			require.NoError(t, u.Commit(t.Context(), "claim prepare"))
			input := stepToolOutput(prepared, work.RuntimeID, 1)
			u, h := f.handle(t, a)
			accepted, err := h.AcceptOutput(t.Context(), input)
			require.NoError(t, err)
			agentexecution.LoseTestCommitAcknowledgement(u, committed)
			require.ErrorIs(t, u.Commit(t.Context(), "result release"), storeerr.ErrCommitOutcomeUnknown)
			outputs, leases, wakes := stepCounts(t, f, a)
			if !committed {
				require.Zero(t, outputs)
				require.Equal(t, 1, leases)
				require.Zero(t, wakes)
				u, h = f.handle(t, a)
				_, found, err := h.ResolveOutput(t.Context(), input)
				require.NoError(t, err)
				require.False(t, found)
				require.NoError(t, u.Rollback(t.Context()))
				expireStep(t, f, work.RuntimeID)
				u, h = f.handle(t, a)
				reaped, err := h.Reap(t.Context(), work.RuntimeID)
				require.NoError(t, err)
				require.True(t, reaped)
				require.NoError(t, u.Commit(t.Context(), "reap before result"))
				return
			}
			require.Equal(t, 1, outputs)
			require.Zero(t, leases)
			require.Equal(t, 1, wakes)
			var deliveries int
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1`, a).Scan(&deliveries))
			require.Equal(t, 2, deliveries)
			u, h = f.handle(t, a)
			newer, err := h.Claim(t.Context(), uuid.New(), time.Minute)
			require.NoError(t, err)
			require.NoError(t, u.Commit(t.Context(), "new tool claim"))
			u, h = f.handle(t, a)
			replay, found, err := h.ResolveOutput(t.Context(), input)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, accepted.ID, replay.ID)
			require.False(t, replay.Created)
			require.NoError(t, u.Rollback(t.Context()))
			u, h = f.handle(t, a)
			replay, err = h.AcceptOutput(t.Context(), input)
			require.NoError(t, err)
			require.False(t, replay.Created)
			require.NoError(t, u.Rollback(t.Context()))
			var current uuid.UUID
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT id FROM agent_runtime_locks WHERE agent_id=$1`, a).Scan(&current))
			require.Equal(t, newer.RuntimeID, current)
			var after int
			require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1`, a).Scan(&after))
			require.Equal(t, deliveries, after)
			u, h = f.handle(t, a)
			reaped, err := h.Reap(t.Context(), work.RuntimeID)
			require.NoError(t, err)
			require.False(t, reaped)
			require.NoError(t, u.Rollback(t.Context()))
			input.Tools[0].ID = uuid.New()
			u, h = f.handle(t, a)
			_, _, err = h.ResolveOutput(t.Context(), input)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			require.NoError(t, u.Rollback(t.Context()))
		})
	}
}

func TestStepCancellationCompletionLockOrders(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelFirst), func(t *testing.T) {
			f, a := stepFixture(t)
			u, _, work, prepared := claimPreparedStep(t, f)
			require.NoError(t, u.Commit(t.Context(), "claim"))
			input := stepToolOutput(prepared, work.RuntimeID, 1)
			u, h := f.handle(t, a)
			var pid int32
			require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid))
			done := make(chan error, 1)
			run := func(cancel bool, h *agentexecution.Handle) error {
				if cancel {
					_, err := h.Cancel(t.Context(), agentexecution.CancelInput{Reason: "test", Message: "cancel"})
					return err
				}
				_, err := h.AcceptOutput(t.Context(), input)
				return err
			}
			require.NoError(t, run(cancelFirst, h))
			go func() {
				other, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = other.Rollback(context.Background()) }()
				handle, err := agentexecution.LockTestAgent(t.Context(), other, f.project, a, nil)
				if err == nil {
					err = run(!cancelFirst, handle)
				}
				if err == nil {
					err = other.Commit(t.Context(), "racing transition")
				}
				done <- err
			}()
			integrationdb.WaitForLockWaitBlockedBy(t, t.Context(), f.pool, "", pid)
			require.NoError(t, u.Commit(t.Context(), "winning transition"))
			err := <-done
			if cancelFirst {
				require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
			} else {
				require.NoError(t, err)
			}
			outputs, _, _ := stepCounts(t, f, a)
			if cancelFirst {
				require.Zero(t, outputs)
			} else {
				require.Equal(t, 1, outputs)
			}
		})
	}
}

func TestRenewalWaitsForSuccessfulStepRelease(t *testing.T) {
	f, a := stepFixture(t)
	u, _, work, prepared := claimPreparedStep(t, f)
	require.NoError(t, u.Commit(t.Context(), "claim"))
	u, h := f.handle(t, a)
	_, err := h.AcceptOutput(t.Context(), stepToolOutput(prepared, work.RuntimeID, 1))
	require.NoError(t, err)
	var pid int32
	require.NoError(t, u.DB().QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid))
	done := make(chan error, 1)
	go func() {
		other, err := agentexecution.BeginTestUnit(t.Context(), agentexecution.NewCell(f.pool, nil, nil))
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = other.Rollback(context.Background()) }()
		_, err = other.RenewRuntime(t.Context(), agentexecution.AgentRoute{ProjectID: f.project, AgentID: a}, work.RuntimeID, time.Minute)
		done <- err
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, t.Context(), f.pool, "", pid)
	require.NoError(t, u.Commit(t.Context(), "release"))
	require.ErrorIs(t, <-done, storeerr.ErrRuntimeLockInactive)
	outputs, leases, wakes := stepCounts(t, f, a)
	require.Equal(t, 1, outputs)
	require.Zero(t, leases)
	require.Equal(t, 1, wakes)
}
