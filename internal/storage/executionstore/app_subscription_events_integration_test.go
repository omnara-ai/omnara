//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestAppSubscriptionEventChangesRequireExplicitReattachmentAndFenceFrozenInput(t *testing.T) {
	t.Parallel()
	f := newAppActivationFixture(t)
	f.app = inboxInputApp(t, f, "github")
	_, err := f.store.Integrations().UpdateProjectApp(f.ctx, f.app.ID, integrationstore.SaveProjectAppInput{
		OrgID: testOrgID, ProjectID: testProjectID, Name: f.app.Name, AppType: f.app.AppType,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "installation", ScopeRef: f.app.ProviderAccountRef,
			Slots: []integrationstore.AppLaunchSlot{{Key: "review", AgentProfileID: &f.profile.ID}},
		}},
	})
	require.NoError(t, err)
	definition := f.definition(t, "Review comments")
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#42"}
	launch := f.launchInput(uuid.Nil, "subscription-events-launch")
	launch.ProfileID, launch.DerivedBaseConfigID = f.profile.ID, f.profile.CurrentConfigID
	launch.DerivedConfig = &definition
	launch.Subscriptions = []integrationstore.AppSubscriptionAttachment{
		{AppID: f.app.ID, Type: "pull_request", Conversation: json.RawMessage(`{"repository_id":123,"pull_request":41}`), Events: []string{"discussion_comment"}},
		{AppID: f.app.ID, Type: "pull_request", Conversation: json.RawMessage(`{"repository_id":123,"pull_request":42}`), Events: []string{"discussion_comment"}},
	}
	launch.InitialInput = &executionstore.LaunchInitialInput{
		ContentBlocks:    json.RawMessage(`[{"type":"text","text":"Review this pull request"}]`),
		Actor:            mustAppActorParams(t, f.app.ID, "789"),
		Origin:           &executionstore.LaunchInputOrigin{AppID: f.app.ID, Address: address},
		SemanticEventKey: "launch-review",
	}
	slot := executionstore.InboxLaunchSlot{
		AgentID: uuid.Must(uuid.NewV7()), Launch: f.freezeLaunch(t, launch),
		Selection: integrationstore.InboxAppSelection{AppID: f.app.ID, Address: address, Slot: "review"},
	}
	_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: testProjectID, AppID: f.app.ID, ReceiptKey: "launch", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	receipt, found, err := f.store.Integrations().ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: testProjectID, AppID: f.app.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	plan, err := json.Marshal(map[string]executionstore.InboxLaunchSlot{"review": slot})
	require.NoError(t, err)
	require.NoError(t, f.store.Integrations().WithIntegrationInboxLease(f.ctx, receipt.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.FreezePlan(f.ctx, plan) }))
	launched, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, receipt.Lease(), "review")
	require.NoError(t, err)
	require.True(t, launched.Created)
	before := f.subscriptions(t, launched.Agent.ID)
	require.Len(t, before, 2, "both explicit PR attachments are admitted with the launch")
	var selectedID uuid.UUID
	for _, row := range before {
		require.Equal(t, []string{"discussion_comment"}, row.Events)
		if row.ScopeRef == address.Ref {
			selectedID = row.ID
		}
	}
	require.NotEqual(t, uuid.Nil, selectedID)

	freeze := func(event string) integrationstore.IntegrationInboxRecord {
		input := inboxInputPlan(t, launched.Agent.ID, f.app, event)
		input.Subscription = &executionstore.InboxSubscriptionAuthority{
			Event:        event,
			Alternatives: []executionstore.InboxSubscriptionReference{{Type: "pull_request", Address: address}},
		}
		return freezeInboxInput(t, f, input, event, time.Minute)
	}
	discussion, review := freeze("discussion_comment"), freeze("review_comment")
	require.NoError(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, discussion.Lease(), "recipient", address))
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, review.Lease(), "recipient", address), storeerr.ErrUnauthorized)
	_, err = f.store.Execution().ChangeAgentConfig(f.ctx, f.changeInput(t, launched.Agent.ID,
		"Review comments and commits", "new-instruction"))
	require.NoError(t, err)
	require.Equal(t, before, f.subscriptions(t, launched.Agent.ID), "config does not own event filters")
	require.NoError(
		t,
		f.store.Execution().CheckInboxConversationAuthority(f.ctx, discussion.Lease(), "recipient", address),
	)
	require.ErrorIs(
		t,
		f.store.Execution().CheckInboxConversationAuthority(f.ctx, review.Lease(), "recipient", address),
		storeerr.ErrUnauthorized,
	)
	attachment := launch.Subscriptions[1]
	attachment.Events = []string{"review_comment", "commit"}
	_, err = f.store.Integrations().CreateAppSubscription(f.ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: testOrgID, ProjectID: testProjectID, AppID: f.app.ID, AgentID: launched.Agent.ID,
		Type: attachment.Type, Conversation: attachment.Conversation, Events: attachment.Events,
	})
	require.ErrorIs(t, err, storeerr.ErrConflict, "attach cannot overwrite another receive filter")
	require.Equal(t, before, f.subscriptions(t, launched.Agent.ID))
	require.NoError(t, f.store.Integrations().DeleteAppSubscription(f.ctx, testOrgID, testProjectID, f.app.ID, selectedID))
	replacement := f.attach(t, launched.Agent.ID, attachment)
	require.NotEqual(t, selectedID, replacement.ID)
	require.NoError(t, f.store.Integrations().DeleteAppSubscription(f.ctx, testOrgID, testProjectID, f.app.ID, selectedID))
	after := f.subscriptions(t, launched.Agent.ID)
	require.Len(t, after, 2)
	for _, row := range after {
		if row.ScopeRef == address.Ref {
			require.Equal(t, replacement.ID, row.ID)
			require.Equal(t, []string{"commit", "review_comment"}, row.Events)
		} else {
			require.Contains(t, before, row, "the independent PR attachment is unchanged")
		}
	}
	_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, discussion.Lease(), "recipient")
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "the old event must stop reaching the subscribed PR")
	accepted, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, review.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, accepted.Created)
	require.Equal(t, launched.Agent.ID, accepted.AgentInput.AgentID)
	f.detach(t, launched.Agent.ID)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, review.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, accepted.AgentInput.ID, replayed.AgentInput.ID)
	require.Empty(t, f.subscriptions(t, launched.Agent.ID), "committed replay cannot recreate receive authority")
}

