//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createDiscordToolApp(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID uuid.UUID,
) integrationstore.ProjectAppRecord {
	t.Helper()
	secret, version, err := store.Secrets().
		CreateSecret(
			ctx,
			secretstore.CreateSecretInput{
				OrgID:          toolsTestOrgID,
				OwnerKind:      secretstore.SecretOwnerProject,
				OwnerProjectID: toolsTestProjectID,
				Name:           "discord-token",
				Material:       secrets.GenericMaterial{Value: "test-token"},
				Actor:          toolsTestUserPrincipal(userID),
			},
		)
	require.NoError(t, err)
	app, err := store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Name: "chat", DefinitionID: appdefinition.Discord,
	})
	require.NoError(t, err)
	app, err = store.Integrations().ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
		OrgID:                 toolsTestOrgID,
		ProjectID:             toolsTestProjectID,
		AppID:                 app.ID,
		ExpectedSetupRevision: app.SetupRevision,
		InstalledByUserID:     userID,
		Provider:              appdefinition.ProviderDiscord,
		ProviderTenantID:      "111",
		ProviderAccountRef:    "222",
		CredentialSecretID:    secret.ID,
		CredentialVersionID:   version.ID,
		ProviderConfig:        json.RawMessage(`{"public_key":"` + strings.Repeat("ab", 32) + `"}`),
	})
	require.NoError(t, err)
	return app
}

func TestDiscordToolScopeAndIdentityBeforePublication(t *testing.T) {
	for _, scenario := range []string{
		"foreign-parent",
		"foreign-guild",
		"not-thread",
		"wrong-bot",
		"wrong-app",
		"dm",
		"valid-without-guild",
		"new-thread-without-guild",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			opts := toolFixtureOptions{withDiscordApp: true}
			f := newIntegrationToolFixtureWithOptions(t, ctx, "discord-scope", opts)
			posts, threadPosts, reads := 0, 0, 0
			thread := map[string]any{"id": "555", "parent_id": "444", "guild_id": "333", "type": 11}
			switch scenario {
			case "foreign-parent":
				thread["parent_id"] = "999"
			case "foreign-guild":
				thread["guild_id"] = "999"
			case "not-thread":
				thread["type"] = 0
			}
			var nonce string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v10/users/@me":
					id := "222"
					if scenario == "wrong-bot" {
						id = "999"
					}
					writeToolTestJSON(w, map[string]any{"id": id, "bot": true})
				case "/v10/applications/@me":
					id := "111"
					if scenario == "wrong-app" {
						id = "999"
					}
					writeToolTestJSON(w, map[string]any{"id": id})
				case "/v10/channels/444":
					channel := map[string]any{"id": "444", "guild_id": "333", "type": 0}
					if scenario == "dm" {
						channel = map[string]any{"id": "444", "type": 1}
					}
					writeToolTestJSON(w, channel)
				case "/v10/channels/555":
					writeToolTestJSON(w, thread)
				case "/v10/channels/666":
					w.WriteHeader(http.StatusNotFound)
					writeToolTestJSON(w, map[string]any{"code": 10003})
				case "/v10/channels/444/messages/666":
					reads++
					writeToolTestJSON(
						w,
						map[string]any{
							"id":         "666",
							"channel_id": "444",
							"author":     map[string]any{"id": "222", "bot": true},
							"nonce":      nonce,
						},
					)
				case "/v10/channels/444/messages/666/threads":
					assert.Equal(t, http.MethodPost, r.Method)
					threadPosts++
					writeToolTestJSON(w, map[string]any{"id": "666", "parent_id": "444", "guild_id": "333", "type": 11})
				case "/v10/channels/444/messages", "/v10/channels/555/messages":
					assert.Equal(t, http.MethodPost, r.Method)
					posts++
					var body struct {
						Nonce string `json:"nonce"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					nonce = body.Nonce
					channel := "555"
					if scenario == "new-thread-without-guild" {
						channel = "444"
					}
					writeToolTestJSON(
						w,
						map[string]any{
							"id":         "666",
							"channel_id": channel,
							"author":     map[string]any{"id": "222", "bot": true},
							"nonce":      nonce,
						},
					)
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			input := `{"channel_id":"444","content":"hello","thread_id":"555","follow_replies":true}`
			if scenario == "foreign-guild" {
				input = `{"channel_id":"444","guild_id":"333","content":"hello","thread_id":"555","follow_replies":true}`
			}
			if scenario == "dm" || scenario == "new-thread-without-guild" {
				input = `{"channel_id":"444","content":"hello","follow_replies":true}`
			}
			call := f.recordToolCall(
				t,
				ctx,
				"post",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				input,
				f.Now,
			)
			turn := f.turn()
			turn.Tools[call.Name] = ToolSpec{
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}
			result, err := dispatchAsyncToolToTerminal(
				t,
				ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
				turn,
				call,
			)
			require.NoError(t, err)
			record, err := f.Store.Execution().
				GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			follows := len(appToolSubscriptions(t, f))
			if scenario == "valid-without-guild" || scenario == "new-thread-without-guild" {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
				require.Equal(t, 1, posts)
				require.Equal(t, 1, follows)
				if scenario == "new-thread-without-guild" {
					require.Equal(t, 1, threadPosts)
					require.Equal(t, "666", toolResultMapFromTestParts(t, result.ContentParts)["thread_id"])
				}
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Zero(t, posts)
				require.Zero(t, threadPosts)
				require.Zero(t, reads)
				require.Zero(t, follows)
				if scenario != "dm" {
					require.Equal(t, "scope_mismatch", toolResultMapFromTestParts(t, result.ContentParts)["code"])
				}
			}
		})
	}
}

func TestDiscordAppReadContextAndGuildVerification(t *testing.T) {
	for _, test := range []struct {
		name, args string
		allowed    bool
	}{
		{"omitted", `{"before":"777","limit":1}`, true},
		{"matching", `{"channel_id":"444","guild_id":"333","before":"777","limit":1}`, true},
		{"foreign-guild", `{"guild_id":"999","before":"777","limit":1}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "discord-read", toolFixtureOptions{
				withDiscordApp: true, withToolContext: true,
			})
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v10/users/@me":
					writeToolTestJSON(w, map[string]any{"id": "222", "bot": true})
				case "/v10/applications/@me":
					writeToolTestJSON(w, map[string]any{"id": "111"})
				case "/v10/channels/444":
					writeToolTestJSON(w, map[string]any{"id": "444", "guild_id": "333", "type": 0})
				case "/v10/channels/444/messages":
					reads++
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Equal(t, "777", r.URL.Query().Get("before"))
					assert.Equal(t, "1", r.URL.Query().Get("limit"))
					writeToolTestJSON(w, []any{map[string]any{
						"id": "666", "channel_id": "444", "content": "hello",
						"author": map[string]any{"id": "888"},
					}})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			call := f.recordToolCall(t, ctx, "read", "app__chat__read", test.args, f.Now)
			result, err := dispatchAsyncToolToTerminal(t, ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			if test.allowed {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(result.ContentParts))
				require.Equal(t, 1, reads)
				require.Contains(t, string(result.ContentParts), "hello")
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Equal(t, "scope_mismatch", toolResultMapFromTestParts(t, result.ContentParts)["code"])
				require.Zero(t, reads, "a supplied guild must be verified before reading messages")
			}
		})
	}
}
