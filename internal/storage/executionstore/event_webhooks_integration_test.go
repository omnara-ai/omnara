//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func enableEventWebhook(t *testing.T, fixture processDaemonFixture, events []string) {
	t.Helper()
	source := "instruction: Webhook test.\nmodel:\n  provider_config: openai-prod\n  name: webhook-test\n" +
		"event_webhook:\n  url: https://example.com/events\n"
	if events == nil {
		events = []string{"agent_input", "model_output", "tool_result", "context_checkpoint", "tool_call_update"}
	}
	encoded, err := json.Marshal(events)
	require.NoError(t, err)
	source += "  events: " + string(encoded) + "\n"
	user := mustCreateProjectDeveloperUser(t, t.Context(), fixture.Store, "webhook-config@example.com", "Webhook")
	compiled := mustCompileAgentYAMLResolved(t, t.Context(), fixture.Store, source)
	_, err = fixture.Store.Execution().ChangeAgentConfig(t.Context(), executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: testProjectID, Source: source, SourceFormat: "yaml",
			ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID: fixture.AgentID, ActorType: identitystore.PrincipalTypeUser, ActorID: user.ID, Reason: "user_update",
	})
	require.NoError(t, err)
}

func assertWebhookToolStates(t *testing.T, fixture processDaemonFixture, toolID uuid.UUID, expected []string) {
	t.Helper()
	rows, err := fixture.Store.pool.Query(t.Context(),
		"SELECT tool_state FROM event_webhook_deliveries WHERE agent_id = $1 AND tool_call_id = $2 ORDER BY id",
		fixture.AgentID, toolID)
	require.NoError(t, err)
	defer rows.Close()
	var states []string
	for rows.Next() {
		var state string
		require.NoError(t, rows.Scan(&state))
		states = append(states, state)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, expected, states)
}

func TestEventWebhookFiltersBeforeEnqueue(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		events      []string
		sequences   []int64
		toolUpdates int
	}{
		{"all", nil, []int64{1, 2, 3, 4}, 1},
		{"tools", []string{"tool_call_update"}, []int64{}, 1},
		{"timeline", []string{"model_output", "context_checkpoint"}, []int64{2, 4}, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := t.Context()
			fixture := newProcessDaemonFixture(t, ctx, "webhook_filter")
			enableEventWebhook(t, fixture, scenario.events)
			_, err := fixture.Store.pool.Exec(ctx, "DELETE FROM event_webhook_deliveries WHERE agent_id = $1", fixture.AgentID)
			require.NoError(t, err)
			pending := notifications.NewTxNotifications()
			for i, kind := range []string{"agent_input", "model_output", "tool_result", "context_checkpoint"} {
				pending.AddAgentEvent(fixture.AgentID, int64(i+1), kind)
			}
			pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready")
			tx, err := fixture.Store.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			require.NoError(t, fixture.Store.Execution().IntegrationCommitTxWithNotifications(ctx, tx, pending, "filter test"))
			var sequences []int64
			var toolUpdates int
			require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
				SELECT coalesce(
				    array_agg(event_sequence ORDER BY event_sequence) FILTER (WHERE event_sequence IS NOT NULL),
				    '{}'::bigint[]),
				       count(tool_call_id)
				FROM event_webhook_deliveries WHERE agent_id = $1 AND org_id = $2`,
				fixture.AgentID, testOrgID).Scan(&sequences, &toolUpdates))
			require.Equal(t, scenario.sequences, sequences)
			require.Equal(t, scenario.toolUpdates, toolUpdates)
		})
	}
}

func TestEventWebhookClaimsRecoverAndExpire(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_claims")
	enableEventWebhook(t, fixture, nil)
	_, err := fixture.Store.pool.Exec(ctx, "DELETE FROM event_webhook_deliveries WHERE agent_id = $1", fixture.AgentID)
	require.NoError(t, err)
	pending := notifications.NewTxNotifications()
	pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready")
	tx, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, fixture.Store.Execution().IntegrationCommitTxWithNotifications(ctx, tx, pending, "webhook test"))
	var attempts int
	var claimed bool
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT attempt_count, claim_token IS NOT NULL
		FROM event_webhook_deliveries WHERE agent_id = $1`, fixture.AgentID).Scan(&attempts, &claimed))
	require.Zero(t, attempts)
	require.False(t, claimed)
	first, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.NoError(t, err)
	require.Equal(t, testOrgID, first.OrgID)
	require.Equal(t, "ready", *first.ToolState)
	require.Equal(t, int32(1), first.AttemptCount)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET claim_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	second, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	require.Equal(t, int32(2), second.AttemptCount)
	require.NoError(t, fixture.Store.Execution().CompleteEventWebhookDelivery(ctx, first.ID, first.ClaimToken))
	_, err = fixture.Store.Execution().RetryEventWebhookDelivery(ctx, first.ID, first.ClaimToken, time.Minute)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	retry, err := fixture.Store.Execution().RetryEventWebhookDelivery(ctx, second.ID, second.ClaimToken, time.Minute)
	require.NoError(t, err)
	require.False(t, retry.GaveUp)
	var nextAttemptAt time.Time
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		"SELECT next_attempt_at FROM event_webhook_deliveries WHERE id = $1", second.ID).Scan(&nextAttemptAt))
	require.True(t, nextAttemptAt.Equal(retry.NextAttemptAt))
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET next_attempt_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	third, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.NoError(t, err)
	require.Equal(t, first.ID, third.ID)
	require.Equal(t, int32(3), third.AttemptCount)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET created_at = statement_timestamp() - interval '9 minutes 30 seconds' WHERE id = $1",
		third.ID)
	require.NoError(t, err)
	retry, err = fixture.Store.Execution().RetryEventWebhookDelivery(ctx, third.ID, third.ClaimToken, time.Minute)
	require.NoError(t, err)
	require.True(t, retry.GaveUp)
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		"SELECT next_attempt_at, claim_token IS NOT NULL FROM event_webhook_deliveries WHERE id = $1",
		third.ID).Scan(&nextAttemptAt, &claimed))
	require.True(t, nextAttemptAt.Equal(retry.NextAttemptAt))
	require.False(t, claimed)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		`UPDATE event_webhook_deliveries
   SET claim_token = $2, claim_expires_at = statement_timestamp() + interval '30 seconds' WHERE id = $1`,
		third.ID, third.ClaimToken)
	require.NoError(t, err)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET created_at = statement_timestamp() - interval '11 minutes' WHERE id = $1", first.ID)
	require.NoError(t, err)
	deleted, err := fixture.Store.Execution().DeleteExpiredEventWebhookDeliveries(ctx, 500)
	require.NoError(t, err)
	require.Zero(t, deleted)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET claim_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	deleted, err = fixture.Store.Execution().DeleteExpiredEventWebhookDeliveries(ctx, 500)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	var count int
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		"SELECT count(*) FROM event_webhook_deliveries WHERE id = $1", first.ID).Scan(&count))
	require.Zero(t, count)
}

