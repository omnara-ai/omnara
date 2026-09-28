//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDiscordConsumerPreparesOnlyAuthorizedFrozenConversation(t *testing.T) {
	for _, scenario := range []string{
		"launch and reply", "reaction denied", "no launcher", "unsupported channel", "revoked before preparation",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			integrationSetup, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
			require.NoError(t, err)
			f, provider := newDiscordInboxFixture(t)
			f.integrationSetup = integrationSetup
			provider.integrations = store.Integrations()
			if scenario == "no launcher" {
				f.message.Attachments = []discord.Attachment{{ID: "600", Size: 4, Filename: "unneeded.txt"}}
			}
			if scenario == "unsupported channel" {
				f.channels["300"] = discord.Channel{ID: "300", GuildID: "100", Type: 2}
			}
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
					UpdateIntegration(
						ctx, integrationID,
						integrationstore.SaveIntegrationInput{
							OrgID:           ids.OrgID,
							ProjectID:       ids.ProjectID,
							Name:            "chat",
							IntegrationKind: integrationdefinition.DiscordThread,
							Settings:        integrationtest.ChatSettings("", profile.ID),
						},
					)
				require.NoError(t, err)
			}
			accept := func(key string, message discord.Message) integrationstore.IntegrationInboxRecord {
				receipt, _, err := store.Integrations().
					AcceptIntegrationReceipt(
						ctx,
						integrationstore.VerifiedIntegrationReceipt{
							ProjectID:     ids.ProjectID,
							IntegrationID: integrationID,
							ReceiptKey:    key,
							Payload:       discordInboxPayload(t, message),
						},
					)
				require.NoError(t, err)
				return receipt
			}
			receipt := accept("root", f.message)
			router := NewIntegrationRouter(store.Execution(), store.Integrations())
			consumer := NewIntegrationInboxConsumer(
				router,
				store.Integrations(),
				nil,
				map[string]IntegrationInboxProvider{"discord": provider},
				nil,
				testIntegrationLaunchWorkflow(router),
			)
			worker := NewIntegrationInboxWorker(store.Integrations(), consumer, IntegrationInboxWorkerOptions{})
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
							`UPDATE integrations SET state='disconnected',updated_at=now() WHERE id=$1`,
							integrationID,
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
			if scenario == "no launcher" || scenario == "unsupported channel" {
				require.Zero(t, f.posts)
				require.Empty(t, reactions())
				require.Equal(t, []string{"GET /api/v10/channels/300"}, f.requests)
				latest, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
				require.NoError(t, err)
				require.Equal(t, integrationstore.IntegrationInboxCompleted, latest.State)
				require.JSONEq(t, `{"recipients":{}}`, string(latest.Plan))
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
			reply.Attachments = []discord.Attachment{{ID: "600", Size: 4, Filename: "unneeded.txt"}}
			beforeUnselected := len(f.requests)
			accept("unselected", reply)
			worked, err = worker.RunOnce(ctx)
			require.True(t, worked)
			require.NoError(t, err)
			require.Empty(t, f.requests[beforeUnselected:], "unsubscribed non-mention must not make any provider request")
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

func TestIntegrationDiscordEarlyReplyWaitsForLaunchOrAcceptedMenu(t *testing.T) {
	for _, viaMenu := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted_menu=%t", viaMenu), func(t *testing.T) {
			ctx := t.Context()
			_, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			integration, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
			require.NoError(t, err)
			config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: Help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
				ProjectID: ids.ProjectID, Name: "discord", CurrentConfigID: config.ID,
			})
			require.NoError(t, err)
			integration, err = store.Integrations().UpdateIntegration(
				ctx,
				integrationID,
				integrationstore.SaveIntegrationInput{
					OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: integration.Name, IntegrationKind: integration.IntegrationKind,
					Settings: integrationtest.ChatSettings("", profile.ID),
				},
			)
			require.NoError(t, err)
			f, provider := newDiscordInboxFixture(t)
			f.integrationSetup, provider.integrations = integration, store.Integrations()
			f.channels["500"] = discord.Channel{ID: "500", GuildID: "100", ParentID: "300", Type: 11}
			router := NewIntegrationRouter(store.Execution(), store.Integrations())
			consumer := NewIntegrationInboxConsumer(router, store.Integrations(), nil,
				map[string]IntegrationInboxProvider{"discord": provider}, nil, testIntegrationLaunchWorkflow(router))
			capture := func(message discord.Message) integrationstore.IntegrationInboxRecord {
				t.Helper()
				_, _, err := store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
					ProjectID:     ids.ProjectID,
					IntegrationID: integrationID,
					ReceiptKey:    message.ID,
					Payload:       discordInboxPayload(t, message),
				})
				require.NoError(t, err)
				receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
				})
				require.NoError(t, err)
				require.True(t, found)
				return receipt
			}
			root := capture(f.message)
			event, ok, err := NormalizeDiscordIntegrationEvent(integration, root.Payload, f.channels["300"])
			require.NoError(t, err)
			require.True(t, ok)
			launchReceipt := root
			if viaMenu {
				profileKey, err := publicid.Encode(publicid.KindAgentProfile, profile.ID)
				require.NoError(t, err)
				raw, err := json.Marshal(event)
				require.NoError(t, err)
				choice, _, err := store.Integrations().EnsureIntegrationProfileChoice(ctx, root.Lease(),
					integrationstore.EnsureIntegrationProfileChoiceInput{
						IntegrationID: integrationID, Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "500"},
						SourceKey: event.SemanticKey, Event: raw, Payload: root.Payload,
						Options: []integrationstore.IntegrationProfileChoiceOption{
							{Key: profileKey, Name: "Support", ProfileID: profile.ID},
						},
					})
				require.NoError(t, err)
				require.NoError(t, store.Integrations().RecordIntegrationProfileChoiceMessage(
					ctx, ids.ProjectID, integrationID, choice.ID, "500", "600",
				))
				_, err = SelectChatIntegrationProfile(
					ctx, store.Integrations(), integration, choice.ID, profileKey, "33", "500", "600",
				)
				require.NoError(t, err)
				_, err = router.Freeze(ctx, root.Lease(), nil)
				require.NoError(t, err)
				_, err = router.Admit(ctx, root.Lease(), nil)
				require.NoError(t, err)
				var found bool
				launchReceipt, found, err = store.Integrations().ClaimIntegrationInbox(
					ctx, integrationstore.ClaimIntegrationInboxInput{
						ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
					},
				)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, integrationstore.IntegrationInboxSourceChoice, launchReceipt.Source)
				require.Empty(t, launchReceipt.Plan, "exercise the accepted-menu handoff before any launch plan exists")
			} else {
				plan, err := freezeTestIntegrationEvent(ctx, router, root.Lease(), &event)
				require.NoError(t, err)
				require.Len(t, plan.Recipients, 1)
			}
			reply := f.message
			reply.ID, reply.ChannelID, reply.Mentions, reply.Content = "501", "500", nil, "continue"
			receipt := capture(reply)
			_, err = consumer.Consume(ctx, receipt.Lease())
			require.ErrorIs(t, err, integrationstore.ErrIntegrationSelectionReserved)
			require.Empty(t, f.requests, "pending reservation is checked before provider I/O")
			latest, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Empty(t, latest.Plan, "a pending launch must prevent an empty frozen plan")
			launched, err := consumer.Consume(ctx, launchReceipt.Lease())
			require.NoError(t, err)
			require.Len(t, launched, 1)
			delivered, err := consumer.Consume(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Len(t, delivered, 1)
			require.Equal(t, launched[0].Launch.Agent.ID, delivered[0].Input.AgentInput.AgentID)
		})
	}
}

