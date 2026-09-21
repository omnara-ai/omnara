//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAppRouterOverlappingSlackSetupsLaunchAndContinueIndependently(t *testing.T) {
	t.Parallel()
	pool, store, ids, appID := appWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewAppRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	app, err := inbox.UpdateProjectApp(ctx, appID, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", DefinitionID: appdefinition.Slack,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
			Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
		}},
	})
	require.NoError(t, err)
	apps := []integrationstore.ProjectAppRecord{app, seedIndependentApp(t, pool, app, "other-chat")}
	capture := func(app integrationstore.ProjectAppRecord, key string) integrationstore.IntegrationInboxRecord {
		t.Helper()
		_, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, AppID: app.ID, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		require.True(t, created)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, AppID: app.ID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	event := AppEvent{
		Event: appdefinition.Event{Kind: "message", Mentioned: true,
			Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	agents := map[uuid.UUID]uuid.UUID{}
	for _, app := range apps {
		receipt := capture(app, "same-physical-delivery")
		plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), []AppEvent{event})
		require.NoError(t, err)
		require.Len(t, plan, 1, "each receipt belongs only to its saved app")
		for _, slot := range plan {
			require.Equal(t, app.ID, slot.Selection.AppID)
			require.Len(t, slot.Launch.Subscriptions, 1)
			require.Equal(t, app.ID, slot.Launch.Subscriptions[0].AppID)
			require.Equal(t, "thread_messages", slot.Launch.Subscriptions[0].Type)
		}
		results, err := router.Admit(ctx, receipt.Lease())
		require.NoError(t, err)
		require.Len(t, results, 1)
		require.True(t, results[0].Launch.Created)
		agents[app.ID] = results[0].Launch.Agent.ID
		results, err = router.Admit(ctx, receipt.Lease())
		require.NoError(t, err)
		require.False(t, results[0].Launch.Created)
		wrongProject := receipt.Lease()
		wrongProject.ProjectID = uuid.New()
		_, err = router.Admit(ctx, wrongProject)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	require.NotEqual(t, agents[apps[0].ID], agents[apps[1].ID])
	for _, app := range apps {
		for _, replay := range []bool{true, false} {
			next := event
			key := "duplicate-provider-shape"
			if !replay {
				key, next.SemanticKey, next.Event.Mentioned = "reply", "slack:message:T123:C123:1.3", false
			}
			receipt := capture(app, key)
			plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), []AppEvent{next})
			require.NoError(t, err)
			require.Len(t, plan, 1)
			for _, slot := range plan {
				require.Equal(t, agents[app.ID], slot.AgentID)
				require.Nil(t, slot.Launch)
				require.NotNil(t, slot.Subscription)
				require.Equal(t, []executionstore.InboxSubscriptionReference{{
					Type:    "thread_messages",
					Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"},
				}}, slot.Subscription.Alternatives)
			}
			results, err := router.Admit(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Equal(t, !replay, results[0].Input.Created)
		}
	}
	var subscriptions, inputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM app_subscriptions WHERE project_id=$1 AND tool_call_id IS NULL),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`, ids.ProjectID).
		Scan(&subscriptions, &inputs))
	require.Equal(t, 2, subscriptions)
	require.Equal(t, 4, inputs)

	// A stale frozen policy decision cannot launch a replacement profile.
	next := event
	next.Event.Scope.Slack = &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "2.1"}
	next.SemanticKey = "slack:message:T123:C123:2.1"
	receipt := capture(apps[0], "choice-before-edit")
	decided, err := testAppLaunchWorkflow(router).Decide(ctx, receipt.Lease(), receipt, apps[0], []AppEvent{next})
	require.NoError(t, err)
	replacement, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "replacement", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	settings := apps[0].Settings
	settings.Launcher.Slots[0].AgentProfileID = &replacement.ID
	_, err = inbox.UpdateProjectApp(ctx, apps[0].ID, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: apps[0].Name,
		DefinitionID: apps[0].DefinitionID, Settings: settings,
	})
	require.NoError(t, err)
	_, err = router.Freeze(ctx, receipt.Lease(), decided)
	require.ErrorIs(t, err, ErrAppLaunchUnavailable)
	unchanged, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Plan)
	attempts := 0
	worker := NewAppInboxWorker(inbox, appWorkerConsumerFunc(func(
		ctx context.Context, lease integrationstore.IntegrationInboxLease,
	) ([]AppSlotAdmission, error) {
		attempts++
		if attempts == 1 {
			_, err := router.Freeze(ctx, lease, decided)
			return nil, err
		}
		if _, err := freezeTestAppEvents(ctx, router, lease, []AppEvent{next}); err != nil {
			return nil, err
		}
		return router.Admit(ctx, lease)
	}), AppInboxWorkerOptions{})
	require.ErrorIs(t, worker.consume(ctx, receipt), ErrAppLaunchUnavailable)
	_, err = pool.Exec(ctx, `UPDATE integration_inbox SET available_at=now() WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	worked, err := worker.RunOnce(ctx)
	require.NoError(t, err)
	require.True(t, worked)
	require.Equal(t, 2, attempts)
}

func TestAppRouterDirectedSettledIntentWithoutSubscription(t *testing.T) {
	t.Parallel()
	pool, store, ids, appSetup := appWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewAppRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	app, err := inbox.UpdateProjectApp(ctx, appSetup, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", DefinitionID: appdefinition.Slack,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{
				Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
			},
		},
	})
	require.NoError(t, err)
	capture := func(key string) integrationstore.IntegrationInboxRecord {
		t.Helper()
		_, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, AppID: appSetup, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, AppID: appSetup, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	event := AppEvent{
		Event: appdefinition.Event{Kind: "message", Mentioned: true,
			Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	initial := capture("initial")
	plan, err := freezeTestAppEvents(ctx, router, initial.Lease(), []AppEvent{event})
	require.NoError(t, err)
	require.Len(t, plan, 1)
	results, err := router.Admit(ctx, initial.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Launch.Created)
	agentID, targetID := results[0].Launch.Agent.ID, results[0].Launch.IntegrationTarget.ID
	removeTestAgentSubscriptions(t, store, app, agentID)

	// The app stage can redeliver its original source to the settled selection
	// without granting a subscription. The ordinary future event still gets none.
	for _, directed := range []bool{false, true} {
		key := "ordinary-followup"
		nextEvent := event
		nextEvent.Event.Mentioned = true
		if directed {
			key = "original-choice-replay"
			nextEvent.Directed = true
			nextEvent.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "reviewer", ProfileID: profile.ID}}
		} else {
			nextEvent.SemanticKey = "slack:message:T123:C123:1.3"
		}
		next := capture(key)
		if directed {
			plan, err = router.Freeze(ctx, next.Lease(), []AppEvent{nextEvent})
		} else {
			plan, err = freezeTestAppEvents(ctx, router, next.Lease(), []AppEvent{nextEvent})
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
		results, err = router.Admit(ctx, next.Lease())
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
		(SELECT count(*) FROM app_subscriptions WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`,
		ids.ProjectID).Scan(&agents, &targets, &subscriptions, &inputs))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, targets)
	require.Zero(t, subscriptions)
	require.Equal(t, 1, inputs)
}
