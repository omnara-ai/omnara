//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func markRuntimeLaunchOwner(t *testing.T, f integrationToolFixture) {
	t.Helper()
	_, err := f.Pool.Exec(t.Context(), `UPDATE integration_targets SET launch_key='default' WHERE id=$1`, f.Target.ID)
	require.NoError(t, err)
}

func TestRuntimeFailureIgnoresPinnedInteractionDestination(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newInteractionToolFixture(t, ctx, "runtime-pinned-handler", "chat", "overlap")
	markRuntimeLaunchOwner(t, f)
	overlap, err := f.Store.Integrations().GetIntegrationByName(ctx, f.Agent.ProjectID, "overlap")
	require.NoError(t, err)
	_, err = f.Pool.Exec(ctx, `UPDATE integration_targets SET scope_ref='COTHER:333.444'
		WHERE project_id=$1 AND agent_id=$2 AND integration_id=$3`, f.Agent.ProjectID, f.Agent.ID, overlap.ID)
	require.NoError(t, err)
	_, err = f.Pool.Exec(ctx, `UPDATE integration_states SET data='{"kind":"thread","ref":"COTHER:333.444"}'
		WHERE project_id=$1 AND integration_id=$2 AND kind='agent_conversation' AND key=$3`,
		f.Agent.ProjectID, overlap.ID, f.Agent.ID.String())
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "pin", toolcatalog.ToolNameSetInteractionHandler,
		`{"handler":"overlap","args":{},"auto_select":false}`, f.Now)
	dispatchInteractionHandler(t, ctx, f, interactionToolTurn(f, toolpermission.ModeAlwaysAllow), call)
	before, err := f.Store.Execution().GetInteractionSelection(ctx, f.Agent.ProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, "overlap", before.HandlerKey)
	require.False(t, before.AutoSelect)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		var body struct {
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/chat.postMessage" || body.Channel != "C123" || body.ThreadTS != "111.222" {
			t.Errorf("runtime notice followed pinned handler: path=%s payload=%+v", r.URL.Path, body)
		}
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	notifier := integrationruntime.RuntimeFailureNotifier{
		Store: f.Store, HTTPClient: integrationProviderTestClient(server),
	}
	require.NoError(t, notifier.Notify(ctx, f.Agent.ProjectID, f.Agent.ID, f.Lock.ID))
	require.EqualValues(t, 1, posts.Load())
	after, err := f.Store.Execution().GetInteractionSelection(ctx, f.Agent.ProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, before, after, "runtime feedback must not change the interaction selection")
}

func TestRuntimeFailureRequiresMatchingAssignment(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing", "mismatched"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationToolFixture(t, t.Context(), "runtime-assignment")
			markRuntimeLaunchOwner(t, f)
			query := `DELETE FROM integration_states
				WHERE project_id=$1 AND integration_id=$2 AND kind='agent_conversation' AND key=$3`
			if scenario == "mismatched" {
				query = `UPDATE integration_states SET data='{"kind":"thread","ref":"COTHER:333.444"}'
					WHERE project_id=$1 AND integration_id=$2 AND kind='agent_conversation' AND key=$3`
			}
			_, err := f.Pool.Exec(t.Context(), query, f.Agent.ProjectID, f.Install.ID, f.Agent.ID.String())
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("invalid assignment must prevent provider I/O: %s", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			notifier := integrationruntime.RuntimeFailureNotifier{
				Store: f.Store, HTTPClient: integrationProviderTestClient(server),
			}
			require.ErrorIs(t, notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID), storeerr.ErrUnauthorized)
		})
	}
}

func TestRuntimeFailureGitHubUsesLaunchPRWithoutHandler(t *testing.T) {
	t.Parallel()
	testRuntimeFailureGitHub(t, false)
}

func TestRuntimeFailureGitHubRechecksLeaseBeforeSend(t *testing.T) {
	t.Parallel()
	testRuntimeFailureGitHub(t, true)
}

