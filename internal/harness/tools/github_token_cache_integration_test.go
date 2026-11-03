//go:build integration

package tools

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/stretchr/testify/require"
)

func TestGitHubToolTokenReuseRetainsLiveAuthority(t *testing.T) {
	for _, change := range []string{"credential version", "disconnected", "tool removed"} {
		t.Run(change, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-token-cache", toolFixtureOptions{
				withGitHubIntegration: true, withToolContext: true,
			})
			call := f.recordToolCall(t, ctx, "read", "int__chat__read", "{}", f.Now)
			record, err := f.Store.Execution().GetToolCall(
				ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID),
			)
			require.NoError(t, err)
			var requests, mints atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch r.URL.Path {
				case "/app/installations/22/access_tokens":
					mints.Add(1)
					writeToolTestJSON(w, map[string]any{"token": "cached-token", "expires_at": time.Now().Add(time.Hour)})
				case "/installation/repositories":
					writeToolTestJSON(w, map[string]any{"total_count": 1, "repositories": []any{map[string]any{
						"id": 123, "name": "repo", "owner": map[string]any{"login": "owner"},
					}}})
				case "/repos/owner/repo/pulls/7":
					writeToolTestJSON(w, map[string]any{
						"id": 700, "number": 7, "base": map[string]any{"repo": map[string]any{"id": 123}},
					})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			access, err := executor.resolveIntegrationToolAccess(ctx, f.turn(), record)
			require.NoError(t, err)
			scope := github.Scope{RepositoryID: 123, PullRequest: 7}
			for range 2 {
				client, err := executor.githubToolClient(f.turn(), record, access)
				require.NoError(t, err)
				_, err = client.GetPullRequest(ctx, scope)
				require.NoError(t, err)
			}
			require.EqualValues(t, 1, mints.Load())
			client, err := executor.githubToolClient(f.turn(), record, access)
			require.NoError(t, err)
			before := requests.Load()
			switch change {
			case "credential version":
				_, _, err := f.Store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
					OrgID: f.Agent.OrgID, SecretID: access.Integration.CredentialSecretID,
					Actor: toolsTestUserPrincipal(f.User.ID),
					Material: secrets.GitHubAppCredentialsMaterial{
						AppID: access.Credential[secrets.KeyAppID], PrivateKey: access.Credential[secrets.KeyPrivateKey],
						WebhookSecret: access.Credential[secrets.KeyWebhookSecret],
					},
				})
				require.NoError(t, err)
			case "disconnected":
				_, err := f.Store.Integrations().DisconnectIntegration(ctx, integrationstore.DisconnectIntegrationInput{
					ProjectID: f.Agent.ProjectID, IntegrationID: access.Integration.ID,
					ExpectedSetupRevision: &access.Integration.SetupRevision,
				})
				require.NoError(t, err)
			case "tool removed":
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat), []byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				delete(source.Tools, record.Name)
				_, err = f.Store.Execution().ChangeAgentConfig(ctx, integrationToolConfigChangeInput(t, f, source))
				require.NoError(t, err)
			}
			_, err = client.GetPullRequest(ctx, scope)
			require.ErrorIs(t, err, ErrToolAuthorizationInvalidated)
			require.Equal(t, before, requests.Load(), "cached tokens must not permit HTTP after authority changes")
			if change == "credential version" {
				access, err = executor.resolveIntegrationToolAccess(ctx, f.turn(), record)
				require.NoError(t, err)
				client, err = executor.githubToolClient(f.turn(), record, access)
				require.NoError(t, err)
				_, err = client.GetPullRequest(ctx, scope)
				require.NoError(t, err)
				require.EqualValues(t, 2, mints.Load(), "fresh access uses the rotated version's token")
			}
		})
	}
}
