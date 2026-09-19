//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSlackAppToolFixture(t *testing.T, label string) integrationToolFixture {
	t.Helper()
	return newIntegrationToolFixtureWithOptions(t, t.Context(), label, toolFixtureOptions{withSlackApp: true})
}

func slackAppToolTurn(f integrationToolFixture) Turn {
	turn := f.turn()
	turn.Tools = map[string]ToolSpec{
		toolcatalog.ToolNameSlackPostMessage: {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		},
		toolcatalog.ToolNameSlackRead: {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		},
	}
	return turn
}

func TestSlackAppSendDistinctCallsFollowAndReplay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newSlackAppToolFixture(t, "slack-follow")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "C123", body["channel"])
		posts++
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222." + strconv.Itoa(posts)})
	}))
	defer server.Close()
	calls := []model.ToolCall{
		{
			ID:    "first",
			Name:  toolcatalog.ToolNameSlackPostMessage,
			Input: json.RawMessage(`{"text":"hello","follow_replies":true}`),
		},
		{
			ID:    "second",
			Name:  toolcatalog.ToolNameSlackPostMessage,
			Input: json.RawMessage(`{"text":"hello","thread_ts":"222.1","follow_replies":true}`),
		},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
		require.NoError(t, err)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.Equal(t, "chat", body["resource"])
		require.Equal(t, "222.1", body["thread_ts"])
	}
	require.Equal(t, 2, posts)
	var listeners int
	require.NoError(
		t,
		f.Pool.QueryRow(
			ctx,
			`SELECT count(*) FROM agent_listeners WHERE agent_id=$1 AND resource_key='chat' AND scope_ref='C123:222.1' AND active`,
			f.Agent.ID,
		).
			Scan(
				&listeners,
			),
	)
	require.Equal(t, 1, listeners, "repeated posts follow the same thread once")
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), calls[0])
	require.NoError(t, err)
	require.Equal(t, "222.1", toolResultMapFromTestParts(t, result.ContentParts)["thread_ts"])
	require.Equal(t, 2, posts, "completed call replay does not repost")
}

func TestSlackAppSendSafeRetriesAndUncertainPublication(t *testing.T) {
	for _, scenario := range []string{
		"rate-limit",
		"rate-limit-exhausted",
		"unknown-found",
		"unknown-missing",
		"revoked-during-retry",
		"config-removed-during-retry",
		"unrelated-config-during-retry",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newSlackAppToolFixture(t, scenario)
			posts, reads := 0, 0
			var change *executionstore.ChangeAgentConfigInput
			if scenario == "config-removed-during-retry" || scenario == "unrelated-config-during-retry" {
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
					[]byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				if scenario == "config-removed-during-retry" {
					delete(source.AppResources, "chat")
				} else {
					source.Instruction += " Be concise."
				}
				input := appToolConfigChangeInput(t, f, source)
				change = &input
			}
			var marker any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				toolcatalog.ToolNameSlackPostMessage,
				`{"text":"reply","thread_ts":"111.222"}`,
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
			switch scenario {
			case "rate-limit", "unrelated-config-during-retry":
				require.Equal(t, 2, posts)
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
	posts := 0
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) { posts++; w.WriteHeader(http.StatusInternalServerError) },
		),
	)
	defer server.Close()
	call := f.recordToolCall(t, ctx, "no-app", toolcatalog.ToolNameSlackPostMessage, `{"text":"hello"}`, f.Now)
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
}

func TestSlackAppReadPagination(t *testing.T) {
	ctx := t.Context()
	f := newSlackAppToolFixture(t, "read-page")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		toolcatalog.ToolNameSlackRead,
		`{"thread_ts":"111.222","cursor":"page-two","limit":10}`,
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

// DM replies are ordinary conversation messages, including when Slack includes
// thread_ts. The outbound follow must match the incoming normalizer's address.
func TestSlackAppDMPostFollowsIncomingReply(t *testing.T) {
	for _, threaded := range []bool{false, true} {
		t.Run(strconv.FormatBool(threaded), func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(
				t,
				ctx,
				"dm-follow",
				toolFixtureOptions{withSlackApp: true, slackChannel: "D123"},
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/chat.postMessage", r.URL.Path)
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "D123", "ts": "222.1"})
			}))
			defer server.Close()
			input := `{"text":"question","follow_replies":true}`
			if threaded {
				input = `{"text":"question","thread_ts":"111.2","follow_replies":true}`
			}
			call := f.recordToolCall(t, ctx, "dm-send", toolcatalog.ToolNameSlackPostMessage, input, f.Now)
			result, err := dispatchAsyncToolToTerminal(
				t,
				ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
				slackAppToolTurn(f),
				call,
			)
			require.NoError(t, err)
			require.Equal(t, "D123", toolResultMapFromTestParts(t, result.ContentParts)["channel_id"])
			payload := json.RawMessage(
				`{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"EvReply","authorizations":[{"team_id":"T123","user_id":"B123","is_bot":true}],"event":{"type":"message","channel":"D123","channel_type":"im","user":"U123","text":"here is the answer","ts":"223.1","thread_ts":"111.2"}}`,
			)
			event, ok, err := integration.NormalizeSlackAppEvent(f.Install, payload)
			require.NoError(t, err)
			require.True(t, ok)
			_, _, err = f.Store.Integrations().
				AcceptIntegrationReceipt(
					ctx,
					integrationstore.VerifiedIntegrationReceipt{
						ProjectID:    f.Agent.ProjectID,
						ConnectionID: f.Install.ID,
						ReceiptKey:   "dm-reply",
						Payload:      payload,
					},
				)
			require.NoError(t, err)
			receipt, ok, err := f.Store.Integrations().
				ClaimIntegrationInbox(
					ctx,
					integrationstore.ClaimIntegrationInboxInput{
						ProjectID:     f.Agent.ProjectID,
						ConnectionID:  f.Install.ID,
						LeaseDuration: time.Minute,
					},
				)
			require.NoError(t, err)
			require.True(t, ok)
			router := integration.NewAppRouter(f.Store.Execution(), f.Store.Integrations())
			plan, err := router.Freeze(ctx, receipt.Lease(), []integration.AppEvent{event})
			require.NoError(t, err)
			require.Len(t, plan, 1, "the followed DM selects the existing agent")
			for _, slot := range plan {
				require.Equal(t, f.Agent.ID, slot.AgentID)
				require.Nil(t, slot.Launch)
			}
			accepted, err := router.Admit(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Len(t, accepted, 1)
			require.NotNil(t, accepted[0].Input)
			require.Contains(t, string(accepted[0].Input.ContentBlocks), "here is the answer")
			var inputs int
			require.NoError(
				t,
				f.Pool.QueryRow(
					ctx,
					`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND id=$2`,
					f.Agent.ID,
					accepted[0].Input.AgentInput.ID,
				).
					Scan(
						&inputs,
					),
			)
			require.Equal(t, 1, inputs)
		})
	}
}
