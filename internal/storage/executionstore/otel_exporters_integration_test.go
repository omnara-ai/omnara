//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
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

func otelDeliverySequences(t *testing.T, fixture processDaemonFixture, exporterID uuid.UUID) []int64 {
	t.Helper()
	var sequences []int64
	require.NoError(t, fixture.Store.pool.QueryRow(t.Context(), `
		SELECT coalesce(array_agg(event_sequence ORDER BY event_sequence), '{}'::bigint[])
		FROM otel_export_deliveries WHERE exporter_id = $1`, exporterID).Scan(&sequences))
	return sequences
}

func TestOtelExportersQueueSampledSpansAndDeliverInBatches(t *testing.T) {
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
			Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse, SampleBasisPoints: 10000,
			Headers:       map[string]string{"x-langfuse-ingestion-version": "4"},
			SecretHeaders: map[string]uuid.UUID{"Authorization": secret.ID},
		})
	project := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		otelconfig.Settings{Endpoint: "https://project.example.com/v1/traces", SampleBasisPoints: 10000})
	unsampled := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{
			Type: executionstore.OtelExporterScopeAgent, ProjectID: testProjectID, AgentID: fixture.AgentID,
		},
		otelconfig.Settings{Endpoint: "https://agent.example.com/v1/traces", SampleBasisPoints: 0})
	require.Equal(t, oteltrace.VariantOTel, project.Settings.Variant)

	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: testOrgID, Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeOrg},
		Settings: otelconfig.Settings{Endpoint: "https://otel.example.com/v1/traces", SampleBasisPoints: 10000},
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: testOrgID, Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeOrg},
		Settings: otelconfig.Settings{Endpoint: "https://other.example.com", SampleBasisPoints: 10000,
			SecretHeaders: map[string]uuid.UUID{"Authorization": uuid.New()}},
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)

	// One model output that calls a tool, then the tool's result.
	toolID := createToolCallForProcessTestWithPermission(t, ctx, fixture, "otel-export", "ask_question", true)
	ownedCompleteTool(t, ctx, fixture, toolID)

	var outputSequence, resultSequence int64
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT max(sequence) FILTER (WHERE event_kind = 'model_output'),
		       max(sequence) FILTER (WHERE event_kind = 'tool_result')
		FROM agent_events WHERE agent_id = $1`, fixture.AgentID).Scan(&outputSequence, &resultSequence))
	expected := []int64{outputSequence, resultSequence}
	require.Equal(t, expected, otelDeliverySequences(t, fixture, orgWide.ID))
	require.Equal(t, expected, otelDeliverySequences(t, fixture, project.ID))
	require.Empty(t, otelDeliverySequences(t, fixture, unsampled.ID))

	// New deliveries wait out the batching delay before they can be claimed.
	_, err = store.ClaimOtelExportDeliveries(ctx, 4, 100)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		`UPDATE otel_export_deliveries SET next_attempt_at = statement_timestamp() WHERE org_id = $1`, testOrgID)
	require.NoError(t, err)

	first, err := store.ClaimOtelExportDeliveries(ctx, 4, 100)
	require.NoError(t, err)
	second, err := store.ClaimOtelExportDeliveries(ctx, 4, 100)
	require.NoError(t, err)
	_, err = store.ClaimOtelExportDeliveries(ctx, 4, 100)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ElementsMatch(t, []uuid.UUID{orgWide.ID, project.ID}, []uuid.UUID{first.ExporterID, second.ExporterID})
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	for _, batch := range []executionstore.OtelExportBatch{first, second} {
		require.Len(t, batch.Deliveries, 2)
		for i, delivery := range batch.Deliveries {
			require.Equal(t, fixture.AgentID, delivery.AgentID)
			require.Equal(t, expected[i], delivery.EventSequence)
			require.EqualValues(t, 1, delivery.AttemptCount)
		}
	}

	turn, err := store.LoadTurnTraceForEvent(ctx, fixture.AgentID, resultSequence)
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, toolID, turn.ToolCalls[0].ID)
	require.NotEmpty(t, turn.ModelCalls)
	bySequence := map[int64][]string{}
	for _, span := range oteltrace.Build(turn, oteltrace.VariantOTel) {
		bySequence[span.EventSequence] = append(bySequence[span.EventSequence], span.Span.Name)
		require.Equal(t, oteltrace.TraceID(turn.TurnID), span.Span.TraceID)
	}
	require.Len(t, bySequence[outputSequence], 1)
	require.Contains(t, bySequence[outputSequence][0], "chat ")
	require.Equal(t, []string{"execute_tool ask_question"}, bySequence[resultSequence])
	_, err = store.LoadTurnTraceForEvent(ctx, fixture.AgentID, resultSequence+1000)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	orgBatch, projectBatch := first, second
	if first.ExporterID != orgWide.ID {
		orgBatch, projectBatch = second, first
	}
	destination, err := store.GetOtelExportDestination(ctx, testOrgID, orgWide.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.OtelExportDestination{
		Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse,
		Headers: map[string]string{"x-langfuse-ingestion-version": "4", "Authorization": "Bearer test-key"},
	}, destination)

	require.NoError(t, store.CompleteOtelExportDeliveries(ctx, orgBatch.ClaimToken))
	require.Empty(t, otelDeliverySequences(t, fixture, orgWide.ID))
	require.NoError(t, store.RetryOtelExportDeliveries(ctx, projectBatch.ClaimToken, 0))
	retried, err := store.ClaimOtelExportDeliveries(ctx, 4, 1)
	require.NoError(t, err)
	require.Equal(t, project.ID, retried.ExporterID)
	require.Len(t, retried.Deliveries, 1)
	require.EqualValues(t, 2, retried.Deliveries[0].AttemptCount)

	sampleRate := int32(2500)
	updated, err := store.UpdateOtelExporter(ctx, executionstore.UpdateOtelExporterInput{
		OrgID: testOrgID, ExporterID: orgWide.ID, SampleBasisPoints: &sampleRate, SecretHeaders: map[string]uuid.UUID{},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2500, updated.Settings.SampleBasisPoints)
	require.Empty(t, updated.Settings.SecretHeaders)
	require.Equal(t, map[string]string{"x-langfuse-ingestion-version": "4"}, updated.Settings.Headers)

	listed, err := store.ListOtelExporters(ctx, executionstore.ListOtelExportersInput{OrgID: testOrgID, Limit: 100})
	require.NoError(t, err)
	require.Len(t, listed.Exporters, 3)
	listed, err = store.ListOtelExporters(ctx, executionstore.ListOtelExportersInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 100,
	})
	require.NoError(t, err)
	require.Len(t, listed.Exporters, 2)

	// Queued deliveries of a deleted exporter have no destination left.
	require.NoError(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID))
	_, err = store.GetOtelExportDestination(ctx, testOrgID, project.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ErrorIs(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID), storeerr.ErrNotFound)
}

func TestOtelTraceSamplingMatchesGoDecision(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "otel_sampling")
	for range 200 {
		turnID := uuid.New()
		for _, basisPoints := range []int32{0, 1, 2500, 9999, 10000} {
			var sampled bool
			require.NoError(t, fixture.Store.pool.QueryRow(ctx,
				`SELECT otel_trace_sampled($1, $2)`, turnID, basisPoints).Scan(&sampled))
			require.Equal(t, oteltrace.Sampled(turnID, basisPoints), sampled, "%s at %d", turnID, basisPoints)
		}
	}
}
