//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationConsumerLaterReceiptsUseLaunchedSubscriptionForMessagesAndMedia(t *testing.T) {
	t.Parallel()
	_, store, ids, integrationID := integrationWorkerFixture(t)
	ctx := t.Context()
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "review", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	integration, err := store.Integrations().UpdateIntegration(
		ctx,
		integrationID,
		integrationstore.SaveIntegrationInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationKind: integrationdefinition.SlackThread,
			Settings: integrationtest.ChatSettings("C123", profile.ID),
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
		Actor: integrationTestActor(t, integration, "U123"),
	}
	reply := event
	reply.SemanticKey, reply.Event.Mentioned = "b:reply", false
	media := reply
	placeholder := uuid.New()
	media.SemanticKey = "a:files"
	media.ContentBlocks = json.RawMessage(`[{"type":"media_ref","artifact_id":"` + placeholder.String() + `"}]`)
	media.Files = []executionstore.InboxPlannedFile{failedIntegrationFile(placeholder, []byte("review"))}
	router := NewIntegrationRouter(store.Execution(), store.Integrations())
	var plannedAgent uuid.UUID
	uploads := &integrationConsumerUploads{}
	provider := &integrationConsumerProvider{
		file: IntegrationInboxFile{Content: []byte("review"), ContentType: "text/plain"},
	}
	consumer := NewIntegrationInboxConsumer(
		router, store.Integrations(), uploads, map[string]IntegrationInboxProvider{"slack": provider},
		nil, testIntegrationLaunchWorkflow(router),
	)
	for i, incoming := range []IntegrationEvent{event, reply, media} {
		if i > 0 {
			_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: incoming.SemanticKey, Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err = store.Integrations().ClaimIntegrationInbox(
				ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
				},
			)
			require.NoError(t, err)
			require.True(t, found)
		}
		plan, err := freezeTestIntegrationEvent(ctx, router, receipt.Lease(), &incoming)
		require.NoError(t, err)
		require.Len(t, plan.Recipients, 1)
		for _, recipient := range plan.Recipients {
			if i == 0 {
				plannedAgent = recipient.AgentID
				require.Len(t, recipient.Launch.Subscriptions, 1)
			} else {
				require.Equal(t, plannedAgent, recipient.AgentID)
				require.NotNil(t, recipient.Subscription)
			}
		}
		results, err := consumer.Consume(ctx, receipt.Lease())
		require.NoError(t, err)
		require.Len(t, results, 1)
		if i == 0 {
			require.True(t, results[0].Launch.Created)
		} else {
			require.True(t, results[0].Input.Created)
		}
		replayed, err := router.Admit(ctx, receipt.Lease(), nil)
		require.NoError(t, err)
		require.Len(t, replayed, 1)
		if i == 0 {
			require.False(t, replayed[0].Launch.Created)
		} else {
			require.False(t, replayed[0].Input.Created)
		}
	}
	require.Equal(t, 1, uploads.uploads)
	subscriptions, err := store.Integrations().ListIntegrationSubscriptions(
		ctx, integrationstore.ListIntegrationSubscriptionsInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, Limit: 100,
		},
	)
	require.NoError(t, err)
	require.Len(t, subscriptions.Subscriptions, 1)
	require.Equal(t, plannedAgent, subscriptions.Subscriptions[0].AgentID)

}

