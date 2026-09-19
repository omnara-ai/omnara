//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppToolApprovalDoesNotBypassCurrentConfig(t *testing.T) {
	for _, scenario := range []string{
		"removed",
		"disabled",
		"tool-disabled",
		"deny",
		"rebound",
		"narrowed-away",
		"unrelated",
		"added-resource",
		"narrowed-within",
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
						toolcatalog.ToolNameSlackPostMessage,
						`{"text":"hello","thread_ts":"111.222","follow_replies":true}`,
						f.Now,
					)
					turn := slackAppToolTurn(f)
					turn.Tools[call.Name] = ToolSpec{
						Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
					}
					posts := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
						resource := source.AppResources["chat"]
						switch scenario {
						case "removed":
							delete(source.AppResources, "chat")
						case "disabled":
							disabled := false
							resource.Enabled = &disabled
							source.AppResources["chat"] = resource
						case "tool-disabled":
							disabled := false
							source.Tools[call.Name] = agentconfig.AgentConfigToolSource{Enabled: &disabled}
						case "deny":
							policy := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
							source.Tools[call.Name] = agentconfig.AgentConfigToolSource{Permission: &policy}
						case "rebound":
							input := integrationToolInstallInput(
								uuid.Nil,
								f.User.ID,
								f.Install.CredentialSecretID,
								f.Now,
							)
							input.ProviderTenantID = "T999"
							other, err := f.Store.Integrations().CreateIntegrationConnection(ctx, input)
							require.NoError(t, err)
							resource.Connection, err = publicid.Encode(publicid.KindIntegrationConnection, other.ID)
							require.NoError(t, err)
							source.AppResources["chat"] = resource
						case "narrowed-away":
							resource.Scope.Slack.ThreadTS = "999.1"
						case "narrowed-within":
							resource.Scope.Slack.ThreadTS = "111.222"
						case "unrelated":
							source.Instruction += " Be concise."
						case "added-resource":
							source.AppResources["second"] = resource
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
					allowed := scenario == "unrelated" || scenario == "added-resource" || scenario == "narrowed-within"
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
		ResolveAppConnection: func(id, _ string) (string, error) { return id, nil },
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
			call := startOverflowTestCall(t, &f, toolcatalog.ToolNameSlackPostMessage, `{"text":"hi"}`)
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
