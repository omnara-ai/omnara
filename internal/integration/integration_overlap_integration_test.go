//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRouterOverlappingSlackSetupsLaunchAndContinueIndependently(t *testing.T) {
	t.Parallel()
	pool, store, ids, integrationID := integrationWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewIntegrationRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	integration, err := inbox.UpdateProjectIntegration(ctx, integrationID, integrationstore.SaveProjectIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{Launcher: &integrationstore.IntegrationLauncher{
			Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
			Slots: []integrationstore.IntegrationLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
		}},
	})
	require.NoError(t, err)
	integrations := []integrationstore.ProjectIntegrationRecord{
		integration,
		seedIndependentIntegration(t, pool, integration, "other-chat"),
	}
	capture := func(
		integration integrationstore.ProjectIntegrationRecord,
		key string,
	) integrationstore.IntegrationInboxRecord {
		t.Helper()
		_, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, IntegrationID: integration.ID, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		require.True(t, created)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integration.ID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "message", Mentioned: true,
			Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: integrationTestActor(t, integrations[0].ID, "U123"),
	}
	agents := map[uuid.UUID]uuid.UUID{}
	for _, integration := range integrations {
		event.Actor = integrationTestActor(t, integration.ID, "U123")
		receipt := capture(integration, "same-physical-delivery")
		plan, err := freezeTestIntegrationEvents(ctx, router, receipt.Lease(), []IntegrationEvent{event})
		require.NoError(t, err)
		require.Len(t, plan, 1, "each receipt belongs only to its saved integration")
		for _, slot := range plan {
			require.Equal(t, integration.ID, slot.Selection.IntegrationID)
			require.Len(t, slot.Launch.Subscriptions, 1)
			require.Equal(t, integration.ID, slot.Launch.Subscriptions[0].IntegrationID)
		}
		results, err := router.Admit(ctx, receipt.Lease(), nil)
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.True(t, results[0].Launch.Created)
		agents[integration.ID] = results[0].Launch.Agent.ID
		results, err = router.Admit(ctx, receipt.Lease(), nil)
		require.NoError(t, err)
		require.False(t, results[0].Launch.Created)
		wrongProject := receipt.Lease()
		wrongProject.ProjectID = uuid.New()
		_, err = router.Admit(ctx, wrongProject, nil)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	require.NotEqual(t, agents[integrations[0].ID], agents[integrations[1].ID])
	for _, integration := range integrations {
		for _, replay := range []bool{true, false} {
			next := event
			next.Actor = integrationTestActor(t, integration.ID, "U123")
			key := "duplicate-provider-shape"
			if !replay {
				key, next.SemanticKey, next.Event.Mentioned = "reply", "slack:message:T123:C123:1.3", false
			}
			receipt := capture(integration, key)
			plan, err := freezeTestIntegrationEvents(ctx, router, receipt.Lease(), []IntegrationEvent{next})
			require.NoError(t, err)
			require.Len(t, plan, 1)
			for _, slot := range plan {
				require.Equal(t, agents[integration.ID], slot.AgentID)
				require.Nil(t, slot.Launch)
				require.NotNil(t, slot.Subscription)
				require.Equal(t, []integrationstore.ConversationAddress{{Kind: "thread", Ref: "C123:1.2"}},
					slot.Subscription.Alternatives)
			}
			results, err := router.Admit(ctx, receipt.Lease(), nil)
			require.NoError(t, err)
			require.Equal(t, !replay, results[0].Input.Created)
		}
	}
	var subscriptions, inputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM integration_subscriptions WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`, ids.ProjectID).
		Scan(&subscriptions, &inputs))
	require.Equal(t, 2, subscriptions)
	require.Equal(t, 4, inputs)

	next := event
	next.Actor = integrationTestActor(t, integrations[0].ID, "U123")
	next.Event.Scope.Slack = &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "2.1"}
	next.SemanticKey = "slack:message:T123:C123:2.1"
	receipt := capture(integrations[0], "choice-before-edit")
	decided, err := testIntegrationLaunchWorkflow(
		router,
	).Decide(ctx, receipt.Lease(), receipt, integrations[0], []IntegrationEvent{next})
	require.NoError(t, err)
	replacement, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "replacement", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	settings := integrations[0].Settings
	settings.Launcher.Slots[0].AgentProfileID = &replacement.ID
	_, err = inbox.UpdateProjectIntegration(ctx, integrations[0].ID, integrationstore.SaveProjectIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: integrations[0].Name,
		IntegrationType: integrations[0].IntegrationType, Settings: settings,
	})
	require.NoError(t, err)
	_, err = router.Freeze(ctx, receipt.Lease(), decided)
	require.ErrorIs(t, err, ErrIntegrationLaunchUnavailable)
	unchanged, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Plan)
	attempts := 0
	worker := NewIntegrationInboxWorker(inbox, integrationWorkerConsumerFunc(func(
		ctx context.Context, lease integrationstore.IntegrationInboxLease,
	) ([]IntegrationSlotAdmission, error) {
		attempts++
		if attempts == 1 {
			_, err := router.Freeze(ctx, lease, decided)
			return nil, err
		}
		if _, err := freezeTestIntegrationEvents(ctx, router, lease, []IntegrationEvent{next}); err != nil {
			return nil, err
		}
		return router.Admit(ctx, lease, nil)
	}), IntegrationInboxWorkerOptions{})
	require.ErrorIs(t, worker.consume(ctx, receipt), ErrIntegrationLaunchUnavailable)
	_, err = pool.Exec(ctx, `UPDATE integration_inbox SET available_at=now() WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	worked, err := worker.RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	require.Equal(t, 2, attempts)
}

