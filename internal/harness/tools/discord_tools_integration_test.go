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
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Name: "chat", AppType: appdefinition.DiscordThread,
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
	for _, scenario := range []string{"foreign-parent", "not-thread", "wrong-bot", "wrong-app", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "discord-scope", toolFixtureOptions{
				withDiscordApp: true, withToolContext: true,
			})
			posts := 0
			thread := map[string]any{"id": "555", "parent_id": "444", "guild_id": "333", "type": 11}
			switch scenario {
			case "foreign-parent":
				thread["parent_id"] = "999"
			case "not-thread":
				thread["type"] = 0
			}
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
				case "/v10/channels/555":
					writeToolTestJSON(w, thread)
				case "/v10/channels/555/messages":
					assert.Equal(t, http.MethodPost, r.Method)
					posts++
					var body struct {
						Content      string `json:"content"`
						Nonce        string `json:"nonce"`
						EnforceNonce bool   `json:"enforce_nonce"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, "hello", body.Content)
					assert.NotEmpty(t, body.Nonce)
					assert.True(t, body.EnforceNonce)
					writeToolTestJSON(w, map[string]any{
						"id": "666", "channel_id": "555", "nonce": body.Nonce,
						"author": map[string]any{"id": "222", "bot": true},
					})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			call := f.recordToolCall(t, ctx, "post", toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				`{"content":"hello"}`, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Empty(t, appToolSubscriptions(t, f), "sending must not create subscriptions")
			if scenario == "valid" {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
				require.Equal(t, 1, posts)
				body := toolResultMapFromTestParts(t, result.ContentParts)
				require.Equal(t, "444", body["channel_id"])
				require.Equal(t, "555", body["thread_id"])
				replay, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
				require.NoError(t, err)
				require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
				require.Equal(t, 1, posts, "completed replay does not repost")
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Zero(t, posts)
				require.Equal(t, "scope_mismatch", toolResultMapFromTestParts(t, result.ContentParts)["code"])
			}
		})
	}
}

func TestDiscordAppReadSavedThreadPagination(t *testing.T) {
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
		case "/v10/channels/555":
			writeToolTestJSON(w, map[string]any{"id": "555", "parent_id": "444", "guild_id": "333", "type": 11})
		case "/v10/channels/555/messages":
			reads++
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "777", r.URL.Query().Get("before"))
			assert.Equal(t, "1", r.URL.Query().Get("limit"))
			writeToolTestJSON(w, []any{map[string]any{
				"id": "666", "channel_id": "555", "content": "hello",
				"author": map[string]any{"id": "888"},
			}})
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	call := f.recordToolCall(t, ctx, "read", "app__chat__read", `{"before":"777","limit":1}`, f.Now)
	result, err := dispatchAsyncToolToTerminal(t, ctx,
		Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
	require.NoError(t, err)
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(result.ContentParts))
	require.Equal(t, 1, reads)
	require.Contains(t, string(result.ContentParts), "hello")
	require.Equal(t, "666", toolResultMapFromTestParts(t, result.ContentParts)["next_before"])
}
