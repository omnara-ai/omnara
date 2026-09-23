//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSlackAppToolFixture(t *testing.T, label string) integrationToolFixture {
	t.Helper()
	return newIntegrationToolFixtureWithOptions(t, t.Context(), label, toolFixtureOptions{
		withSlackApp: true, withToolContext: true,
	})
}

func slackAppToolTurn(f integrationToolFixture) Turn {
	turn := f.turn()
	turn.Tools = map[string]ToolSpec{}
	for _, operation := range []string{toolcatalog.AppOperationPostMessage, toolcatalog.AppOperationRead} {
		name := toolcatalog.AppToolName("chat", operation)
		spec := f.AppTools[name]
		if spec.Permission.Mode == "" {
			spec.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
		}
		turn.Tools[name] = spec
	}
	return turn
}

func TestSlackAppSendDistinctCallsAndReplay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newSlackAppToolFixture(t, "slack-send")
	require.Empty(t, appToolSubscriptions(t, f))
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "C123", body["channel"])
		assert.Equal(t, "111.222", body["thread_ts"])
		posts++
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222." + strconv.Itoa(posts)})
	}))
	defer server.Close()
	calls := []model.ToolCall{
		{
			ID:    "first",
			Name:  toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
			Input: json.RawMessage(`{"text":"hello"}`),
		},
		{
			ID:    "second",
			Name:  toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
			Input: json.RawMessage(`{"text":"hello again"}`),
		},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
		require.NoError(t, err)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.Equal(t, "chat", body["app"])
		require.Equal(t, "111.222", body["thread_ts"])
	}
	require.Equal(t, 2, posts)
	require.Empty(t, appToolSubscriptions(t, f), "sending must not create subscriptions")
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), calls[0])
	require.NoError(t, err)
	require.Equal(t, "111.222", toolResultMapFromTestParts(t, result.ContentParts)["thread_ts"])
	require.Equal(t, 2, posts, "completed call replay does not repost")
}

func TestSlackAppSendSafeRetriesAndUncertainPublication(t *testing.T) {
	for _, scenario := range []string{
		"rate-limit",
		"send-only",
		"rate-limit-exhausted",
		"unknown-found",
		"unknown-missing",
		"revoked-during-retry",
		"config-removed-during-retry",
		"unrelated-config-during-retry",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, scenario, toolFixtureOptions{
				withSlackApp: true, withToolContext: true,
			})
			posts, reads := 0, 0
			var change *executionstore.ChangeAgentConfigInput
			if scenario == "config-removed-during-retry" || scenario == "unrelated-config-during-retry" {
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
					[]byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				if scenario == "config-removed-during-retry" {
					delete(source.Tools, toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage))
				} else {
					source.Instruction += " Be concise."
				}
				input := appToolConfigChangeInput(t, f, source)
				change = &input
			}
			var marker any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				switch r.URL.Path {
				case "/chat.postMessage":
					posts++
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					marker = body["metadata"]
					if scenario == "revoked-during-retry" {
						_, _, err := f.Store.Secrets().
							CreateSecretVersion(
								ctx,
								secretstore.CreateSecretVersionInput{
									OrgID:    toolsTestOrgID,
									SecretID: f.Install.CredentialSecretID,
									Actor:    toolsTestUserPrincipal(f.User.ID),
									Material: secrets.SlackAppCredentialsMaterial{
										AccessToken:   "rotated",
										ClientID:      "client-id",
										ClientSecret:  "client-secret",
										SigningSecret: "signing-secret",
									},
								},
							)
						assert.NoError(t, err)
					}
					if change != nil && posts == 1 {
						_, err := f.Store.Execution().ChangeAgentConfig(ctx, *change)
						assert.NoError(t, err)
					}
					if scenario == "rate-limit-exhausted" || scenario == "revoked-during-retry" ||
						((scenario == "rate-limit" || change != nil) && posts == 1) {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if scenario == "unknown-found" || scenario == "unknown-missing" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1"})
				case "/conversations.replies":
					reads++
					messages := []any{}
					if scenario == "unknown-found" {
						messages = append(
							messages,
							map[string]any{"channel": "C123", "ts": "222.1", "metadata": marker},
						)
					}
					writeToolTestJSON(w, map[string]any{"ok": true, "messages": messages})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			call := f.recordToolCall(
				t,
				ctx,
				"send",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				`{"text":"reply"}`,
				f.Now,
			)
			result, err := dispatchAsyncToolToTerminal(
				t,
				ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
				slackAppToolTurn(f),
				call,
			)
			require.NoError(t, err)
			record, err := f.Store.Execution().
				GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			require.Empty(t, appToolSubscriptions(t, f), "sending must not create subscriptions")
			switch scenario {
			case "rate-limit", "unrelated-config-during-retry":
				require.Equal(t, 2, posts)
				require.Equal(t, "222.1", body["message_ts"])
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
			case "send-only":
				require.Equal(t, 1, posts)
				require.Equal(t, "222.1", body["message_ts"])
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
			case "rate-limit-exhausted":
				require.Equal(t, 3, posts)
				require.Equal(t, "rate_limited", body["code"])
			case "unknown-found":
				require.Equal(t, 1, posts)
				require.Equal(t, 1, reads)
				require.Equal(t, "222.1", body["message_ts"])
			case "unknown-missing":
				require.Equal(t, 1, posts)
				require.Equal(t, 1, reads)
				require.Equal(t, "delivery_unknown", body["code"])
			case "revoked-during-retry", "config-removed-during-retry":
				require.Equal(t, 1, posts)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			}
		})
	}
}

