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
	return newIntegrationToolFixtureWithOptions(t, t.Context(), label, toolFixtureOptions{withSlackApp: true})
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

func TestSlackAppSendDistinctCallsFollowAndReplay(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newSlackAppToolFixture(t, "slack-follow")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
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
			Name:  toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
			Input: json.RawMessage(`{"text":"hello","follow_replies":true}`),
		},
		{
			ID:    "second",
			Name:  toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
			Input: json.RawMessage(`{"text":"hello","thread_ts":"222.1","follow_replies":true}`),
		},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
		require.NoError(t, err)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.Equal(t, "chat", body["app"])
		require.Equal(t, "222.1", body["thread_ts"])
	}
	require.Equal(t, 2, posts)
	var listeners int
	require.NoError(
		t,
		f.Pool.QueryRow(
			ctx,
			`SELECT count(*) FROM agent_listeners
WHERE agent_id=$1 AND listener_key='chat__thread_messages' AND scope_ref='C123:222.1' AND active`,
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
		"without-listener",
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
				withSlackApp: true, withoutAppListener: scenario == "without-listener",
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
			case "without-listener":
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
				if serveSlackToolIdentity(w, r) {
					return
				}
				assert.Equal(t, "/chat.postMessage", r.URL.Path)
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "D123", "ts": "222.1"})
			}))
			defer server.Close()
			input := `{"text":"question","follow_replies":true}`
			if threaded {
				input = `{"text":"question","thread_ts":"111.2","follow_replies":true}`
			}
			call := f.recordToolCall(
				t,
				ctx,
				"dm-send",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				input,
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
						ProjectID:  f.Agent.ProjectID,
						AppID:      f.Install.ID,
						ReceiptKey: "dm-reply",
						Payload:    payload,
					},
				)
			require.NoError(t, err)
			receipt, ok, err := f.Store.Integrations().
				ClaimIntegrationInbox(
					ctx,
					integrationstore.ClaimIntegrationInboxInput{
						ProjectID:     f.Agent.ProjectID,
						AppID:         f.Install.ID,
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

func TestSlackRuntimeFollowBelongsToListener(t *testing.T) {
	for _, scenario := range []string{
		"tool-removed", "tool-denied", "tool-reconfigured", "initial-conversations-changed", "listener-removed",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newSlackAppToolFixture(t, "follow-owner")
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				posts++
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1"})
			}))
			defer server.Close()
			call := f.recordToolCall(
				t,
				ctx,
				"follow",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				`{"text":"hello","follow_replies":true}`,
				f.Now,
			)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
			require.NoError(t, err)
			require.Equal(t, "222.1", toolResultMapFromTestParts(t, result.ContentParts)["thread_ts"])
			var listenerID string
			require.NoError(t, f.Pool.QueryRow(ctx, `SELECT id FROM agent_listeners
WHERE agent_id=$1 AND app_id=$2 AND listener_key='chat__thread_messages'
  AND scope_ref='C123:222.1' AND origin='runtime' AND active`, f.Agent.ID, f.Install.ID).Scan(&listenerID))
			source, err := agentconfig.ParseSource(
				agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
				[]byte(f.AgentConfig.Source),
			)
			require.NoError(t, err)
			entry := source.Tools[call.Name]
			switch scenario {
			case "tool-removed":
				delete(source.Tools, call.Name)
			case "tool-denied":
				permission := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				entry.Permission = &permission
				source.Tools[call.Name] = entry
			case "tool-reconfigured":
				entry.Config["channel_id"] = "C999"
				source.Tools[call.Name] = entry
			case "initial-conversations-changed":
				source.Listeners["chat__thread_messages"] = agentconfig.AgentConfigAppCapabilitySource{Config: map[string]any{
					"conversations": []any{map[string]any{"channel_id": "C999", "thread_ts": "333.4"}},
					"events":        []string{"message"},
				}}
			case "listener-removed":
				delete(source.Listeners, "chat__thread_messages")
			}
			changed, err := f.Store.Execution().ChangeAgentConfig(ctx, appToolConfigChangeInput(t, f, source))
			require.NoError(t, err)
			var active bool
			var configID string
			var events []string
			require.NoError(
				t,
				f.Pool.QueryRow(ctx, `SELECT active, source_config_id, events FROM agent_listeners WHERE id=$1`, listenerID).
					Scan(&active, &configID, &events),
			)
			require.Equal(t, scenario != "listener-removed", active)
			if active {
				require.Equal(t, changed.AgentConfig.ID.String(), configID)
				require.Equal(t, []string{"message"}, events)
			}
			if scenario == "listener-removed" {
				// Restoring a listener does not resurrect runtime follows revoked earlier.
				source.Listeners["chat__thread_messages"] = agentconfig.AgentConfigAppCapabilitySource{}
				changeAppToolConfig(t, ctx, f, source)
			}
			_, err = dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
			require.NoError(t, err)
			require.Equal(t, 1, posts, "completed replay must never send again")
			require.NoError(t, f.Pool.QueryRow(ctx, `SELECT active FROM agent_listeners WHERE id=$1`, listenerID).Scan(&active))
			require.Equal(t, scenario != "listener-removed", active, "completed replay cannot restore a revoked follow")
		})
	}
}