func TestAppSubscriptionLaunchBatchRejectsInvalidOrConflictingAttachment(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"unknown type",
		"wrong provider",
		"invalid address",
		"unknown event",
		"conflicting duplicate",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAppActivationFixture(t)
			f.app = inboxInputApp(t, f, "github")
			first := integrationstore.AppSubscriptionAttachment{
				AppID: f.app.ID, Type: "pull_request", Conversation: json.RawMessage(`{"repository_id":123,"pull_request":42}`),
				Events: []string{"discussion_comment"},
			}
			second := first
			second.Conversation = json.RawMessage(`{"repository_id":123,"pull_request":43}`)
			want := storeerr.ErrInvalidRequest
			switch scenario {
			case "unknown type":
				second.Type = "thread_messages"
			case "wrong provider":
				second.Conversation = json.RawMessage(`{"channel_id":"C123"}`)
			case "invalid address":
				second.Conversation = json.RawMessage(`{"repository_id":123}`)
			case "unknown event":
				second.Events = []string{"message"}
			case "conflicting duplicate":
				second.Conversation = first.Conversation
				second.Events = []string{"commit"}
				want = storeerr.ErrConflict
			}
			definition := f.definition(t, "Invalid attachment batch")
			input := f.launchInput(uuid.Nil, "invalid-batch")
			input.DerivedConfig = &definition
			input.Subscriptions = []integrationstore.AppSubscriptionAttachment{first, second}
			input.Message = "Must not be admitted"
			_, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.ErrorIs(t, err, want)
			var agents, configs, subscriptions int
			require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT
				(SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2),
				(SELECT count(*) FROM agent_configs WHERE project_id=$1 AND effective_definition_hash=$3),
				(SELECT count(*) FROM app_subscriptions WHERE project_id=$1)`,
				testProjectID, input.IdempotencyKey, definition.EffectiveDefinitionHash).Scan(&agents, &configs, &subscriptions))
			require.Zero(t, agents)
			require.Zero(t, configs)
			require.Zero(t, subscriptions)
		})
	}
}
