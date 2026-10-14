//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
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

func assertInteractionWebhookUpdates(
	t *testing.T, fixture processDaemonFixture, publisher *recordingPostCommitPublisher,
	toolID uuid.UUID, expected []notifications.ToolCallUpdatedCommitted,
) {
	t.Helper()
	var published []notifications.ToolCallUpdatedCommitted
	for _, intent := range publisher.intents {
		if update, ok := intent.(notifications.ToolCallUpdatedCommitted); ok && update.ToolCallID == toolID {
			published = append(published, update)
		}
	}
	require.Equal(t, expected, published)
	rows, err := fixture.Store.pool.Query(t.Context(),
		`SELECT tool_state, interaction_update FROM event_webhook_deliveries
		 WHERE agent_id = $1 AND tool_call_id = $2 ORDER BY id`, fixture.AgentID, toolID)
	require.NoError(t, err)
	defer rows.Close()
	var queued []notifications.ToolCallUpdatedCommitted
	for rows.Next() {
		update := notifications.ToolCallUpdatedCommitted{AgentID: fixture.AgentID, ToolCallID: toolID}
		var interaction json.RawMessage
		require.NoError(t, rows.Scan(&update.State, &interaction))
		if len(interaction) > 0 {
			require.NoError(t, json.Unmarshal(interaction, &update.InteractionUpdate))
		}
		queued = append(queued, update)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, expected, queued)
}

func TestInteractionWebhookLifecycle(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"permission", "question"} {
		for _, action := range []string{"resolve", "deny", "stop", "archive", "steer", "fail"} {
			if kind == "question" && (action == "deny" || action == "fail") {
				continue
			}
			t.Run(kind+"/"+action, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				fixture := newProcessDaemonFixture(t, ctx, "interaction_webhook")
				toolID := createToolCallForProcessTestWithPermission(t, ctx, fixture,
					"interaction_webhook", "ask_question", kind == "question")
				enableEventWebhook(t, fixture, []string{"tool_call_update"})
				publisher := &recordingPostCommitPublisher{}
				fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
				var interaction executionstore.AgentInteractionRecord
				openingState := "awaiting_permission"
				if kind == "permission" {
					interaction = createPermissionInteractionForTest(t, ctx, fixture, toolID,
						permissionRequestForStorageTest(t, "ask_question"))
				} else {
					interaction = createQuestionInteractionForTest(t, ctx, fixture, toolID)
					replayed := createQuestionInteractionForTest(t, ctx, fixture, toolID)
					require.Equal(t, interaction.ID, replayed.ID)
					openingState = "waiting"
				}
				closingState, interactionState := "completed", "canceled"
				switch action {
				case "resolve", "deny":
					option := toolpermission.AllowOptionIndex
					if action == "deny" {
						option = toolpermission.DenyOptionIndex
					} else if kind == "permission" {
						closingState = "ready"
					}
					input := executionstore.ResolveAgentInteractionInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, ID: interaction.ID,
						Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{option}}}},
						Actor:      mustOmnaraActorParams(t, fixture.UserID),
					}
					for range 2 {
						_, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, input)
						require.NoError(t, err)
					}
					interactionState = "resolved"
				case "stop":
					_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
					})
					require.NoError(t, err)
				case "archive":
					_, _, err := fixture.Store.Execution().ArchiveAgent(
						ctx, testProjectID, fixture.AgentID, userPrincipal(fixture.UserID),
					)
					require.NoError(t, err)
				case "steer":
					_, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
						ContentBlocks: json.RawMessage(`[{"type":"text","text":"change direction"}]`),
						DeliveryMode:  executionstore.DeliveryModeSteering, CancelOpenInteractions: true,
						IdempotencyKey: "cancel-interaction",
					})
					require.NoError(t, err)
				case "fail":
					_, err := fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
						ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolID,
						RuntimeLockID: fixture.Lock.ID, Outcome: executionstore.ToolResultOutcomeFailed,
					})
					require.NoError(t, err)
				}
				assertInteractionWebhookUpdates(t, fixture, publisher, toolID, []notifications.ToolCallUpdatedCommitted{
					{AgentID: fixture.AgentID, ToolCallID: toolID, State: openingState,
						InteractionUpdate: &notifications.AgentInteractionUpdate{
							ID: interaction.ID, InteractionKind: kind, State: "open",
						}},
					{AgentID: fixture.AgentID, ToolCallID: toolID, State: closingState,
						InteractionUpdate: &notifications.AgentInteractionUpdate{
							ID: interaction.ID, InteractionKind: kind, State: interactionState,
						}},
				})
			})
		}
	}
}

