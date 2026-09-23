//go:build integration

package apps

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type scheduledTestProvider struct {
	appConsumerProvider
	posts, ensures int
	root           appdefinition.Scope
	publish        func(context.Context) error
	ensure         func(context.Context) error
}

func (p *scheduledTestProvider) PublishScheduledRoot(
	ctx context.Context,
	_ appstore.ProjectAppRecord,
	_ appdefinition.ScheduledThreadLaunch,
	_ uuid.UUID,
	check func(context.Context) error,
) (appdefinition.Scope, error) {
	if err := check(ctx); err != nil {
		return appdefinition.Scope{}, err
	}
	p.posts++
	if p.publish != nil {
		if err := p.publish(ctx); err != nil {
			return appdefinition.Scope{}, err
		}
	}
	return p.root, nil
}
func (p *scheduledTestProvider) EnsureScheduledThread(
	ctx context.Context,
	_ appstore.ProjectAppRecord,
	_ appdefinition.Scope,
	check func(context.Context) error,
) error {
	p.ensures++
	if err := check(ctx); err != nil {
		return err
	}
	if p.ensure != nil {
		return p.ensure(ctx)
	}
	return nil
}

type scheduledJourney struct {
	firings  int
	t        *testing.T
	pool     *pgxpool.Pool
	store    *storage.Store
	ids      storagefixture.ProjectIDs
	appID    uuid.UUID
	trigger  executionstore.CronTriggerRecord
	profile  executionstore.AgentProfileRecord
	provider *scheduledTestProvider
	consumer *AppInboxConsumer
	handler  *ThreadAppScheduledHandler
}

func newScheduledJourney(t *testing.T) *scheduledJourney {
	t.Helper()
	return newScheduledProviderJourney(t, appdefinition.ProviderSlack)
}

func newScheduledProviderJourney(t *testing.T, providerName string) *scheduledJourney {
	t.Helper()
	tenant, account := "T123", "A123"
	channel := "C123"
	root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
	if providerName == appdefinition.ProviderDiscord {
		tenant, account = "100", "22"
		channel = "300"
		root = appdefinition.Scope{Discord: &appdefinition.DiscordScope{
			GuildID: "100", ChannelID: "300", ThreadID: "500",
		}}
	}
	pool, store, ids, appID := appProviderFixture(t, providerName, tenant, account)
	base := storagefixture.SeedAgentConfig(t, t.Context(), store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: report\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	profile, err := store.Execution().CreateAgentProfile(t.Context(), executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "daily", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	profileID, err := publicid.Encode(publicid.KindAgentProfile, profile.ID)
	require.NoError(t, err)
	settings, err := json.Marshal(appdefinition.ThreadScheduleSettings{
		AgentProfileID: profileID, ChannelID: channel,
		OpeningMessageTemplate: "Daily update {{.trigger.local_date}}",
		MessageTemplate:        "Review today and post your findings.",
	})
	require.NoError(t, err)
	trigger, err := store.Execution().CreateCronTrigger(t.Context(), executionstore.CreateCronTriggerInput{
		ProjectID:      ids.ProjectID,
		Name:           "Daily update",
		CronExpression: "0 9 * * *",
		Timezone:       "America/Los_Angeles",
		Enabled:        true,
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetApp, ID: appID, Settings: settings,
		},
	})
	require.NoError(t, err)
	provider := &scheduledTestProvider{root: root}
	router := NewAppRouter(store.Execution(), store.Apps())
	handler := NewThreadAppScheduledHandler(router, store.Apps(), provider)
	consumer := NewAppInboxConsumer(
		router, store.Apps(), nil, nil,
		nil, testAppLaunchWorkflow(router),
		WithAppScheduledHandlers(map[appdefinition.Type]AppScheduledHandler{
			appdefinition.Type(appdefinition.AppTypesForProvider(providerName)[0]): handler.Handle,
		}),
	)
	return &scheduledJourney{
		t: t, pool: pool, store: store, ids: ids, appID: appID,
		trigger: trigger, profile: profile, provider: provider, consumer: consumer, handler: handler,
	}
}

func (f *scheduledJourney) fire() appstore.AppInboxRecord {
	f.t.Helper()
	_, err := f.pool.Exec(
		f.t.Context(),
		"UPDATE cron_triggers SET next_fire_after=$2 WHERE id=$1",
		f.trigger.ID,
		time.Now().UTC().Truncate(time.Minute).Add(-24*time.Hour+time.Duration(f.firings)*time.Minute),
	)
	require.NoError(f.t, err)
	claims, err := f.store.Execution().ClaimDueCronTriggers(f.t.Context(), 10)
	require.NoError(f.t, err)
	require.Len(f.t, claims.Claimed, 1)
	f.firings++
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.t.Context(), claims.Claimed[0])
	require.NoError(f.t, err)
	require.True(f.t, queued)
	return f.claim()
}
func (f *scheduledJourney) claim() appstore.AppInboxRecord {
	f.t.Helper()
	receipt, found, err := f.store.Apps().ClaimAppInbox(
		f.t.Context(),
		appstore.ClaimAppInboxInput{
			ProjectID: f.ids.ProjectID, AppID: f.appID, LeaseDuration: time.Minute,
		},
	)
	require.NoError(f.t, err)
	require.True(f.t, found)
	return receipt
}

