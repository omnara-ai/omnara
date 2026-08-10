//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestConcurrentNormalModelCallClaimsHaveOneSender(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, agent := newMultiInputContinuationSeedFixture(t, ctx, "normal_creator_authority")
	openingInputIDs := make([]ID, 0, len(admitted.Inputs))
	for _, input := range admitted.Inputs {
		openingInputIDs = append(openingInputIDs, input.ID)
	}
	claimInput := executionstore.ClaimNormalModelCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		RuntimeLockID:      fixture.Lock.ID,
		OpeningInputIDs:    openingInputIDs,
		AgentConfigID:      agent.CurrentConfigID,
		InputEventSequence: admitted.Events[len(admitted.Events)-1].Sequence,
	}

	type result struct {
		claim executionstore.ModelCallClaim
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			claim, err := fixture.Store.Execution().ClaimNormalModelCall(ctx, claimInput)
			results <- result{claim: claim, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	claims := make([]executionstore.ModelCallClaim, 0, 2)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent normal model call claim: %v", result.err)
		}
		claims = append(claims, result.claim)
	}
	if len(claims) != 2 || claims[0].Context.ID != claims[1].Context.ID ||
		claims[0].Created == claims[1].Created || claims[0].Claimed == claims[1].Claimed ||
		claims[0].Created != claims[0].Claimed || claims[1].Created != claims[1].Claimed {
		t.Fatalf("concurrent normal claims = %+v, want exactly one creator and sender", claims)
	}
}

func TestModelCallOpeningInputsRejectWrongProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"opening_inputs_wrong_project",
	)
	wrongProjectID := seedAdditionalProjectForTest(
		t,
		ctx,
		fixture.Store.pool,
		"opening_inputs_wrong_scope",
	)
	frontier := admitted.Events[len(admitted.Events)-1].Sequence

	var correctProjectCount, wrongProjectCount int
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_model_call_opening_content_inputs($1, $2, $3, $4)`,
		testProjectID,
		fixture.AgentID,
		admitted.Turn.ID,
		frontier,
	).Scan(&correctProjectCount); err != nil {
		t.Fatalf("load correct-project opening inputs: %v", err)
	}
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_model_call_opening_content_inputs($1, $2, $3, $4)`,
		wrongProjectID,
		fixture.AgentID,
		admitted.Turn.ID,
		frontier,
	).Scan(&wrongProjectCount); err != nil {
		t.Fatalf("load wrong-project opening inputs: %v", err)
	}
	if correctProjectCount != len(admitted.Inputs) || wrongProjectCount != 0 {
		t.Fatalf(
			"opening input counts correct=%d wrong=%d, want %d/0",
			correctProjectCount,
			wrongProjectCount,
			len(admitted.Inputs),
		)
	}
}

func TestModelCallContextDatabaseGuardsRejectRebinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "attempt_transition_guards")
	rebindTests := []struct {
		name      string
		query     string
		secondArg bool
	}{
		{name: "attempt number", query: `UPDATE model_call_contexts SET attempt_number = attempt_number + 1 WHERE id = $1`},
		{name: "runtime lock", query: `UPDATE model_call_contexts SET runtime_lock_id = $2 WHERE id = $1`, secondArg: true},
		{name: "event frontier", query: `UPDATE model_call_contexts SET input_event_sequence = input_event_sequence + 1 WHERE id = $1`},
	}
	for _, test := range rebindTests {
		t.Run(test.name, func(t *testing.T) {
			args := []any{claim.Context.ID}
			if test.secondArg {
				args = append(args, fixture.AgentID)
			}
			_, err := fixture.Store.pool.Exec(ctx, test.query, args...)
			assertPgErrorMessage(
				t,
				err,
				"25006",
				"model_call_context identity and runtime ownership are immutable",
			)
		})
	}
}

func TestModelCallContextDatabaseGuardsRejectInvalidProviderCost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, claim := newStartedNormalModelCallTestFixture(t, ctx, "provider_cost_guard")
	_, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'succeeded',
    api_format = 'openai-chat-completions',
    api_variant = 'openrouter',
    provider_reported_cost_usd = 'NaN'::numeric,
    completed_at = statement_timestamp()
WHERE id = $1
`, claim.Context.ID)
	assertPgConstraint(
		t,
		err,
		"23514",
		"model_call_contexts_provider_cost_value_check",
	)

	_, err = fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'failed',
    error_kind = 'provider_unavailable',
    provider_reported_cost_usd = 1,
    completed_at = statement_timestamp()
WHERE id = $1
`, claim.Context.ID)
	assertPgConstraint(
		t,
		err,
		"23514",
		"model_call_contexts_provider_cost_api_format_check",
	)
}