func TestSlackAppTargetAloneDoesNotGrantSend(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "no-app-authority")
	require.NotZero(t, f.Target.ID)
	require.NotContains(t, f.AppTools, toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage))
	posts := 0
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { posts++; w.WriteHeader(http.StatusInternalServerError) },
		),
	)
	defer server.Close()
	call := f.recordToolCall(
		t,
		ctx,
		"no-app",
		toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
		`{"text":"hello"}`,
		f.Now,
	)
	_, err := dispatchAsyncToolToTerminal(
		t,
		ctx,
		Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
		slackAppToolTurn(f),
		call,
	)
	assert.NoError(t, err)
	assert.Zero(t, posts)
	record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
	assert.NoError(t, err)
	assert.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
	assert.Contains(t, string(record.ResultContentParts), "was not configured")
}

func TestSlackAppReadPagination(t *testing.T) {
	ctx := t.Context()
	f := newSlackAppToolFixture(t, "read-page")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		assert.Equal(t, "/conversations.replies", r.URL.Path)
		if !assert.NoError(t, r.ParseForm()) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, "C123", r.Form.Get("channel"))
		assert.Equal(t, "111.222", r.Form.Get("ts"))
		assert.Equal(t, "page-two", r.Form.Get("cursor"))
		assert.Equal(t, "10", r.Form.Get("limit"))
		writeToolTestJSON(
			w,
			map[string]any{
				"ok":                true,
				"messages":          []any{map[string]any{"ts": "222.1", "text": "message"}},
				"response_metadata": map[string]any{"next_cursor": "page-three"},
			},
		)
	}))
	defer server.Close()
	call := f.recordToolCall(
		t,
		ctx,
		"read",
		toolcatalog.AppToolName("chat", toolcatalog.AppOperationRead),
		`{"cursor":"page-two","limit":10}`,
		f.Now,
	)
	result, err := dispatchAsyncToolToTerminal(
		t,
		ctx,
		Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
		slackAppToolTurn(f),
		call,
	)
	require.NoError(t, err)
	require.Equal(t, "page-three", toolResultMapFromTestParts(t, result.ContentParts)["next_cursor"])
}

