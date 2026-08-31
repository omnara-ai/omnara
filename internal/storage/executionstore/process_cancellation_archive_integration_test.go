//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestCancelAgentOrdersCancelBeforeCanceledToolResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_orders_cause_before_effect")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "cancel_orders_cause_before_effect", "run_command")

	_, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    toolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID,
	); err != nil {
		t.Fatalf("accept process: %v", err)
	} else if !found {
		t.Fatal("expected process accept")
	}

	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel agent: %v", err)
	}
	if !cancelResult.Affected {
		t.Fatal("cancel should report affected")
	}

	var toolResultSequence int64
	err = fixture.Store.pool.QueryRow(ctx, `
SELECT event.sequence
FROM agent_events event
JOIN tool_call_results result ON result.agent_id = event.agent_id
  AND result.id = event.tool_call_result_id
JOIN tool_call_read_projection tool_call
  ON tool_call.agent_id = result.agent_id
 AND tool_call.id = result.tool_call_id
WHERE tool_call.project_id = $1
  AND event.agent_id = $2
  AND result.tool_call_id = $3
  AND event.event_kind = 'tool_result'
`, testProjectID, fixture.AgentID, toolCallID).Scan(&toolResultSequence)
	if err != nil {
		t.Fatalf("load canceled tool result event sequence: %v", err)
	}
	if cancelResult.Event.Sequence >= toolResultSequence {
		t.Fatalf(
			"cancel sequence %d should precede canceled tool result sequence %d",
			cancelResult.Event.Sequence,
			toolResultSequence,
		)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	var latestEventID ID
	err = fixture.Store.pool.QueryRow(ctx, `
SELECT turn.latest_event_id
FROM agent_turns turn
JOIN agents agent ON agent.id = turn.agent_id
WHERE agent.project_id = $1 AND turn.agent_id = $2 AND turn.id = $3
`, testProjectID, fixture.AgentID, toolCall.TurnID).Scan(&latestEventID)
	if err != nil {
		t.Fatalf("load latest turn event: %v", err)
	}
	if latestEventID == cancelResult.Event.ID {
		t.Fatalf("latest turn event should track canceled tool fallout, not rely on cancel being last")
	}

	repeatedCancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("repeat cancel after fallout: %v", err)
	}
	if repeatedCancelResult.Event.ID != NilID ||
		!repeatedCancelResult.RuntimeCancelRequested ||
		repeatedCancelResult.Affected {
		t.Fatalf(
			"repeat cancel after tool-result fallout = event %+v runtime_cancel_requested %v affected %v, want pending runtime cancel without new event",
			repeatedCancelResult.Event,
			repeatedCancelResult.RuntimeCancelRequested,
			repeatedCancelResult.Affected,
		)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release canceled runtime lock: %v", err)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("wakeups after canceled runtime release = %d, want 0", wakeups)
	}
}