func TestModelCallContextIdentityIndexesRejectDuplicateLogicalContexts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	normalFixture, _, normal := newStartedNormalModelCallTestFixture(t, ctx, "normal_identity_guard")
	_, err := normalFixture.Store.pool.Exec(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind,
  attempt_number,
  agent_config_id, configured_model_revision_id,
  input_event_sequence, runtime_lock_id, state, created_at
)
SELECT org_id, project_id, agent_id, operation_kind,
       attempt_number,
       agent_config_id, configured_model_revision_id,
       input_event_sequence, runtime_lock_id, 'started', created_at + interval '1 second'
FROM model_call_contexts
WHERE id = $1`, normal.Context.ID)
	assertPgConstraint(t, err, "23505", "model_call_contexts_normal_identity_idx")

	compactionFixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "compaction_identity_guard")
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	compaction := claimSentCompactionForRangeTest(
		t,
		ctx,
		compactionFixture,
		1,
		frontier,
		frontier,
		compactionFixture.Now.Add(4*time.Second),
	)
	_, err = compactionFixture.Store.pool.Exec(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind,
  attempt_number,
  agent_config_id, configured_model_revision_id,
  input_event_sequence, source_event_sequence_end,
  runtime_lock_id, state, created_at
)
SELECT org_id, project_id, agent_id, operation_kind,
       attempt_number,
       agent_config_id, configured_model_revision_id,
       input_event_sequence, source_event_sequence_end,
       runtime_lock_id, 'started',
       created_at + interval '1 second'
FROM model_call_contexts
WHERE id = $1`, compaction.Context.ID)
	assertPgConstraint(t, err, "23505", "model_call_contexts_compaction_identity_idx")
	parent, found, err := compactionFixture.Store.Execution().GetNormalModelCallContextForFrontier(
		ctx,
		testProjectID,
		compactionFixture.AgentID,
		compaction.Context.InputEventSequence,
	)
	if err != nil || !found {
		t.Fatalf("load compaction parent: found=%v err=%v", found, err)
	}

	replayed, err := compactionFixture.Store.Execution().ClaimCompactionModelCall(
		ctx,
		executionstore.ClaimCompactionModelCallInput{
			ProjectID:              testProjectID,
			AgentID:                compactionFixture.AgentID,
			RuntimeLockID:          compactionFixture.Lock.ID,
			InputEventSequence:     compaction.Context.InputEventSequence,
			SourceEventSequenceEnd: *compaction.Context.SourceEventSequenceEnd,
			ParentContextID:        parent.ID,
		},
	)
	if err != nil {
		t.Fatalf("re-find compaction identity: %v", err)
	}
	if replayed.Context.ID != compaction.Context.ID || replayed.Created || replayed.Claimed ||
		replayed.Context.State != executionstore.ModelCallContextStarted {
		t.Fatalf("replayed compaction claim = %+v, want existing context without send authority", replayed)
	}
}

func TestModelCallRecoveryKindMatchesOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	normalFixture, _, normal := newStartedNormalModelCallTestFixture(
		t,
		ctx,
		"normal_source_adjustment_guard",
	)
	_, err := normalFixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'failed',
    recovery_kind = 'reduce_compaction_source',
    error_kind = 'context_window',
    error_message = 'invalid normal source adjustment',
    completed_at = created_at
WHERE id = $1
`, normal.Context.ID)
	if !isPgCheckViolation(err) {
		t.Fatalf("normal source adjustment error = %v, want check violation", err)
	}
	compactionFixture, admitted, _ := newMultiInputContinuationSeedFixture(
		t,
		ctx,
		"compaction_compact_guard",
	)
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	compaction := claimSentCompactionForRangeTest(
		t,
		ctx,
		compactionFixture,
		admitted.Events[0].Sequence,
		frontier,
		frontier,
		compactionFixture.Now.Add(4*time.Second),
	)
	_, err = compactionFixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'failed',
    recovery_kind = 'compact',
    error_kind = 'context_window',
    error_message = 'invalid nested compaction',
    retry_at = created_at + interval '1 second',
    completed_at = created_at + interval '1 second'
WHERE id = $1
`, compaction.Context.ID)
	if !isPgCheckViolation(err) {
		t.Fatalf("compaction compact recovery error = %v, want check violation", err)
	}
}

func TestModelCallOperationRejectsSecondSemanticOutcome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, first := newStartedNormalModelCallTestFixture(t, ctx, "operation_outcome_guard")

	var turnID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT turn_id
