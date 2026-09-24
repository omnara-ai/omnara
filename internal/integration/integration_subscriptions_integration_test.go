//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRouterSameExpansionSubscriptionAdmitsMessagesAndMedia(t *testing.T) {
	t.Parallel()
	_, store, ids, integrationID := integrationWorkerFixture(t)
	ctx := t.Context()
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "review", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	_, err = store.Integrations().UpdateProjectIntegration(
		ctx,
		integrationID,
		integrationstore.SaveProjectIntegrationInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationType: integrationdefinition.SlackThread,
			Settings: integrationstore.ProjectIntegrationSettings{Launcher: &integrationstore.IntegrationLauncher{
				Trigger: "mention", ScopeKind: "channel", ScopeRef: "C123",
				Slots: []integrationstore.IntegrationLaunchSlot{{Key: "review", AgentProfileID: &profile.ID}},
			}},
		},
	)
	require.NoError(t, err)
	_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "expanded", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "message", Mentioned: true,
			Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		SemanticKey: "z:launch", ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor: integrationTestActor(t, integrationID, "U123"),
	}
	reply := event
	reply.SemanticKey, reply.Event.Mentioned = "b:reply", false
	media := reply
	placeholder := uuid.New()
	media.SemanticKey = "a:files"
	media.ContentBlocks = json.RawMessage(`[{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`)
	media.Files = []IntegrationPlannedFile{{ArtifactID: placeholder, ProviderFileID: "F123"}}
	router := NewIntegrationRouter(store.Execution(), store.Integrations())
	plan, err := freezeTestIntegrationEvents(ctx, router, receipt.Lease(), []IntegrationEvent{event, reply, media})
	require.NoError(t, err)
	require.Len(t, plan, 3)
	var plannedAgent uuid.UUID
	for _, slot := range plan {
		if slot.Launch != nil {
			plannedAgent = slot.AgentID
			require.Equal(t, []string{"message"}, slot.Launch.Subscriptions[0].Events)
		}
	}
	require.NotEqual(t, uuid.Nil, plannedAgent)
	for key, slot := range plan {
		require.Equal(t, plannedAgent, slot.AgentID)
		if slot.Input != nil {
			require.NotNil(t, slot.Subscription)
		}
		if len(slot.Files) != 0 {
			require.NoError(t, router.Prepare(ctx, receipt.Lease(), key, []artifactstore.PreparedArtifact{{
				ID: slot.Files[0].ArtifactID, Filename: "review.txt", ContentType: "text/plain",
				Digest: blobstore.ContentDigest([]byte("review")), SizeBytes: 6,
			}}))
		}
	}
	results, err := router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 3)
	require.NotNil(t, results[0].Launch, "admission must launch before later expansion inputs")
	require.True(t, results[0].Launch.Created)
	for _, result := range results[1:] {
		require.NotNil(t, result.Input)
		require.True(t, result.Input.Created)
		require.Equal(t, plannedAgent, result.Input.AgentInput.AgentID)
	}
	subscriptions, err := store.Integrations().ListIntegrationSubscriptions(
		ctx,
		integrationstore.ListIntegrationSubscriptionsInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, Limit: 100,
		},
	)
	require.NoError(t, err)
	require.Len(t, subscriptions.Subscriptions, 1)
	require.Equal(t, plannedAgent, subscriptions.Subscriptions[0].AgentID)
	replayed, err := router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Len(t, replayed, 3)
	require.False(t, replayed[0].Launch.Created)
	for _, result := range replayed[1:] {
		require.False(t, result.Input.Created)
	}
}