func TestScheduledLaunchHasConversationContextWithoutMentionLauncher(t *testing.T) {
	f := newScheduledJourney(t)
	receipt := f.fire()
	require.Equal(t, appstore.AppInboxSourceScheduled, receipt.Source)
	var count int
	require.NoError(t, f.pool.QueryRow(
		t.Context(), "SELECT count(*) FROM agents WHERE project_id=$1", f.ids.ProjectID,
	).Scan(&count))
	require.Zero(t, count)
	results, err := f.consumer.Consume(t.Context(), receipt.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	launched := results[0].Launch
	require.NotNil(t, launched)
	require.True(t, launched.Created)
	require.Equal(t, 1, f.provider.posts)
	require.Zero(t, f.provider.expansions, "trusted cron must not enter provider normalization")
	config, found, err := f.store.Execution().GetAgentConfig(t.Context(), f.ids.ProjectID, launched.Agent.CurrentConfigID)
	require.NoError(t, err)
	require.True(t, found)
	raw := string(config.CompiledDefinition)
	context, found, err := f.store.Apps().GetAgentAppConversation(
		t.Context(), f.ids.ProjectID, launched.Agent.ID, f.appID,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "C123:100.1", context.Ref)
	require.Contains(t, raw, "app__chat__post_message")
	subscriptions, err := f.store.Apps().ListAppSubscriptions(
		t.Context(), appstore.ListAppSubscriptionsInput{ProjectID: f.ids.ProjectID, AppID: f.appID, Limit: 100},
	)
	require.NoError(t, err)
	require.Len(t, subscriptions.Subscriptions, 1)
	require.Equal(t, launched.Agent.ID, subscriptions.Subscriptions[0].AgentID)
	require.Equal(t, "thread_messages", subscriptions.Subscriptions[0].Type)
	require.Equal(t, []string{"message"}, subscriptions.Subscriptions[0].Events)
	require.Equal(
		t,
		appstore.ConversationAddress{Kind: "thread", Ref: "C123:100.1"},
		subscriptions.Subscriptions[0].Address,
	)
	require.NotEqual(t, uuid.Nil, launched.AppTarget.ID)
	var selected uuid.UUID
	var handler string
	require.NoError(t, f.pool.QueryRow(
		t.Context(), "SELECT app_target_id,interaction_handler_key FROM agents WHERE id=$1", launched.Agent.ID,
	).Scan(&selected, &handler))
	require.Equal(t, launched.AppTarget.ID, selected, "first interaction must already have its thread")
	require.Equal(t, "chat", handler)
	var provider string
	require.NoError(t, f.pool.QueryRow(
		t.Context(),
		`SELECT actor.provider FROM agent_inputs input JOIN actors actor ON actor.id=input.actor_id WHERE input.id=$1`,
		launched.AgentInput.ID,
	).Scan(&provider))
	require.Equal(t, executionstore.ActorProviderOmnara, provider)
	again, err := f.consumer.Consume(t.Context(), receipt.Lease())
	require.NoError(t, err)
	require.False(t, again[0].Launch.Created)
	require.Equal(t, 1, f.provider.posts)
	require.Equal(t, 1, f.provider.ensures)
	f.provider.root.Slack.ThreadTS = "101.1"
	next := f.fire()
	second, err := f.consumer.Consume(t.Context(), next.Lease())
	require.NoError(t, err)
	require.NotEqual(t, launched.Agent.ID, second[0].Launch.Agent.ID)
	require.Equal(t, 2, f.provider.posts)
}

func TestScheduledLaunchRetriesFrozenPlanAndBlocksEarlyFollowup(t *testing.T) {
	f := newScheduledJourney(t)
	receipt := f.fire()
	f.provider.ensure = func(context.Context) error { return errors.New("thread preparation unavailable") }
	worker := NewAppInboxWorker(f.store.Apps(), f.consumer, AppInboxWorkerOptions{})
	err := worker.consume(t.Context(), receipt)
	require.ErrorContains(t, err, "thread preparation unavailable")
	saved, err := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.NotEmpty(t, saved.Plan)
	require.Equal(t, appstore.AppInboxPending, saved.State)
	plan, err := decodeAppInboxPlan(saved.Plan)
	require.NoError(t, err)
	require.Equal(t, f.provider.root, plan["scheduled"].Scope)
	require.Len(t, plan["scheduled"].Launch.Subscriptions, 1)
	require.Equal(t, f.appID, plan["scheduled"].Launch.Subscriptions[0].AppID)
	require.Equal(t, []string{"message"}, plan["scheduled"].Launch.Subscriptions[0].Events)
	require.JSONEq(
		t,
		`{"channel_id":"C123","thread_ts":"100.1"}`,
		string(plan["scheduled"].Launch.Subscriptions[0].Conversation),
	)
	follow, _, err := f.store.Apps().AcceptAppReceipt(
		t.Context(),
		appstore.VerifiedAppReceipt{
			ProjectID: f.ids.ProjectID, AppID: f.appID, ReceiptKey: "early-reply", Payload: []byte(`{}`),
		},
	)
	require.NoError(t, err)
	claimed := f.claim()
	require.Equal(t, follow.ID, claimed.ID)
	app, err := f.store.Apps().GetProjectAppByID(t.Context(), f.appID)
	require.NoError(t, err)
	event := AppEvent{
		Event:         appdefinition.Event{Kind: "message", Scope: f.provider.root},
		SemanticKey:   "reply",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Also consider this"}]`),
		Actor:         appTestActor(t, app.ID, "U123"),
	}
	_, err = f.consumer.router.freezeEmptyIfUnrouted(t.Context(), claimed.Lease(), app, event)
	require.ErrorIs(t, err, appstore.ErrAppSelectionReserved)
	f.provider.ensure = nil
	_, err = f.pool.Exec(t.Context(),
		`UPDATE app_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	resumed := f.claim()
	require.Equal(t, receipt.ID, resumed.ID)
	require.NotEqual(t, receipt.ClaimToken, resumed.ClaimToken)
	result, err := f.consumer.Consume(t.Context(), resumed.Lease())
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, plan["scheduled"].AgentID, result[0].Launch.Agent.ID)
	require.Equal(t, 1, f.provider.posts)
	require.Equal(t, 2, f.provider.ensures)
	after, err := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(saved.Plan), string(after.Plan))
	require.Equal(t, appstore.AppInboxCompleted, after.State)
	replayed, err := f.consumer.Consume(t.Context(), resumed.Lease())
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	require.False(t, replayed[0].Launch.Created)
	require.Equal(t, result[0].Launch.Agent.ID, replayed[0].Launch.Agent.ID)
	var agents int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, f.provider.posts)
	require.Equal(t, 2, f.provider.ensures)
}
