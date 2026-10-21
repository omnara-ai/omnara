//go:build integration

package agentexecution_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func commandFixture(t *testing.T) (executionFixture, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := migratedExecutionFixture(t)
	a := f.create(t, uuid.New(), uuid.Nil)
	lease := uuid.New()
	_, err := f.pool.Exec(
		t.Context(),
		`INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,$3,statement_timestamp(),statement_timestamp(),statement_timestamp()+interval '90 seconds')`,
		lease,
		a,
		uuid.New(),
	)
	require.NoError(t, err)
	return f, a, lease
}

func assertCommandState(
	t *testing.T,
	u *agentexecution.Unit,
	h *agentexecution.Handle,
) agentexecution.ExecutionSnapshot {
	t.Helper()
	got, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	reloaded, err := agentexecution.ReloadExecution(t.Context(), h)
	require.NoError(t, err)
	require.Equal(t, reloaded.View, got.View)
	rebuilt, err := h.ReconstructExecution(t.Context())
	require.NoError(t, err)
	left, right := got.Head, rebuilt.Head
	if got.Selection.Work == agentexecution.WorkInput || got.Selection.Work == agentexecution.WorkTool {
		left.LogicalReadyAt = nil
		right.LogicalReadyAt = nil
	}
	require.Equal(t, left, right)

	_, err = u.DB().Exec(t.Context(), "SET CONSTRAINTS ALL IMMEDIATE")
	require.NoError(t, err)
	_, err = u.DB().Exec(t.Context(), "SET CONSTRAINTS ALL DEFERRED")
	require.NoError(t, err)
	return got
}

func receiveCommand(
	t *testing.T,
	h *agentexecution.Handle,
	mode, text string,
) agentexecution.ReceivedContent {
	t.Helper()
	row, err := h.ReceiveContent(
		t.Context(),
		agentexecution.ReceiveContentInput{DeliveryMode: mode,
			Content: []agentexecution.Content{{Kind: "text", Text: text}}},
	)
	require.NoError(t, err)
	return row
}

func prepareCommand(t *testing.T, h *agentexecution.Handle, lease uuid.UUID) agentexecution.PreparedModel {
	t.Helper()
	snapshot, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	require.NotNil(t, snapshot.Selection.Model)
	prepared, err := h.PrepareModel(
		t.Context(),
		agentexecution.PrepareModelInput{RuntimeLockID: lease, Selected: *snapshot.Selection.Model},
	)
	require.NoError(t, err)
	require.True(t, prepared.Claimed)
	return prepared
}

func commandResponse(reason modelenvelope.StopReason) modelenvelope.ResponseEnvelope {
	return modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "test",
		APIFormat:                  "openai-responses",
		APIVariant:                 "default",
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "response",
			StopReason: reason,
			Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "answer"}},
		},
	}
}