func TestCancelAgentNoOpsTerminalTurnWithUncanceledRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_terminal_turn_live_runtime")
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"finish"}]`),
		IdempotencyKey: "cancel-terminal-turn-live-runtime",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found {
		t.Fatal("expected admitted content input")
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, OpeningInputIDs: []ID{input.ID}, AgentConfigID: agent.CurrentConfigID,
		InputEventSequence: admitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim model context: %v", err)
	}
	providerResponseID := "resp_cancel_terminal_turn_live_runtime"
	providerResponse := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: modelProviderSlugForContext(
			t, ctx, fixture.Store, testProjectID, fixture.AgentID, modelClaim.Context.ID,
		),
		APIFormat:  modelprotocol.APIFormatOpenAIResponses,
		APIVariant: modelprotocol.APIVariantDefault,
		Normalized: modelenvelope.ResponseNormalized{
			ID:         providerResponseID,
			Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "done"}},
			StopReason: modelenvelope.StopReasonEndTurn,
			Usage:      modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1, OutputTokens: 1},
		},
	}
	if _, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, executionstore.RecordModelOutputAndCompleteContextInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		ModelCallContextID: modelClaim.Context.ID,
		ProviderResponse:   providerResponse,
	}); err != nil {
		t.Fatalf("record terminal model output: %v", err)
	}

	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel terminal turn with live runtime: %v", err)
	}
	if cancelResult.Event.ID != NilID || cancelResult.RuntimeCancelRequested || cancelResult.Affected {
		t.Fatalf(
			"cancel terminal turn with live runtime = event %+v runtime_cancel_requested %v affected %v, want no-op",
			cancelResult.Event,
			cancelResult.RuntimeCancelRequested,
			cancelResult.Affected,
		)
	}
	var cancelRequestedAt *time.Time
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT runtime_lock.cancel_requested_at
FROM agent_runtime_locks runtime_lock
JOIN agents agent ON agent.id = runtime_lock.agent_id
WHERE agent.project_id = $1
  AND runtime_lock.agent_id = $2
  AND runtime_lock.id = $3
`, testProjectID, fixture.AgentID, fixture.Lock.ID).
		Scan(&cancelRequestedAt); err != nil {
		t.Fatalf("load runtime cancel marker: %v", err)
	}
	if cancelRequestedAt != nil {
		t.Fatalf("no-op cancel should not mark runtime canceled, got %v", cancelRequestedAt)
	}
}

