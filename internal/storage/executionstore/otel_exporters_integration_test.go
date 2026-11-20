//go:build integration

package executionstore_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
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
func startOtelTestTurn(
	t *testing.T, fixture processDaemonFixture, key string, priorOpeningIDs ...uuid.UUID,
) (turnID, contextID, inputID uuid.UUID) {
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
	openingIDs := append(append([]uuid.UUID(nil), priorOpeningIDs...), input.ID)
	prepared, err := fixture.Store.Execution().PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		OpeningInputIDs: openingIDs,
	})
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT id FROM agent_turns WHERE agent_id = $1 ORDER BY turn_sequence DESC LIMIT 1`,
		fixture.AgentID).Scan(&turnID))
	return turnID, prepared.Claim.Context.ID, input.ID
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
		Material: secrets.GenericMaterial{Value: "sk-lf-secret"}, Actor: userPrincipal(owner.ID),
	})
	require.NoError(t, err)
	extraSecret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "otel-extra",
		Material: secrets.GenericMaterial{Value: "extra-token"}, Actor: userPrincipal(owner.ID),
	})
	require.NoError(t, err)

	projectExporter := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		otelconfig.Settings{
			Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse, SamplePercent: 100,
			Encoding:           oteltrace.EncodingProtobuf,
			AuthKind:           otelconfig.AuthKindBasic,
			AuthOptions:        otelconfig.AuthOptions{Username: "pk-lf-123"},
			CredentialSecretID: secret.ID,
			Headers:            map[string]string{"x-langfuse-ingestion-version": "4"},
			SecretHeaders:      map[string]uuid.UUID{"x-extra-auth": extraSecret.ID},
		})
	agentExporter := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{
			Type: executionstore.OtelExporterScopeAgent, ProjectID: testProjectID, AgentID: fixture.AgentID,
		},
		otelconfig.Settings{Endpoint: "https://agent-active.example.com/v1/traces", SamplePercent: 100})
	unsampled := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{
			Type: executionstore.OtelExporterScopeAgent, ProjectID: testProjectID, AgentID: fixture.AgentID,
		},
		otelconfig.Settings{Endpoint: "https://agent.example.com/v1/traces", SamplePercent: 0})
	require.Equal(t, oteltrace.VariantOTel, agentExporter.Settings.Variant)
	require.Equal(t, oteltrace.EncodingProtobuf, agentExporter.Settings.Encoding)
	require.Equal(t, otelconfig.AuthKindNone, agentExporter.Settings.AuthKind)

	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID:    testOrgID,
		Scope:    executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		Settings: otelconfig.Settings{Endpoint: "https://otel.example.com/v1/traces", SamplePercent: 100},
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	_, err = store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: testOrgID,
		Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		Settings: otelconfig.Settings{Endpoint: "https://other.example.com", SamplePercent: 100,
			AuthKind: otelconfig.AuthKindBearerToken, CredentialSecretID: uuid.New()},
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)

	text := modelenvelope.ResponsePart{Type: "text", Text: "done"}

	// An output that neither calls tools nor is cut off finishes its turn.
	turnID, contextID, _ := startOtelTestTurn(t, fixture, "finished")
	require.Empty(t, otelDeliveryTurns(t, fixture, projectExporter.ID))
	recordOtelTestOutput(t, fixture, contextID, modelenvelope.StopReasonEndTurn, text)
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, projectExporter.ID))
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, agentExporter.ID))
	require.Empty(t, otelDeliveryTurns(t, fixture, unsampled.ID))

	first, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	second, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	_, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ElementsMatch(t,
		[]uuid.UUID{projectExporter.ID, agentExporter.ID},
		[]uuid.UUID{first.ExporterID, second.ExporterID})
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

	projectDelivery, agentDelivery := first, second
	if first.ExporterID != projectExporter.ID {
		projectDelivery, agentDelivery = second, first
	}
	destination, err := store.GetOtelExportDestination(ctx, testOrgID, projectExporter.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.OtelExportDestination{
		Endpoint: "https://otel.example.com/v1/traces", Variant: oteltrace.VariantLangfuse,
		Encoding: oteltrace.EncodingProtobuf,
		Headers: map[string]string{
			"x-langfuse-ingestion-version": "4",
			"Authorization":                "Basic " + base64.StdEncoding.EncodeToString([]byte("pk-lf-123:sk-lf-secret")),
			"x-extra-auth":                 "extra-token",
		},
	}, destination)

	require.NoError(t, store.CompleteOtelExportDelivery(ctx, projectDelivery.ID, projectDelivery.ClaimToken))
	require.Empty(t, otelDeliveryTurns(t, fixture, projectExporter.ID))

	retryResult, err := store.RetryOtelExportDelivery(ctx, agentDelivery.ID, agentDelivery.ClaimToken, 0)
	require.NoError(t, err)
	require.False(t, retryResult.GaveUp)
	agentDelivery, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	// Only the current claim can release the delivery for another attempt.
	_, err = store.RetryOtelExportDelivery(ctx, agentDelivery.ID, uuid.New(), 0)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = store.ClaimOtelExportDelivery(ctx, 4)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	retryResult, err = store.RetryOtelExportDelivery(ctx, agentDelivery.ID, agentDelivery.ClaimToken, 0)
	require.NoError(t, err)
	require.False(t, retryResult.GaveUp)
	retried, err := store.ClaimOtelExportDelivery(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, agentExporter.ID, retried.ExporterID)
	require.EqualValues(t, 3, retried.AttemptCount)
	require.NoError(t, store.CompleteOtelExportDelivery(ctx, retried.ID, retried.ClaimToken))

	// A retry-waiting model attempt (e.g. after 429) superseded by a steering
	// message must also queue its replaced turn for export.
	retryingTurn, retryContextID, retryingInputID := startOtelTestTurn(t, fixture, "retry-replaced")
	_, err = store.RecordRetryableModelCallFailure(ctx, executionstore.RecordRecoverableModelCallFailureInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		ModelCallContextID: retryContextID, ErrorKind: "rate_limited", ErrorCode: "429",
		ErrorMessage: "rate limited", RetryDelay: time.Minute,
	})
	require.NoError(t, err)
	require.Empty(t, otelDeliveryTurns(t, fixture, projectExporter.ID))
	nextTurn, nextContextID, _ := startOtelTestTurn(t, fixture, "steering-supersedes", retryingInputID)
	require.Equal(t, []uuid.UUID{retryingTurn}, otelDeliveryTurns(t, fixture, projectExporter.ID))
	recordOtelTestOutput(t, fixture, nextContextID, modelenvelope.StopReasonEndTurn, text)
	require.Equal(t, []uuid.UUID{retryingTurn, nextTurn}, otelDeliveryTurns(t, fixture, projectExporter.ID))

	// An output cut off at the output limit is continued by another model
	// call, so its turn stays open until a new turn replaces it.
	cutOffTurn, contextID, _ := startOtelTestTurn(t, fixture, "cut-off")
	recordOtelTestOutput(t, fixture, contextID, modelenvelope.StopReasonMaxTokens, text)
	require.Equal(t, []uuid.UUID{retryingTurn, nextTurn}, otelDeliveryTurns(t, fixture, projectExporter.ID))
	canceledTurn, _, _ := startOtelTestTurn(t, fixture, "canceled")
	require.Equal(t, []uuid.UUID{retryingTurn, nextTurn, cutOffTurn}, otelDeliveryTurns(t, fixture, projectExporter.ID))

	// Cancellation finishes a turn that has no closing output.
	_, err = store.CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
	})
	require.NoError(t, err)
	wantTurns := []uuid.UUID{retryingTurn, nextTurn, cutOffTurn, canceledTurn}
	require.Equal(t, wantTurns, otelDeliveryTurns(t, fixture, projectExporter.ID))
	require.Equal(t, wantTurns, otelDeliveryTurns(t, fixture, agentExporter.ID))
	canceled, err := store.LoadTurnTraceForExport(ctx, fixture.AgentID, canceledTurn)
	require.NoError(t, err)
	require.Len(t, canceled.ModelCalls, 1)
	require.Equal(t, "canceled", canceled.ModelCalls[0].State)

	// A secret referenced by credential_secret_id or secret_headers cannot be deleted from under it.
	for _, secretID := range []uuid.UUID{secret.ID, extraSecret.ID} {
		_, err = fixture.Store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
			OrgID: testOrgID, SecretID: secretID, Actor: userPrincipal(owner.ID),
		})
		require.ErrorIs(t, err, storeerr.ErrConflict)
	}

	sampleRate := 12.345
	noneAuth := otelconfig.AuthKindNone
	updated, err := store.UpdateOtelExporter(ctx, executionstore.UpdateOtelExporterInput{
		OrgID: testOrgID, ExporterID: projectExporter.ID, SamplePercent: &sampleRate,
		AuthKind: &noneAuth, SecretHeaders: map[string]uuid.UUID{},
	})
	require.NoError(t, err)
	require.InDelta(t, 12.345, updated.Settings.SamplePercent, 0)
	require.Equal(t, otelconfig.AuthKindNone, updated.Settings.AuthKind)
	require.Equal(t, uuid.Nil, updated.Settings.CredentialSecretID)
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
	require.Equal(t, projectExporter.ID, listed.Exporters[0].ID)

	for _, secretID := range []uuid.UUID{secret.ID, extraSecret.ID} {
		_, err = fixture.Store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
			OrgID: testOrgID, SecretID: secretID, Actor: userPrincipal(owner.ID),
		})
		require.NoError(t, err)
	}

	// Deleting an exporter drops the turns still queued for it.
	require.NotEmpty(t, otelDeliveryTurns(t, fixture, agentExporter.ID))
	require.NoError(t, store.DeleteOtelExporter(ctx, testOrgID, agentExporter.ID))
	require.Empty(t, otelDeliveryTurns(t, fixture, agentExporter.ID))
	_, err = store.GetOtelExportDestination(ctx, testOrgID, agentExporter.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.ErrorIs(t, store.DeleteOtelExporter(ctx, testOrgID, agentExporter.ID), storeerr.ErrNotFound)

	// Expired deliveries are returned with metadata so maintenance can log them.
	_, err = fixture.Store.pool.Exec(ctx, `
		UPDATE otel_export_deliveries
		SET created_at = statement_timestamp() - interval '2 hours'
		WHERE exporter_id = $1`, projectExporter.ID)
	require.NoError(t, err)
	expired, err := store.DeleteExpiredOtelExportDeliveries(ctx, 100)
	require.NoError(t, err)
	require.Len(t, expired, 4)
	for _, d := range expired {
		require.Equal(t, testOrgID, d.OrgID)
		require.Equal(t, projectExporter.ID, d.ExporterID)
		require.Equal(t, fixture.AgentID, d.AgentID)
		require.NotEqual(t, uuid.Nil, d.TurnID)
	}
}

func TestTurnTraceIgnoresCallerSpoofedIdempotencyKeyAndBoundsContent(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "otel_provenance")
	store := fixture.Store.Execution()

	// A caller passing a spawn-shaped idempotency key on an ordinary content
	// input must not forge a trace link, and oversized content is bounded in SQL.
	spoofedToolID := uuid.New()
	bigText := strings.Repeat("a", 600*1024)
	contentBlocks, err := json.Marshal([]map[string]string{{"type": "text", "text": bigText}})
	require.NoError(t, err)
	input, _, _, err := store.CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
		ContentBlocks:  contentBlocks,
		IdempotencyKey: "launch:spawn:" + spoofedToolID.String() + ":content-input",
		DeliveryMode:   executionstore.DeliveryModeSteering,
	})
	require.NoError(t, err)
	_, found := admitNextAgentInputAndOpenTurnForTest(
		t, ctx, fixture.Store, testProjectID, fixture.AgentID, fixture.Lock.ID)
	require.True(t, found)
	prepared, err := store.PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		OpeningInputIDs: []uuid.UUID{input.ID},
	})
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	var turnID uuid.UUID
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT id FROM agent_turns WHERE agent_id = $1 ORDER BY turn_sequence DESC LIMIT 1`,
		fixture.AgentID).Scan(&turnID))
	recordOtelTestOutput(t, fixture, prepared.Claim.Context.ID, modelenvelope.StopReasonEndTurn,
		modelenvelope.ResponsePart{Type: "text", Text: "ok"})

	turn, err := store.LoadTurnTrace(ctx, testProjectID, fixture.AgentID, turnID)
	require.NoError(t, err)
	require.Empty(t, turn.Links)
	require.True(t, turn.ContentTruncated)
	spans := oteltrace.Build(turn, oteltrace.VariantOTel).ResourceSpans().At(0).ScopeSpans().At(0).Spans()
	truncated, ok := spans.At(0).Attributes().Get("omnara.trace.content_truncated")
	require.True(t, ok)
	require.True(t, truncated.Bool())
}

