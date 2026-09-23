//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppToolApprovalShowsResolvedDestination(t *testing.T) {
	for _, tt := range []struct{ provider, operation, input, destination string }{
		{"slack", "post_message", `{"text":"Review ready"}`, `{"channel_id":"C123","thread_ts":"111.222"}`},
		{"discord", "post_message", `{"content":"Review ready"}`, `{"channel_id":"444","thread_id":"555"}`},
		{"github", "discussion_comment", `{"body":"Review ready"}`, `{"repository_id":123,"pull_request":7}`},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "approval-destination", toolFixtureOptions{
				withSlackApp: tt.provider == "slack", withDiscordApp: tt.provider == "discord",
				withGitHubApp: tt.provider == "github", withToolContext: true,
				slackPermission: toolpermission.ModeAlwaysAsk, githubPermission: toolpermission.ModeAlwaysAsk,
			})
			call := f.recordPendingToolCall(t, ctx, "approval", toolcatalog.AppToolName("chat", tt.operation), tt.input, f.Now)
			var schema struct {
				Properties map[string]any `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(f.AppTools[call.Name].InputSchema, &schema))
			var destination map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.destination), &destination))
			for field := range destination {
				require.NotContains(t, schema.Properties, field, "destinations are assigned by the app")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			require.NoError(t, executor.PrepareToolCallPermission(ctx, f.turn(), call))
			interaction := integrationToolInteraction(t, ctx, f, f.toolCallID(t, ctx, call.ID),
				executionstore.AgentInteractionKindPermission)
			form, err := interaction.Form()
			require.NoError(t, err)
			require.Len(t, form.Context, 1)
			var summary struct {
				Destination json.RawMessage `json:"destination"`
				Arguments   json.RawMessage `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal([]byte(form.Context[0].Value), &summary))
			require.JSONEq(t, tt.input, string(summary.Arguments))
			require.JSONEq(t, tt.destination, string(summary.Destination), "approvers see the complete resolved address")
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)
			require.Zero(t, requests.Load(), "requesting approval must not contact the provider")
		})
	}
}

func TestAppToolApprovalFailsInvalidScopeBeforeProviderIO(t *testing.T) {
	for _, test := range []struct {
		name, provider, operation, args, message string
		context, malformed                       bool
	}{
		{"slack-destination", "slack", "post_message",
			`{"channel_id":"C999","text":"hello"}`, "additional properties", true, true},
		{"discord-destination", "discord", "post_message",
			`{"channel_id":"999","content":"hello"}`, "additional properties", true, true},
		{"github-destination", "github", "discussion_comment",
			`{"pull_request":8,"body":"hello"}`, "additional properties", true, true},
		{"slack-missing", "slack", "post_message", `{"text":"hello"}`, "no assigned conversation", false, false},
		{"discord-missing", "discord", "post_message", `{"content":"hello"}`, "no assigned conversation", false, false},
		{"github-missing", "github", "discussion_comment", `{"body":"hello"}`, "no assigned conversation", false, false},
		{"disconnected", "slack", "post_message", `{"text":"hello"}`, "app is unavailable", true, false},
		{"removed-tool", "slack", "post_message",
			`{"text":"hello"}`, "unavailable in the original or current config", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "approval-scope", toolFixtureOptions{
				withSlackApp: test.provider == "slack", withDiscordApp: test.provider == "discord",
				withGitHubApp: test.provider == "github", withToolContext: test.context,
				slackPermission: toolpermission.ModeAlwaysAsk, githubPermission: toolpermission.ModeAlwaysAsk,
			})
			call := f.recordPendingToolCall(t, ctx, "invalid",
				toolcatalog.AppToolName("chat", test.operation), test.args, f.Now)
			if test.name == "disconnected" {
				_, err := f.Store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
					ProjectID: toolsTestProjectID, AppID: f.Install.ID, ExpectedSetupRevision: &f.Install.SetupRevision,
				})
				require.NoError(t, err)
			}
			if test.name == "removed-tool" {
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				delete(source.Tools, call.Name)
				changeAppToolConfig(t, ctx, f, source)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			require.NoError(t, executor.PrepareToolCallPermission(ctx, f.turn(), call))
			id := f.toolCallID(t, ctx, call.ID)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, id)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			body := toolResultMapFromTestParts(t, record.ResultContentParts)
			if test.malformed {
				require.Equal(t, "malformed", body["error_code"])
				require.Contains(t, body["error"], test.message)
			} else {
				require.Equal(t, "app_tool_failed", body["code"])
				require.Contains(t, body["message"], test.message)
			}
			require.NoError(t, executor.PrepareToolCallPermission(ctx, f.turn(), call), "terminal failure replay must finish")
			_, found, err := f.Store.Execution().GetAgentInteractionByToolCallKind(ctx, toolsTestProjectID,
				f.Agent.ID, id, executionstore.AgentInteractionKindPermission)
			require.NoError(t, err)
			require.False(t, found, "an invalid destination must not reach an approver")
			require.Zero(t, requests.Load())
		})
	}
}

