//go:build integration

package executionstore_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func enableEventWebhook(t *testing.T, fixture processDaemonFixture, events *[]string) {
	t.Helper()
	source := "instruction: Webhook test.\nmodel:\n  provider_config: openai-prod\n  name: webhook-test\n" +
		"event_webhook:\n  url: https://example.com/events\n"
	if events != nil {
		encoded, err := json.Marshal(*events)
		require.NoError(t, err)
		source += "  events: " + string(encoded) + "\n"
	}
	user := mustCreateProjectDeveloperUser(t, t.Context(), fixture.Store, "webhook-config@example.com", "Webhook")
	compiled := mustCompileAgentYAMLResolved(t, t.Context(), fixture.Store, source)
	_, err := fixture.Store.Execution().ChangeAgentConfig(t.Context(), executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: testProjectID, Source: source, SourceFormat: "yaml",
			ConfiguredModelID: parseConfiguredModelID(t, compiled), CompiledDefinition: compiled.CanonicalJSON,
			CompilerVersion: agentconfig.CompilerVersion, EffectiveDefinitionHash: compiled.Hash,
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
			var filter *[]string
			if scenario.events != nil {
				filter = &scenario.events
			}
			enableEventWebhook(t, fixture, filter)
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

func TestEventWebhookRetryClassification(t *testing.T) {
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_retry_policy")
	for _, tool := range []struct {
		name        string
		kind        string
		retryStates []string
	}{
		{"external_lookup", toolcatalog.ToolTypeCustom, []string{"awaiting_permission", "ready"}},
		{"ask_question", toolcatalog.ToolTypeCustom, []string{"awaiting_permission", "ready"}},
		{toolcatalog.ToolNameAskQuestion, toolcatalog.ToolTypeBuiltIn, []string{"awaiting_permission", "running"}},
		{"run_command", toolcatalog.ToolTypeBuiltIn, []string{"awaiting_permission"}},
		{"ask_question", toolcatalog.ToolTypeMCP, []string{"awaiting_permission"}},
	} {
		t.Run(tool.kind+"/"+tool.name, func(t *testing.T) {
			fixture := newProcessDaemonFixture(t, ctx, "webhook_retry_policy")
			toolID := createTypedToolCallForProcessTest(t, ctx, fixture,
				"webhook_"+tool.kind+"_"+tool.name, tool.name, tool.kind, false)
			for _, state := range []string{
				"awaiting_authorization", "awaiting_permission", "ready", "running", "waiting", "completed",
			} {
				t.Run(state, func(t *testing.T) {
					_, err := fixture.Store.pool.Exec(ctx, `
						INSERT INTO event_webhook_deliveries (agent_id, tool_call_id, tool_state, org_id)
						VALUES ($1, $2, $3, $4)`, fixture.AgentID, toolID, state, testOrgID)
					require.NoError(t, err)
					delivery, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
					require.NoError(t, err)
					require.Equal(t, slices.Contains(tool.retryStates, state), delivery.Retryable)
					require.NoError(t, fixture.Store.Execution().CompleteEventWebhookDelivery(ctx, delivery.ID, delivery.ClaimToken))
				})
			}
		})
	}
	_, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO event_webhook_deliveries (agent_id, event_sequence, org_id)
		VALUES ($1, 1, $2)`, fixture.AgentID, testOrgID)
	require.NoError(t, err)
	delivery, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.NoError(t, err)
	require.False(t, delivery.Retryable)
}

func TestEventWebhookClaimSkipsExcludedOrganizations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "webhook_exclusion")
	_, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO event_webhook_deliveries (agent_id, tool_call_id, tool_state, org_id)
		VALUES ($1, uuidv7(), 'ready', $2)`, fixture.AgentID, testOrgID)
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, []uuid.UUID{testOrgID})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var attempts int
	var claimed bool
	require.NoError(t, fixture.Store.pool.QueryRow(ctx, `
		SELECT attempt_count, claim_token IS NOT NULL
		FROM event_webhook_deliveries WHERE agent_id = $1`, fixture.AgentID).Scan(&attempts, &claimed))
	require.Zero(t, attempts)
	require.False(t, claimed)
	delivery, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, []uuid.UUID{uuid.New()})
	require.NoError(t, err)
	require.Equal(t, testOrgID, delivery.OrgID)
	require.Equal(t, int32(1), delivery.AttemptCount)
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
	first, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "ready", *first.ToolState)
	require.Equal(t, int32(1), first.AttemptCount)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET claim_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	second, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	require.Equal(t, int32(2), second.AttemptCount)
	require.NoError(t, fixture.Store.Execution().CompleteEventWebhookDelivery(ctx, first.ID, first.ClaimToken))
	require.NoError(t, fixture.Store.Execution().RetryEventWebhookDelivery(ctx, second.ID, second.ClaimToken, time.Minute))
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET next_attempt_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	third, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, first.ID, third.ID)
	require.Equal(t, int32(3), third.AttemptCount)
	_, err = fixture.Store.pool.Exec(ctx,
		"UPDATE event_webhook_deliveries SET created_at = statement_timestamp() - interval '11 minutes', claim_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1", first.ID)
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	deleted, err := fixture.Store.Execution().DeleteExpiredEventWebhookDeliveries(ctx, 500)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	var count int
	require.NoError(t, fixture.Store.pool.QueryRow(ctx,
		"SELECT count(*) FROM event_webhook_deliveries WHERE id = $1", first.ID).Scan(&count))
	require.Zero(t, count)
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
	_, err := fixture.Store.pool.Exec(ctx, `
		INSERT INTO event_webhook_deliveries (agent_id, tool_call_id, tool_state, created_at, org_id)
		SELECT $1, uuidv7(), 'ready',
			statement_timestamp() - n * interval '4 minutes', $2
		FROM generate_series(0, 5) n`, fixture.AgentID, testOrgID)
	require.NoError(t, err)
	for _, expected := range []int64{2, 1, 0} {
		deleted, err := fixture.Store.Execution().DeleteExpiredEventWebhookDeliveries(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, expected, deleted)
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
			delivery, err := fixture.Store.Execution().ClaimEventWebhookDelivery(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, testOrgID, delivery.OrgID)
			target, err = fixture.Store.Execution().GetAgentEventWebhookTarget(ctx, delivery.AgentID)
			require.NoError(t, err)
			require.Empty(t, target.URL)
			require.NoError(t, fixture.Store.Execution().CompleteEventWebhookDelivery(ctx, delivery.ID, delivery.ClaimToken))
		})
	}
}