func TestSlackAppLegacyContextsPreserveReadAndPost(t *testing.T) {
	for _, test := range []struct{ kind, channel string }{{"dm", "D123"}, {"channel", "C123"}} {
		t.Run(test.kind, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "legacy-context", toolFixtureOptions{
				withSlackApp: true, withToolContext: true,
				toolContextAddress: integrationstore.ConversationAddress{Kind: test.kind, Ref: test.channel},
			})
			reads, posts := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				switch r.URL.Path {
				case "/conversations.history":
					reads++
					assert.NoError(t, r.ParseForm())
					assert.Equal(t, test.channel, r.Form.Get("channel"))
					assert.Empty(t, r.Form.Get("ts"))
					writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{map[string]any{"ts": "222.1", "text": "hello"}}})
				case "/chat.postMessage":
					posts++
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, test.channel, body["channel"])
					assert.NotContains(t, body, "thread_ts", "legacy contexts keep posting to their saved conversation")
					assert.Equal(t, "hello", body["text"])
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": test.channel, "ts": "222.2"})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			calls := []model.ToolCall{
				{ID: "read", Name: "app__chat__read", Input: json.RawMessage(`{}`)},
				{ID: "post", Name: "app__chat__post_message", Input: json.RawMessage(`{"text":"hello"}`)},
			}
			f.recordToolCalls(t, ctx, calls, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			for _, call := range calls {
				_, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(record.ResultContentParts))
			}
			require.Equal(t, 1, reads)
			require.Equal(t, 1, posts)
			require.Empty(t, appToolSubscriptions(t, f))
		})
	}
}

func serveSlackToolIdentity(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/auth.test" {
		return false
	}
	writeToolTestJSON(w, map[string]any{"ok": true, "team_id": "T123", "user_id": "B123", "bot_id": "BOT123"})
	return true
}

func TestSlackAppRotatedTokenKeepsVerifiedIdentity(t *testing.T) {
	t.Parallel()
	for _, identity := range []string{"same", "different-workspace", "different-bot"} {
		for _, operation := range []string{"read", "post_message", "upload"} {
			if identity == "same" && operation == "upload" {
				continue
			}
			t.Run(identity+"/"+operation, func(t *testing.T) {
				t.Parallel()
				ctx := t.Context()
				f := newSlackAppToolFixture(t, "rotated-identity")
				_, _, err := f.Store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
					OrgID: toolsTestOrgID, SecretID: f.Install.CredentialSecretID,
					Actor: toolsTestUserPrincipal(f.User.ID),
					Material: secrets.SlackAppCredentialsMaterial{
						AccessToken: "rotated-token", ClientID: "client-id",
						ClientSecret: "client-secret", SigningSecret: "signing-secret",
					},
				})
				require.NoError(t, err)
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "Bearer rotated-token", r.Header.Get("Authorization"))
					if r.URL.Path == "/auth.test" {
						team, user := "T123", "B123"
						if identity == "different-workspace" {
							team = "T456"
						}
						if identity == "different-bot" {
							user = "B456"
						}
						writeToolTestJSON(w, map[string]any{
							"ok": true, "team_id": team, "user_id": user, "bot_id": "BOT123",
						})
						return
					}
					requests++
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1", "messages": []any{}})
				}))
				defer server.Close()
				name, input := operation, `{}`
				if operation != "read" {
					name, input = "post_message", `{"text":"hello"}`
				}
				if operation == "upload" {
					input = `{"text":"report","artifact_ids":["art_aeaqcaibaeaqcaibaeaqcaibae"]}`
				}
				call := f.recordToolCall(t, ctx, "rotated", toolcatalog.AppToolName("chat", name), input, f.Now)
				_, err = dispatchAsyncToolToTerminal(t, ctx,
					Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
					slackAppToolTurn(f), call)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				if identity == "same" {
					require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
					require.Equal(t, 1, requests)
				} else {
					require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
					require.Zero(t, requests, "mismatched token must not read, post or begin an upload")
					require.Contains(t, string(record.ResultContentParts), "workspace and bot identity")
				}
			})
		}
	}
}