func TestInteractionWebhookCancelMultipleTools(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "cancel_interaction_webhooks")
	toolIDs := createToolCallBatchForProcessTest(
		t, ctx, fixture, "cancel_interaction_webhooks", []processToolCallBatchItem{
			{TestName: "permission", ToolName: "ask_question", ToolType: toolcatalog.ToolTypeBuiltIn},
			builtInProcessToolCallBatchItem("question", "ask_question"),
			customProcessToolCallBatchItem("custom", "custom_tool"),
		},
	)
	permission := createPermissionInteractionForTest(
		t, ctx, fixture, toolIDs[0], permissionRequestForStorageTest(t, "ask_question"),
	)
	question := createQuestionInteractionForTest(t, ctx, fixture, toolIDs[1])
	enableEventWebhook(t, fixture, []string{"tool_call_update"})
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
	for range 2 {
		_, err := fixture.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
			ProjectID: testProjectID, AgentID: fixture.AgentID, Actor: mustOmnaraActorParams(t, fixture.UserID),
		})
		require.NoError(t, err)
	}
	for i, interaction := range []*executionstore.AgentInteractionRecord{&permission, &question, nil} {
		expected := notifications.ToolCallUpdatedCommitted{
			AgentID: fixture.AgentID, ToolCallID: toolIDs[i], State: "completed",
		}
		if interaction != nil {
			expected.InteractionUpdate = &notifications.AgentInteractionUpdate{
				ID: interaction.ID, InteractionKind: string(interaction.InteractionKind), State: "canceled",
			}
		}
		assertInteractionWebhookUpdates(t, fixture, publisher, toolIDs[i], []notifications.ToolCallUpdatedCommitted{expected})
	}
}

func TestInteractionWebhookPermissionThenQuestion(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fixture := newProcessDaemonFixture(t, ctx, "permission_then_question_webhook")
	toolID := createToolCallForProcessTestWithPermission(t, ctx, fixture,
		"permission_then_question_webhook", "ask_question", false)
	enableEventWebhook(t, fixture, []string{"tool_call_update"})
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
	permission := createPermissionInteractionForTest(
		t, ctx, fixture, toolID, permissionRequestForStorageTest(t, "ask_question"),
	)
	resolve := executionstore.ResolveAgentInteractionInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: permission.ID,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{OptionIndices: []int{0}}}},
		Actor:      mustOmnaraActorParams(t, fixture.UserID),
	}
	_, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, resolve)
	require.NoError(t, err)
	question := createQuestionInteractionForTest(t, ctx, fixture, toolID)
	require.NotEqual(t, permission.ID, question.ID)
	resolve.ID = question.ID
	_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, resolve)
	require.NoError(t, err)
	assertInteractionWebhookUpdates(t, fixture, publisher, toolID, []notifications.ToolCallUpdatedCommitted{
		{AgentID: fixture.AgentID, ToolCallID: toolID, State: "awaiting_permission",
			InteractionUpdate: &notifications.AgentInteractionUpdate{
				ID: permission.ID, InteractionKind: "permission", State: "open",
			}},
		{AgentID: fixture.AgentID, ToolCallID: toolID, State: "ready",
			InteractionUpdate: &notifications.AgentInteractionUpdate{
				ID: permission.ID, InteractionKind: "permission", State: "resolved",
			}},
		{AgentID: fixture.AgentID, ToolCallID: toolID, State: "waiting",
			InteractionUpdate: &notifications.AgentInteractionUpdate{
				ID: question.ID, InteractionKind: "question", State: "open",
			}},
		{AgentID: fixture.AgentID, ToolCallID: toolID, State: "completed",
			InteractionUpdate: &notifications.AgentInteractionUpdate{
				ID: question.ID, InteractionKind: "question", State: "resolved",
			}},
	})
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
			pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready", nil)
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
	toolID := uuid.New()
	interaction := notifications.AgentInteractionUpdate{ID: uuid.New(), InteractionKind: "permission", State: "resolved"}
	pending.AddToolCallUpdate(fixture.AgentID, toolID, "ready", &interaction)
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
	require.Equal(t, &interaction, first.InteractionUpdate)
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
	require.Equal(t, first.InteractionUpdate, third.InteractionUpdate)
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
		pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready", nil)
	}
	pending.AddToolCallUpdate(otherAgent.ID, uuid.New(), "ready", nil)
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
	publisher := &recordingPostCommitPublisher{}
	fixture.Store = newIntegrationStore(fixture.Store.pool, storage.WithPostCommitPublisher(publisher))
	_, err := fixture.Store.pool.Exec(ctx,
		"ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_ready CHECK (tool_state <> 'ready')")
	require.NoError(t, err)
	input := executionstore.MarkToolCallReadyInput{
		ProjectID: testProjectID, AgentID: fixture.AgentID, ID: toolID, RuntimeLockID: fixture.Lock.ID,
	}
	_, err = fixture.Store.Execution().MarkToolCallReady(ctx, input)
	require.Error(t, err)
	require.Empty(t, publisher.intents)
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
		pending.AddToolCallUpdate(fixture.AgentID, toolIDs[i], "ready", nil)
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
			pending.AddToolCallUpdate(fixture.AgentID, uuid.New(), "ready", nil)
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