func TestEventWebhookClaimsShareOrganizationLimits(t *testing.T) {
	t.Parallel()
	const perOrgLimit = 3
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_org_limits")
	enableEventWebhook(t, fixture, []string{"tool_call_update"})
	pool := fixture.Store.pool
	_, err := pool.Exec(ctx, "DELETE FROM event_webhook_deliveries")
	require.NoError(t, err)

	otherOrgID, otherProjectID := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx,
		"INSERT INTO orgs(id, name, created_at, updated_at) VALUES ($1, 'Other org', statement_timestamp(), statement_timestamp())",
		otherOrgID)
	require.NoError(t, err)
	storagefixture.InsertProject(t, ctx, pool, otherOrgID, otherProjectID, "Other project", "other-project", fixture.Now)
	_, err = fixture.Store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: otherOrgID, UserID: fixture.UserID, Role: "admin",
	})
	require.NoError(t, err)
	secret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: otherOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "provider-key",
		Material: secrets.GenericMaterial{Value: "test-key"}, Actor: userPrincipal(fixture.UserID),
	})
	require.NoError(t, err)
	_, err = fixture.Store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: otherOrgID, Name: "openai-prod", APIFormat: "openai-responses", APIVariant: "default",
		BaseURL: "https://example.com/v1", EndpointPath: "/responses", CredentialSecretID: secret.ID,
	})
	require.NoError(t, err)
	config := storagefixture.SeedAgentConfig(t, ctx, fixture.Store.Models(), fixture.Store.Execution(),
		otherOrgID, otherProjectID, testAgentConfigYAML()+`
event_webhook:
  url: https://example.com/events
  events: [tool_call_update]
`)
	otherAgent, err := fixture.Store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: otherProjectID, CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	pending := notifications.NewTxNotifications()
	for range perOrgLimit + 2 {
		pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready")
	}
	pending.AddToolCallUpdate(otherAgent.ID, uuid.New(), "ready")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, fixture.Store.Execution().IntegrationCommitTxWithNotifications(ctx, tx, pending, "org limits test"))

	workers := []*executionstore.Store{fixture.Store.Execution(), newIntegrationStore(pool).Execution()}
	claimed := make([]executionstore.EventWebhookDelivery, 0, perOrgLimit)
	for i := range perOrgLimit {
		delivery, claimErr := workers[i%2].ClaimEventWebhookDelivery(ctx, perOrgLimit)
		require.NoError(t, claimErr)
		require.Equal(t, testOrgID, delivery.OrgID)
		claimed = append(claimed, delivery)
	}
	other, err := workers[0].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.NoError(t, err)
	require.Equal(t, otherOrgID, other.OrgID)
	require.NoError(t, workers[0].CompleteEventWebhookDelivery(ctx, other.ID, other.ClaimToken))
	_, err = workers[1].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	require.NoError(t, workers[0].CompleteEventWebhookDelivery(ctx, claimed[0].ID, claimed[0].ClaimToken))
	delivery, err := workers[1].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.NoError(t, err)
	require.Equal(t, testOrgID, delivery.OrgID)
	_, err = workers[0].RetryEventWebhookDelivery(ctx, claimed[1].ID, claimed[1].ClaimToken, time.Minute)
	require.NoError(t, err)
	_, err = workers[1].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.NoError(t, err)
	_, err = workers[0].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, err = pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET claim_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1",
		claimed[2].ID)
	require.NoError(t, err)
	recovered, err := workers[1].ClaimEventWebhookDelivery(ctx, perOrgLimit)
	require.NoError(t, err)
	require.Equal(t, claimed[2].ID, recovered.ID)
	require.NotEqual(t, claimed[2].ClaimToken, recovered.ClaimToken)
}