func testRuntimeFailureGitHub(t *testing.T, loseLease bool) {
	t.Helper()
	f := newIntegrationToolFixtureWithOptions(t, t.Context(), "github-runtime-failure", toolFixtureOptions{
		withGitHubIntegration: true, withToolContext: true,
	})
	markRuntimeLaunchOwner(t, f)
	selection, err := f.Store.Execution().GetInteractionSelection(t.Context(), f.Agent.ProjectID, f.Agent.ID)
	require.NoError(t, err)
	require.Empty(t, selection.HandlerKey)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/installations/22/access_tokens":
			var scope struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
				t.Error(err)
			}
			if len(scope.RepositoryIDs) != 1 || scope.RepositoryIDs[0] != 123 || scope.Permissions["pull_requests"] != "write" {
				t.Errorf("runtime failure token scope: %+v", scope)
			}
			writeToolTestJSON(w, map[string]any{"token": "runtime-token", "expires_at": time.Now().Add(time.Hour)})
		case "/installation/repositories":
			writeToolTestJSON(w, map[string]any{"total_count": 1, "repositories": []any{map[string]any{
				"id": 123, "name": "repo", "owner": map[string]any{"login": "owner"},
			}}})
		case "/repos/owner/repo/pulls/7":
			if loseLease {
				if err := f.Store.Execution().ReleaseAgentRuntimeLock(
					t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID,
				); err != nil {
					t.Error(err)
				}
			}
			writeToolTestJSON(w, map[string]any{
				"id": 700, "number": 7, "base": map[string]any{"repo": map[string]any{"id": 123}},
			})
		case "/repos/owner/repo/issues/7/comments":
			var body struct{ Body string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Body != integrationruntime.AgentRequestFailureMessage {
				t.Errorf("runtime failure must be generic: %q", body.Body)
			}
			posts.Add(1)
			writeToolTestJSON(w, map[string]any{"id": 800})
		default:
			t.Errorf("unexpected provider request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	notifier := integrationruntime.RuntimeFailureNotifier{
		Store: f.Store, HTTPClient: integrationProviderTestClient(server),
	}
	err = notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID)
	if loseLease {
		require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
		require.Zero(t, posts.Load(), "lease loss after PR validation must prevent posting")
	} else {
		require.NoError(t, err)
		require.EqualValues(t, 1, posts.Load())
	}
}

func TestRuntimeFailureRechecksAuthorityBeforeSend(t *testing.T) {
	for _, change := range []string{
		"credential rotated", "credential deleted", "setup changed", "owner retired", "lease lost",
	} {
		t.Run(change, func(t *testing.T) {
			f := newIntegrationToolFixture(t, t.Context(), "runtime-authority")
			markRuntimeLaunchOwner(t, f)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.test" {
					var err error
					switch change {
					case "credential rotated":
						_, _, err = f.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
							OrgID: f.Agent.OrgID, SecretID: f.Install.CredentialSecretID,
							Actor: toolsTestUserPrincipal(f.User.ID),
							Material: secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-rotated", ClientID: "client",
								ClientSecret: "secret", SigningSecret: "signing"},
						})
					case "credential deleted":
						_, err = f.Pool.Exec(t.Context(), `UPDATE secrets SET deleted_at=now() WHERE id=$1`,
							f.Install.CredentialSecretID)
					case "setup changed":
						_, err = f.Pool.Exec(t.Context(), `UPDATE integrations SET setup_revision=setup_revision+1 WHERE id=$1`,
							f.Install.ID)
					case "owner retired":
						_, err = f.Pool.Exec(t.Context(), `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, f.Target.ID)
					case "lease lost":
						err = f.Store.Execution().ReleaseAgentRuntimeLock(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID)
					}
					if err != nil {
						t.Error(err)
					}
				}
				if serveSlackToolIdentity(w, r) {
					return
				}
				posts.Add(1)
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
			}))
			defer server.Close()
			notifier := integrationruntime.RuntimeFailureNotifier{
				Store: f.Store, HTTPClient: integrationProviderTestClient(server),
			}
			require.Error(t, notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID))
			require.Zero(t, posts.Load(), "changed authority must prevent provider mutation")
		})
	}
}