func TestCancelAgentWinsAgainstActiveModelCallAndRejectsLateAcceptance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_active_model_call")
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"cancel active model call"}]`),
		IdempotencyKey: "cancel-active-model-call",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	)
	if !found {
		t.Fatal("expected admitted content input")
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, OpeningInputIDs: []ID{input.ID}, AgentConfigID: agent.CurrentConfigID,
		InputEventSequence: admitted.Events[0].Sequence,
	})
	if err != nil {
		t.Fatalf("claim model context: %v", err)
	}
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel active model call: %v", err)
	}
	if !cancelResult.Affected || !cancelResult.RuntimeCancelRequested || cancelResult.Event.ID == NilID {
		t.Fatalf("cancel result = %+v, want affected turn and runtime", cancelResult)
	}
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load canceled model context: found=%v err=%v", found, err)
	}
	if contextRecord.State != executionstore.ModelCallContextCanceled ||
		contextRecord.RecoveryKind != "" || contextRecord.ErrorKind != "canceled" ||
		contextRecord.ErrorCode != "agent_canceled" || contextRecord.RetryAt != nil {
		t.Fatalf("canceled context = %+v", contextRecord)
	}
	providerResponse := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: modelProviderSlugForContext(
			t, ctx, fixture.Store, testProjectID, fixture.AgentID, modelClaim.Context.ID,
		),
		APIFormat:  modelprotocol.APIFormatOpenAIResponses,
		APIVariant: modelprotocol.APIVariantDefault,
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "resp_late_after_cancel",
			Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "late"}},
			StopReason: modelenvelope.StopReasonEndTurn,
		},
	}
	_, err = fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, executionstore.RecordModelOutputAndCompleteContextInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID,
		RuntimeLockID: fixture.Lock.ID, ModelCallContextID: modelClaim.Context.ID,
		ProviderResponse: providerResponse,
	})
	if err == nil {
		t.Fatal("canceled model call accepted a late provider transition")
	}
	if _, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	); err != nil || found {
		t.Fatalf("late canceled output found=%v err=%v, want none", found, err)
	}
}

func TestArchiveAgentAtomicallyStopsDurableModelCallWork(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name             string
		recordRetry      bool
		wantContextState executionstore.ModelCallState
		wantRecoveryKind executionstore.ModelCallRecoveryKind
		wantErrorCode    string
	}{
		{
			name:             "started",
			wantContextState: executionstore.ModelCallContextCanceled,
			wantErrorCode:    "agent_archived",
		},
		{
			name:             "retry_without_runtime",
			recordRetry:      true,
			wantContextState: executionstore.ModelCallContextFailed,
			wantRecoveryKind: executionstore.ModelCallRecoveryRetry,
			wantErrorCode:    "provider_unavailable",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newProcessDaemonFixture(t, ctx, "archive_model_call_"+test.name)
			now := fixture.Now.Add(time.Minute)
			input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
				ProjectID:      testProjectID,
				AgentID:        fixture.AgentID,
				Actor:          mustOmnaraActorParams(t, fixture.UserID),
				ContentBlocks:  json.RawMessage(`[{"type":"text","text":"archive durable model work"}]`),
				IdempotencyKey: "archive-model-call-" + test.name,
			})
			if err != nil {
				t.Fatalf("create content input: %v", err)
			}
			admitted, found := admitNextAgentInputAndOpenTurnForTest(
				t,
				ctx,
				fixture.Store,
				testProjectID,
				fixture.AgentID,
				fixture.Lock.ID,
			)
			if !found {
				t.Fatal("expected admitted content input")
			}
			agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
			if err != nil {
				t.Fatalf("load agent: %v", err)
			}
			claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				RuntimeLockID:      fixture.Lock.ID,
				OpeningInputIDs:    []ID{input.ID},
				AgentConfigID:      agent.CurrentConfigID,
				InputEventSequence: admitted.Events[0].Sequence,
			})
			if err != nil {
				t.Fatalf("claim model context: %v", err)
			}
			if test.recordRetry {
				failedAt := now.Add(3 * time.Second)
				claim.Context, err = fixture.Store.Execution().RecordRetryableModelCallFailure(ctx, executionstore.RecordRecoverableModelCallFailureInput{
					ProjectID:          testProjectID,
					AgentID:            fixture.AgentID,
					ModelCallContextID: claim.Context.ID,
					RuntimeLockID:      fixture.Lock.ID,
					ErrorKind:          "transient",
					ErrorCode:          "provider_unavailable",
					ErrorMessage:       "provider is temporarily unavailable",
					ErrorDetails:       json.RawMessage(`{"test":"archive_retry"}`),
					RetryDelay:         now.Add(time.Hour).Sub(failedAt),
				})
				if err != nil {
					t.Fatalf("record retryable failure: %v", err)
				}
				if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx,
					testProjectID,
					fixture.AgentID,
					fixture.Lock.ID,
				); err != nil {
					t.Fatalf("release retry runtime: %v", err)
				}
			}

			if _, _, err := fixture.Store.Execution().ArchiveAgent(
				ctx,
				testProjectID,
				fixture.AgentID,
				userPrincipal(fixture.UserID),
			); err != nil {
				t.Fatalf("archive agent: %v", err)
			}
			archived, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
			if err != nil || archived.State != executionstore.AgentStateArchived {
				t.Fatalf("archived agent = %+v err=%v", archived, err)
			}
			contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
				ctx,
				testProjectID,
				fixture.AgentID,
				claim.Context.ID,
			)
			if err != nil || !found {
				t.Fatalf("archived context = %+v found=%v err=%v", contextRecord, found, err)
			}
			if contextRecord.State != test.wantContextState ||
				contextRecord.RecoveryKind != test.wantRecoveryKind ||
				contextRecord.ErrorCode != test.wantErrorCode ||
				contextRecord.RetryAt != nil && !test.recordRetry {
				t.Fatalf("archived context = %+v", contextRecord)
			}
			if archived.ArchivedAt == nil || contextRecord.CompletedAt == nil ||
				contextRecord.CompletedAt.Before(contextRecord.CreatedAt) {
				t.Fatalf("archived model context has invalid timestamps: agent=%+v context=%+v", archived, contextRecord)
			}
			if test.recordRetry && archived.ArchivedAt.Before(*contextRecord.CompletedAt) {
				t.Fatalf("agent archive predates the existing retry outcome: agent=%+v context=%+v", archived, contextRecord)
			}
			if !test.recordRetry && contextRecord.CompletedAt.Before(*archived.ArchivedAt) {
				t.Fatalf("archive-triggered model cancellation predates the agent archive: agent=%+v context=%+v", archived, contextRecord)
			}
			var activeContexts, wakeups int
			if err := fixture.Store.pool.QueryRow(ctx, `SELECT count(*)::integer FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2 AND state = 'started'`, testProjectID, fixture.AgentID).
				Scan(&activeContexts); err != nil {
				t.Fatalf("count active contexts: %v", err)
			}
			if err := fixture.Store.pool.QueryRow(ctx, `SELECT count(*)::integer FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, testProjectID, fixture.AgentID).
				Scan(&wakeups); err != nil {
				t.Fatalf("count wakeups: %v", err)
			}
			if activeContexts != 0 || wakeups != 0 {
				t.Fatalf("archived agent active contexts=%d wakeups=%d, want zero", activeContexts, wakeups)
			}
		})
	}
}