func TestIntegrationRouterFrozenSubscriptionEventRechecksLiveAttachment(t *testing.T) {
	t.Parallel()
	_, store, ids, integrationID := integrationProviderFixture(t, "github", "11", "22")
	ctx := t.Context()
	integration, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
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
		conversation,
	)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: "commit", Scope: integrationdefinition.Scope{
			GitHub: &integrationdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42},
		}},
		SemanticKey: "commit:1", ContentBlocks: json.RawMessage(`[{"type":"text","text":"new commit"}]`),
		Actor: integrationTestActor(t, integration, "33"),
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
	excluded.Event.Kind, excluded.SemanticKey = "pull_request_opened", "opened:1"
	excludedReceipt := capture("excluded")
	frozen, err := router.freezeEmptyIfUnrouted(ctx, excludedReceipt.Lease(), integration, excluded)
	require.NoError(t, err)
	require.True(t, frozen, "launch-only events do not keep subscription processing alive")
	plan, _, err := router.Freeze(ctx, excludedReceipt.Lease(), &event, nil)
	require.NoError(t, err)
	require.Empty(t, plan.Recipients)
	_, err = router.Admit(ctx, excludedReceipt.Lease(), nil)
	require.NoError(t, err)
	integration, err = store.Integrations().UpdateIntegration(
		ctx, integration.ID, integrationstore.SaveIntegrationInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: integration.Name, IntegrationKind: integration.IntegrationKind,
			Settings: integrationtest.GitHubSettings(uuid.New(), "pull_request_opened", ""),
		},
	)
	require.NoError(t, err)
	launcherCalled := false
	workflow := NewIntegrationLaunchWorkflow(router, map[integrationdefinition.Kind]IntegrationLauncher{
		integrationdefinition.GitHubPR: func(
			_ context.Context, input IntegrationLaunchContext,
		) ([]IntegrationLaunchIntent, error) {
			launcherCalled = true
			require.Empty(t, input.Candidates.Subscriptions, "launch decisions use the same forwarding policy")
			return nil, nil
		},
	}, nil)
	unforwarded := capture("launch-decision")
	decided, _, err := workflow.Decide(ctx, unforwarded.Lease(), unforwarded, integration, excluded)
	require.NoError(t, err)
	require.True(t, launcherCalled)
	plan, _, err = router.Freeze(ctx, unforwarded.Lease(), decided, nil)
	require.NoError(t, err)
	require.Empty(t, plan.Recipients, "an existing subscription does not receive PR-open without a launch decision")
	_, err = router.Admit(ctx, unforwarded.Lease(), nil)
	require.NoError(t, err)
	receipt := capture("included")
	plan, _, err = router.Freeze(ctx, receipt.Lease(), &event, nil)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 1)
	for _, recipient := range plan.Recipients {
		require.Equal(t, []integrationstore.ConversationAddress{{Kind: "pull_request", Ref: "123#42"}},
			recipient.Subscription.Alternatives)
	}
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	_, _, err = router.Freeze(ctx, receipt.Lease(), &excluded, nil)
	require.NoError(t, err)
	results, err := router.Admit(ctx, receipt.Lease(), nil)
	require.Error(t, err)
	require.Empty(t, results)
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	replacement := createTestIntegrationSubscription(
		t,
		store,
		integration,
		launched.Agent.ID,
		conversation,
	)
	require.NotEqual(t, original.ID, replacement.ID)
	results, err = router.Admit(ctx, receipt.Lease(), nil)
	require.NoError(t, err, "a matching live attachment may authorize already frozen work")
	require.Len(t, results, 1)
	require.True(t, results[0].Input.Created)
	removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
	results, err = router.Admit(ctx, receipt.Lease(), nil)
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
	t *testing.T, store *storage.Store, integration integrationstore.IntegrationRecord, agentID uuid.UUID,
	conversation string,
) integrationstore.IntegrationSubscriptionRecord {
	t.Helper()
	subscription, err := store.Integrations().CreateIntegrationSubscription(
		t.Context(), integrationstore.CreateIntegrationSubscriptionInput{
			OrgID: integration.OrgID, ProjectID: integration.ProjectID, IntegrationID: integration.ID, AgentID: agentID,
			Conversation: json.RawMessage(conversation),
		},
	)
	require.NoError(t, err)
	return subscription
}

func removeTestAgentSubscriptions(
	t *testing.T, store *storage.Store, integration integrationstore.IntegrationRecord, agentID uuid.UUID,
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