func TestEventWebhookEnqueueFailureRollsBackTransaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_rollback")
	enableEventWebhook(t, fixture, nil)
	toolID := createTypedToolCallForProcessTest(t, ctx, fixture,
		"webhook_rollback", "external_lookup", toolcatalog.ToolTypeCustom, false)
	_, err := fixture.Store.pool.Exec(ctx,
		"ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_ready CHECK (tool_state <> 'ready')")
	require.NoError(t, err)
	input := executionstore.MarkToolCallReadyInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolID, RuntimeLockID: fixture.Lock.ID,
	}
	_, err = fixture.Store.Execution().MarkToolCallReady(ctx, input)
	require.Error(t, err)
	tool, err := fixture.Store.Execution().GetToolCall(ctx, testProjectID, fixture.AgentID, toolID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateAwaitingAuthorization, tool.State)
	assertWebhookToolStates(t, fixture, toolID, []string{"awaiting_authorization"})
	_, err = fixture.Store.pool.Exec(ctx, "ALTER TABLE event_webhook_deliveries DROP CONSTRAINT reject_ready")
	require.NoError(t, err)
	_, err = fixture.Store.Execution().MarkToolCallReady(ctx, input)
	require.NoError(t, err)
	assertWebhookToolStates(t, fixture, toolID, []string{"awaiting_authorization", "ready"})
}

func TestEventWebhookCleanupIsBoundedAndPreservesUnexpiredDeliveries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_cleanup")
	enableEventWebhook(t, fixture, []string{"tool_call_update"})
	pending := notifications.NewTxNotifications()
	toolIDs := make([]uuid.UUID, 6)
	for i := range toolIDs {
		toolIDs[i] = uuid.New()
		pending.AddToolCallUpdate(fixture.AgentID, toolIDs[i], "ready")
	}
	tx, err := fixture.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, fixture.Store.Execution().IntegrationCommitTxWithNotifications(ctx, tx, pending, "cleanup test"))
	for i, toolID := range toolIDs {
		_, err := fixture.Store.pool.Exec(ctx, `
			UPDATE event_webhook_deliveries
			SET created_at = statement_timestamp() - $2::double precision * interval '4 minutes'
			WHERE tool_call_id = $1`, toolID, i)
		require.NoError(t, err)
	}
	for _, expected := range []int{2, 1, 0} {
		deleted, err := fixture.Store.Execution().DeleteExpiredEventWebhookDeliveries(ctx, 2)
		require.NoError(t, err)
		require.EqualValues(t, expected, deleted)
	}
	var remaining int
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		"SELECT count(*) FROM event_webhook_deliveries WHERE agent_id = $1", fixture.AgentID).Scan(&remaining))
	require.Equal(t, 3, remaining)
}

func TestEventWebhookDeletedOwnersHaveNoDeliveryTarget(t *testing.T) {
	for _, owner := range []struct {
		name  string
		query string
		id    uuid.UUID
	}{
		{"project", "UPDATE projects SET deleted_at = statement_timestamp() WHERE id = $1", testProjectID},
		{"org", "UPDATE orgs SET deleted_at = statement_timestamp() WHERE id = $1", testOrgID},
	} {
		t.Run(owner.name, func(t *testing.T) {
			ctx := t.Context()
			fixture := newProcessDaemonFixture(t, ctx, "webhook_deleted_owner")
			enableEventWebhook(t, fixture, nil)
			target, err := fixture.Store.Execution().GetAgentEventWebhookTarget(ctx, fixture.AgentID)
			require.NoError(t, err)
			require.NotEmpty(t, target.URL)
			pending := notifications.NewTxNotifications()
			pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready")
			tx, err := fixture.Store.pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			require.NoError(t, fixture.Store.Execution().IntegrationCommitTxWithNotifications(ctx, tx, pending, "deleted owner"))
			_, err = fixture.Store.pool.Exec(ctx, owner.query, owner.id)
			require.NoError(t, err)
			delivery, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, 128)
			require.NoError(t, err)
			require.Equal(t, testOrgID, delivery.OrgID)
			target, err = fixture.Store.Execution().GetAgentEventWebhookTarget(ctx, delivery.AgentID)
			require.NoError(t, err)
			require.Empty(t, target.URL)
			require.NoError(t, fixture.Store.Execution().CompleteEventWebhookDelivery(ctx, delivery.ID, delivery.ClaimToken))
		})
	}
}
