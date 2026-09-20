//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func githubToolTestCalls() []model.ToolCall {
	return []model.ToolCall{
		{ID: "read", Name: "app__chat__read", Input: json.RawMessage(`{}`)},
		{ID: "discussion", Name: "app__chat__discussion_comment", Input: json.RawMessage(`{"body":"Review"}`)},
		{
			ID: "inline", Name: "app__chat__inline_comment",
			Input: json.RawMessage(`{"body":"Fix","commit_id":"abc123","path":"service.go","line":9,"side":"RIGHT"}`),
		},
		{ID: "reply", Name: "app__chat__reply", Input: json.RawMessage(`{"comment_id":31,"body":"Resolved"}`)},
	}
}

func TestGitHubAppAuthorityBeforeEveryRequest(t *testing.T) {
	for _, call := range githubToolTestCalls() {
		for _, scenario := range []string{"removed-before-dispatch", "removed-after-token", "reconfigured", "disconnected"} {
			t.Run(call.ID+"/"+scenario, func(t *testing.T) {
				ctx := t.Context()
				f := newIntegrationToolFixtureWithOptions(t, ctx, "github-authority", toolFixtureOptions{withGitHubApp: true})
				f.recordToolCalls(t, ctx, []model.ToolCall{call}, f.Now)
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
					[]byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				if scenario == "reconfigured" {
					entry := source.Tools[call.Name]
					entry.Config["pull_request"] = 8
					source.Tools[call.Name] = entry
				} else {
					delete(source.Tools, call.Name)
				}
				changeInput := appToolConfigChangeInput(t, f, source)
				revoke := func() error {
					if scenario == "disconnected" {
						_, err := f.Store.Integrations().DisconnectProjectApp(ctx, integrationstore.DisconnectProjectAppInput{
							ProjectID: f.Agent.ProjectID, AppID: f.Install.ID, ExpectedSetupRevision: &f.Install.SetupRevision,
						})
						return err
					}
					_, err := f.Store.Execution().ChangeAgentConfig(ctx, changeInput)
					return err
				}
				if scenario == "removed-before-dispatch" {
					require.NoError(t, revoke())
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if requests.Add(1) != 1 || scenario == "removed-before-dispatch" ||
						r.URL.Path != "/app/installations/22/access_tokens" {
						t.Errorf("provider request after authority was revoked: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if !assert.NoError(t, revoke()) {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					writeToolTestJSON(w, map[string]any{
						"token": "installation-token", "expires_at": time.Now().Add(time.Hour),
					})
				}))
				defer server.Close()
				_, err = dispatchAsyncToolToTerminal(t, ctx,
					Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Equal(t, "app_tool_failed", toolResultMapFromTestParts(t, record.ResultContentParts)["code"])
				if scenario == "removed-before-dispatch" {
					require.Zero(t, requests.Load())
				} else {
					require.EqualValues(t, 1, requests.Load(), "minting a token must not preserve revoked authority")
				}
			})
		}
	}
}

func TestGitHubAppRejectsFixedOverridesAndUnsupportedFollow(t *testing.T) {
	for _, call := range githubToolTestCalls() {
		for _, extra := range []string{`{"repository_id":123}`, `{"pull_request":8}`, `{"follow_replies":true}`} {
			t.Run(call.ID+"/"+extra, func(t *testing.T) {
				ctx := t.Context()
				f := newIntegrationToolFixtureWithOptions(t, ctx, "github-arguments", toolFixtureOptions{withGitHubApp: true})
				var args map[string]any
				require.NoError(t, json.Unmarshal(call.Input, &args))
				require.NoError(t, json.Unmarshal([]byte(extra), &args))
				input, err := json.Marshal(args)
				require.NoError(t, err)
				proposed := f.recordToolCall(t, ctx, call.ID, call.Name, string(input), f.Now)
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.WriteHeader(http.StatusForbidden)
				}))
				defer server.Close()
				_, err = dispatchAsyncToolToTerminal(t, ctx,
					Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), proposed)
				require.NoError(t, err)
				record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
				require.NoError(t, err)
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
				require.Zero(t, requests.Load(), "invalid arguments must not mint a provider token")
			})
		}
	}
}

func TestGitHubAppReplyRejectsForeignPullRequest(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixtureWithOptions(t, ctx, "github-foreign-reply", toolFixtureOptions{withGitHubApp: true})
	var reads, posts atomic.Int32
	server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reads.Add(1)
		assert.Equal(t, "/repos/octo/renamed/pulls/comments/31", r.URL.Path)
		writeToolTestJSON(w, map[string]any{
			"id": 31, "pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/8",
		})
	})
	call := f.recordToolCall(t, ctx, "reply", "app__chat__reply", `{"comment_id":31,"body":"Reply"}`, f.Now)
	result, err := dispatchAsyncToolToTerminal(t, ctx,
		Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}, f.turn(), call)
	require.NoError(t, err)
	require.Equal(t, "scope_mismatch", toolResultMapFromTestParts(t, result.ContentParts)["code"])
	require.EqualValues(t, 1, reads.Load())
	require.Zero(t, posts.Load(), "a comment ID from another PR must not redirect the reply")
}