func TestOtelExporterConfigChangeDefersTurnExport(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "otel_config_defer")
	store := fixture.Store.Execution()

	exporter := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		otelconfig.Settings{Endpoint: "https://config-defer.example.com/v1/traces", SamplePercent: 100})

	turnID, firstContextID, openingInputID := startOtelTestTurn(t, fixture, "config-in-flight")

	// Activate a new config while the first model call is still in flight.
	newConfigID := mustCreateAgentConfig(t, ctx, fixture.Store, testProjectID)
	unit, err := store.IntegrationBeginUnit(ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(ctx) }()
	_, err = executionstore.IntegrationActivateAgentConfigTx(
		ctx, unit, dbsqlc.New(unit.DB()), executionstore.ActivateAgentConfigInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, AgentConfigID: newConfigID,
			ActorType: identitystore.PrincipalTypeSystem, Reason: "update", IdempotencyKey: "otel-config-defer",
		})
	require.NoError(t, err)
	require.NoError(t, unit.Commit(ctx, "activate config"))

	// Finishing the first call must NOT export the turn yet because OriginConfig
	// still continues this turn.
	text := modelenvelope.ResponsePart{Type: "text", Text: "first"}
	recordOtelTestOutput(t, fixture, firstContextID, modelenvelope.StopReasonEndTurn, text)
	require.Empty(t, otelDeliveryTurns(t, fixture, exporter.ID))

	// Completing the follow-up OriginConfig model call finishes and queues the turn.
	prepared, err := store.PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, RuntimeLockID: fixture.Lock.ID,
		OpeningInputIDs: []uuid.UUID{openingInputID},
	})
	require.NoError(t, err)
	require.True(t, prepared.Claim.Claimed)
	recordOtelTestOutput(t, fixture, prepared.Claim.Context.ID, modelenvelope.StopReasonEndTurn,
		modelenvelope.ResponsePart{Type: "text", Text: "after-config"})
	require.Equal(t, []uuid.UUID{turnID}, otelDeliveryTurns(t, fixture, exporter.ID))
}

