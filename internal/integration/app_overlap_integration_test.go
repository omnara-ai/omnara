//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAppRouterOverlappingSlackSetupsLaunchAndContinueIndependently(t *testing.T) {
	t.Parallel()
	pool, store, ids, connection := appWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewAppRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	publicConnection, err := publicid.Encode(publicid.KindIntegrationConnection, connection)
	require.NoError(t, err)

	// Overlapping saved setups deliberately fan out. Even the same profile and
	// slot key belong to separate app selections; there is no global winner.
	apps := make([]integrationstore.ProjectAppRecord, 0, 2)
	for _, name := range []string{"review-one", "review-two"} {
		app, err := inbox.CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: name, DefinitionID: appdefinition.Slack, Enabled: true,
			Settings: integrationstore.ProjectAppSettings{
				Resource: agentconfig.AgentConfigAppResourceSource{
					Definition: appdefinition.Slack, Connection: publicConnection,
					Listener: &appdefinition.Listener{Events: []string{"message"}},
				},
				Launcher: &integrationstore.AppLauncher{
					Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
					Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
				},
			},
		})
		require.NoError(t, err)
		require.Equal(t, ids.ProjectID, app.ProjectID)
		apps = append(apps, app)
	}
	require.NotEqual(t, apps[0].ID, apps[1].ID)

	capture := func(key string) integrationstore.IntegrationInboxRecord {
		t.Helper()
		accepted, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, ConnectionID: connection, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		require.True(t, created)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, ConnectionID: connection, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, accepted.ID, receipt.ID)
		return receipt
	}
	event := AppEvent{
		Event: appdefinition.Event{Kind: "message", Mentioned: true,
			Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "slack:message:T123:C123:1.2", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	receipt := capture("initial-mention")
	plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), []AppEvent{event})
	require.NoError(t, err)
	require.Len(t, plan, 2)
	agentsByApp := make(map[uuid.UUID]uuid.UUID, 2)
	for _, slot := range plan {
		require.NotNil(t, slot.Launch)
		require.Nil(t, slot.Input)
		require.NotNil(t, slot.Selection)
		require.Equal(t, connection, slot.Selection.ConnectionID)
		require.Equal(t, address, slot.Selection.Address)
		require.Equal(t, "reviewer", slot.Selection.Slot)
		require.Equal(t, ids.ProjectID, slot.Launch.ProjectID)
		require.Equal(t, profile.ID, slot.Launch.ProfileID)
		agentsByApp[slot.Selection.AppID] = slot.AgentID
	}
	require.Contains(t, agentsByApp, apps[0].ID)
	require.Contains(t, agentsByApp, apps[1].ID)
	require.NotEqual(t, agentsByApp[apps[0].ID], agentsByApp[apps[1].ID])

	results, err := router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	targetsByAgent := make(map[uuid.UUID]uuid.UUID, 2)
	for _, result := range results {
		require.NotNil(t, result.Launch)
		launch := result.Launch
		require.True(t, launch.Created)
		require.Equal(t, ids.OrgID, launch.Agent.OrgID)
		require.Equal(t, ids.ProjectID, launch.Agent.ProjectID)
		require.Equal(t, plan[result.Slot].AgentID, launch.Agent.ID)
		target := launch.IntegrationTarget
		require.Equal(t, integrationstore.TargetSelected, target.RoutingRole)
		require.Equal(t, plan[result.Slot].Selection.AppID, target.AppID)
		require.Equal(t, "reviewer", target.SelectionSlot)
		require.Equal(t, launch.Agent.ID, target.AgentID)
		require.Equal(t, ids.ProjectID, target.ProjectID)
		require.Equal(t, connection, target.IntegrationConnectionID)
		require.Equal(t, address.Kind, target.ProviderRefKind)
		require.Equal(t, address.Ref, target.ProviderRef)
		require.Equal(t, target.ID, launch.AgentInput.IntegrationTargetID)
		require.Equal(t, ids.ProjectID, launch.AgentInput.ProjectID)
		require.Equal(t, event.SemanticKey, launch.AgentInput.InputIdempotencyKey)
		targetsByAgent[launch.Agent.ID] = target.ID
	}
	require.NotEqual(t, targetsByAgent[agentsByApp[apps[0].ID]], targetsByAgent[agentsByApp[apps[1].ID]])
	assertCounts := func(inputsPerAgent int) {
		t.Helper()
		var agents, targets, listeners int
		require.NoError(t, pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agents WHERE project_id=$1),
			(SELECT count(*) FROM integration_targets WHERE project_id=$1),
			(SELECT count(*) FROM agent_listeners WHERE project_id=$1 AND connection_id=$2
			 AND scope_kind='thread' AND scope_ref='C123:1.2' AND active AND tool_call_id IS NULL)`,
			ids.ProjectID, connection).Scan(&agents, &targets, &listeners))
		require.Equal(t, 2, agents)
		require.Equal(t, 2, targets)
		require.Equal(t, 2, listeners)
		for agentID := range targetsByAgent {
			var inputs, initialInputs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE input_idempotency_key=$3)
				FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND input_kind='content'`,
				ids.ProjectID, agentID, event.SemanticKey).Scan(&inputs, &initialInputs))
			require.Equal(t, inputsPerAgent, inputs)
			require.Equal(t, 1, initialInputs, "each setup admits the initial message exactly once")
		}
	}
	assertCounts(1)

	// Replaying the same frozen receipt preserves each setup's pinned identity.
	replayed, err := freezeTestAppEvents(ctx, router, receipt.Lease(), nil)
	require.NoError(t, err)
	require.Len(t, replayed, 2)
	for key, slot := range replayed {
		require.Equal(t, plan[key].AgentID, slot.AgentID)
		require.Equal(t, plan[key].Selection, slot.Selection)
	}
	results, err = router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, result := range results {
		require.NotNil(t, result.Launch)
		require.False(t, result.Launch.Created)
		require.Equal(t, plan[result.Slot].AgentID, result.Launch.Agent.ID)
	}
	assertCounts(1)
	wrongProject := receipt.Lease()
	wrongProject.ProjectID = uuid.New()
	_, err = freezeTestAppEvents(ctx, router, wrongProject, nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = router.Admit(ctx, wrongProject)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	// A second receipt for the same provider message cannot relaunch either
	// setup or duplicate its input. A later nonmention reaches both listeners.
	for _, continuation := range []bool{false, true} {
		nextEvent := event
		key := "sibling-mention"
		if continuation {
			key = "thread-reply"
			nextEvent.Event.Mentioned = false
			nextEvent.SemanticKey = "slack:message:T123:C123:1.3"
			nextEvent.ContentBlocks = json.RawMessage(`[{"type":"text","text":"continue"}]`)
		}
		next := capture(key)
		follow, err := freezeTestAppEvents(ctx, router, next.Lease(), []AppEvent{nextEvent})
		require.NoError(t, err)
		require.Len(t, follow, 2)
		for _, slot := range follow {
			require.Nil(t, slot.Launch)
			require.Nil(t, slot.Selection)
			require.NotNil(t, slot.Input)
			require.Equal(t, ids.ProjectID, slot.Input.ProjectID)
			require.Contains(t, targetsByAgent, slot.AgentID)
			require.NotNil(t, slot.Listener)
			require.Equal(t, "message", slot.Listener.Event)
			require.Len(t, slot.Listener.Alternatives, 1)
			require.Equal(t, address, slot.Listener.Alternatives[0].Address)
			require.False(t, slot.Listener.Alternatives[0].Followed)
		}
		results, err = router.Admit(ctx, next.Lease())
		require.NoError(t, err)
		require.Len(t, results, 2)
		for _, result := range results {
			require.Nil(t, result.Launch)
			require.NotNil(t, result.Input)
			require.Equal(t, continuation, result.Input.Created)
			input := result.Input.AgentInput
			require.Equal(t, follow[result.Slot].AgentID, input.AgentID)
			require.Equal(t, ids.ProjectID, input.ProjectID)
			require.Equal(t, targetsByAgent[input.AgentID], input.IntegrationTargetID)
			require.Equal(t, nextEvent.SemanticKey, input.InputIdempotencyKey)
		}
		if continuation {
			assertCounts(2)
		} else {
			assertCounts(1)
		}
	}

	// A choice captures the expected profile. Editing the saved slot between
	// app policy and Freeze must reject it rather than launch the replacement.
	choiceEvent := event
	choiceEvent.Event.Scope = appdefinition.Scope{
		Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "2.1"},
	}
	choiceEvent.SemanticKey = "slack:message:T123:C123:2.1"
	choiceReceipt := capture("choice-before-edit")
	connectionRecord, err := inbox.GetIntegrationConnectionByID(ctx, connection)
	require.NoError(t, err)
	decided, err := testAppLaunchWorkflow(router).Decide(
		ctx, choiceReceipt.Lease(), choiceReceipt, connectionRecord, []AppEvent{choiceEvent},
	)
	require.NoError(t, err)
	require.Len(t, decided, 1)
	require.Len(t, decided[0].Launches, 2)
	replacement, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "replacement", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	changed := apps[0]
	changed.Settings.Launcher.Slots[0].AgentProfileID = &replacement.ID
	_, err = inbox.UpdateProjectApp(ctx, changed.ID, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: changed.Name, DefinitionID: changed.DefinitionID,
		Enabled: true, Settings: changed.Settings,
	})
	require.NoError(t, err)
	_, err = router.Freeze(ctx, choiceReceipt.Lease(), decided)
	require.ErrorIs(t, err, ErrAppLaunchUnavailable)
	unchanged, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, choiceReceipt.ID)
	require.NoError(t, err)
	require.Empty(t, unchanged.Plan, "a stale choice must not freeze different authority")
	assertCounts(2)

	t.Run("raw provider receipt retries stale intent", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		var admitted []AppSlotAdmission
		worker := NewAppInboxWorker(inbox, appWorkerConsumerFunc(
			func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]AppSlotAdmission, error) {
				attempts++
				if attempts == 1 {
					_, err := router.Freeze(ctx, lease, decided)
					return nil, err
				}
				if _, err := freezeTestAppEvents(ctx, router, lease, []AppEvent{choiceEvent}); err != nil {
					return nil, err
				}
				var err error
				admitted, err = router.Admit(ctx, lease)
				return admitted, err
			}), AppInboxWorkerOptions{})
		require.ErrorIs(t, worker.consume(ctx, choiceReceipt), ErrAppLaunchUnavailable)
		pending, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, choiceReceipt.ID)
		require.NoError(t, err)
		require.Equal(t, integrationstore.IntegrationInboxPending, pending.State)
		require.Empty(t, pending.Events, "raw provider events are re-decided rather than treated as a frozen choice")
		require.Empty(t, pending.Plan)
		require.Equal(t, choiceReceipt.Payload, pending.Payload)
		require.Contains(t, pending.LastError, ErrAppLaunchUnavailable.Error())
		// Advance the durable schedule instead of sleeping through retry backoff.
		_, err = pool.Exec(ctx, `UPDATE integration_inbox SET available_at=now() WHERE id=$1`, choiceReceipt.ID)
		require.NoError(t, err)
		worked, err := worker.RunOnce(ctx)
		require.NoError(t, err)
		require.True(t, worked)
		require.Equal(t, 2, attempts)
		require.Len(t, admitted, 2)
		for _, result := range admitted {
			require.NotNil(t, result.Launch)
			require.True(t, result.Launch.Created)
			expectedProfile := profile.ID
			if result.Launch.IntegrationTarget.AppID == changed.ID {
				expectedProfile = replacement.ID
			}
			require.Equal(t, expectedProfile, result.Launch.Agent.AgentProfileID)
			require.Equal(t, choiceEvent.SemanticKey, result.Launch.AgentInput.InputIdempotencyKey)
		}
		completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, choiceReceipt.ID)
		require.NoError(t, err)
		require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
		require.Equal(t, choiceReceipt.Payload, completed.Payload)
	})
}

