//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
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

func closeTurnForOtelTest(t *testing.T, fixture processDaemonFixture, sequence int64) {
	t.Helper()
	ctx := t.Context()
	pending := notifications.NewTxNotifications()
	pending.AddAgentEvent(fixture.AgentID, sequence, "model_output")
	unit, err := fixture.Store.Execution().IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(ctx) }()
	*unit.Notifications() = *pending
	require.NoError(t, unit.Commit(ctx, "otel export test"))
}

func TestOtelExportersQueueSampledTurnsAndClaimDeliveries(t *testing.T) {
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

	toolID := createToolCallForProcessTestWithPermission(t, ctx, fixture, "otel-export", "ask_question", true)
	ownedCompleteTool(t, ctx, fixture, toolID)
	var outputSequence int64
	var turnID uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT sequence, turn_id FROM agent_events
		WHERE agent_id = $1 AND event_kind = 'model_output' ORDER BY sequence DESC LIMIT 1`,
		fixture.AgentID).Scan(&outputSequence, &turnID))
	closeTurnForOtelTest(t, fixture, outputSequence)
	require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))

	// An output cut off at max_tokens is continued by another model call, so
	// it leaves the turn open; an output that ends the turn queues it.
	for _, stopReason := range []string{"max_tokens", "end_turn"} {
		for _, statement := range []string{
			`ALTER TABLE model_outputs DISABLE TRIGGER USER`,
			`UPDATE model_outputs SET stop_reason = '` + stopReason + `' WHERE agent_id = '` + fixture.AgentID.String() + `'`,
			`ALTER TABLE model_outputs ENABLE TRIGGER USER`,
		} {
			_, err = fixture.Store.pool.Exec(ctx, statement)
			require.NoError(t, err)
		}
		if stopReason == "max_tokens" {
			closeTurnForOtelTest(t, fixture, outputSequence)
			require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))
		}
	}
	closeTurnForOtelTest(t, fixture, outputSequence)
	closeTurnForOtelTest(t, fixture, outputSequence)
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
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, toolID, turn.ToolCalls[0].ID)
	require.NotEmpty(t, turn.ModelCalls)
	var names []string
	spans := oteltrace.Build(turn, oteltrace.VariantOTel).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	for _, span := range spans.All() {
		names = append(names, span.Name())
		require.Equal(t, oteltrace.TraceID(turnID), span.TraceID())
	}
	require.Len(t, names, 2+len(turn.ModelCalls))
	require.Contains(t, names[0], "invoke_agent")
	require.Equal(t, "execute_tool ask_question", names[len(names)-1])
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

	// The turn closing again re-arms the claimed delivery, so the stale claim
	// can no longer complete it.
	closeTurnForOtelTest(t, fixture, outputSequence)
	require.NoError(t, store.CompleteOtelExportDelivery(ctx, projectDelivery.ID, projectDelivery.ClaimToken))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, project.ID))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, orgWide.ID))
	_, err = fixture.Store.pool.Exec(ctx, `DELETE FROM otel_export_deliveries WHERE exporter_id = $1`, orgWide.ID)
	require.NoError(t, err)
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

	require.NoError(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID))
	_, err = store.GetOtelExportDestination(ctx, testOrgID, project.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ErrorIs(t, store.DeleteOtelExporter(ctx, testOrgID, project.ID), storeerr.ErrNotFound)
}