func TestOtelExporterCreateAndUpdateSerializeWithSecretDeletion(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "otel_secret_race")
	store := fixture.Store.Execution()
	owner := createSecretTestUser(t, ctx, fixture.Store, "Otel Race Owner", "owner")

	newSecret := func(name string) uuid.UUID {
		s, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: name,
			Material: secrets.GenericMaterial{Value: "token-val"}, Actor: userPrincipal(owner.ID),
		})
		require.NoError(t, err)
		return s.ID
	}

	// Case 1: CreateOtelExporter waits on LockSecretForReference while DeleteSecret
	// holds the secret row lock, then fails cleanly after deletion commits.
	createSecretID := newSecret("otel-race-create")
	controlTx, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = controlTx.Rollback(ctx) }()
	var versionID uuid.UUID
	require.NoError(t, controlTx.QueryRow(ctx, `
		SELECT id FROM secret_versions WHERE org_id = $1 AND secret_id = $2 FOR UPDATE`,
		testOrgID, createSecretID).Scan(&versionID))

	deleteDone := integrationdb.RunAsyncError(func() error {
		_, delErr := fixture.Store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
			OrgID: testOrgID, SecretID: createSecretID, Actor: userPrincipal(owner.ID),
		})
		return delErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "DeleteSecretVersions", 1)

	createDone := integrationdb.RunAsyncError(func() error {
		_, createErr := store.CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
			OrgID: testOrgID,
			Scope: executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
			Settings: otelconfig.Settings{
				Endpoint: "https://race-create.example.com/v1/traces", SamplePercent: 100,
				AuthKind: otelconfig.AuthKindBearerToken, CredentialSecretID: createSecretID,
			},
		})
		return createErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockSecretForReference", 1)
	require.NoError(t, controlTx.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, deleteDone, "delete secret for create race"))
	require.ErrorIs(t,
		integrationdb.Await(t, createDone, "create exporter after secret deletion"), storeerr.ErrInvalidRequest)

	// Case 2: UpdateOtelExporter also serializes with DeleteSecret via LockSecretForReference.
	existing := createOtelExporterForTest(t, fixture,
		executionstore.OtelExporterScope{Type: executionstore.OtelExporterScopeProject, ProjectID: testProjectID},
		otelconfig.Settings{Endpoint: "https://race-update.example.com/v1/traces", SamplePercent: 100})
	updateSecretID := newSecret("otel-race-update")
	updateTx, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = updateTx.Rollback(ctx) }()
	require.NoError(t, updateTx.QueryRow(ctx, `
		SELECT id FROM secret_versions WHERE org_id = $1 AND secret_id = $2 FOR UPDATE`,
		testOrgID, updateSecretID).Scan(&versionID))

	deleteDone2 := integrationdb.RunAsyncError(func() error {
		_, delErr := fixture.Store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
			OrgID: testOrgID, SecretID: updateSecretID, Actor: userPrincipal(owner.ID),
		})
		return delErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "DeleteSecretVersions", 1)

	updateDone := integrationdb.RunAsyncError(func() error {
		bearer := otelconfig.AuthKindBearerToken
		_, updateErr := store.UpdateOtelExporter(ctx, executionstore.UpdateOtelExporterInput{
			OrgID: testOrgID, ExporterID: existing.ID,
			AuthKind: &bearer, SetCredentialSecretID: true, CredentialSecretID: updateSecretID,
		})
		return updateErr
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.Store.pool, "LockSecretForReference", 1)
	require.NoError(t, updateTx.Commit(ctx))
	require.NoError(t, integrationdb.Await(t, deleteDone2, "delete secret for update race"))
	require.ErrorIs(t,
		integrationdb.Await(t, updateDone, "update exporter after secret deletion"), storeerr.ErrInvalidRequest)
}
