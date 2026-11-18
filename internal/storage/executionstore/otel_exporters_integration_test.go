//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func createOtelExporterForTest(
	t *testing.T, fixture processDaemonFixture, scope executionstore.OtelExporterScope, settings otelconfig.Settings,
) executionstore.OtelExporterRecord {
	t.Helper()
	record, err := fixture.Store.Execution().CreateOtelExporter(t.Context(), executionstore.CreateOtelExporterInput{
		OrgID: testOrgID, Scope: scope, Settings: settings,
	})
	require.NoError(t, err)
	return record
}

func otelDeliveryTurns(t *testing.T, fixture processDaemonFixture, exporterID uuid.UUID) []uuid.UUID {
	t.Helper()
	var turns []uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(t.Context(), `
		SELECT coalesce(array_agg(turn_id ORDER BY id), '{}'::uuid[])
		FROM otel_export_deliveries WHERE exporter_id = $1`, exporterID).Scan(&turns))
	return turns
}

// startOtelTestTurn opens a turn with a model call in flight, through the
// commands the worker uses.
func startOtelTestTurn(t *testing.T, fixture processDaemonFixture, key string) (turnID, contextID uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"` + key + `"}]`),
		IdempotencyKey: "otel-export-" + key, DeliveryMode: executionstore.DeliveryModeSteering,
	})
	require.NoError(t, err)
	_, found := admitNextAgentInputAndOpenTurnForTest(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, fixture.Lock.ID)
	require.True(t, found)
	prepared, err := fixture.Store.Execution().PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		OpeningInputIDs: []uuid.UUID{input.ID},
	})
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT id FROM agent_turns WHERE agent_id = $1 ORDER BY turn_sequence DESC LIMIT 1`,
		fixture.AgentID).Scan(&turnID))
	return turnID, prepared.Claim.Context.ID
}

func recordOtelTestOutput(
	t *testing.T, fixture processDaemonFixture, contextID uuid.UUID,
	stopReason modelenvelope.StopReason, content ...modelenvelope.ResponsePart,
) {
	t.Helper()
	ctx := t.Context()
	_, err := fixture.Store.Execution().RecordModelOutputAndCompleteContext(ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
			ModelCallContextID: contextID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: modelProviderSlugForContext(
					t, ctx, fixture.Store, testProjectID, fixture.AgentID, contextID),
				APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID: "resp_" + contextID.String(), Content: content, StopReason: stopReason,
					Usage: modelenvelope.Usage{InputTokens: 1, UncachedInputTokens: 1, OutputTokens: 1},
				},
			},
		})
	require.NoError(t, err)
}

