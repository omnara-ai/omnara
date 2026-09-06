//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestModelCallRowConstraintsProtectImmutableEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "model_call_database_guards")
	openingInputIDs := make([]ID, 0, len(admitted.Inputs))
	for _, input := range admitted.Inputs {
		openingInputIDs = append(openingInputIDs, input.ID)
	}
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	claimInput := executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    openingInputIDs,
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: frontier,
	}
	claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, claimInput)
	if err != nil {
		t.Fatalf("claim guarded model context: %v", err)
	}
	replayed, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, claimInput)
	if err != nil {
		t.Fatalf("re-find normal model context: %v", err)
	}
	if replayed.Context.ID != claim.Context.ID || replayed.Created || replayed.Claimed {
		t.Fatalf("replayed normal claim = %+v, want existing context without send authority", replayed)
	}
	_, terminalInsertErr := fixture.Store.pool.Exec(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind,
  attempt_number, agent_config_id, configured_model_revision_id,
  input_event_sequence, runtime_lock_id, state,
  error_kind, error_message, created_at, completed_at
)
SELECT org_id, project_id, agent_id, operation_kind,
       attempt_number + 1000, agent_config_id, configured_model_revision_id,
       input_event_sequence, runtime_lock_id,
       'failed', 'invalid_request', 'inserted terminal', created_at, created_at
FROM model_call_contexts
WHERE id = $1`, claim.Context.ID)
	assertPgErrorMessage(
		t,
		terminalInsertErr,
		"23514",
		"model_call_contexts must be inserted in started state",
	)

	nullRangeTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin NULL compaction range test: %v", err)
	}
	_, nullRangeErr := nullRangeTx.Exec(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind,
  attempt_number,
  agent_config_id, configured_model_revision_id,
  input_event_sequence, source_event_sequence_end,
  runtime_lock_id, state, created_at
)
SELECT org_id, project_id, agent_id, 'compaction',
       attempt_number + 1001,
       agent_config_id, configured_model_revision_id,
       input_event_sequence, NULL, runtime_lock_id, 'started', created_at
FROM model_call_contexts
WHERE id = $1`, claim.Context.ID)
	_ = nullRangeTx.Rollback(ctx)
	if !isPgCheckViolation(nullRangeErr) {
		t.Fatalf("NULL compaction range error = %v, want check violation", nullRangeErr)
	}

	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin terminal context mutation test: %v", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'canceled', error_kind = 'canceled', error_code = 'test_canceled',
    error_message = 'test cancellation', completed_at = now()
WHERE id = $1`, claim.Context.ID); err != nil {
		t.Fatalf("perform legal context transition: %v", err)
	}
	_, terminalContextErr := tx.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'succeeded'
WHERE id = $1`, claim.Context.ID)
	assertPgErrorMessage(t, terminalContextErr, "25006", "terminal model_call_contexts are immutable")
	_ = tx.Rollback(ctx)

	tx, err = fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin terminal failed-context mutation test: %v", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'failed', error_kind = 'invalid_request',
    error_message = 'test terminal transition', completed_at = created_at
WHERE id = $1`, claim.Context.ID); err != nil {
		t.Fatalf("perform legal failed-context transition: %v", err)
	}
	_, terminalFailedContextErr := tx.Exec(ctx, `
UPDATE model_call_contexts
SET error_message = 'mutated terminal evidence'
WHERE id = $1`, claim.Context.ID)
	assertPgErrorMessage(
		t,
		terminalFailedContextErr,
		"25006",
		"terminal model_call_contexts are immutable",
	)
	_ = tx.Rollback(ctx)

	_, err = fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET api_format = 'openai_responses', api_variant = 'default',
    provider_response_id = 'injected-response-id'
WHERE id = $1`, claim.Context.ID)
	if !isPgCheckViolation(err) {
		t.Fatalf("started provider evidence error = %v, want check violation", err)
	}

	_, err = fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET api_format = 'openai_responses', api_variant = ''
WHERE id = $1`, claim.Context.ID)
	if !isPgCheckViolation(err) {
		t.Fatalf("empty API variant error = %v, want check violation", err)
	}

	_, err = fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'succeeded', completed_at = created_at,
    api_format = 'openai_responses', api_variant = 'default',
    error_code = 'stale_error', error_message = 'stale response error',
    error_details = '{"status_code":429}'::jsonb
WHERE id = $1`, claim.Context.ID)
	if !isPgCheckViolation(err) {
		t.Fatalf("succeeded context error evidence error = %v, want check violation", err)
	}
}

func TestKernelRecordModelOutputWritesTypedAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_model_output_authority")
	now := fixture.Now.Add(time.Minute)
	turnID := testID("turn_kernel_model_output_authority")
	inputID := testID("input_kernel_model_output_authority")
	providerResponse := modelenvelope.ResponseEnvelope{
		RequestedProviderModelSlug: "test",
		ServedProviderModelSlug:    "test",
		APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                 modelprotocol.APIVariantDefault,
		ProviderReportedCostUSD:    "0.0000125",
		ProviderMetadata: modelenvelope.ProviderMetadata{
			OpenRouter: modelenvelope.OpenRouterMetadata{Provider: "Moonshot AI"},
		},
		Normalized: modelenvelope.ResponseNormalized{
			ID:         "resp_kernel_model_output_authority",
			Content:    []modelenvelope.ResponsePart{{Type: "text", Text: "hello"}},
			StopReason: modelenvelope.StopReasonEndTurn,
			Usage:      modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1, OutputTokens: 1},
		},
	}
	actorID := fixture.omnaraActorID(t, ctx)
	if _, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO agent_inputs(id, project_id, agent_id, state, delivery_mode, actor_id, input_kind, queued_at, metadata)
		VALUES ($1, $2, $3, 'received', 'queued', $4, 'content', $5, '{}'::jsonb)
	`, inputID, testProjectID, fixture.AgentID, actorID, now); err != nil {
		t.Fatalf("insert model output fixture input: %v", err)
	}
	turnTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin model output fixture turn: %v", err)
	}
	defer func() { _ = turnTx.Rollback(ctx) }()
	inputEvent, err := executionstore.IntegrationAppendTypedAgentEventTx(
		ctx,
		notifications.NewTxNotifications(),
		turnTx,
		executionstore.AppendTypedAgentEventInput{
			ProjectID:      testProjectID,
			AgentID:        fixture.AgentID,
			TurnID:         turnID,
			IsOpeningEvent: true,
			Kind:           events.KindAgentInput,
			IdempotencyKey: "agent_input:" + inputID.String(),
			AgentInputID:   inputID,
		},
	)
	if err != nil {
		t.Fatalf("append model output fixture input event: %v", err)
	}
	if _, err := turnTx.Exec(ctx, `
		UPDATE agent_inputs
		SET state = 'resolved',
		    admitted_event_id = $4,
		    admitted_at = $5,
		    resolved_at = $5
		WHERE id = $1 AND project_id = $2 AND agent_id = $3
	`, inputID, testProjectID, fixture.AgentID, inputEvent.Event.ID, now); err != nil {
		t.Fatalf("resolve model output fixture input: %v", err)
	}
	if _, err := turnTx.Exec(ctx, `
		INSERT INTO agent_turns(id, agent_id, turn_sequence, latest_event_id, latest_semantic_event_id)
		VALUES ($1, $2, 100, $3, $3)
	`, turnID, fixture.AgentID, inputEvent.Event.ID); err != nil {
		t.Fatalf("insert model output fixture turn: %v", err)
	}
	if err := turnTx.Commit(ctx); err != nil {
		t.Fatalf("commit model output fixture turn: %v", err)
	}
	agent, err := fixture.Store.Execution().GetAgentInProject(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load agent for model output fixture context: %v", err)
	}
	modelClaim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    []ID{inputID},
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: inputEvent.Event.Sequence,
	})
	if err != nil {
		t.Fatalf("claim model output fixture context: %v", err)
	}
	recordInput := executionstore.RecordModelOutputAndCompleteContextInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		ModelCallContextID: modelClaim.Context.ID,
		ProviderResponse:   providerResponse,
	}
	event, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, recordInput)
	if err != nil {
		t.Fatalf("record typed model output: %v", err)
	}
	if event.Kind != events.KindModelOutput {
		t.Fatalf("event=%+v, want typed model output projection", event)
	}
	replayed, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, recordInput)
	if err != nil {
		t.Fatalf("replay typed model output: %v", err)
	}
	if replayed.ID != event.ID || replayed.Sequence != event.Sequence {
		t.Fatalf("replayed event = %s/%d, want %s/%d", replayed.ID, replayed.Sequence, event.ID, event.Sequence)
	}
	conflictingInput := recordInput
	conflictingInput.ProviderResponse.Normalized.Content = append(
		[]modelenvelope.ResponsePart(nil),
		recordInput.ProviderResponse.Normalized.Content...,
	)
	conflictingInput.ProviderResponse.Normalized.Content[0].Text = "changed output"
	if _, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, conflictingInput); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting output replay error = %v, want %v", err, storeerr.ErrIdempotencyConflict)
	}
	conflictingCostInput := recordInput
	conflictingCostInput.ProviderResponse.ProviderReportedCostUSD = "0.0000126"
	if _, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx, conflictingCostInput); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting cost replay error = %v, want %v", err, storeerr.ErrIdempotencyConflict)
	}
	var modelOutputID ID
	if err := fixture.Store.pool.QueryRow(ctx, `SELECT event.model_output_id FROM agent_events event JOIN agents agent ON agent.id = event.agent_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.id = $3`, testProjectID, fixture.AgentID, event.ID).Scan(&modelOutputID); err != nil {
		t.Fatalf("load model output event pointer: %v", err)
	}
	if isNilID(modelOutputID) {
		t.Fatalf("model_output event missing model_output_id")
	}
	modelOutput, found, err := fixture.Store.Execution().GetModelOutputForContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load model output authority: found=%v err=%v", found, err)
	}
	if modelOutput.ProviderResponseID != providerResponse.Normalized.ID {
		t.Fatalf(
			"model output provider response id = %q, want context-derived %q",
			modelOutput.ProviderResponseID,
			providerResponse.Normalized.ID,
		)
	}
	completedContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		modelClaim.Context.ID,
	)
	if err != nil || !found {
		t.Fatalf("load completed model call context: found=%v err=%v", found, err)
	}
	if completedContext.ProviderReportedCostUSD != providerResponse.ProviderReportedCostUSD {
		t.Fatalf(
			"model call provider-reported cost = %q, want %q",
			completedContext.ProviderReportedCostUSD,
			providerResponse.ProviderReportedCostUSD,
		)
	}
	if completedContext.ProviderMetadata != providerResponse.ProviderMetadata {
		t.Fatalf(
			"model call provider metadata = %+v, want %+v",
			completedContext.ProviderMetadata,
			providerResponse.ProviderMetadata,
		)
	}
	readEvents, err := fixture.Store.Execution().ListAgentEventsForRead(ctx, testProjectID, fixture.AgentID, 0, 100)
	if err != nil {
		t.Fatalf("list agent events for read: %v", err)
	}
	var readModelOutput *executionstore.AgentEventReadRecord
	for index := range readEvents {
		if readEvents[index].ID == event.ID {
			readModelOutput = &readEvents[index]
		}
	}
	if readModelOutput == nil {
		t.Fatalf("model output event %s missing from read projection", event.ID)
	}
	if readModelOutput.ModelUsage != providerResponse.Normalized.Usage ||
		readModelOutput.ProviderMetadata != providerResponse.ProviderMetadata {
		t.Fatalf(
			"read projection usage=%+v metadata=%+v, want %+v and %+v",
			readModelOutput.ModelUsage,
			readModelOutput.ProviderMetadata,
			providerResponse.Normalized.Usage,
			providerResponse.ProviderMetadata,
		)
	}
	var inputTokens, outputTokens, contentBlocks int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT coalesce(context.input_tokens_total, 0),
       coalesce(context.output_tokens_total, 0),
       (SELECT count(*) FROM content_blocks block
        WHERE block.agent_id = output.agent_id
          AND block.owner_model_output_id = output.id)
FROM model_outputs output
JOIN model_call_contexts context
  ON context.agent_id = output.agent_id
 AND context.id = output.model_call_context_id
WHERE context.project_id = $1 AND output.agent_id = $2 AND output.id = $3
	`, testProjectID, fixture.AgentID, modelOutputID).Scan(&inputTokens, &outputTokens, &contentBlocks); err != nil {
		t.Fatalf("load model output usage: %v", err)
	}
	if inputTokens != 1 || outputTokens != 1 || contentBlocks != 1 {
		t.Fatalf(
			"model output usage/content input=%d output=%d blocks=%d, want 1/1/1",
			inputTokens,
			outputTokens,
			contentBlocks,
		)
	}
	appendTx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin late model output append: %v", err)
	}
	_, err = appendTx.Exec(ctx, `
INSERT INTO content_blocks(
  agent_id, owner_kind, owner_model_output_id,
  ordinal, block_kind, text_content, created_at
)
VALUES ($1, 'model_output', $2, 1, 'text', 'late append', $3)
`, fixture.AgentID, modelOutputID, now.Add(2*time.Second))
	_ = appendTx.Rollback(ctx)
	if !isPgCheckViolation(err) {
		t.Fatalf("late model output block error = %v, want check violation", err)
	}
	var matchingLineage int
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT count(*)
FROM model_outputs output
JOIN model_call_contexts context
  ON context.agent_id = output.agent_id
 AND context.id = output.model_call_context_id
