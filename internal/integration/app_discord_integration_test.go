//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppDiscordConsumerPreparesOnlyAuthorizedFrozenConversation(t *testing.T) {
	for _, scenario := range []string{"launch and reply", "reaction denied", "no launcher", "revoked before preparation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, appID := appProviderFixture(t, "discord", "11", "22")
			appSetup, err := store.Integrations().GetProjectApp(ctx, ids.ProjectID, appID)
			require.NoError(t, err)
			f, provider := newDiscordInboxFixture(t)
			f.appSetup = appSetup
			provider.apps = store.Integrations()
			base := storagefixture.SeedAgentConfig(
				t,
				ctx,
				store.Models(),
				store.Execution(),
				ids.OrgID,
				ids.ProjectID,
				"instruction: help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			)
			profile, err := store.Execution().
				CreateAgentProfile(
					ctx,
					executionstore.CreateAgentProfileInput{
						ProjectID:       ids.ProjectID,
						Name:            "discord",
						CurrentConfigID: base.ID,
					},
				)
			require.NoError(t, err)
			if scenario != "no launcher" {
				_, err = store.Integrations().
					UpdateProjectApp(
						ctx, appID,
						integrationstore.SaveProjectAppInput{
							OrgID:     ids.OrgID,
							ProjectID: ids.ProjectID,
							Name:      "chat",
							AppType:   appdefinition.DiscordThread,
							Settings: integrationstore.ProjectAppSettings{
								Launcher: &integrationstore.AppLauncher{
									Trigger: "mention",
									Slots: []integrationstore.AppLaunchSlot{
										{Key: "review", AgentProfileID: &profile.ID},
									},
								},
							},
						},
					)
				require.NoError(t, err)
			}
			accept := func(key string, message discord.Message) integrationstore.IntegrationInboxRecord {
				receipt, _, err := store.Integrations().
					AcceptIntegrationReceipt(
						ctx,
						integrationstore.VerifiedIntegrationReceipt{
							ProjectID:  ids.ProjectID,
							AppID:      appID,
							ReceiptKey: key,
							Payload:    discordInboxPayload(t, message),
						},
					)
				require.NoError(t, err)
				return receipt
			}
			receipt := accept("root", f.message)
			router := NewAppRouter(store.Execution(), store.Integrations())
			consumer := NewAppInboxConsumer(
				router,
				store.Integrations(),
				nil,
				map[string]AppInboxProvider{"discord": provider},
				nil,
				testAppLaunchWorkflow(router),
			)
			worker := NewAppInboxWorker(store.Integrations(), consumer, AppInboxWorkerOptions{})
			identityReads := 0
			reactions := func() []string {
				f.mu.Lock()
				defer f.mu.Unlock()
				paths := []string{}
				for _, request := range f.requests {
					if strings.Contains(request, "/reactions/") {
						paths = append(paths, request)
					}
				}
				return paths
			}
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/reactions/") {
					var count int
					assert.NoError(t, pool.QueryRow(ctx,
						`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'`, ids.ProjectID,
					).Scan(&count))
					assert.Equal(t, 1, count, "acknowledgement must follow committed input")
					if scenario == "reaction denied" {
						w.WriteHeader(http.StatusForbidden)
						return true
					}
				}
				if r.URL.Path == "/api/v10/users/@me" {
					identityReads++
					if scenario == "revoked before preparation" && identityReads == 2 {
						_, err := pool.Exec(
							ctx,
							`UPDATE project_apps SET state='disconnected',updated_at=now() WHERE id=$1`,
							appID,
						)
						assert.NoError(t, err)
					}
				}
				if r.Method == http.MethodPost {
					var plan json.RawMessage
					assert.NoError(
						t,
						pool.QueryRow(ctx, `SELECT plan FROM integration_inbox WHERE id=$1`, receipt.ID).Scan(&plan),
					)
					assert.NotEqual(t, "{}", string(plan))
					var count int
					assert.NoError(
						t,
						pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`, ids.ProjectID).
							Scan(&count),
					)
					assert.Zero(t, count)
				}
				return false
			}
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			if scenario == "revoked before preparation" {
				require.Error(t, err)
				require.Zero(t, f.posts)
				require.Empty(t, reactions())
				return
			}
			require.NoError(t, err)
			if scenario == "no launcher" {
				require.Zero(t, f.posts)
				require.Empty(t, reactions())
				return
			}
			require.Equal(t, 1, f.posts)
			rootReaction := "PUT /api/v10/channels/300/messages/500/reactions/👀/@me"
			require.Equal(t, []string{rootReaction}, reactions())
			latest, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, latest.State)
			results, err := consumer.Consume(
				ctx,
				integrationstore.IntegrationInboxLease{
					ProjectID: ids.ProjectID,
					ReceiptID: receipt.ID,
					Token:     uuid.New(),
				},
			)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.False(t, results[0].Launch.Created)
			require.Equal(t, []string{rootReaction}, reactions(), "replay must not repeat feedback")
			require.Equal(t, 1, f.posts)
			f.override = nil
			reply := f.message
			reply.ID, reply.ChannelID, reply.Content, reply.Mentions = "501", "500", "continue", nil
			accept("reply", reply)
			worked, err = worker.RunOnce(ctx)
			require.True(t, worked)
			require.NoError(t, err)
			reply.ID, reply.ChannelID = "502", "400"
			accept("unselected", reply)
			worked, err = worker.RunOnce(ctx)
			require.True(t, worked)
			require.NoError(t, err)
			var count int
			require.NoError(
				t,
				pool.QueryRow(
					ctx,
					`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content'`,
					ids.ProjectID,
				).
					Scan(
						&count,
					),
			)
			require.Equal(t, 2, count)
			require.Equal(t, []string{
				rootReaction, "PUT /api/v10/channels/500/messages/501/reactions/👀/@me",
			}, reactions(), "react to accepted thread replies, not unrelated messages")
			require.Equal(t, 1, f.posts)
			if scenario == "launch and reply" {
				f.mu.Lock()
				f.channels["600"] = discord.Channel{ID: "600", GuildID: "200", Type: 0, Name: "other-server"}
				f.message.ID, f.message.ChannelID, f.message.GuildID = "700", "600", "200"
				other := f.message
				f.mu.Unlock()
				accept("other-server", other)
				worked, err = worker.RunOnce(ctx)
				require.True(t, worked)
				require.NoError(t, err)
				other.ID, other.ChannelID, other.Mentions, other.Content = "701", "700", nil, "other reply"
				accept("other-reply", other)
				worked, err = worker.RunOnce(ctx)
				require.True(t, worked)
				require.NoError(t, err)
				var agents, smallest, largest int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), min(inputs), max(inputs)
					FROM (SELECT agent_id, count(*) AS inputs FROM agent_inputs
					WHERE project_id=$1 AND input_kind='content' GROUP BY agent_id) counts`, ids.ProjectID).
					Scan(&agents, &smallest, &largest))
				require.Equal(t, 2, agents)
				require.Equal(t, 2, smallest)
				require.Equal(t, 2, largest)
				require.Equal(t, 2, f.posts)
			}
		})
	}
}

func TestAppDiscordChannelSubscriptionReceivesRootMentionWithoutLauncher(t *testing.T) {
	for _, exactThread := range []bool{false, true} {
		t.Run(fmt.Sprintf("exact_thread=%t", exactThread), func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, appID := appProviderFixture(t, "discord", "11", "22")
			app, err := store.Integrations().GetProjectApp(ctx, ids.ProjectID, appID)
			require.NoError(t, err)
			require.Nil(t, app.Settings.Launcher)
			f, provider := newDiscordInboxFixture(t)
			f.appSetup, provider.apps = app, store.Integrations()
			config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: Help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: ids.ProjectID, LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
				AgentConfigID: config.ID,
			})
			require.NoError(t, err)
			createTestAppSubscription(t, store, app, launched.Agent.ID, "thread_messages", `{"channel_id":"300"}`)
			if exactThread {
				createTestAppSubscription(
					t,
					store,
					app,
					launched.Agent.ID,
					"thread_messages",
					`{"channel_id":"300","thread_id":"500"}`,
				)
			}
			router := NewAppRouter(store.Execution(), store.Integrations())
			consumer := NewAppInboxConsumer(router, store.Integrations(), nil,
				map[string]AppInboxProvider{"discord": provider}, nil, testAppLaunchWorkflow(router))
			capture := func(key string, message discord.Message) integrationstore.IntegrationInboxRecord {
				t.Helper()
				_, _, err := store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
					ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: key, Payload: discordInboxPayload(t, message),
				})
				require.NoError(t, err)
				receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
				})
				require.NoError(t, err)
				require.True(t, found)
				return receipt
			}
			for _, key := range []string{"root", "root-redelivery"} {
				receipt := capture(key, f.message)
				results, err := consumer.Consume(ctx, receipt.Lease())
				require.NoError(t, err)
				require.Len(t, results, 1, "overlapping channel/thread subscriptions must deduplicate recipients")
				require.Nil(t, results[0].Launch)
				require.Equal(t, launched.Agent.ID, results[0].Input.AgentInput.AgentID)
				require.Equal(t, key == "root", results[0].Input.Created)
			}
			require.Equal(t, 1, f.posts, "root mention prepares its single thread only once")
			for _, mention := range []bool{false, true} {
				reply := f.message
				reply.ID, reply.ChannelID = "501", "500"
				if !mention {
					reply.Mentions = nil
				} else {
					reply.ID = "502"
				}
				receipt := capture(reply.ID, reply)
				results, err := consumer.Consume(ctx, receipt.Lease())
				require.NoError(t, err)
				if exactThread {
					require.Len(t, results, 1)
				} else {
					require.Empty(t, results, "even a thread mention cannot borrow its parent's subscription")
				}
			}
			next := f.message
			next.ID = "600"
			receipt := capture("removed-after-freeze", next)
			expansion, err := provider.Expand(ctx, app, receipt.Payload)
			require.NoError(t, err)
			plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), expansion.Events)
			require.NoError(t, err)
			require.Len(t, plan, 1)
			removeTestAgentSubscriptions(t, store, app, launched.Agent.ID)
			_, err = consumer.Consume(ctx, receipt.Lease())
			require.Error(t, err)
			require.Equal(t, 1, f.posts, "revoked channel subscription must prevent preparation and input")
			var agents, inputs int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM agents WHERE project_id=$1),
				(SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_kind='content')`, ids.ProjectID).
				Scan(&agents, &inputs))
			require.Equal(t, 1, agents, "receiving through a subscription never creates another agent")
			wantInputs := 1
			if exactThread {
				wantInputs += 2
			}
			require.Equal(t, wantInputs, inputs)
		})
	}
}
