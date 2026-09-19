//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppDiscordConsumerPreparesOnlyAuthorizedFrozenConversation(t *testing.T) {
	for _, scenario := range []string{"launch and reply", "no launcher", "revoked before preparation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, connectionID := appWorkerFixture(t)
			_, err := pool.Exec(
				ctx,
				`UPDATE integration_connections SET provider='discord',provider_tenant_id='11',provider_account_ref='22' WHERE id=$1`,
				connectionID,
			)
			require.NoError(t, err)
			connection, err := store.Integrations().GetIntegrationConnection(ctx, ids.ProjectID, connectionID)
			require.NoError(t, err)
			f, provider := newDiscordInboxFixture(t)
			f.connection = connection
			// Local provider fixture supplies decrypted credentials; durable project,
			// receipt, plan, connection and agent authority use the production stores.
			provider.connections = store.Integrations()
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
			connectionPublic, err := publicid.Encode(publicid.KindIntegrationConnection, connectionID)
			require.NoError(t, err)
			if scenario != "no launcher" {
				_, err = store.Integrations().
					CreateProjectApp(
						ctx,
						integrationstore.SaveProjectAppInput{
							OrgID:        ids.OrgID,
							ProjectID:    ids.ProjectID,
							Name:         "discord",
							DefinitionID: appdefinition.Discord,
							Enabled:      true,
							Settings: integrationstore.ProjectAppSettings{
								Resource: agentconfig.AgentConfigAppResourceSource{
									Definition: appdefinition.Discord,
									Connection: connectionPublic,
									Listener:   &appdefinition.Listener{Events: []string{"message"}},
								},
								Launcher: &integrationstore.AppLauncher{
									Trigger:   "mention",
									ScopeKind: "guild",
									ScopeRef:  "100",
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
							ProjectID:    ids.ProjectID,
							ConnectionID: connectionID,
							ReceiptKey:   key,
							Payload:      discordInboxPayload(t, message),
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
			// Observe real thread POST only after a nonempty plan is durable, before an
			// agent exists. Revoke between expansion and preparation via the second
			// identity request; the next pre-request authority check must stop the POST.
			identityReads := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/api/v10/users/@me" {
					identityReads++
					if scenario == "revoked before preparation" && identityReads == 2 {
						_, err := pool.Exec(
							ctx,
							`UPDATE integration_connections SET state='disabled',updated_at=now() WHERE id=$1`,
							connectionID,
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
				return
			}
			require.NoError(t, err)
			if scenario == "no launcher" {
				require.Zero(t, f.posts)
				return
			}
			require.Equal(t, 1, f.posts)
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
			require.Equal(t, 1, f.posts)
			// Exact selected thread delivers human steering. A sibling thread has no
			// listener and must not launch or create input even under the same parent.
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
			require.Equal(t, 1, f.posts)
		})
	}
}