WHERE context.project_id = $1
  AND output.agent_id = $2
  AND output.id = $3
  AND output.model_call_context_id = $4
  AND context.state = 'succeeded'
`, testProjectID, fixture.AgentID, modelOutputID, modelClaim.Context.ID).Scan(&matchingLineage); err != nil {
		t.Fatalf("load model output context lineage: %v", err)
	}
	if matchingLineage != 1 {
		t.Fatalf("model output lineage rows = %d, want 1", matchingLineage)
	}
}

func TestKernelProviderModelOutputRequiresSucceededNormalContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_model_output_context_boundary")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_model_output_context_boundary",
		"read_process",
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	firstContextID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		"kernel_model_output_context_boundary_first",
		fixture.Now.Add(time.Minute),
	)
	firstContext, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		firstContextID,
	)
	if err != nil || !found {
		t.Fatalf("load first model context found=%v err=%v", found, err)
	}
	failedAt := fixture.Now.Add(time.Minute + time.Second)
	handoff, err := fixture.Store.Execution().RecordModelCallFailureAndClaimCompaction(
		ctx,
		executionstore.RecordModelCallFailureAndClaimCompactionInput{
			ParentContextID: firstContextID,
			Failure: executionstore.RecordRecoverableModelCallFailureInput{
				ProjectID:          testProjectID,
				AgentID:            fixture.AgentID,
				ModelCallContextID: firstContextID,
				RuntimeLockID:      fixture.Lock.ID,
				RecoveryKind:       executionstore.ModelCallRecoveryCompact,
				ErrorKind:          "context_window",
				ErrorCode:          "test_cross_context_output",
				ErrorMessage:       "model context exceeded",
			},
			SourceEventSequenceEnd: firstContext.InputEventSequence,
		},
	)
	if err != nil {
		t.Fatalf("fail first model context for compaction: %v", err)
	}
	secondClaim := handoff.CompactionCall
	if _, err := fixture.Store.pool.Exec(ctx, `