func TestCancelAgentCancelsSteeringButPreservesQueuedBacklogWhenActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_active_cancels_steering")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"cancel_active_cancels_steering",
		"read_process",
	)
	claimToolCallForTest(
		t,
		ctx,
		fixture.Store,
		fixture.AgentID,
		toolCallID,
		fixture.Lock.ID,
		true,
	)
	queuedInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"queued"}]`),
		IdempotencyKey: "cancel-active-preserve-queued",
	})
	if err != nil {
		t.Fatalf("create queued input: %v", err)
	}
	steeringInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"steering"}]`),
		DeliveryMode:   "steering",
		IdempotencyKey: "cancel-active-cancel-steering",
	})
	if err != nil {
		t.Fatalf("create steering input: %v", err)
	}
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel active agent: %v", err)
	}
	if cancelResult.Event.ID == NilID || !cancelResult.Affected {
		t.Fatalf(
			"cancel active agent = event %+v runtime_cancel_requested %v affected %v, want affected event",
			cancelResult.Event,
			cancelResult.RuntimeCancelRequested,
			cancelResult.Affected,
		)
	}
	if !cancelResult.RuntimeCancelRequested {
		t.Fatalf("runtime_cancel_requested = false, want durable runtime cancel mark")
	}
	var queuedState, steeringState string
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id = $1`, queuedInput.ID).
		Scan(&queuedState); err != nil {
		t.Fatalf("load queued input state: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id = $1`, steeringInput.ID).
		Scan(&steeringState); err != nil {
		t.Fatalf("load steering input state: %v", err)
	}
	if queuedState != "received" {
		t.Fatalf("queued input state = %q, want received", queuedState)
	}
	if steeringState != "canceled" {
		t.Fatalf("steering input state = %q, want canceled", steeringState)
	}
}