func TestAppRouterDirectedSettledIntentWithoutListener(t *testing.T) {
	t.Parallel()
	pool, store, ids, connection := appWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewAppRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "reviewer", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	publicConnection, err := publicid.Encode(publicid.KindIntegrationConnection, connection)
	require.NoError(t, err)
	app, err := inbox.CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "one-shot", DefinitionID: appdefinition.Slack, Enabled: true,
		Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{
				Definition: appdefinition.Slack,
				Connection: publicConnection,
			},
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
			ProjectID: ids.ProjectID, ConnectionID: connection, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, ConnectionID: connection, LeaseDuration: time.Minute,
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

	// The app stage can redeliver its original source to the settled selection
	// without granting a subscription. The ordinary future event still gets none.
	for _, directed := range []bool{false, true} {
		key := "ordinary-followup"
		nextEvent := event
		nextEvent.Event.Mentioned = false
		if directed {
			key = "original-choice-replay"
			nextEvent.Directed = true
			nextEvent.Launches = []AppLaunchIntent{{AppID: app.ID, Slot: "reviewer", ProfileID: profile.ID}}
		} else {
			nextEvent.SemanticKey = "slack:message:T123:C123:1.3"
		}
		next := capture(key)
		plan, err = router.Freeze(ctx, next.Lease(), []AppEvent{nextEvent})
		require.NoError(t, err)
		if directed {
			require.Len(t, plan, 1)
			for _, slot := range plan {
				require.Equal(t, agentID, slot.AgentID)
				require.NotNil(t, slot.Input)
				require.Nil(t, slot.Launch)
				require.Nil(t, slot.Selection)
				require.Nil(t, slot.Listener)
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
	var agents, targets, listeners, inputs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM integration_targets WHERE project_id=$1),
		(SELECT count(*) FROM agent_listeners WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`,
		ids.ProjectID).Scan(&agents, &targets, &listeners, &inputs))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, targets)
	require.Zero(t, listeners)
	require.Equal(t, 1, inputs)
}
