//go:build integration

package tools

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
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

func createGitHubToolApp(
	t *testing.T, ctx context.Context, store *storage.Store, userID uuid.UUID,
) integrationstore.ProjectAppRecord {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	secret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: toolsTestOrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: toolsTestProjectID,
		Name: "github-tool-credentials", Actor: toolsTestUserPrincipal(userID),
		Material: secrets.GitHubAppCredentialsMaterial{
			AppID: "11", PrivateKey: string(privateKey), WebhookSecret: "test-webhook-secret",
		},
	})
	require.NoError(t, err)
	app, err := store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Name: "chat", AppType: appdefinition.GitHubPR,
	})
	require.NoError(t, err)
	app, err = store.Integrations().ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, AppID: app.ID,
		ExpectedSetupRevision: app.SetupRevision, InstalledByUserID: userID,
		Provider: appdefinition.ProviderGitHub, ProviderTenantID: "11", ProviderAccountRef: "22",
		CredentialSecretID: secret.ID, CredentialVersionID: version.ID, CredentialAppID: 11,
	})
	require.NoError(t, err)
	return app
}

func githubToolTestServer(t *testing.T, permission string, operation http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/22/access_tokens" {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Len(t, strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "."), 3)
			var input struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&input)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, []int64{123}, input.RepositoryIDs)
			assert.Equal(t, map[string]string{"pull_requests": permission}, input.Permissions)
			writeToolTestJSON(w, map[string]any{
				"token": "installation-token", "expires_at": time.Now().Add(time.Hour),
			})
			return
		}
		assert.Equal(t, "Bearer installation-token", r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/installation/repositories":
			assert.Equal(t, http.MethodGet, r.Method)
			writeToolTestJSON(w, map[string]any{
				"total_count": 1,
				"repositories": []any{map[string]any{
					"id": 123, "name": "renamed", "owner": map[string]any{"login": "octo"},
				}},
			})
		case r.URL.Path == "/repos/octo/renamed/pulls/7" &&
			r.Header.Get("Accept") != "application/vnd.github.diff":
			assert.Equal(t, http.MethodGet, r.Method)
			writeToolTestJSON(w, map[string]any{
				"id": 700, "number": 7, "title": "Review this change", "body": "PR context",
				"base": map[string]any{"repo": map[string]any{"id": 123}},
			})
		default:
			operation(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestGitHubAppReadSections(t *testing.T) {
	for _, tt := range []struct {
		name, input, path, response, collection string
		want                                    map[string]any
	}{
		{"default", `{}`, "", "", "", map[string]any{"number": float64(7), "title": "Review this change"}},
		{"pull_request", `{"section":"pull_request"}`, "", "", "",
			map[string]any{"body": "PR context"}},
		{
			"discussion_comments", `{"section":"discussion_comments","page":2,"limit":1}`,
			"/repos/octo/renamed/issues/7/comments", `[{"id":31,"body":"discussion"}]`, "comments",
			map[string]any{"id": float64(31), "body": "discussion"},
		},
		{
			"review_comments", `{"section":"review_comments","page":2,"limit":1}`,
			"/repos/octo/renamed/pulls/7/comments", `[{"id":32,"body":"review","in_reply_to_id":30}]`, "comments",
			map[string]any{"id": float64(32), "body": "review", "in_reply_to_id": float64(30)},
		},
		{
			"files", `{"section":"files","page":2,"limit":1}`,
			"/repos/octo/renamed/pulls/7/files", `[{"filename":"service.go","patch":"+fixed"}]`, "files",
			map[string]any{"filename": "service.go", "patch": "+fixed"},
		},
		{
			"diff", `{"section":"diff"}`, "/repos/octo/renamed/pulls/7", "diff --git a/service.go b/service.go\n+fixed\n", "",
			map[string]any{"text": "diff --git a/service.go b/service.go\n+fixed\n"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-read", toolFixtureOptions{
				withGitHubApp: true, withToolContext: true,
			})
			var operationRequests atomic.Int32
			server := githubToolTestServer(t, "read", func(w http.ResponseWriter, r *http.Request) {
				operationRequests.Add(1)
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, tt.path, r.URL.Path)
				if tt.collection != "" {
					assert.Equal(t, "2", r.URL.Query().Get("page"))
					assert.Equal(t, "1", r.URL.Query().Get("per_page"))
					w.Header().Set("Link", "<https://api.github.com"+tt.path+`?page=3&per_page=1>; rel="next"`)
				} else {
					assert.Equal(t, "application/vnd.github.diff", r.Header.Get("Accept"))
				}
				_, _ = w.Write([]byte(tt.response))
			})
			call := f.recordToolCall(t, ctx, "read", toolcatalog.AppToolName("chat", "read"), tt.input, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			if tt.collection != "" {
				require.Equal(t, float64(3), body["next_page"])
				items, ok := body[tt.collection].([]any)
				require.True(t, ok)
				require.Len(t, items, 1)
				body, ok = items[0].(map[string]any)
				require.True(t, ok)
			}
			require.Subset(t, body, tt.want)
			if tt.path == "" {
				require.Zero(t, operationRequests.Load())
			} else {
				require.EqualValues(t, 1, operationRequests.Load(), "pagination must not fetch later pages automatically")
			}
		})
	}
}