func TestAppToolApprovalPreservesInfrastructureErrors(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-lock", true: "canceled"}[canceled], func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "approval-infrastructure", toolFixtureOptions{
				withSlackApp: true, withToolContext: true, slackPermission: toolpermission.ModeAlwaysAsk,
			})
			call := f.recordPendingToolCall(
				t, ctx, "post", toolcatalog.AppToolName("chat", "post_message"), `{"text":"hello"}`, f.Now,
			)
			turn := f.turn()
			operationCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			wantErr := storeerr.ErrRuntimeLockInactive
			if canceled {
				cancel()
				wantErr = context.Canceled
			} else {
				turn.RuntimeLockID = uuid.New()
			}
			executor := Executor{Store: f.Store}
			_, err := appPermissionChallenge(operationCtx, executor, turn, call, permissionModeContext{})
			require.ErrorIs(t, err, wantErr)
			var preparationErr *toolCallPreparationError
			require.False(t, errors.As(err, &preparationErr), "infrastructure errors must not become model failures")
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateAwaitingAuthorization, record.State)
		})
	}
}

func TestAppToolApprovalDoesNotBypassCurrentConfig(t *testing.T) {
	for _, scenario := range []string{
		"removed",
		"app-disconnected",
		"tool-disabled",
		"deny",
		"rebound",
		"unrelated",
		"added-handler",
		"subscription-detached",
	} {
		for _, changeBeforeApproval := range []bool{false, true} {
			t.Run(
				scenario+map[bool]string{false: "/after-approval", true: "/before-approval"}[changeBeforeApproval],
				func(t *testing.T) {
					ctx := t.Context()
					f := newIntegrationToolFixtureWithOptions(
						t,
						ctx,
						"scope-approval",
						toolFixtureOptions{withSlackApp: true, withToolContext: true, slackPermission: toolpermission.ModeAlwaysAsk},
					)
					call := f.recordPendingToolCall(
						t,
						ctx,
						"post",
						toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
						`{"text":"hello"}`,
						f.Now,
					)
					var subscription integrationstore.AppSubscriptionRecord
					if scenario == "subscription-detached" {
						subscription = attachToolSubscription(t, f, "thread_messages", `{"channel_id":"C123","thread_ts":"111.222"}`, nil)
					}
					turn := slackAppToolTurn(f)
					require.Equal(t, toolpermission.ModeAlwaysAsk, turn.Tools[call.Name].Permission.Mode)
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
						writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1"})
					}))
					defer server.Close()
					executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
					require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
					permission := integrationToolInteraction(
						t,
						ctx,
						f,
						f.toolCallID(t, ctx, call.ID),
						executionstore.AgentInteractionKindPermission,
					)
					change := func() {
						source, err := agentconfig.ParseSource(
							agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
							[]byte(f.AgentConfig.Source),
						)
						require.NoError(t, err)
						entry := source.Tools[call.Name]
						switch scenario {
						case "removed":
							delete(source.Tools, call.Name)
						case "app-disconnected":
							_, err := f.Store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
								ProjectID: toolsTestProjectID, AppID: f.Install.ID, ExpectedSetupRevision: &f.Install.SetupRevision,
							})
							require.NoError(t, err)
							return
						case "tool-disabled":
							disabled := false
							entry.Enabled = &disabled
							source.Tools[call.Name] = entry
						case "deny":
							permission := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
							entry.Permission = &permission
							source.Tools[call.Name] = entry
						case "rebound":
							require.NoError(
								t,
								f.Store.Integrations().DeleteProjectApp(ctx, toolsTestOrgID, toolsTestProjectID, f.Install.ID),
							)
							replacement := createSlackToolApp(t, ctx, f.Store, f.User.ID, "chat", "replacement")
							require.NotEqual(t, f.Install.ID, replacement.ID)
						case "unrelated":
							source.Instruction += " Be concise."
						case "added-handler":
							source.InteractionHandlers = map[string]agentconfig.AgentConfigAppCapabilitySource{
								"chat": {},
							}
						case "subscription-detached":
							require.NoError(t, f.Store.Integrations().DeleteAppSubscription(
								ctx, toolsTestOrgID, toolsTestProjectID, f.Install.ID, subscription.ID,
							))
							return
						}

						changeAppToolConfig(t, ctx, f, source)
					}
					if changeBeforeApproval {
						change()
					}
					actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(f.User.ID))
					require.NoError(t, err)
					_, err = f.Store.Execution().
						ResolveAgentInteraction(
							ctx,
							executionstore.ResolveAgentInteractionInput{
								ProjectID: f.Agent.ProjectID,
								AgentID:   f.Agent.ID,
								ID:        permission.ID,
								Actor:     actor,
								Resolution: interactionform.Resolution{
									Answers: []interactionform.Answer{
										{OptionIndices: []int{toolpermission.AllowOptionIndex}},
									},
								},
							},
						)
					require.NoError(t, err)
					require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
					if !changeBeforeApproval {
						change()
					}
					_, err = dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
					require.NoError(t, err)
					record, err := f.Store.Execution().
						GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
					require.NoError(t, err)
					allowed := scenario == "unrelated" || scenario == "added-handler" || scenario == "subscription-detached"
					wantPosts, wantOutcome := 0, executionstore.ToolResultOutcomeFailed
					if allowed {
						wantPosts, wantOutcome = 1, executionstore.ToolResultOutcomeSucceeded
					}
					require.Equal(t, wantPosts, posts)
					require.Equal(t, wantOutcome, record.Outcome)
					listed := f
					if scenario == "rebound" {
						listed.Install, err = f.Store.Integrations().GetProjectAppByName(ctx, toolsTestProjectID, "chat")
						require.NoError(t, err)
					}
					require.Empty(t, appToolSubscriptions(t, listed), "approval and sending must not create subscriptions")
				},
			)
		}
	}
}

