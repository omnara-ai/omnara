//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestAppRouterFailedMixedPlanPreservesAdmittedSubscriptionInput(t *testing.T) {
	t.Parallel()
	pool, store, ids, appSetup := appWorkerFixture(t)
	ctx := t.Context()
	inbox := store.Integrations()
	router := NewAppRouter(store.Execution(), inbox)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	existing, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: ids.ProjectID, LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
		AgentConfigID: base.ID,
	})
	require.NoError(t, err)
	appRecord, err := inbox.GetProjectApp(ctx, ids.ProjectID, appSetup)
	require.NoError(t, err)
	createTestAppSubscription(t, store, appRecord, existing.Agent.ID, "thread_messages", `{"channel_id":"C123"}`)

	createProfile := func(name string) executionstore.AgentProfileRecord {
		t.Helper()
		profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
			ProjectID: ids.ProjectID, Name: name, CurrentConfigID: base.ID,
		})
		require.NoError(t, err)
		return profile
	}
	profile := createProfile("first")
	setup := integrationstore.SaveProjectAppInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", AppType: appdefinition.SlackThread,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "review", AgentProfileID: &profile.ID}}},
		},
	}
	app, err := inbox.UpdateProjectApp(ctx, appSetup, setup)
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
		Actor: appTestActor(t, app.ID, "U123"),
	}
	first := capture("first-delivery")
	plan, err := freezeTestAppEvents(ctx, router, first.Lease(), []AppEvent{event})
	require.NoError(t, err)
	require.Len(t, plan, 2)
	var abandonedAgent uuid.UUID
	for _, slot := range plan {
		if slot.Launch != nil {
			abandonedAgent = slot.AgentID
		}
	}
	require.NotEqual(t, uuid.Nil, abandonedAgent)
	// The broad channel subscription still accepts the input when the independently
	// selected launch profile disappears after freeze.
	_, err = pool.Exec(ctx, `UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, profile.ID)
	require.NoError(t, err)
	results, err := router.Admit(ctx, first.Lease())
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.Len(t, results, 1)
	require.True(t, results[0].Input.Created)
	require.NoError(
		t,
		inbox.WithIntegrationInboxLease(ctx, first.Lease(), func(work *integrationstore.IntegrationInboxLeaseTx) error {
			return work.Fail(ctx, "profile disappeared after freeze")
		}),
	)
	replacement := createProfile("replacement")
	setup.Settings.Launcher.Slots[0].AgentProfileID = &replacement.ID
	_, err = inbox.UpdateProjectApp(ctx, app.ID, setup)
	require.NoError(t, err)
	second := capture("sibling-delivery")
	fresh, err := freezeTestAppEvents(ctx, router, second.Lease(), []AppEvent{event})
	require.NoError(t, err)
	require.Len(t, fresh, 2)
	for _, slot := range fresh {
		if slot.Launch != nil {
			require.NotEqual(t, abandonedAgent, slot.AgentID)
		}
	}
	results, err = router.Admit(ctx, second.Lease())
	require.NoError(t, err)
	require.Len(t, results, 2)
	var launches, inputReplays int
	for _, result := range results {
		if result.Launch != nil {
			require.True(t, result.Launch.Created)
			launches++
		} else {
			require.False(t, result.Input.Created)
			inputReplays++
		}
	}
	require.Equal(t, 1, launches)
	require.Equal(t, 1, inputReplays)
	var count int
	require.NoError(
		t,
		pool.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_idempotency_key=$2`,
			existing.Agent.ID, event.SemanticKey).Scan(&count),
	)
	require.Equal(t, 1, count, "terminal failure and fresh selection must not duplicate the committed subscription input")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE id=$1`, abandonedAgent).Scan(&count))
	require.Zero(t, count, "failed planned identity must remain unlaunched")
}