func TestExecutionSemanticCommands(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	_, err := h.ActivateConfig(
		t.Context(),
		agentexecution.ActivateConfigInput{ConfigID: f.config, IdempotencyKey: "initial"},
	)
	require.NoError(t, err)
	assertCommandState(t, u, h)
	first := receiveCommand(t, h, "queued", "first")
	second := receiveCommand(t, h, "queued", "second")
	assertCommandState(t, u, h)
	changed, err := h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{ID: second.ID, DeliveryMode: "steering"},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assertCommandState(t, u, h)
	changed, err = h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{ID: first.ID, DeliveryMode: "queued", Cancel: true},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assertCommandState(t, u, h)
	admitted, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	require.Len(t, admitted.Inputs, 1)
	require.Equal(t, second.ID, admitted.Inputs[0].ID)
	snapshot := assertCommandState(t, u, h)
	selected := *snapshot.Selection.Model
	prepared := prepareCommand(t, h, lease)
	require.Equal(t, []uuid.UUID{second.ID}, prepared.Context.Opening.InputIDs)
	assertCommandState(t, u, h)
	replay, err := h.PrepareModel(
		t.Context(),
		agentexecution.PrepareModelInput{RuntimeLockID: lease, Selected: selected},
	)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, prepared.Context.ID, replay.Context.ID)
	failure := agentexecution.ModelFailure{
		ContextID:     prepared.Context.ID,
		RuntimeLockID: lease,
		Recovery:      agentexecution.RecoveryRetry,
		ErrorKind:     "transient",
		ErrorMessage:  "retry",
		RetryDelay:    time.Second,
	}
	changed, err = h.FailModel(t.Context(), failure)
	require.NoError(t, err)
	require.True(t, changed)
	waiting := assertCommandState(t, u, h)
	require.Equal(t, agentexecution.WaitModelDeadline, waiting.Selection.Wait)
	changed, err = h.FailModel(t.Context(), failure)
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, u.Commit(t.Context(), "semantic retry"))
	u, h = f.handle(t, a)
	require.Eventually(t, func() bool {
		snapshot, err := h.LoadExecution(t.Context())
		return err == nil && snapshot.Selection.Work == agentexecution.WorkModel
	}, 2*time.Second, 10*time.Millisecond)
	retried := prepareCommand(t, h, lease)
	require.Equal(t, int32(2), retried.Context.Attempt)
	require.Equal(t, prepared.Context.Opening, retried.Context.Opening)
	assertCommandState(t, u, h)
	input := agentexecution.AcceptOutputInput{
		ContextID:     retried.Context.ID,
		RuntimeLockID: lease,
		Response:      commandResponse(modelenvelope.StopReasonMaxTokens),
	}
	beforeOutput := slices.Clone(u.Notifications().AgentEvents()[a])
	accepted, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, accepted.Created)
	assertCommandState(t, u, h)
	afterOutput := slices.Clone(u.Notifications().AgentEvents()[a])
	require.NotEqual(t, beforeOutput, afterOutput)
	replayed, err := h.AcceptOutput(t.Context(), input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, accepted.ID, replayed.ID)
	require.Equal(t, afterOutput, slices.Clone(u.Notifications().AgentEvents()[a]))
	continuation := prepareCommand(t, h, lease)
	assertCommandState(t, u, h)
	_, err = h.AcceptOutput(
		t.Context(),
		agentexecution.AcceptOutputInput{ContextID: continuation.Context.ID, RuntimeLockID: lease,
			Response: commandResponse(modelenvelope.StopReasonEndTurn)},
	)
	require.NoError(t, err)
	final := assertCommandState(t, u, h)
	require.Equal(t, agentexecution.WorkNone, final.Selection.Work)
	require.NoError(t, u.Commit(t.Context(), "semantic completion"))
}