func TestIntegrationDiscordChannelSubscriptionReceivesRootMentionWithoutLauncher(t *testing.T) {
	for _, exactThread := range []bool{false, true} {
		t.Run(fmt.Sprintf("exact_thread=%t", exactThread), func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			integration, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
			require.NoError(t, err)
			require.JSONEq(t, `{}`, string(integration.Settings))
			f, provider := newDiscordInboxFixture(t)
			f.integrationSetup, provider.integrations = integration, store.Integrations()
			config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: Help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: ids.ProjectID, LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
				AgentConfigID: config.ID,
			})
			require.NoError(t, err)
			createTestIntegrationSubscription(
				t,
				store,
				integration,
				launched.Agent.ID,

				`{"channel_id":"300"}`,
			)
			if exactThread {
				createTestIntegrationSubscription(
					t,
					store,
					integration,
					launched.Agent.ID,

					`{"thread_id":"500"}`,
				)
			}
			router := NewIntegrationRouter(store.Execution(), store.Integrations())
			consumer := NewIntegrationInboxConsumer(router, store.Integrations(), nil,
				map[string]IntegrationInboxProvider{"discord": provider}, nil, testIntegrationLaunchWorkflow(router))
			capture := func(key string, message discord.Message) integrationstore.IntegrationInboxRecord {
				t.Helper()
				_, _, err := store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
					ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: key, Payload: discordInboxPayload(t, message),
				})
				require.NoError(t, err)
				receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
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
				f.requests = nil
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
					if mention {
						require.Equal(t, []string{"GET /api/v10/channels/500"}, f.requests)
					} else {
						require.Empty(t, f.requests, "a parent observer cannot route an ordinary thread reply")
					}
				}
			}
			f.requests = nil
			ordinary := f.message
			ordinary.ID, ordinary.Mentions, ordinary.Content = "503", nil, "ordinary channel message"
			ordinaryReceipt := capture(ordinary.ID, ordinary)
			results, err := consumer.Consume(ctx, ordinaryReceipt.Lease())
			require.NoError(t, err)
			require.Empty(t, results, "a channel subscriber still requires a mentioned starter")
			require.Empty(t, f.requests)
			ignored, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, ordinaryReceipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, ignored.State)
			require.JSONEq(t, `{"recipients":{}}`, string(ignored.Plan), "the empty route decision must be frozen")
			next := f.message
			next.ID = "600"
			receipt := capture("removed-after-freeze", next)
			expansion, err := provider.Expand(ctx, integration, receipt.Payload)
			require.NoError(t, err)
			plan, err := freezeTestIntegrationEvent(ctx, router, receipt.Lease(), expansion.Event)
			require.NoError(t, err)
			require.Len(t, plan.Recipients, 1)
			removeTestAgentSubscriptions(t, store, integration, launched.Agent.ID)
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
