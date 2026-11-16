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

// closeTurnForOtelTest commits a model output event notification the way the
// execution unit does, after marking that output as the turn's final answer.
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
			Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse, SamplePercent: 100,
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

	// A model output that calls a tool, then the tool's result: the turn is
	// still open, so nothing is queued.
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

	// An output that does not ask for tools closes the turn: one delivery per
	// sampled exporter, however many times the turn closes while it is queued.
	for _, statement := range []string{
		`ALTER TABLE model_outputs DISABLE TRIGGER USER`,
		`UPDATE model_outputs SET stop_reason = 'end_turn' WHERE agent_id = '` + fixture.AgentID.String() + `'`,
		`ALTER TABLE model_outputs ENABLE TRIGGER USER`,
	} {
		_, err = fixture.Store.pool.Exec(ctx, statement)
		require.NoError(t, err)
	}
	closeTurnForOtelTest(t, fixture, outputSequence)
	closeTurnForOtelTest(t, fixture, outputSequence)
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, orgWide.ID))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, project.ID))
	require.Empty(t, otelDeliveryTurns(t, fixture, unsampled.ID))

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
		require.Len(t, batch.Deliveries, 1)
		require.Equal(t, fixture.AgentID, batch.Deliveries[0].AgentID)
		require.Equal(t, turnID, batch.Deliveries[0].TurnID)
		require.EqualValues(t, 1, batch.Deliveries[0].AttemptCount)
	}

	turn, err := store.LoadTurnTraceForExport(ctx, fixture.AgentID, turnID)
	require.NoError(t, err)
	require.Len(t, turn.ToolCalls, 1)
	require.Equal(t, toolID, turn.ToolCalls[0].ID)
	require.NotEmpty(t, turn.ModelCalls)
	var names []string
	for _, span := range oteltrace.Build(turn, oteltrace.VariantOTel) {
		names = append(names, span.Name)
		require.Equal(t, oteltrace.TraceID(turnID), span.TraceID)
	}
	require.Len(t, names, 2+len(turn.ModelCalls))
	require.Contains(t, names[0], "invoke_agent")
	require.Equal(t, "execute_tool ask_question", names[len(names)-1])
	_, err = store.LoadTurnTraceForExport(ctx, fixture.AgentID, uuid.New())
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = store.LoadTurnTrace(ctx, uuid.New(), fixture.AgentID, turnID)
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
	require.Empty(t, otelDeliveryTurns(t, fixture, orgWide.ID))

	// A turn that closes again while its export is in flight is re-armed: the
	// in-flight claim no longer owns the row, so completing it deletes nothing.
	closeTurnForOtelTest(t, fixture, outputSequence)
	require.NoError(t, store.CompleteOtelExportDeliveries(ctx, projectBatch.ClaimToken))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, project.ID))
	// Closing again also queued the organization exporter afresh; set it aside.
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, orgWide.ID))
	_, err = fixture.Store.pool.Exec(ctx, `DELETE FROM otel_export_deliveries WHERE exporter_id = $1`, orgWide.ID)
	require.NoError(t, err)
	_, err = fixture.Store.pool.Exec(ctx,
		`UPDATE otel_export_deliveries SET next_attempt_at = statement_timestamp() WHERE org_id = $1`, testOrgID)
	require.NoError(t, err)
	projectBatch, err = store.ClaimOtelExportDeliveries(ctx, 4, 100)
	require.NoError(t, err)
	require.NoError(t, store.RetryOtelExportDeliveries(ctx, projectBatch.ClaimToken, 0))
	retried, err := store.ClaimOtelExportDeliveries(ctx, 4, 1)
	require.NoError(t, err)
	require.Equal(t, project.ID, retried.ExporterID)
	require.Len(t, retried.Deliveries, 1)
	require.EqualValues(t, 3, retried.Deliveries[0].AttemptCount)

	sampleRate := 25.5
	updated, err := store.UpdateOtelExporter(ctx, executionstore.UpdateOtelExporterInput{
		OrgID: testOrgID, ExporterID: orgWide.ID, SamplePercent: &sampleRate, SecretHeaders: map[string]uuid.UUID{},
	})
	require.NoError(t, err)
	require.InDelta(t, 25.5, updated.Settings.SamplePercent, 0)
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
		for _, percent := range []float64{0, 0.01, 25, 33.33, 99.99, 100} {
			var sampled bool
			require.NoError(t, fixture.Store.pool.QueryRow(ctx,
				`SELECT otel_trace_sampled($1, $2::numeric)`, turnID, percent).Scan(&sampled))
			require.Equal(t, oteltrace.Sampled(turnID, percent), sampled, "%s at %v", turnID, percent)
		}
	}
}
