//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListGitHubRepositoriesUsesAgentGitCredentials(t *testing.T) {
	app, lib := map[string]any{"full_name": "octo/app"}, map[string]any{"full_name": "octo/lib"}
	for _, tt := range []struct {
		name, gitCredentials, revoke, input, permission string
		repositoryIDs                                   []int64
		requests                                        int32
		outcome                                         executionstore.ToolResultOutcome
		want                                            map[string]any
	}{
		{
			name:           "selected read-only page",
			gitCredentials: "{integration: chat, repository_ids: [123, 456], read_only: true}",
			input:          `{"limit":1}`, permission: "read", repositoryIDs: []int64{123, 456}, requests: 2,
			outcome: executionstore.ToolResultOutcomeSucceeded,
			want: map[string]any{
				"can_push": false, "repositories": []any{app}, "has_more": true, "next_offset": float64(1),
			},
		},
		{
			name:           "configured repository outside the page lost access",
			gitCredentials: "{integration: chat, repository_ids: [123, 456]}", revoke: "repository",
			input: `{"limit":1}`, permission: "write", repositoryIDs: []int64{123, 456}, requests: 1,
			outcome: executionstore.ToolResultOutcomeFailed,
			want: map[string]any{
				"error_code": "permanent_failure", "message": "github permanent_failure (HTTP 422)",
			},
		},
		{
			name: "all repositories", gitCredentials: "{integration: chat}", permission: "write", requests: 2,
			outcome: executionstore.ToolResultOutcomeSucceeded,
			want:    map[string]any{"can_push": true, "repositories": []any{app, lib}, "has_more": false},
		},
		{
			name: "no git credentials", outcome: executionstore.ToolResultOutcomeFailed,
			want: map[string]any{
				"error_code": "integration_tool_failed", "message": "no Git credentials are configured for this agent",
			},
		},
		{
			name: "credentials removed from current config", gitCredentials: "{integration: chat}", revoke: "config",
			outcome: executionstore.ToolResultOutcomeFailed,
			want: map[string]any{
				"error_code": "integration_tool_failed",
				"message":    ErrToolAuthorizationInvalidated.Error() + ": Git credentials changed; submit a new call",
			},
		},
		{
			name: "integration disconnected mid-call", gitCredentials: "{integration: chat}", revoke: "integration",
			permission: "write", requests: 1, outcome: executionstore.ToolResultOutcomeFailed,
			want: map[string]any{"error_code": "integration_tool_failed"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-repositories", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true, gitCredentials: tt.gitCredentials,
			})
			install, err := f.Store.Integrations().UpdateIntegration(ctx, f.Install.ID, integrationstore.SaveIntegrationInput{
				OrgID: f.Install.OrgID, ProjectID: f.Install.ProjectID, Name: f.Install.Name,
				IntegrationKind: f.Install.IntegrationKind, Settings: json.RawMessage(`{"code_write":true}`),
			})
			require.NoError(t, err)
			if tt.revoke == "config" {
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				source.GitCredentials = nil
				changeIntegrationToolConfig(t, ctx, f, source)
			}
			repositories := []any{
				map[string]any{"id": 123, "name": "app", "full_name": "octo/app"},
				map[string]any{"id": 456, "name": "lib", "full_name": "octo/lib"},
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/app/installations/22/access_tokens":
					var input struct {
						Permissions   map[string]string `json:"permissions"`
						RepositoryIDs []int64           `json:"repository_ids"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&input))
					assert.Equal(t, tt.permission, input.Permissions["contents"])
					assert.Equal(t, tt.repositoryIDs, input.RepositoryIDs)
					if tt.revoke == "repository" {
						w.WriteHeader(http.StatusUnprocessableEntity)
						writeToolTestJSON(w, map[string]any{"message": "There is at least one repository that does " +
							"not exist or is not accessible to the parent installation."})
						return
					}
					if tt.revoke == "integration" {
						_, err := f.Store.Integrations().DisconnectIntegration(ctx, integrationstore.DisconnectIntegrationInput{
							ProjectID: install.ProjectID, IntegrationID: install.ID, ExpectedSetupRevision: &install.SetupRevision,
						})
						assert.NoError(t, err)
					}
					writeToolTestJSON(w, map[string]any{
						"token": "installation-token", "expires_at": time.Now().Add(time.Hour),
						"permissions": map[string]string{"contents": tt.permission},
					})
				case "/installation/repositories":
					assert.Equal(t, "Bearer installation-token", r.Header.Get("Authorization"))
					writeToolTestJSON(w, map[string]any{"total_count": 2, "repositories": repositories})
				default:
					t.Errorf("unexpected GitHub request %s", r.URL)
				}
			}))
			t.Cleanup(server.Close)
			turn := f.turn()
			turn.Tools[toolcatalog.ToolNameListGitHubRepositories] = ToolSpec{
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}
			input := tt.input
			if input == "" {
				input = `{}`
			}
			call := f.recordToolCall(t, ctx, "repositories", toolcatalog.ToolNameListGitHubRepositories, input, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			if tt.revoke == "integration" {
				message, _ := body["message"].(string)
				require.True(t, strings.HasPrefix(message, ErrToolAuthorizationInvalidated.Error()+": "), message)
				delete(body, "message")
			}
			require.Equal(t, tt.want, body)
			require.Equal(t, tt.requests, requests.Load())
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, tt.outcome, record.Outcome)
		})
	}
}