func TestSlackConfirmedSendDoesNotRepeatForFollowPersistence(t *testing.T) {
	for _, scenario := range []string{
		"transient-write-failure", "listener-removed-during-send",
		"sender-removed", "sender-denied", "sender-disabled", "sender-reconfigured",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newSlackAppToolFixture(t, "follow-completion")
			if scenario == "transient-write-failure" {
				_, err := f.Pool.Exec(ctx, `CREATE SEQUENCE follow_write_attempts;
CREATE FUNCTION fail_first_follow_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF nextval('follow_write_attempts') = 1 THEN
    RAISE EXCEPTION 'injected follow persistence failure' USING ERRCODE = '40001';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER fail_first_follow_write BEFORE INSERT ON agent_listeners
FOR EACH ROW WHEN (NEW.origin = 'runtime') EXECUTE FUNCTION fail_first_follow_write();`)
				require.NoError(t, err)
			}
			source, err := agentconfig.ParseSource(
				agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
				[]byte(f.AgentConfig.Source),
			)
			require.NoError(t, err)
			name := toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage)
			entry := source.Tools[name]
			switch scenario {
			case "listener-removed-during-send":
				delete(source.Listeners, "chat__thread_messages")
			case "sender-removed":
				delete(source.Tools, name)
			case "sender-denied":
				permission := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
				entry.Permission = &permission
				source.Tools[name] = entry
			case "sender-disabled":
				disabled := false
				entry.Enabled = &disabled
				source.Tools[name] = entry
			case "sender-reconfigured":
				entry.Config["channel_id"] = "C999"
				source.Tools[name] = entry
			}
			change := appToolConfigChangeInput(t, f, source)
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				posts++
				if scenario != "transient-write-failure" {
					_, err := f.Store.Execution().ChangeAgentConfig(ctx, change)
					assert.NoError(t, err)
				}
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1"})
			}))
			defer server.Close()
			call := f.recordToolCall(
				t,
				ctx,
				"follow",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				`{"text":"hello","follow_replies":true}`,
				f.Now,
			)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			var follows int
			require.NoError(
				t,
				f.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_listeners
WHERE agent_id=$1 AND origin='runtime' AND active`, f.Agent.ID).
					Scan(&follows),
			)
			if scenario != "listener-removed-during-send" {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
				require.Equal(t, "222.1", body["message_ts"])
				require.Equal(t, 1, follows)
				if scenario == "transient-write-failure" {
					var attempts int
					require.NoError(t, f.Pool.QueryRow(ctx, `SELECT last_value FROM follow_write_attempts`).Scan(&attempts))
					require.Equal(t, 2, attempts, "retry persistence after the confirmed send")
				}
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Equal(t, "follow_registration_failed", body["code"])
				require.Contains(t, string(record.ResultContentParts), "222.1", "preserve the confirmed provider receipt")
				require.Zero(t, follows)
			}
			_, err = dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(f), call)
			require.NoError(t, err)
			require.Equal(t, 1, posts, "persistence retry and replay must not repeat a confirmed send")
		})
	}
}

// The fixture's saved Slack identity is T123/B123. Individual identity-rotation
// tests supply their own auth.test responses instead of using this handler.
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
				continue // Successful uploads are covered by the artifact journey.
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
