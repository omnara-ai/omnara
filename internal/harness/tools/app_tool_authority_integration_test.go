//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppToolApprovalShowsFixedDestination(t *testing.T) {
	for _, tt := range []struct {
		provider, operation, input string
		fixed                      map[string]string
	}{
		{"slack", "post_message", `{"text":"Review ready"}`, map[string]string{"channel_id": "C123"}},
		{"discord", "post_message", `{"content":"Review ready"}`, map[string]string{"channel_id": "444"}},
		{
			"github", "discussion_comment", `{"body":"Review ready"}`,
			map[string]string{"repository_id": "123", "pull_request": "7"},
		},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "approval-destination", toolFixtureOptions{
				withSlackApp:     tt.provider == "slack",
				withDiscordApp:   tt.provider == "discord",
				withGitHubApp:    tt.provider == "github",
				slackPermission:  toolpermission.ModeAlwaysAsk,
				githubPermission: toolpermission.ModeAlwaysAsk,
			})
			call := f.recordPendingToolCall(t, ctx, "approval", toolcatalog.AppToolName("chat", tt.operation), tt.input, f.Now)
			var schema struct {
				Properties map[string]any `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(f.AppTools[call.Name].InputSchema, &schema))
			for field := range tt.fixed {
				require.NotContains(t, schema.Properties, field, "fixed destination fields are hidden from model arguments")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			require.NoError(t, executor.PrepareToolCallPermission(ctx, f.turn(), call))
			interaction := integrationToolInteraction(
				t,
				ctx,
				f,
				f.toolCallID(t, ctx, call.ID),
				executionstore.AgentInteractionKindPermission,
			)
			form, err := interaction.Form()
			require.NoError(t, err)
			require.Len(t, form.Context, 1)
			var summary struct {
				Description string          `json:"description"`
				Arguments   json.RawMessage `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal([]byte(form.Context[0].Value), &summary))
			require.JSONEq(t, tt.input, string(summary.Arguments))
			for field, value := range tt.fixed {
				require.Contains(t, summary.Description, field)
				require.Contains(t, summary.Description, value, "approvers must see the actual fixed destination")
			}
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)
			require.Zero(t, requests.Load(), "requesting approval must not contact the provider")
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
		"narrowed-away",
		"unrelated",
		"added-handler",
		"fixed-same-destination",
		"listener-removed",
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
						toolFixtureOptions{withSlackApp: true, slackPermission: toolpermission.ModeAlwaysAsk},
					)
					call := f.recordPendingToolCall(
						t,
						ctx,
						"post",
						toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
						`{"text":"hello","thread_ts":"111.222","follow_replies":true}`,
						f.Now,
					)
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
						case "narrowed-away":
							entry.Config["thread_ts"] = "999.1"
							source.Tools[call.Name] = entry
						case "fixed-same-destination":
							entry.Config["thread_ts"] = "111.222"
							source.Tools[call.Name] = entry
						case "unrelated":
							source.Instruction += " Be concise."
						case "added-handler":
							source.InteractionHandlers = map[string]agentconfig.AgentConfigAppCapabilitySource{
								"chat": {Config: map[string]any{"channel_id": "C123"}},
							}
						case "listener-removed":
							delete(source.Listeners, "chat__thread_messages")
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
					allowed := scenario == "unrelated" || scenario == "added-handler"
					wantPosts, wantOutcome := 0, executionstore.ToolResultOutcomeFailed
					if allowed {
						wantPosts, wantOutcome = 1, executionstore.ToolResultOutcomeSucceeded
					}
					require.Equal(t, wantPosts, posts)
					require.Equal(t, wantOutcome, record.Outcome)
					var follows int
					require.NoError(
						t,
						f.Pool.QueryRow(
							ctx,
							`SELECT count(*) FROM agent_listeners WHERE agent_id=$1 AND active AND tool_call_id IS NOT NULL`,
							f.Agent.ID,
						).
							Scan(
								&follows,
							),
					)
					require.Equal(t, wantPosts, follows)
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
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: f.AgentConfig.ConfiguredModelID.String()}, nil
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
			CompilerVersion:         compiled.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        f.Agent.ID,
		ActorType:      "user",
		ActorID:        f.User.ID,
		IdempotencyKey: uuid.NewString(),
	}
}

func TestAsyncFailurePreservesProviderEvidenceOnTimeout(t *testing.T) {
	for _, code := range []string{"delivery_unknown", "follow_registration_failed", ""} {
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

func TestAppToolRejectsFixedArgumentOverrideBeforeProviderIO(t *testing.T) {
	for _, provider := range []string{"slack", "discord"} {
		for _, sameDestination := range []bool{false, true} {
			t.Run(provider+map[bool]string{false: "/different", true: "/same"}[sameDestination], func(t *testing.T) {
				ctx := t.Context()
				f := newIntegrationToolFixtureWithOptions(t, ctx, "fixed-args", toolFixtureOptions{
					withSlackApp: provider == "slack", withDiscordApp: provider == "discord",
				})
				channel, textKey := "C999", "text"
				if provider == "discord" {
					channel, textKey = "999", "content"
				}
				if sameDestination {
					channel = "C123"
					if provider == "discord" {
						channel = "444"
					}
				}
				input, err := json.Marshal(map[string]any{"channel_id": channel, textKey: "hello"})
				require.NoError(t, err)
				call := f.recordToolCall(
					t,
					ctx,
					"override",
					toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
					string(input),
					f.Now,
				)
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if serveSlackToolIdentity(w, r) {
						return
					}
					requests++
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer server.Close()
				_, err = dispatchAsyncToolToTerminal(
					t,
					ctx,
					Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
					slackAppToolTurn(f),
					call,
				)
				require.NoError(t, err)
				require.Zero(t, requests, "fixed fields are hidden and cannot be supplied, even with the same value")
				record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Contains(t, string(record.ResultContentParts), "channel_id")
			})
		}
	}
}

func TestAppToolMissingListenerRejectsFollowBeforeProviderIO(t *testing.T) {
	for _, provider := range []string{"slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "missing-listener", toolFixtureOptions{
				withSlackApp: provider == "slack", withDiscordApp: provider == "discord", withoutAppListener: true,
			})
			input := `{"text":"hello","follow_replies":true}`
			if provider == "discord" {
				input = `{"content":"hello","follow_replies":true}`
			}
			call := f.recordToolCall(
				t,
				ctx,
				"follow",
				toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
				input,
				f.Now,
			)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				requests++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			_, err := dispatchAsyncToolToTerminal(
				t,
				ctx,
				Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)},
				slackAppToolTurn(f),
				call,
			)
			require.NoError(t, err)
			require.Zero(t, requests, "missing listener must fail before credential or destination provider checks")
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
			require.Contains(t, string(record.ResultContentParts), "listener")
		})
	}
}