func TestCancelAgentStopsUnstartedTurnWithoutLiveRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_unstarted_turn_without_runtime")
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release fixture runtime lock: %v", err)
	}
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"cancel me"}]`),
		IdempotencyKey: "cancel-unstarted-turn",
	})
	if err != nil {
		t.Fatalf("create content input: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || !claimedOpeningInputIDsEqual(claim, input.ID) {
		t.Fatalf("claim = %+v found=%v, want executable input %s", claim, found, input.ID)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		claim.RuntimeLock.ID,
	); err != nil {
		t.Fatalf("release runtime before cancel: %v", err)
	}
	steeringInput, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      testProjectID,
		AgentID:        fixture.AgentID,
		Actor:          mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"steer"}]`),
		DeliveryMode:   "steering",
		IdempotencyKey: "cancel-unstarted-turn-steering",
	})
	if err != nil {
		t.Fatalf("create steering input: %v", err)
	}
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel unstarted turn: %v", err)
	}
	if cancelResult.Event.ID == NilID || cancelResult.RuntimeCancelRequested || !cancelResult.Affected {
		t.Fatalf(
			"cancel unstarted turn = event %+v runtime_cancel_requested %v affected %v, want affected event without runtime cancel",
			cancelResult.Event,
			cancelResult.RuntimeCancelRequested,
			cancelResult.Affected,
		)
	}
	var steeringState string
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND id = $3`, testProjectID, fixture.AgentID, steeringInput.ID).
		Scan(&steeringState); err != nil {
		t.Fatalf("load steering state: %v", err)
	}
	if steeringState != "canceled" {
		t.Fatalf("steering state = %q, want canceled", steeringState)
	}
	requireAgentWakeupCoverage(t, ctx, fixture.Store, testProjectID, fixture.AgentID)
	if wakeups := countAgentWakeups(t, ctx, fixture.Store, fixture.AgentID); wakeups != 0 {
		t.Fatalf("wakeups after canceling unstarted turn = %d, want 0", wakeups)
	}
	if seed, found, err := fixture.Store.Execution().NextAgentModelWork(ctx, testProjectID, fixture.AgentID); err != nil {
		t.Fatalf("load continuation seed after cancel: %v", err)
	} else if found {
		t.Fatalf("continuation seed after cancel = %+v, want none", seed)
	}
}

func TestCancelAgentReportsAlreadyPendingRuntimeCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_already_canceled_runtime")
	if _, err := requestAgentRuntimeCancelForTest(
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Now.Add(time.Second),
	); err != nil {
		t.Fatalf("request runtime cancel: %v", err)
	}
	repeatedRuntimeCancel, err := requestAgentRuntimeCancelForTest(
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		fixture.Now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatalf("repeat runtime cancel request: %v", err)
	}
	if repeatedRuntimeCancel.ID != NilID {
		t.Fatalf("repeat runtime cancel = %+v, want no-op", repeatedRuntimeCancel)
	}
	cancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("cancel already canceled runtime: %v", err)
	}
	if cancelResult.Event.ID != NilID || !cancelResult.RuntimeCancelRequested || cancelResult.Affected {
		t.Fatalf(
			"cancel already canceled runtime = event %+v runtime_cancel_requested=%v affected=%v, want pending runtime cancel without new event",
			cancelResult.Event,
			cancelResult.RuntimeCancelRequested,
			cancelResult.Affected,
		)
	}
}

func TestCancelAgentCanCancelLaterFrontierAfterPriorCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_later_frontier_after_prior_cancel")
	firstToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"cancel_later_frontier_after_prior_cancel_first",
		"run_command",
	)
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    firstToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start first process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID,
	); err != nil {
		t.Fatalf("accept first process: %v", err)
	} else if !found {
		t.Fatal("first process accept not found")
	}
	firstCancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	firstToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, firstToolCallID)
	if err != nil {
		t.Fatalf("load first tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, firstToolCall, "canceled")

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release canceled runtime lock: %v", err)
	}
	nextLock, err := fixture.Store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		fixture.AgentID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire post-cancel runtime lock: %v", err)
	}
	fixture.Lock = nextLock
	secondToolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"cancel_later_frontier_after_prior_cancel_second",
		"run_command",
	)
	if _, err := startProcessForTest(ctx, fixture.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       fixture.AgentID,
		ToolCallID:    secondToolCallID,
		RuntimeLockID: fixture.Lock.ID,
	}, executionstore.CreateProcessInput{
		AgentMachineBindingID: fixture.BindingID,
		Command:               "sleep 30",
		ShellSelector:         "sh",
		Cwd:                   "/work",
	}); err != nil {
		t.Fatalf("start second process: %v", err)
	}
	if _, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		testOrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		NilID,
	); err != nil {
		t.Fatalf("accept second process: %v", err)
	} else if !found {
		t.Fatal("second process accept not found")
	}
	secondCancelResult, err := fixture.Store.Execution().CancelAgent(
		ctx,
		executionstore.CancelAgentInput{
			ProjectID: testProjectID,
			AgentID:   fixture.AgentID,
			Actor:     mustOmnaraActorParams(t, fixture.UserID),
		},
	)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if secondCancelResult.Event.ID == firstCancelResult.Event.ID ||
		secondCancelResult.Event.Sequence <= firstCancelResult.Event.Sequence {
		t.Fatalf(
			"second cancel = %+v, first = %+v; want new later cancel event",
			secondCancelResult.Event,
			firstCancelResult.Event,
		)
	}
	secondToolCall, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, secondToolCallID)
	if err != nil {
		t.Fatalf("load second tool call: %v", err)
	}
	assertCompletedToolCallWithResult(t, fixture.Store, fixture.AgentID, secondToolCall, "canceled")
}
