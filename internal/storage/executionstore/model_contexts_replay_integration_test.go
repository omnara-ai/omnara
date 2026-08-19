//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

func TestProviderReplaySuppressionCutoffUsesCompatiblePriorFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, _, agent := newMultiInputContinuationSeedFixture(t, ctx, "provider_replay_cutoff")
	originalRevisionID := currentModelRevisionForConfig(
		t,
		ctx,
		fixture.Store,
		testProjectID,
		agent.CurrentConfigID,
	)
	originalRevision, err := fixture.Store.Models().GetConfiguredModelRevisionForUse(
		ctx,
		testOrgID,
		originalRevisionID,
	)
	if err != nil {
		t.Fatalf("load original model revision: %v", err)
	}

	insertStarted := func(revisionID ID, frontier int64) ID {
		t.Helper()
		var contextID ID
		if err := fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number,
  agent_config_id, configured_model_revision_id, input_event_sequence,
  runtime_lock_id, state, created_at
)
VALUES ($1, $2, $3, 'normal', 1, $4, $5, $6, $7, 'started', statement_timestamp())
RETURNING id
`, testOrgID, testProjectID, fixture.AgentID, agent.CurrentConfigID,
			revisionID, frontier, fixture.Lock.ID).Scan(&contextID); err != nil {
			t.Fatalf("insert model call at frontier %d: %v", frontier, err)
		}
		return contextID
	}
	insertStartedCompaction := func(revisionID ID, frontier int64) ID {
		t.Helper()
		var contextID ID
		if err := fixture.Store.pool.QueryRow(ctx, `
INSERT INTO model_call_contexts(
  org_id, project_id, agent_id, operation_kind, attempt_number,
  agent_config_id, configured_model_revision_id, input_event_sequence,
  source_event_sequence_end, runtime_lock_id, state, created_at
)
VALUES ($1, $2, $3, 'compaction', 1, $4, $5, $6, 1, $7, 'started', statement_timestamp())
RETURNING id
`, testOrgID, testProjectID, fixture.AgentID, agent.CurrentConfigID,
			revisionID, frontier, fixture.Lock.ID).Scan(&contextID); err != nil {
			t.Fatalf("insert compaction call at frontier %d: %v", frontier, err)
		}
		return contextID
	}
	finishFailure := func(
		contextID ID,
		apiFormat modelprotocol.APIFormat,
		apiVariant modelprotocol.APIVariant,
		errorKind modelprotocol.ErrorKind,
	) {
		t.Helper()
		if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'failed', recovery_kind = 'retry',
    api_format = $2, api_variant = $3,
    error_kind = $4, error_code = 'test_provider_failure',
    error_message = 'test provider failure',
    retry_at = statement_timestamp(), completed_at = statement_timestamp()
WHERE id = $1
`, contextID, apiFormat, apiVariant, errorKind); err != nil {
			t.Fatalf("finish provider failure %s: %v", contextID, err)
		}
	}
	recordReplayRejection := func(
		revisionID ID,
		frontier int64,
		apiFormat modelprotocol.APIFormat,
		apiVariant modelprotocol.APIVariant,
	) {
		t.Helper()
		finishFailure(
			insertStarted(revisionID, frontier),
			apiFormat,
			apiVariant,
			modelprotocol.ErrorKindReplayRejected,
		)
	}
	cutoff := func(contextID ID) int64 {
		t.Helper()
		value, err := fixture.Store.Execution().GetProviderReplaySuppressionCutoff(
			ctx,
			testProjectID,
			fixture.AgentID,
			contextID,
		)
		if err != nil {
			t.Fatalf("get provider replay suppression cutoff: %v", err)
		}
		return value
	}

	currentContextID := insertStarted(originalRevisionID, 500)
	if got := cutoff(currentContextID); got != 0 {
		t.Fatalf("cutoff without a replay rejection = %d, want 0", got)
	}
	if _, err := fixture.Store.pool.Exec(ctx, `
UPDATE model_call_contexts
SET state = 'canceled', error_kind = 'canceled', error_code = 'test_complete',
    error_message = 'test setup complete', completed_at = statement_timestamp()
WHERE id = $1
`, currentContextID); err != nil {
		t.Fatalf("finish current model call: %v", err)
	}

	recordReplayRejection(
		originalRevisionID,
		100,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
	)
	recordReplayRejection(
		originalRevisionID,
		300,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
	)

	differentSlug := originalRevision.ProviderModelSlug + "-different"
	patchedModel, err := fixture.Store.Models().PatchConfiguredModel(
		ctx,
		modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: originalRevision.ModelProviderConfigID,
			ID:                    originalRevision.ConfiguredModelID,
			ProviderModelSlug:     &differentSlug,
		},
	)
	if err != nil {
		t.Fatalf("create different-slug revision: %v", err)
	}
	recordReplayRejection(
		patchedModel.CurrentRevisionID,
		420,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
	)

	differentProvider, err := fixture.Store.Models().CreateModelProviderConfig(
		ctx,
		modelstore.CreateModelProviderConfigInput{
			OrgID:              testOrgID,
			Name:               "replay-cutoff-other-provider",
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         modelprotocol.APIVariantDefault,
			BaseURL:            "https://other.example.com/v1",
			EndpointPath:       "/responses",
			CredentialSecretID: testDefaultProviderCredentialSecretID,
		},
	)
	if err != nil {
		t.Fatalf("create different provider config: %v", err)
	}
	differentProviderModel, err := fixture.Store.Models().CreateConfiguredModel(
		ctx,
		modelstore.CreateConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: differentProvider.ID,
			Name:                  "replay-cutoff-other-model",
			ProviderModelSlug:     originalRevision.ProviderModelSlug,
			ContextWindowTokens:   128_000,
			MaxOutputTokens:       8_192,
		},
	)
	if err != nil {
		t.Fatalf("create model under different provider config: %v", err)
	}
	recordReplayRejection(
		differentProviderModel.CurrentRevisionID,
		410,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
	)

	recordReplayRejection(
		originalRevisionID,
		430,
		modelprotocol.APIFormatOpenAIChatCompletions,
		modelprotocol.APIVariantDefault,
	)
	recordReplayRejection(
		originalRevisionID,
		440,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantOpenRouter,
	)
	finishFailure(
		insertStarted(originalRevisionID, 450),
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		modelprotocol.ErrorKindProviderUnavailable,
	)
	finishFailure(
		insertStartedCompaction(originalRevisionID, 460),
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		modelprotocol.ErrorKindReplayRejected,
	)
	recordReplayRejection(
		originalRevisionID,
		600,
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
	)

	if got := cutoff(currentContextID); got != 300 {
		t.Fatalf("compatible prior replay cutoff = %d, want maximum matching frontier 300", got)
	}

	changedContextWindow := originalRevision.ContextWindowTokens - 1
	sameRouteModel, err := fixture.Store.Models().PatchConfiguredModel(
		ctx,
		modelstore.PatchConfiguredModelInput{
			OrgID:                 testOrgID,
			ModelProviderConfigID: originalRevision.ModelProviderConfigID,
			ID:                    originalRevision.ConfiguredModelID,
			ProviderModelSlug:     &originalRevision.ProviderModelSlug,
			ContextWindowTokens:   &changedContextWindow,
		},
	)
	if err != nil {
		t.Fatalf("create compatible new revision: %v", err)
	}
	sameRouteContextID := insertStarted(sameRouteModel.CurrentRevisionID, 501)
	if got := cutoff(sameRouteContextID); got != 300 {
		t.Fatalf("compatible new revision replay cutoff = %d, want 300", got)
	}
}