func TestIntegrationRouterDirectedSettledIntentWithoutSubscription(t *testing.T) {
	t.Parallel()
	pool, store, ids, integrationSetup := integrationWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewIntegrationRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	integration, err := inbox.UpdateProjectIntegration(ctx, integrationSetup, integrationstore.SaveProjectIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{
			Launcher: &integrationstore.IntegrationLauncher{
				Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
				Slots: []integrationstore.IntegrationLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
			},
		},
	})
	require.NoError(t, err)
	capture := func(key string) integrationstore.IntegrationInboxRecord {
		t.Helper()
		_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, IntegrationID: integrationSetup, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationSetup, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "message", Mentioned: true,
			Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: integrationTestActor(t, integration.ID, "U123"),
	}
	initial := capture("initial")
	plan, err := freezeTestIntegrationEvents(ctx, router, initial.Lease(), []IntegrationEvent{event})
	require.NoError(t, err)
	require.Len(t, plan, 1)
	results, err := router.Admit(ctx, initial.Lease(), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Launch.Created)
	agentID, targetID := results[0].Launch.Agent.ID, results[0].Launch.IntegrationTarget.ID
	removeTestAgentSubscriptions(t, store, integration, agentID)

	for _, directed := range []bool{false, true} {
		key := "ordinary-followup"
		nextEvent := event
		nextEvent.Event.Mentioned = true
		if directed {
			key = "original-choice-replay"
			nextEvent.Directed = true
			nextEvent.Launches = []IntegrationLaunchIntent{
				{IntegrationID: integration.ID, Slot: "reviewer", ProfileID: profile.ID},
			}
		} else {
			nextEvent.SemanticKey = "slack:message:T123:C123:1.3"
		}
		next := capture(key)
		if directed {
			plan, err = router.Freeze(ctx, next.Lease(), []IntegrationEvent{nextEvent})
		} else {
			plan, err = freezeTestIntegrationEvents(ctx, router, next.Lease(), []IntegrationEvent{nextEvent})
		}
		require.NoError(t, err)
		if directed {
			require.Len(t, plan, 1)
			for _, slot := range plan {
				require.Equal(t, agentID, slot.AgentID)
				require.NotNil(t, slot.Input)
				require.Nil(t, slot.Launch)
				require.Nil(t, slot.Selection)
				require.Nil(t, slot.Subscription)
			}
		} else {
			require.Empty(t, plan)
		}
		results, err = router.Admit(ctx, next.Lease(), nil)
		require.NoError(t, err)
		if directed {
			require.Len(t, results, 1)
			require.False(t, results[0].Input.Created)
			require.Equal(t, targetID, results[0].Input.AgentInput.IntegrationTargetID)
		} else {
			require.Empty(t, results)
		}
	}
	var agents, targets, subscriptions, inputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM integration_targets WHERE project_id=$1),
		(SELECT count(*) FROM integration_subscriptions WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`,
		ids.ProjectID).Scan(&agents, &targets, &subscriptions, &inputs))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, targets)
	require.Zero(t, subscriptions)
	require.Equal(t, 1, inputs)
}