func changeAppToolConfig(
	t *testing.T,
	ctx context.Context,
	f integrationToolFixture,
	source agentconfig.AgentConfigSource,
) {
	t.Helper()
	_, err := f.Store.Execution().ChangeAgentConfig(ctx, appToolConfigChangeInput(t, f, source))
	require.NoError(t, err)
}

func appToolConfigChangeInput(
	t *testing.T,
	f integrationToolFixture,
	source agentconfig.AgentConfigSource,
) executionstore.ChangeAgentConfigInput {
	t.Helper()
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatJSON, raw, agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: f.AgentConfig.ConfiguredModelID}, nil
		},
		ResolveAppName: func(name string) (agentconfig.AppResolution, error) {
			return resolveToolsAppName(t.Context(), f.Store, name)
		},
	})
	require.NoError(t, err)
	return executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               f.Agent.ProjectID,
			Source:                  string(raw),
			SourceFormat:            "json",
			ConfiguredModelID:       f.AgentConfig.ConfiguredModelID,
			CompiledDefinition:      compiled.CanonicalJSON,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        f.Agent.ID,
		ActorType:      "user",
		ActorID:        f.User.ID,
		IdempotencyKey: uuid.NewString(),
	}
}

func TestAsyncFailurePreservesProviderEvidenceOnTimeout(t *testing.T) {
	for _, code := range []string{"delivery_unknown", ""} {
		t.Run(code, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "provider-timeout")
			call := startOverflowTestCall(
				t,
				&f,
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				`{"text":"hi"}`,
			)
			var content toolResultContent
			if code != "" {
				var err error
				content, err = structuredToolResultContent(map[string]any{"code": code, "message_id": "222.1"})
				require.NoError(t, err)
			}
			executor := Executor{Store: f.Store}
			require.NoError(
				t,
				executor.completeAsyncToolFailure(ctx, f.turn(), call.ToolCallID, content, context.DeadlineExceeded),
			)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, call.ToolCallID)
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, record.ResultContentParts)
			if code == "" {
				require.Equal(t, "async_tool_interrupted", body["code"])
			} else {
				require.Equal(t, code, body["code"])
				require.Equal(t, "222.1", body["message_id"])
			}
		})
	}
}