func TestExecutionCompactionCommands(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	_, err := h.ActivateConfig(t.Context(), agentexecution.ActivateConfigInput{ConfigID: f.config})
	require.NoError(t, err)
	receiveCommand(t, h, "queued", "input")
	_, err = h.AdmitInputs(t.Context())
	require.NoError(t, err)
	normal := prepareCommand(t, h, lease)
	start, err := h.BeginCompaction(
		t.Context(),
		agentexecution.BeginCompactionInput{SourceEnd: normal.Context.InputEventSequence,
			Failure: agentexecution.ModelFailure{
				Recovery:      agentexecution.RecoveryCompact,
				ContextID:     normal.Context.ID,
				RuntimeLockID: lease,
				ErrorKind:     "context_length",
				ErrorMessage:  "compact",
			}},
	)
	require.NoError(t, err)
	require.True(t, start.Model.Created)
	assertCommandState(t, u, h)
	_, err = h.FailModel(t.Context(), agentexecution.ModelFailure{ContextID: start.Model.Context.ID,
		RuntimeLockID: lease, Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"})
	require.NoError(t, err)
	retry := prepareCommand(t, h, lease)
	snapshot := assertCommandState(t, u, h)
	require.Equal(t, retry.Context.ID, snapshot.Head.CompactionContextID)
	require.Equal(t, start.Model.Context.Opening, retry.Context.Opening)
	reduced, err := h.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{SourceEnd: 1,
		Failure: agentexecution.ModelFailure{
			Recovery:      agentexecution.RecoveryReduce,
			ContextID:     retry.Context.ID,
			RuntimeLockID: lease,
			ErrorKind:     "context_length",
			ErrorMessage:  "reduce",
		}})
	require.NoError(t, err)
	require.True(t, reduced.Model.Created)
	assertCommandState(t, u, h)
	cp := agentexecution.PublishCheckpointInput{
		ContextID:     reduced.Model.Context.ID,
		RuntimeLockID: lease,
		Summary:       "summary",
		Evidence:      agentexecution.ModelEvidence{APIFormat: "openai-responses", APIVariant: "default"},
	}
	checkpoint, err := h.PublishCheckpoint(t.Context(), cp)
	require.NoError(t, err)
	require.True(t, checkpoint.Created)
	var sameTimestamp bool
	require.NoError(t, u.DB().QueryRow(t.Context(), `
SELECT e.created_at=c.created_at FROM context_checkpoints c
 JOIN agent_events e ON e.agent_id=c.agent_id AND e.context_checkpoint_id=c.id WHERE c.id=$1`,
		checkpoint.ID).Scan(&sameTimestamp))
	require.True(t, sameTimestamp)
	snapshot = assertCommandState(t, u, h)
	require.Equal(t, checkpoint.ID, snapshot.Head.PendingCheckpointID)
	again, err := h.PublishCheckpoint(t.Context(), cp)
	require.NoError(t, err)
	require.False(t, again.Created)
	next := prepareCommand(t, h, lease)
	require.Equal(t, normal.Context.Opening, next.Context.Opening)
	assertCommandState(t, u, h)
	changed, err := h.FailModel(
		t.Context(),
		agentexecution.ModelFailure{ContextID: next.Context.ID, RuntimeLockID: lease,
			ErrorKind: "runtime", ErrorMessage: "terminal"},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assertCommandState(t, u, h)
	require.NoError(t, u.Commit(t.Context(), "compaction trace"))
}

func TestExecutionCommandFencing(t *testing.T) {
	for _, mode := range []string{"expired", "canceled", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "input")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			selected, err := h.LoadExecution(t.Context())
			require.NoError(t, err)
			if mode == "expired" {
				_, err = u.DB().
					Exec(t.Context(), `
UPDATE agent_runtime_locks SET started_at=statement_timestamp()-interval '3
minutes',renewed_at=statement_timestamp()-interval '2
minutes',lease_expires_at=statement_timestamp()-interval '1 minute' WHERE id=$1`, lease)
			} else if mode == "canceled" {
				_, err = u.DB().Exec(t.Context(), `
UPDATE agent_runtime_locks SET cancel_requested_at=statement_timestamp() WHERE id=$1`, lease)
			} else {
				lease = uuid.New()
			}
			require.NoError(t, err)
			_, err = h.PrepareModel(
				t.Context(),
				agentexecution.PrepareModelInput{RuntimeLockID: lease, Selected: *selected.Selection.Model},
			)
			require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
			require.ErrorIs(t, u.Commit(context.Background(), "failed fence"), agentexecution.ErrUnitAborted)
		})
	}
}