func TestGitHubAppCommentsAndReplay(t *testing.T) {
	for _, tt := range []struct {
		operation, input, path, payload string
	}{
		{"discussion_comment", `{"body":"Review ready"}`,
			"/repos/octo/renamed/issues/7/comments", `{"body":"Review ready"}`},
		{
			"inline_comment",
			`{"body":"Fix this range","commit_id":"abc123","path":"service.go","line":9,"side":"RIGHT","start_line":7,"start_side":"RIGHT"}`,
			"/repos/octo/renamed/pulls/7/comments",
			`{"body":"Fix this range","commit_id":"abc123","path":"service.go","line":9,"side":"RIGHT","start_line":7,"start_side":"RIGHT"}`,
		},
		{
			"reply", `{"comment_id":31,"body":"Resolved"}`,
			"/repos/octo/renamed/pulls/7/comments/30/replies", `{"body":"Resolved"}`,
		},
	} {
		for _, withSubscription := range []bool{false, true} {
			scenario := tt.operation + map[bool]string{false: "/no-subscription", true: "/subscription"}[withSubscription]
			t.Run(scenario, func(t *testing.T) {
				ctx := t.Context()
				f := newIntegrationToolFixtureWithOptions(t, ctx, "github-comment", toolFixtureOptions{
					withGitHubApp: true, withToolContext: true,
				})
				if withSubscription {
					attachToolSubscription(t, f, "pull_request", `{"repository_id":123,"pull_request":7}`, []string{"commit"})
				}
				subscriptionsBefore := appToolSubscriptions(t, f)
				if withSubscription {
					require.Len(t, subscriptionsBefore, 1)
				} else {
					require.Empty(t, subscriptionsBefore)
				}
				var posts, replyReads atomic.Int32
				server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && tt.operation == "reply" {
						replyReads.Add(1)
						id, parent := 31, 30
						if r.URL.Path == "/repos/octo/renamed/pulls/comments/30" {
							id, parent = 30, 0
						} else {
							assert.Equal(t, "/repos/octo/renamed/pulls/comments/31", r.URL.Path)
						}
						writeToolTestJSON(w, map[string]any{
							"id": id, "in_reply_to_id": parent,
							"pull_request_url": "https://api.github.com/repos/octo/renamed/pulls/7",
						})
						return
					}
					posts.Add(1)
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, tt.path, r.URL.Path)
					var payload json.RawMessage
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.JSONEq(t, tt.payload, string(payload))
					writeToolTestJSON(w, map[string]any{
						"id": 501, "body": "provider-confirmed", "html_url": "https://github.com/octo/renamed/pull/7#comment-501",
					})
				})
				name := toolcatalog.AppToolName("chat", tt.operation)
				call := f.recordToolCall(t, ctx, "comment", name, tt.input, f.Now)
				executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
				result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
				require.NoError(t, err)
				body := toolResultMapFromTestParts(t, result.ContentParts)
				require.Equal(t, float64(501), body["id"])
				require.Equal(t, "provider-confirmed", body["body"])
				require.Equal(t, "https://github.com/octo/renamed/pull/7#comment-501", body["html_url"])
				require.Equal(t, subscriptionsBefore, appToolSubscriptions(t, f), "sending must not add subscriptions")
				source, err := agentconfig.ParseSource(
					agentconfig.SourceFormat(f.AgentConfig.SourceFormat),
					[]byte(f.AgentConfig.Source),
				)
				require.NoError(t, err)
				delete(source.Tools, name)
				changeAppToolConfig(t, ctx, f, source)
				require.Equal(t, subscriptionsBefore, appToolSubscriptions(t, f),
					"removing a sender must not revoke its subscription")
				replay, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
				require.NoError(t, err)
				require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
				require.EqualValues(t, 1, posts.Load(), "a completed call must not publish twice")
				if tt.operation == "reply" {
					require.EqualValues(t, 2, replyReads.Load(), "replies must resolve and verify the root comment")
				}
			})
		}
	}
}

func TestGitHubAppProviderFailureDoesNotResend(t *testing.T) {
	for _, tt := range []struct {
		name, code string
		status     int
	}{
		{"rate-limit", "rate_limited", http.StatusTooManyRequests},
		{"uncertain-publication", "delivery_unknown", http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "github-failure", toolFixtureOptions{
				withGitHubApp: true, withToolContext: true,
			})
			var posts atomic.Int32
			server := githubToolTestServer(t, "write", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/repos/octo/renamed/issues/7/comments", r.URL.Path)
				posts.Add(1)
				if tt.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "23")
				}
				w.WriteHeader(tt.status)
				writeToolTestJSON(w, map[string]any{"message": "private-provider-details"})
			})
			call := f.recordToolCall(t, ctx, "comment", "app__chat__discussion_comment", `{"body":"Review"}`, f.Now)
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			require.Equal(t, tt.code, body["code"])
			if tt.status == http.StatusTooManyRequests {
				require.Equal(t, float64(23), body["retry_after_seconds"])
			} else {
				require.Contains(t, body["message"], "Read the PR before deciding whether to resend")
			}
			require.NotContains(t, string(result.ContentParts), "private-provider-details")
			replay, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
			require.EqualValues(t, 1, posts.Load())
			record, err := f.Store.Execution().GetToolCall(ctx, f.Agent.ProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome)
		})
	}
}