func TestAppToolsUseSavedConversation(t *testing.T) {
	for _, provider := range []string{"slack", "discord"} {
		for _, state := range []string{"active", "retired"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				ctx := t.Context()
				f := newIntegrationToolFixtureWithOptions(t, ctx, "saved-context", toolFixtureOptions{
					withSlackApp: provider == "slack", withDiscordApp: provider == "discord", withToolContext: true,
				})
				input := `{"text":"hello"}`
				if provider == "discord" {
					input = `{"content":"hello"}`
				}
				if state == "retired" {
					_, err := f.Pool.Exec(ctx, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, f.Target.ID)
					require.NoError(t, err)
				}
				call := f.recordToolCall(t, ctx, "send", toolcatalog.AppToolName("chat", "post_message"), input, f.Now)
				var posts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if serveSlackToolIdentity(w, r) {
						return
					}
					switch r.URL.Path {
					case "/chat.postMessage":
						posts.Add(1)
						var body map[string]any
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						assert.Equal(t, "C123", body["channel"])
						assert.Equal(t, "111.222", body["thread_ts"])
						writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.1"})
					case "/v10/users/@me":
						writeToolTestJSON(w, map[string]any{"id": "222", "bot": true})
					case "/v10/applications/@me":
						writeToolTestJSON(w, map[string]any{"id": "111"})
					case "/v10/channels/555":
						writeToolTestJSON(w, map[string]any{"id": "555", "parent_id": "444", "guild_id": "333", "type": 11})
					case "/v10/channels/555/messages":
						posts.Add(1)
						var body struct {
							Nonce   string `json:"nonce"`
							Content string `json:"content"`
						}
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						assert.Equal(t, "hello", body.Content)
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
				_, err := dispatchAsyncToolToTerminal(t, ctx,
					Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(record.ResultContentParts))
				require.EqualValues(t, 1, posts.Load())
			})
		}
	}
}

func TestAppToolMissingContextBeforeProviderIO(t *testing.T) {
	for _, test := range []struct{ provider, operation, input string }{
		{"slack", "read", `{}`},
		{"slack", "post_message", `{"text":"hello"}`},
		{"discord", "read", `{}`},
		{"discord", "post_message", `{"content":"hello"}`},
		{"github", "read", `{}`},
		{"github", "discussion_comment", `{"body":"Review"}`},
		{"github", "inline_comment", `{"body":"Fix","commit_id":"abc123","path":"service.go","line":9,"side":"RIGHT"}`},
		{"github", "reply", `{"comment_id":31,"body":"Resolved"}`},
	} {
		t.Run(test.provider+"/"+test.operation, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "missing-context", toolFixtureOptions{
				withSlackApp: test.provider == "slack", withDiscordApp: test.provider == "discord",
				withGitHubApp: test.provider == "github",
			})
			call := f.recordToolCall(t, ctx, "missing", toolcatalog.AppToolName("chat", test.operation), test.input, f.Now)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			_, err := dispatchAsyncToolToTerminal(t, ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			body := toolResultMapFromTestParts(t, record.ResultContentParts)
			require.Equal(t, "app_tool_failed", body["code"])
			require.Contains(t, body["message"], `app "chat" has no assigned conversation`)
			require.Zero(t, requests.Load(), "missing context fails before provider I/O, including identity checks")
		})
	}
}

func TestAppToolRejectsDestinationArgumentsBeforeProviderIO(t *testing.T) {
	for _, test := range []struct{ provider, input string }{
		{"slack", `{"channel_id":"C999"}`},
		{"discord", `{"thread_id":"999"}`},
		{"github", `{"pull_request":8}`},
	} {
		t.Run(test.provider, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "invalid-destination", toolFixtureOptions{
				withSlackApp: test.provider == "slack", withDiscordApp: test.provider == "discord",
				withGitHubApp: test.provider == "github", withToolContext: true,
			})
			call := f.recordToolCall(t, ctx, "invalid", toolcatalog.AppToolName("chat", "read"), test.input, f.Now)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			_, err := dispatchAsyncToolToTerminal(t, ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			require.Contains(t, string(record.ResultContentParts), "additional properties")
			require.Zero(t, requests.Load(), "schema validation must precede any provider I/O")
		})
	}
}

func appToolSubscriptions(t *testing.T, f integrationToolFixture) []integrationstore.AppSubscriptionRecord {
	t.Helper()
	page, err := f.Store.Integrations().ListAppSubscriptions(t.Context(), integrationstore.ListAppSubscriptionsInput{
		ProjectID: toolsTestProjectID, AppID: f.Install.ID, Limit: 100,
	})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	return page.Subscriptions
}

func attachToolSubscription(
	t *testing.T,
	f integrationToolFixture,
	subscriptionType, conversation string,
	events []string,
) integrationstore.AppSubscriptionRecord {
	t.Helper()
	subscription, err := f.Store.Integrations().
		CreateAppSubscription(t.Context(), integrationstore.CreateAppSubscriptionInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, AppID: f.Install.ID, AgentID: f.Agent.ID,
			Type: subscriptionType, Conversation: json.RawMessage(conversation), Events: events,
		})
	require.NoError(t, err)
	return subscription
}