func TestExecutionPreparationPreservesSourceLineage(t *testing.T) {
	for _, tools := range []bool{false, true} {
		t.Run(map[bool]string{false: "output_limit", true: "tools"}[tools], func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			older := receiveCommand(t, h, "queued", "older")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			newer := receiveCommand(t, h, "steering", "newer")
			_, err = h.AdmitInputs(t.Context())
			require.NoError(t, err)
			source := prepareCommand(t, h, lease)
			require.Equal(t, []uuid.UUID{older.ID, newer.ID}, source.Context.Opening.InputIDs)
			input := agentexecution.AcceptOutputInput{ContextID: source.Context.ID, RuntimeLockID: lease,
				Response: commandResponse(modelenvelope.StopReasonMaxTokens)}
			if tools {
				input.Response.Normalized.StopReason = modelenvelope.StopReasonToolUse
				input.Response.Normalized.Content = []modelenvelope.ResponsePart{{Type: "tool_call",
					ProviderCallID: "one", ToolName: "list_agents", ToolInput: json.RawMessage(`{}`)}}
				input.Tools = []agentexecution.ToolProposal{{ProviderCallID: "one", Type: "built_in"}}
			}
			output, err := h.AcceptOutput(t.Context(), input)
			require.NoError(t, err)
			if tools {
				ref := agentexecution.ToolRef{ID: output.Tools[0].ID, RuntimeLockID: lease}
				_, err = h.AuthorizeTool(t.Context(), ref)
				require.NoError(t, err)
				_, err = h.CompleteTool(t.Context(), agentexecution.ToolCompletion{ToolRef: ref,
					Outcome: "succeeded", Content: []agentexecution.Content{{Kind: "text", Text: "done"}}})
				require.NoError(t, err)
			}
			snapshot := assertCommandState(t, u, h)
			watermark := source.Context.InputEventSequence + 1
			if tools {
				watermark = snapshot.View.ToolBatch.Completion.LastResultSequence
			}
			require.Greater(t, watermark, source.Context.InputEventSequence)
			continuation := prepareCommand(t, h, lease)
			require.Equal(t, []uuid.UUID{older.ID, newer.ID}, continuation.Context.Opening.InputIDs)
			require.Equal(t, watermark, continuation.Context.InputEventSequence)
			_, err = h.FailModel(
				t.Context(),
				agentexecution.ModelFailure{ContextID: continuation.Context.ID, RuntimeLockID: lease,
					Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"},
			)
			require.NoError(t, err)
			retry := prepareCommand(t, h, lease)
			require.Equal(t, continuation.Context.Opening, retry.Context.Opening)
			_, err = h.AcceptOutput(
				t.Context(),
				agentexecution.AcceptOutputInput{ContextID: retry.Context.ID, RuntimeLockID: lease,
					Response: commandResponse(modelenvelope.StopReasonMaxTokens)},
			)
			require.NoError(t, err)
			next := prepareCommand(t, h, lease)
			require.Equal(t, continuation.Context.Opening, next.Context.Opening)
			rebuilt, err := h.ReconstructExecution(t.Context())
			require.NoError(t, err)
			got, err := h.LoadExecution(t.Context())
			require.NoError(t, err)
			require.Equal(t, got.Head, rebuilt.Head)
			require.NoError(t, u.Commit(t.Context(), "corrected lineage"))
		})
	}
}

func TestExecutionConfigAndCheckpointBoundaries(t *testing.T) {
	for _, boundary := range []string{"config", "steering", "checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "input")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			if boundary == "config" {
				prior := prepareCommand(t, h, lease)
				_, err = h.FailModel(
					t.Context(),
					agentexecution.ModelFailure{ContextID: prior.Context.ID, RuntimeLockID: lease,
						Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"},
				)
				require.NoError(t, err)
				selected, err := h.LoadExecution(t.Context())
				require.NoError(t, err)
				cfg := agentexecution.ActivateConfigInput{ConfigID: f.config, IdempotencyKey: "change"}
				_, err = h.ActivateConfig(t.Context(), cfg)
				require.NoError(t, err)
				assertCommandState(t, u, h)
				require.NoError(t, u.Commit(t.Context(), "before rejected command"))
				u, h = f.handle(t, a)
				_, err = h.PrepareModel(
					t.Context(),
					agentexecution.PrepareModelInput{
						RuntimeLockID: lease,
						Selected:      *selected.Selection.Model,
					},
				)
				require.ErrorIs(t, err, storeerr.ErrAgentNotAdvanceable)
				require.NoError(t, u.Rollback(t.Context()))
				u, h = f.handle(t, a)
			}
			normal := prepareCommand(t, h, lease)
			if boundary == "steering" {
				receiveCommand(t, h, "steering", "new boundary")
			}
			compact, err := h.BeginCompaction(
				t.Context(),
				agentexecution.BeginCompactionInput{SourceEnd: normal.Context.InputEventSequence,
					Failure: agentexecution.ModelFailure{
						Recovery:      agentexecution.RecoveryCompact,
						ContextID:     normal.Context.ID,
						RuntimeLockID: lease,
						ErrorKind:     "context_length",
						ErrorMessage:  "compact",
					}},
			)
			require.NoError(t, err)
			if boundary == "steering" {
				require.True(t, compact.Preempted)
				require.Len(t, compact.Admission.Inputs, 1)
			} else {
				receiveCommand(t, h, "queued", "later input")
				checkpoint,
					err := h.PublishCheckpoint(t.Context(),
					agentexecution.PublishCheckpointInput{ContextID: compact.Model.Context.ID,
						RuntimeLockID: lease,
						Summary:       "summary",
						Evidence: agentexecution.ModelEvidence{APIFormat: "openai-responses",
							APIVariant: "default"}})
				require.NoError(t, err)
				require.True(t, checkpoint.Created)
			}
			assertCommandState(t, u, h)
			require.NoError(t, u.Commit(t.Context(), "boundary"))
		})
	}
}