func TestIntegrationRouterFrozenSubscriptionEventRechecksLiveAttachment(t *testing.T) {
	t.Parallel()
	_, store, ids, integrationID := integrationProviderFixture(t, "github", "11", "22")
	ctx := t.Context()
	integration, err := store.Integrations().GetProjectIntegration(ctx, ids.ProjectID, integrationID)
	require.NoError(t, err)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: ids.ProjectID, AgentConfigID: base.ID,
		LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
	})
	require.NoError(t, err)
	const conversation = `{"repository_id":123,"pull_request":42}`
	original := createTestIntegrationSubscription(
		t,
		store,
		integration,
		launched.Agent.ID,
		"pull_request",
		conversation,
		"commit",
	)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "commit", Scope: integrationdefinition.Scope{
			GitHub: &integrationdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42},
		}},
		SemanticKey: "commit:1", ContentBlocks: json.RawMessage(`[{"type":"text","text":"new commit"}]`),
		Actor: integrationTestActor(t, integrationID, "33"),
	}
	capture := func(key string) integrationstore.IntegrationInboxRecord {
		t.Helper()
		_, _, err := store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: key, Payload: []byte(`{}`),
		})
		require.NoError(t, err)
		receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	router := NewIntegrationRouter(store.Execution(), store.Integrations())
	excluded := event
	excluded.Event.Kind, excluded.SemanticKey = "discussion_comment", "comment:1"
	excludedReceipt := capture("excluded")
	plan, err := router.Freeze(ctx, excludedReceipt.Lease(), []IntegrationEvent{excluded})
	require.NoError(t, err)
	require.Empty(t, plan)
	_, err = router.Admit(ctx, excludedReceipt.Lease())
	require.NoError(t, err)
	receipt := capture("included")
	plan, err = router.Freeze(ctx, receipt.Lease(), []IntegrationEvent{event})
	require.NoError(t, err)
	require.Len(t, plan, 1)
	for _, slot := range plan {
		require.Equal(t, "commit", slot.Subscription.Event)
	}
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	createTestIntegrationSubscription(
		t,
		store,
		integration,
		launched.Agent.ID,
		"pull_request",
		conversation,
		"discussion_comment",
	)
	_, err = router.Freeze(ctx, receipt.Lease(), []IntegrationEvent{excluded})
	require.NoError(t, err)
	results, err := router.Admit(ctx, receipt.Lease())
	require.Error(t, err)
	require.Empty(t, results)
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	replacement := createTestIntegrationSubscription(
		t,
		store,
		integration,
		launched.Agent.ID,
		"pull_request",
		conversation,
		"commit",
	)
	require.NotEqual(t, original.ID, replacement.ID)
	results, err = router.Admit(ctx, receipt.Lease())
	require.NoError(t, err, "matching live type/address/events may authorize already frozen work")
	require.Len(t, results, 1)
	require.True(t, results[0].Input.Created)
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	results, err = router.Admit(ctx, receipt.Lease())
	require.NoError(t, err)
	require.False(t, results[0].Input.Created)
	subscriptions, err := store.Integrations().ListIntegrationSubscriptions(
		ctx,
		integrationstore.ListIntegrationSubscriptionsInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, Limit: 100,
		},
	)
	require.NoError(t, err)
	require.Empty(t, subscriptions.Subscriptions, "committed replay cannot recreate subscriptions")
}

func createTestIntegrationSubscription(
	t *testing.T, store *storage.Store, integration integrationstore.ProjectIntegrationRecord, agentID uuid.UUID,
	subscriptionType string, conversation string, events ...string,
) integrationstore.IntegrationSubscriptionRecord {
	t.Helper()
	subscription, err := store.Integrations().CreateIntegrationSubscription(
		t.Context(), integrationstore.CreateIntegrationSubscriptionInput{
			OrgID: integration.OrgID, ProjectID: integration.ProjectID, IntegrationID: integration.ID, AgentID: agentID,
			Type: subscriptionType, Conversation: json.RawMessage(conversation), Events: events,
		},
	)
	require.NoError(t, err)
	return subscription
}

func removeTestAgentSubscriptions(
	t *testing.T, store *storage.Store, integration integrationstore.ProjectIntegrationRecord, agentID uuid.UUID,
) {
	t.Helper()
	input := integrationstore.ListIntegrationSubscriptionsInput{
		ProjectID:     integration.ProjectID,
		IntegrationID: integration.ID,
		Limit:         100,
	}
	for {
		result, err := store.Integrations().ListIntegrationSubscriptions(t.Context(), input)
		require.NoError(t, err)
		for _, subscription := range result.Subscriptions {
			if subscription.AgentID == agentID {
				require.NoError(
					t,
					store.Integrations().DeleteIntegrationSubscription(
						t.Context(),
						integration.OrgID,
						integration.ProjectID,
						integration.ID,
						subscription.ID,
					),
				)
			}
		}
		if !result.HasMore {
			return
		}
		input.After = result.Next
	}
}
