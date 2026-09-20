//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type scheduledTestProvider struct {
	appConsumerProvider
	posts, reconciles, ensures int
	root                       appdefinition.Scope
	publishError               error
	notSent                    bool
	ensure                     func(context.Context) error
}

func (p *scheduledTestProvider) PublishScheduledRoot(
	ctx context.Context,
	_ integrationstore.ProjectAppRecord,
	_ integrationstore.ScheduledAppLaunch,
	_ uuid.UUID,
	_ integrationstore.ScheduledLaunchPreparation,
	fresh bool,
	check func(context.Context) error,
) (appdefinition.Scope, bool, error) {
	if err := check(ctx); err != nil {
		return appdefinition.Scope{}, true, err
	}
	if fresh {
		p.posts++
	} else {
		p.reconciles++
	}
	return p.root, p.notSent, p.publishError
}
func (p *scheduledTestProvider) EnsureScheduledThread(
	ctx context.Context,
	_ integrationstore.ProjectAppRecord,
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
}

func newScheduledJourney(t *testing.T) *scheduledJourney {
	t.Helper()
	return newScheduledProviderJourney(t, appdefinition.ProviderSlack)
}

func newScheduledProviderJourney(t *testing.T, providerName string) *scheduledJourney {
	t.Helper()
	tenant, account := "T123", "A123"
	destination := json.RawMessage(`{"channel_id":"C123"}`)
	root := appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "100.1"}}
	if providerName == appdefinition.ProviderDiscord {
		tenant, account = "100", "22"
		destination = json.RawMessage(`{"channel_id":"300"}`)
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
	trigger, err := store.Execution().CreateCronTrigger(t.Context(), executionstore.CreateCronTriggerInput{
		ProjectID:       ids.ProjectID,
		Name:            "Daily update",
		CronExpression:  "0 9 * * *",
		Timezone:        "America/Los_Angeles",
		Enabled:         true,
		MessageTemplate: "Review today and post your findings.",
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAppLaunch, ID: appID,
			AppLaunch: &executionstore.CronAppLaunchTarget{
				ProfileID:              profile.ID,
				Destination:            destination,
				OpeningMessageTemplate: "Daily update {{.trigger.local_date}}",
			},
		},
	})
	require.NoError(t, err)
	provider := &scheduledTestProvider{root: root}
	router := NewAppRouter(store.Execution(), store.Integrations())
	consumer := NewAppInboxConsumer(
		router, store.Integrations(), nil, map[string]AppInboxProvider{providerName: provider},
		nil, testAppLaunchWorkflow(router),
	)
	return &scheduledJourney{
		t: t, pool: pool, store: store, ids: ids, appID: appID,
		trigger: trigger, profile: profile, provider: provider, consumer: consumer,
	}
}

func (f *scheduledJourney) fire() integrationstore.IntegrationInboxRecord {
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
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.t.Context(), claims.Claimed[0])
	require.NoError(f.t, err)
	require.True(f.t, queued)
	return f.claim()
}
func (f *scheduledJourney) claim() integrationstore.IntegrationInboxRecord {
	f.t.Helper()
	receipt, found, err := f.store.Integrations().ClaimIntegrationInbox(
		f.t.Context(),
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID: f.ids.ProjectID, AppID: f.appID, LeaseDuration: time.Minute,
		},
	)
	require.NoError(f.t, err)
	require.True(f.t, found)
	return receipt
}

func TestScheduledLaunchHasFixedCapabilitiesWithoutMentionLauncher(t *testing.T) {
	f := newScheduledJourney(t)
	receipt := f.fire()
	require.Equal(t, integrationstore.IntegrationInboxSourceScheduledLaunch, receipt.Source)
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
	var compiled map[string]any
	require.NoError(t, json.Unmarshal(config.CompiledDefinition, &compiled))
	raw := string(config.CompiledDefinition)
	require.Contains(t, raw, "100.1")
	require.Contains(t, raw, "app__chat__post_message")
	require.Contains(t, raw, "chat__thread_messages")
	require.NotEqual(t, uuid.Nil, launched.IntegrationTarget.ID)
	var selected uuid.UUID
	var handler string
	require.NoError(t, f.pool.QueryRow(
		t.Context(), "SELECT integration_target_id,interaction_handler_key FROM agents WHERE id=$1", launched.Agent.ID,
	).Scan(&selected, &handler))
	require.Equal(t, launched.IntegrationTarget.ID, selected, "first interaction must already have its thread")
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

func TestScheduledLaunchRetriesSavedRootAndBlocksEarlyFollowup(t *testing.T) {
	f := newScheduledJourney(t)
	receipt := f.fire()
	f.provider.ensure = func(context.Context) error { return errors.New("thread preparation unavailable") }
	_, err := f.consumer.Consume(t.Context(), receipt.Lease())
	require.ErrorContains(t, err, "thread preparation unavailable")
	saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.NotEmpty(t, saved.Plan)
	prep, err := saved.ScheduledPreparation()
	require.NoError(t, err)
	require.NotNil(t, prep.Root)
	// A plain reply after the reservation cannot freeze as unrouted before launch.
	follow, _, err := f.store.Integrations().AcceptIntegrationReceipt(
		t.Context(),
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID: f.ids.ProjectID, AppID: f.appID, ReceiptKey: "early-reply", Payload: []byte(`{}`),
		},
	)
	require.NoError(t, err)
	claimed := f.claim()
	require.Equal(t, follow.ID, claimed.ID)
	app, err := f.store.Integrations().GetProjectAppByID(t.Context(), f.appID)
	require.NoError(t, err)
	event := AppEvent{
		Event:         appdefinition.Event{Kind: "message", Scope: f.provider.root},
		SemanticKey:   "reply",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Also consider this"}]`),
		Actor:         executionstore.ActorParams{Provider: "slack", ProviderTenantID: "T123", ProviderUserID: "U123"},
	}
	_, err = f.consumer.router.freezeEmptyIfUnrouted(t.Context(), claimed.Lease(), app, event)
	require.ErrorIs(t, err, integrationstore.ErrAppSelectionReserved)
	f.provider.ensure = nil
	result, err := f.consumer.Consume(t.Context(), receipt.Lease())
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, 1, f.provider.posts)
	require.Equal(t, 2, f.provider.ensures)
}

func TestScheduledPublicationRetryPreservesUncertainty(t *testing.T) {
	for _, definite := range []bool{false, true} {
		t.Run(fmt.Sprint(definite), func(t *testing.T) {
			f := newScheduledJourney(t)
			receipt := f.fire()
			f.provider.publishError = errors.New("provider unavailable")
			f.provider.notSent = definite
			_, err := f.consumer.Consume(t.Context(), receipt.Lease())
			require.Error(t, err)
			saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			prep, err := saved.ScheduledPreparation()
			require.NoError(t, err)
			require.Equal(t, !definite, prep.AttemptedAt != nil)
			f.provider.publishError = nil
			_, err = f.consumer.Consume(t.Context(), receipt.Lease())
			require.NoError(t, err)
			if definite {
				require.Equal(t, 2, f.provider.posts)
				require.Zero(t, f.provider.reconciles)
			} else {
				require.Equal(t, 1, f.provider.posts)
				require.Equal(t, 1, f.provider.reconciles)
			}
		})
	}
}