func TestExecutionCoalescedPublication(t *testing.T) {
	f, a, _ := commandFixture(t)
	_, err := f.pool.Exec(t.Context(), `CREATE TABLE execution_publications(kind text,agent_id uuid);
 CREATE FUNCTION audit_execution_publication() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN INSERT INTO execution_publications VALUES(TG_TABLE_NAME,NEW.agent_id); RETURN NEW; END; $$;
 CREATE TRIGGER audit_head AFTER UPDATE ON agent_execution_state FOR EACH ROW EXECUTE FUNCTION
audit_execution_publication();
 CREATE TRIGGER audit_queue AFTER INSERT OR UPDATE ON agent_wakeups FOR EACH ROW EXECUTE FUNCTION
audit_execution_publication();`)
	require.NoError(t, err)
	u, h := f.handle(t, a)
	_, err = u.DB().Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE agent_id=$1`, a)
	require.NoError(t, err)
	first := receiveCommand(t, h, "queued", "first")
	second := receiveCommand(t, h, "queued", "second")
	_, err = h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{
			ID:           second.ID,
			DeliveryMode: "queued",
			Position:     "before",
			BeforeID:     first.ID,
		},
	)
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		u.DB().QueryRow(t.Context(), `SELECT count(*) FROM execution_publications`).Scan(&count),
	)
	require.Zero(t, count)
	require.NoError(t, u.Commit(t.Context(), "coalesced"))
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT count(*) FROM execution_publications`).Scan(&count),
	)
	require.Equal(t, 2, count)
	var age time.Time
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&age),
	)
	u, h = f.handle(t, a)
	third := receiveCommand(t, h, "steering", "third")
	require.NoError(t, u.Commit(t.Context(), "preserve ready age"))
	var after time.Time
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&after),
	)
	require.True(t, age.Equal(after))
	u, h = f.handle(t, a)
	changed, err := h.ChangeBacklog(
		t.Context(),
		agentexecution.BacklogChange{ID: third.ID, DeliveryMode: "steering"},
	)
	require.NoError(t, err)
	require.False(t, changed)
	var before int
	require.NoError(
		t,
		u.DB().QueryRow(t.Context(), `SELECT count(*) FROM execution_publications`).Scan(&before),
	)
	require.NoError(t, u.Commit(t.Context(), "no-op publication"))
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT count(*) FROM execution_publications`).Scan(&count),
	)
	require.Equal(t, before, count)
}

func TestExecutionCheckpointRejectsCutBatch(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "input")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	normal := prepareCommand(t, h, lease)
	response := commandResponse(modelenvelope.StopReasonToolUse)
	response.Normalized.Content = []modelenvelope.ResponsePart{
		{
			Type:           "tool_call",
			ProviderCallID: "call",
			ToolName:       "list_agents",
			ToolInput:      json.RawMessage(`{}`),
		},
	}
	output, err := h.AcceptOutput(
		t.Context(),
		agentexecution.AcceptOutputInput{
			ContextID:     normal.Context.ID,
			RuntimeLockID: lease,
			Response:      response,
			Tools:         []agentexecution.ToolProposal{{ProviderCallID: "call", Type: "custom"}},
		},
	)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "tool proposal before checkpoint"))
	f.complete(t, a, normal.Context.TurnID, output.Tools[0].ID, 3)
	_, err = f.pool.Exec(t.Context(), `UPDATE agents SET next_event_sequence=4 WHERE id=$1`, a)
	require.NoError(t, err)
	u, h = f.handle(t, a)
	next := prepareCommand(t, h, lease)
	compact, err := h.BeginCompaction(
		t.Context(),
		agentexecution.BeginCompactionInput{SourceEnd: 2, Failure: agentexecution.ModelFailure{
			Recovery:  agentexecution.RecoveryCompact,
			ContextID: next.Context.ID, RuntimeLockID: lease, ErrorKind: "context_length", ErrorMessage: "compact"}},
	)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "before rejected command"))
	u, h = f.handle(t, a)
	_, err = h.PublishCheckpoint(
		t.Context(),
		agentexecution.PublishCheckpointInput{ContextID: compact.Model.Context.ID,
			RuntimeLockID: lease,
			Summary:       "unsafe",
			Evidence: agentexecution.ModelEvidence{APIFormat: "openai-responses",
				APIVariant: "default"}},
	)
	require.NoError(t, u.Rollback(t.Context()))
	u, h = f.handle(t, a)
	require.ErrorContains(t, err, "boundary is unsafe")
	assertCommandState(t, u, h)
	var count int
	require.NoError(
		t,
		u.DB().
			QueryRow(t.Context(), `SELECT count(*) FROM context_checkpoints WHERE agent_id=$1`, a).
			Scan(&count),
	)
	require.Zero(t, count)
}

func TestExecutionQueueDeadlineAndOwnership(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "input")
	require.NoError(t, u.Commit(t.Context(), "owned queue"))
	var count int
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&count),
	)
	require.Zero(t, count)
	u, h = f.handle(t, a)
	_, err := u.DB().
		Exec(t.Context(), `