FROM model_call_context_turns
WHERE project_id = $1 AND agent_id = $2 AND model_call_context_id = $3
`, testProjectID, fixture.AgentID, first.Context.ID).Scan(&turnID); err != nil {
		t.Fatalf("load model call turn: %v", err)
	}
	createModelOutputEventForTurnTest(
		t,
		ctx,
		fixture,
		turnID,
		first.Context.ID,
		"operation_outcome_guard_first",
		string(modelenvelope.StopReasonEndTurn),
		"resp_operation_outcome_guard_first",
		fixture.Now.Add(2*time.Second),
	)

	var secondContextID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind,
  attempt_number, agent_config_id,
  configured_model_revision_id, input_event_sequence, runtime_lock_id,
  state, created_at
)
SELECT org_id, project_id, agent_id, operation_kind,
       attempt_number + 1, agent_config_id,
       configured_model_revision_id, input_event_sequence, runtime_lock_id,
       'started', statement_timestamp()
FROM model_call_contexts
WHERE id = $1
RETURNING id
`, first.Context.ID).Scan(&secondContextID); err != nil {
		t.Fatalf("insert second operation context: %v", err)
	}
	_, found, err := fixture.Store.Execution().GetModelCallContext(
		ctx,
		testProjectID,
		fixture.AgentID,
		secondContextID,
	)
	if err != nil || !found {
		t.Fatalf("load second operation context: found=%v err=%v", found, err)
	}
	providerModelSlug := modelProviderSlugForContext(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		fixture.AgentID,
		secondContextID,
	)
	_, err = fixture.Store.Execution().RecordModelOutputAndCompleteContext(
		ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID:          testProjectID,
			AgentID:            fixture.AgentID,
			RuntimeLockID:      fixture.Lock.ID,
			ModelCallContextID: secondContextID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: providerModelSlug,
				ServedProviderModelSlug:    providerModelSlug,
				APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
				APIVariant:                 modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID:         "resp_operation_outcome_guard_second",
					StopReason: modelenvelope.StopReasonEndTurn,
				},
			},
		},
	)
	assertPgConstraint(t, err, "23505", "model_call_contexts_one_normal_outcome_idx")
}

func TestContextCheckpointDatabaseGuardsRejectMutationAndDuplicateEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, admitted, _ := newMultiInputContinuationSeedFixture(t, ctx, "checkpoint_mutation_guards")
	frontier := admitted.Events[len(admitted.Events)-1].Sequence
	claim := claimSentCompactionForRangeTest(
		t,
		ctx,
		fixture,
		1,
		frontier,
		frontier,
		fixture.Now.Add(4*time.Second),
	)
	checkpoint, err := publishCheckpointForRangeTest(
		t,
		ctx,
		fixture,
		claim,
		"immutable checkpoint",
		fixture.Now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatalf("publish checkpoint fixture: %v", err)
	}

	_, err = fixture.Store.pool.Exec(
		ctx,
		`UPDATE context_checkpoints SET summary = 'rewritten' WHERE id = $1`,
		checkpoint.ID,
	)
	assertPgErrorMessage(t, err, "25006", "context_checkpoints are immutable")
	_, err = fixture.Store.pool.Exec(ctx, `DELETE FROM context_checkpoints WHERE id = $1`, checkpoint.ID)
	assertPgErrorMessage(t, err, "25006", "context_checkpoints are immutable")

	var turnID ID
	if err := fixture.Store.pool.QueryRow(ctx, `
SELECT event.turn_id
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.id = $3
`, testProjectID, fixture.AgentID, checkpoint.CheckpointEventID).Scan(&turnID); err != nil {
		t.Fatalf("load checkpoint event turn: %v", err)
	}
	sequence, err := fixture.Store.Execution().MaxEventSequence(ctx, testProjectID, fixture.AgentID)
	if err != nil {
		t.Fatalf("load checkpoint event sequence: %v", err)
	}
	_, err = fixture.Store.pool.Exec(ctx, `
INSERT INTO agent_events(
  agent_id, turn_id, sequence, event_kind,
  idempotency_key, context_checkpoint_id, created_at
)
VALUES ($1, $2, $3, 'context_checkpoint', $4, $5, $6)
`,
		fixture.AgentID,
		turnID,
		sequence+1,
		"duplicate-checkpoint-event:"+checkpoint.ID.String(),
		checkpoint.ID,
		fixture.Now.Add(6*time.Second),
	)
	assertPgConstraint(t, err, "23505", "agent_events_context_checkpoint_idx")
}

func assertPgConstraint(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("database error = %v, want SQLSTATE %s from %s", err, code, constraint)
	}
}