INSERT INTO model_outputs(
  agent_id, model_call_context_id,
  served_provider_model_slug,
  stop_reason, created_at
)
VALUES ($1, $2, 'test-model', 'end_turn', $3)
`, fixture.AgentID, secondClaim.Context.ID, failedAt); !isPgCheckViolation(err) {
		t.Fatalf("non-normal producer context error = %v, want check violation", err)
	}
}

func TestKernelModelOutputRequiresTypedEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "kernel_model_output_typed_event_required")
	toolCallID := createToolCallForProcessTest(
		t,
		ctx,
		fixture,
		"kernel_model_output_typed_event_required",
		"read_process",
	)
	turnID := turnIDForProcessToolCallTest(t, ctx, fixture, toolCallID)
	contextID := modelOutputContextForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		"kernel_model_output_typed_event_required",
		fixture.Now.Add(2*time.Minute),
	)
	contextRecord, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	if err != nil || !found {
		t.Fatalf("load typed-event model context: found=%v err=%v", found, err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		contextID,
	)
	tx, err := fixture.Store.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin orphan model output tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'succeeded', completed_at = created_at,
    api_format = 'openai_responses', api_variant = 'default'
WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		fixture.AgentID,
		contextRecord.ID,
	); err != nil {
		t.Fatalf("stage succeeded typed-event context: %v", err)
	}
	if _, err := executionstore.IntegrationCreateModelOutputAuthorityTx(ctx, tx, executionstore.CreateModelOutputAuthorityInput{
		ProjectID:               testProjectID,
		AgentID:                 fixture.AgentID,
		ModelCallContextID:      contextID,
		ServedProviderModelSlug: providerModelSlug,
		StopReason:              "end_turn",
	}); err != nil {
		t.Fatalf("create orphan model output fixture: %v", err)
	}
	if err := tx.Commit(ctx); !isPgCheckViolation(err) {
		t.Fatalf("commit orphan model output error = %v, want check violation", err)
	}
}