INSERT INTO agent_wakeups(agent_id,ready_at,updated_at) VALUES($1,statement_timestamp()-interval '1
hour',statement_timestamp())`, a)
	require.NoError(t, err)
	_, err = h.AdmitInputs(t.Context())
	require.NoError(t, err)
	normal := prepareCommand(t, h, lease)
	_, err = h.FailModel(
		t.Context(),
		agentexecution.ModelFailure{ContextID: normal.Context.ID, RuntimeLockID: lease,
			Recovery:     agentexecution.RecoveryRetry,
			RetryDelay:   time.Hour,
			ErrorKind:    "transient",
			ErrorMessage: "retry"},
	)
	require.NoError(t, err)
	pending, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	_, err = u.DB().Exec(t.Context(), `DELETE FROM agent_runtime_locks WHERE agent_id=$1`, a)
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "future delivery"))
	var ready time.Time
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&ready),
	)
	require.True(t, ready.Equal(pending.Selection.Model.ReadyAt))
	u, h = f.handle(t, a)
	receiveCommand(t, h, "queued", "queued later")
	require.NoError(t, u.Commit(t.Context(), "retain retry deadline"))
	var later time.Time
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&later),
	)
	require.True(t, ready.Equal(later))
	u, h = f.handle(t, a)
	receiveCommand(t, h, "steering", "interrupt")
	require.NoError(t, u.Commit(t.Context(), "steering delivery"))
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT ready_at FROM agent_wakeups WHERE agent_id=$1`, a).Scan(&later),
	)
	require.True(t, later.Before(ready))
}

func TestExecutionPreparationRevalidatesAfterBlockedConfig(t *testing.T) {
	for _, boundary := range []string{"retry", "checkpoint"} {
		t.Run(boundary, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "input")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			model := prepareCommand(t, h, lease)
			failure := agentexecution.ModelFailure{ContextID: model.Context.ID, RuntimeLockID: lease,
				Recovery: agentexecution.RecoveryRetry, ErrorKind: "transient", ErrorMessage: "retry"}
			if boundary == "retry" {
				_, err = h.FailModel(t.Context(), failure)
				require.NoError(t, err)
			} else {
				failure.Recovery = agentexecution.RecoveryCompact
				compact,
					err := h.BeginCompaction(t.Context(),
					agentexecution.BeginCompactionInput{Failure: failure,
						SourceEnd: model.Context.InputEventSequence})
				require.NoError(t, err)
				_, err = h.PublishCheckpoint(t.Context(),
					agentexecution.PublishCheckpointInput{ContextID: compact.Model.Context.ID,
						RuntimeLockID: lease,
						Summary:       "checkpoint",
						Evidence: agentexecution.ModelEvidence{APIFormat: "openai-responses",
							APIVariant: "default"}})
				require.NoError(t, err)
			}
			selected, err := h.LoadExecution(t.Context())
			require.NoError(t, err)
			require.NotNil(t, selected.Selection.Model)
			require.NoError(t, u.Commit(t.Context(), "candidate"))
			writer, writerHandle := f.handle(t, a)
			_, err = writerHandle.ActivateConfig(
				t.Context(),
				agentexecution.ActivateConfigInput{ConfigID: f.config},
			)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- prepareBlockedCandidate(ctx, f, a, lease, *selected.Selection.Model) }()
			integrationdb.WaitForLockWaiters(t, ctx, f.pool, "", 1)
			require.NoError(t, writer.Commit(ctx, "concurrent config"))
			require.ErrorIs(t, <-result, storeerr.ErrAgentNotAdvanceable)
			check, checkHandle := f.handle(t, a)
			assertCommandState(t, check, checkHandle)
		})
	}
}

func prepareBlockedCandidate(ctx context.Context,
	f executionFixture,
	a,
	lease uuid.UUID,
	decision agentexecution.ModelDecision) error {
	cell := agentexecution.NewCell(f.pool, nil, nil)
	unit, err := agentexecution.BeginTestUnit(ctx, cell)
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(context.WithoutCancel(ctx)) }()
	plan, err := unit.PlanAgents(ctx, []lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: a}},
		nil)
	if err != nil {
		return err
	}
	if err = unit.LockAgents(ctx, plan); err != nil {
		return err
	}
	handle,
		err := unit.Agent(agentexecution.AgentRoute{ProjectID: f.project,
		AgentID:     a,
		RootAgentID: a})
	if err != nil {
		return err
	}
	_, err = handle.PrepareModel(
		ctx,
		agentexecution.PrepareModelInput{RuntimeLockID: lease, Selected: decision},
	)
	return err
}

func TestExecutionFenceSamplesTimeAfterRuntimeLock(t *testing.T) {
	f, a, lease := commandFixture(t)
	unit, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "input")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	selected, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	require.NoError(t, unit.Commit(t.Context(), "fence candidate"))
	_, err = f.pool.Exec(t.Context(), `UPDATE agent_runtime_locks SET
 lease_expires_at=statement_timestamp()+interval '2 seconds' WHERE id=$1`, lease)
	require.NoError(t, err)
	locker, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = locker.Rollback(context.Background()) }()
	_, err = locker.Exec(t.Context(), `SELECT id FROM agent_runtime_locks WHERE id=$1 FOR UPDATE`, lease)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- prepareBlockedCandidate(ctx, f, a, lease, *selected.Selection.Model) }()
	integrationdb.WaitForLockWaiters(t, ctx, f.pool, "", 1)
	require.Eventually(t, func() bool {
		var expired bool
		err := f.pool.QueryRow(ctx, `
