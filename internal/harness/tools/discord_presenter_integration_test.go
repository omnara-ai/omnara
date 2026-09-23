//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/stretchr/testify/require"
)

func TestDiscordPresenterRechecksRotatedCredentialIdentity(t *testing.T) {
	for _, scenario := range []string{"wrong-bot", "wrong-app", "valid-then-rotate"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(
				t,
				ctx,
				"discord-presenter",
				toolFixtureOptions{withDiscordApp: true},
			)
			prepareInteractionPromptFixture(t, ctx, f)

			var rotated atomic.Bool
			rotate := func() {
				t.Helper()
				_, _, err := f.Store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
					OrgID:    toolsTestOrgID,
					SecretID: f.Install.CredentialSecretID,
					Material: secrets.GenericMaterial{Value: "rotated-token"},
					Actor:    toolsTestUserPrincipal(f.User.ID),
				})
				require.NoError(t, err)
				rotated.Store(true)
			}
			var posts, edits, identity atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v10/users/@me":
					identity.Add(1)
					id := "222"
					if rotated.Load() && scenario != "wrong-app" {
						id = "999"
					}
					writeToolTestJSON(w, map[string]any{"id": id, "bot": true})
				case "/v10/applications/@me":
					id := "111"
					if rotated.Load() && scenario == "wrong-app" {
						id = "999"
					}
					writeToolTestJSON(w, map[string]any{"id": id})
				case "/v10/channels/444":
					writeToolTestJSON(w, map[string]any{"id": "444", "guild_id": "333", "type": 0})
				case "/v10/channels/444/messages":
					posts.Add(1)
					var body struct {
						Nonce string `json:"nonce"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					writeToolTestJSON(
						w,
						map[string]any{
							"id":         "666",
							"channel_id": "444",
							"nonce":      body.Nonce,
							"author":     map[string]any{"id": "222", "bot": true},
						},
					)
				case "/v10/channels/444/messages/666":
					edits.Add(1)
					writeToolTestJSON(
						w,
						map[string]any{
							"id":         "666",
							"channel_id": "444",
							"author":     map[string]any{"id": "222", "bot": true},
						},
					)
				default:
					t.Errorf("unexpected Discord request %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			if scenario != "valid-then-rotate" {
				rotate()
			}
			call := f.recordToolCall(t, ctx, "discord-question", "ask_question",
				`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes"},{"label":"No"}]}]}`, f.Now)
			client := appProviderTestClient(server)
			executor := Executor{Store: f.Store, AppHTTPClient: client}
			_, err := dispatchToolAndDrainAsync(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			interaction := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID), "question")
			require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
			require.EqualValues(t, 1, identity.Load())
			if scenario != "valid-then-rotate" {
				require.Zero(t, posts.Load(), "wrong bot/application must be rejected before posting")
				require.Empty(t, interaction.PresentationReceipt, "dashboard interaction remains available")
				return
			}
			require.EqualValues(t, 1, posts.Load())
			require.NotEmpty(t, interaction.PresentationReceipt)
			rotate()
			presenter := apps.InteractionPresenter{Store: f.Store, HTTPClient: client}
			err = presenter.PostRuntimeMessage(ctx, toolsTestProjectID, f.Agent.ID, f.Lock.ID, "Runtime update")
			var apiErr *discord.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, discord.ScopeMismatch, apiErr.Code)
			_, err = f.Store.Execution().CancelAgent(
				ctx,
				executionstore.CancelAgentInput{ProjectID: toolsTestProjectID, AgentID: f.Agent.ID},
			)
			require.NoError(t, err)
			current, found, err := f.Store.Execution().GetAgentInteraction(
				ctx,
				toolsTestProjectID,
				f.Agent.ID,
				interaction.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			err = presenter.Dismiss(ctx, current)
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, discord.ScopeMismatch, apiErr.Code)
			require.EqualValues(t, 1, posts.Load(), "runtime message must not use a different bot")
			require.Zero(t, edits.Load(), "dismissal must not use a different bot")
		})
	}
}