func TestOtelExportersQueueFinishedTurnsAndClaimDeliveries(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "otel_export")
	store := fixture.Store.Execution()
	owner := createSecretTestUser(t, ctx, fixture.Store, "Otel Export Owner", "owner")
	secret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "otel-auth",
		Material: secrets.GenericMaterial{Value: "Bearer test-key"}, Actor: userPrincipal(owner.ID),
	})
	require.NoError(t, err)

	orgWide := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeOrg},
		otelconfig.Settings{
			Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse, SamplePercent: 100,
			Encoding:      oteltrace.EncodingProtobuf,
			Headers:       map[string]string{"x-langfuse-ingestion-version": "4"},
			SecretHeaders: map[string]uuid.UUID{"Authorization": secret.ID},
		})
	project := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		otelconfig.Settings{Endpoint: "https://project.example.com/v1/traces", SamplePercent: 100})
	unsampled := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{
			Type: executionstore.OtelExporterScopeAgent, ProjectID: testProjectID, AgentID: fixture.AgentID,
		},
		otelconfig.Settings{Endpoint: "https://agent.example.com/v1/traces", SamplePercent: 0})
	require.Equal(t, oteltrace.VariantOTel, project.Settings.Variant)
	require.Equal(t, oteltrace.EncodingProtobuf, project.Settings.Encoding)

	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: testOrgID, Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeOrg},
		Settings: otelconfig.Settings{Endpoint: "https://otel.example.com/v1/traces", SamplePercent: 100},
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: testOrgID, Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeOrg},
		Settings: otelconfig.Settings{Endpoint: "https://other.example.com", SamplePercent: 100,
			SecretHeaders: map[string]uuid.UUID{"Authorization": uuid.New()}},
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)

	text := modelenvelope.ResponsePart{Type: "text", Text: "done"}

	// An output that neither calls tools nor is cut off finishes its turn.
	turnID, contextID := startOtelTestTurn(t, fixture, "finished")
	require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))
	recordOtelTestOutput(t, fixture, contextID, modelenvelope.StopReasonEndTurn, text)
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, orgWide.ID))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, project.ID))
	require.Empty(t, otelDeliveryTurns(t, fixture, unsampled.ID))

	first, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	second, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	_, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ElementsMatch(t, []uuid.UUID{orgWide.ID, project.ID}, []uuid.UUID{first.ExporterID, second.ExporterID})
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	for _, delivery := range []executionstore.OtelExportDelivery{first, second} {
		require.Equal(t, testOrgID, delivery.OrgID)
		require.Equal(t, fixture.AgentID, delivery.AgentID)
		require.Equal(t, turnID, delivery.TurnID)
		require.EqualValues(t, 1, delivery.AttemptCount)
	}

	turn, err := store.LoadTurnTraceForExport(ctx, fixture.AgentID, turnID)
	require.NoError(t, err)
	require.Len(t, turn.ModelCalls, 1)
	var names []string
	spans := oteltrace.Build(turn, oteltrace.VariantOTel).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for _, span := range spans.All() {
		names = append(names, span.Name())
		require.Equal(t, oteltrace.TraceID(turnID), span.TraceID())
	}
	require.Len(t, names, 2)
	require.Contains(t, names[0], "invoke_agent")
	_, err = store.LoadTurnTraceForExport(ctx, fixture.AgentID, uuid.New())
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = store.LoadTurnTrace(ctx, uuid.New(), fixture.AgentID, turnID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	orgDelivery, projectDelivery := first, second
	if first.ExporterID != orgWide.ID {
		orgDelivery, projectDelivery = second, first
	}
	destination, err := store.GetOtelExportDestination(ctx, testOrgID, orgWide.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.OtelExportDestination{
		Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse,
		Encoding: oteltrace.EncodingProtobuf,
		Headers:  map[string]string{"x-langfuse-ingestion-version": "4", "Authorization": "Bearer test-key"},
	}, destination)

	require.NoError(t, store.CompleteOtelExportDelivery(ctx, orgDelivery.ID, orgDelivery.ClaimToken))
	require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))

	require.NoError(t, store.RetryOtelExportDelivery(ctx, projectDelivery.ID, projectDelivery.ClaimToken, 0))
	projectDelivery, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	// Only the current claim can release the delivery for another attempt.
	require.NoError(t, store.RetryOtelExportDelivery(ctx, projectDelivery.ID, uuid.New(), 0))
	_, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, store.RetryOtelExportDelivery(ctx, projectDelivery.ID, projectDelivery.ClaimToken, 0))
	retried, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, project.ID, retried.ExporterID)
	require.EqualValues(t, 3, retried.AttemptCount)
	require.NoError(t, store.CompleteOtelExportDelivery(ctx, retried.ID, retried.ClaimToken))

	// An output cut off at the output limit is continued by another model
	// call, so its turn stays open until a new turn replaces it.
	cutOffTurn, contextID := startOtelTestTurn(t, fixture, "cut-off")
	recordOtelTestOutput(t, fixture, contextID, modelenvelope.StopReasonMaxTokens, text)
	require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))
	canceledTurn, _ := startOtelTestTurn(t, fixture, "canceled")
	require.Equal(t, []uuid.UUID{cutOffTurn}, otelDeliveryTurns(t, fixture, orgWide.ID))

	// Cancellation finishes a turn that has no closing output.
	_, err = store.CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{cutOffTurn, canceledTurn}, otelDeliveryTurns(t, fixture, orgWide.ID))
	require.Equal(t, []uuid.UUID{cutOffTurn, canceledTurn}, otelDeliveryTurns(t, fixture, project.ID))
	canceled, err := store.LoadTurnTraceForExport(ctx, fixture.AgentID, canceledTurn)
	require.NoError(t, err)
	require.Len(t, canceled.ModelCalls, 1)
	require.Equal(t, "canceled", canceled.ModelCalls[0].State)

	// A secret an exporter sends as a header cannot be deleted from under it.
	_, err = fixture.Store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(owner.ID),
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)

	sampleRate := 12.345
	updated, err := store.UpdateOtelExporter(ctx, executionstore.UpdateOtelExporterInput{
		OrgID: testOrgID, ExporterID: orgWide.ID, SamplePercent: &sampleRate, SecretHeaders: map[string]uuid.UUID{},
	})
	require.NoError(t, err)
	require.InDelta(t, 12.345, updated.Settings.SamplePercent, 0)
	require.Empty(t, updated.Settings.SecretHeaders)
	require.Equal(t, map[string]string{"x-langfuse-ingestion-version": "4"}, updated.Settings.Headers)

	listed, err := store.ListOtelExporters(ctx, executionstore.ListOtelExportersInput{OrgID: testOrgID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, listed.Exporters, 3)
	listed, err = store.ListOtelExporters(ctx, executionstore.ListOtelExportersInput{
		OrgID: testOrgID, Limit: 100,
		Scope: &executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
	})
	require.NoError(t, err)
	require.Len(t, listed.Exporters, 1)
	require.Equal(t, project.ID, listed.Exporters[0].ID)

	_, err = fixture.Store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(owner.ID),
	})
	require.NoError(t, err)

	// Deleting an exporter drops the turns still queued for it.
	require.NotEmpty(t, otelDeliveryTurns(t, fixture, project.ID))
	require.NoError(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID))
	require.Empty(t, otelDeliveryTurns(t, fixture, project.ID))
	_, err = store.GetOtelExportDestination(ctx, testOrgID, project.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ErrorIs(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID), storeerr.ErrNotFound)
}