SELECT lease_expires_at<=statement_timestamp() FROM agent_runtime_locks WHERE id=$1`, lease).Scan(&expired)
		return err == nil && expired
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, locker.Commit(ctx))
	require.ErrorIs(t, <-result, storeerr.ErrRuntimeLockInactive)
	var contexts int
	require.NoError(t, f.pool.QueryRow(ctx, `
SELECT count(*) FROM model_call_contexts WHERE agent_id=$1`, a).Scan(&contexts))
	require.Zero(t, contexts)
}

func TestExecutionCompactionReplayDoesNotAdmitNewInputs(t *testing.T) {
	for _, phase := range []string{"started", "checkpoint", "preempted"} {
		t.Run(phase, func(t *testing.T) {
			f, a, lease := commandFixture(t)
			u, h := f.handle(t, a)
			receiveCommand(t, h, "queued", "work")
			_, err := h.AdmitInputs(t.Context())
			require.NoError(t, err)
			normal := prepareCommand(t, h, lease)
			if phase == "preempted" {
				receiveCommand(t, h, "steering", "interrupt")
			}
			input := agentexecution.BeginCompactionInput{SourceEnd: normal.Context.InputEventSequence,
				Failure: agentexecution.ModelFailure{
					Recovery:  agentexecution.RecoveryCompact,
					ContextID: normal.Context.ID, RuntimeLockID: lease,
					ErrorKind: "context_length", ErrorMessage: "compact"}}
			first, err := h.BeginCompaction(t.Context(), input)
			require.NoError(t, err)
			if phase == "checkpoint" {
				_, err = h.PublishCheckpoint(t.Context(), agentexecution.PublishCheckpointInput{
					ContextID: first.Model.Context.ID, RuntimeLockID: lease, Summary: "summary",
					Evidence: agentexecution.ModelEvidence{
						APIFormat:  "openai-responses",
						APIVariant: "default",
					}})
				require.NoError(t, err)
			}
			receiveCommand(t, h, "steering", "later")
			before := assertCommandState(t, u, h)
			backlog := readCommandBacklog(t, u, a)
			replay, err := h.BeginCompaction(t.Context(), input)
			require.NoError(t, err)
			require.Equal(t, first.Preempted, replay.Preempted)
			require.Equal(t, first.Model.Context.ID, replay.Model.Context.ID)
			require.False(t, replay.Model.Created)
			require.Empty(t, replay.Admission.Inputs)
			require.Equal(t, backlog, readCommandBacklog(t, u, a))
			after := assertCommandState(t, u, h)
			require.Equal(t, before.Head.CurrentTurnID, after.Head.CurrentTurnID)
		})
	}
}

func TestExecutionContentRejectsDatabaseUnsafeValues(t *testing.T) {
	for _, content := range []agentexecution.Content{
		{Kind: "text", Text: "before\x00after"},
		{Kind: "structured_data", Data: json.RawMessage(`{"value":"before\u0000after"}`)},
		{Kind: "text", Text: "safe", Metadata: json.RawMessage(`{"key":"before\u0000after"}`)},
	} {
		t.Run(content.Kind+string(content.Metadata), func(t *testing.T) {
			f, a, _ := commandFixture(t)
			u, h := f.handle(t, a)
			_, err := h.ReceiveContent(
				t.Context(),
				agentexecution.ReceiveContentInput{
					DeliveryMode: "queued",
					Content:      []agentexecution.Content{content},
				},
			)
			require.ErrorContains(t, err, "U+0000")
			require.Error(t, u.Commit(t.Context(), "unsafe content"))
			var count int
			require.NoError(
				t,
				f.pool.QueryRow(t.Context(),
					`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`,
					a).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestExecutionPreparationOwnsCachedLineage(t *testing.T) {
	f, a, lease := commandFixture(t)
	u, h := f.handle(t, a)
	input := receiveCommand(t, h, "queued", "initial")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	selected, err := h.LoadExecution(t.Context())
	require.NoError(t, err)
	prepared, err := h.PrepareModel(t.Context(), agentexecution.PrepareModelInput{
		RuntimeLockID: lease, Selected: *selected.Selection.Model,
	})
	require.NoError(t, err)
	selected.Selection.Model.Opening.InputIDs[0] = uuid.New()
	prepared.Context.Opening.InputIDs[0] = uuid.New()
	snapshot, count, _, err := agentexecution.CountOrdinaryLoad(t.Context(), h)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, []uuid.UUID{input.ID}, snapshot.View.NormalContext.Opening.InputIDs)
	snapshot.View.NormalContext.Opening.InputIDs[0] = uuid.New()
	assertCommandState(t, u, h)
}

func TestExecutionArchiveHistoryDoesNotDependOnReads(t *testing.T) {
	f, a, _ := commandFixture(t)
	u, h := f.handle(t, a)
	receiveCommand(t, h, "queued", "initial")
	_, err := h.AdmitInputs(t.Context())
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "admit"))
	var histories []string
	for _, loaded := range []bool{false, true} {
		u, h = f.handle(t, a)
		if loaded {
			_, err = h.LoadExecution(t.Context())
			require.NoError(t, err)
		}
		_, err = h.Archive(t.Context(), uuid.Nil)
		require.NoError(t, err)
		var history string
		require.NoError(t,
			u.DB().QueryRow(t.Context(),
				`SELECT coalesce(jsonb_agg(jsonb_build_object('event',to_jsonb(e)-ARRAY['id','agent_input_id','idempotency_key','created_at'],'input',to_jsonb(i)-ARRAY['id','queued_at','admitted_at','resolved_at','canceled_at','admitted_event_id']) ORDER BY e.sequence),'[]')::text FROM agent_events e LEFT JOIN agent_inputs i ON i.id=e.agent_input_id WHERE e.agent_id=$1`,
				a).Scan(&history))
		histories = append(histories, history)
		require.NoError(t, u.Rollback(t.Context()))
	}
	require.Equal(t, histories[0], histories[1])
	require.Contains(t, histories[0], `"input_kind": "control"`)
}
