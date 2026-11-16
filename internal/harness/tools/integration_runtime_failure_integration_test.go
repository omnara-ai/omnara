//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func markRuntimeLaunchOwner(t *testing.T, f integrationToolFixture) {
	t.Helper()
	_, err := f.Pool.Exec(t.Context(), `UPDATE integration_targets SET launch_key='default' WHERE id=$1`, f.Target.ID)
	require.NoError(t, err)
}

func runtimeFailureTurnID(t *testing.T, f integrationToolFixture) uuid.UUID {
	t.Helper()
	var turnID uuid.UUID
	require.NoError(t, f.Pool.QueryRow(t.Context(), `SELECT turn_id FROM model_call_contexts WHERE id=$1`,
		f.ModelCallContextID).Scan(&turnID))
	return turnID
}

func TestTurnInputLaunchOwnerLookup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"active", "attribution only", "other project", "other agent", "other turn", "target deleted",
		"integration disconnected", "integration deleted", "agent archived", "project deleted", "org deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "turn-owner-lookup")
			if scenario != "attribution only" {
				markRuntimeLaunchOwner(t, f)
			}
			project, agent, turn := f.Agent.ProjectID, f.Agent.ID, runtimeFailureTurnID(t, f)
			exec := func(query string, id uuid.UUID) {
				_, err := f.Pool.Exec(ctx, query, id)
				require.NoError(t, err)
			}
			switch scenario {
			case "other project":
				project = uuid.New()
			case "other agent":
				agent = uuid.New()
			case "other turn":
				turn = uuid.New()
			case "target deleted":
				exec(`UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, f.Target.ID)
			case "integration disconnected":
				exec(`UPDATE integrations SET state='disconnected' WHERE id=$1`, f.Install.ID)
			case "integration deleted":
				exec(`UPDATE integrations SET deleted_at=now() WHERE id=$1`, f.Install.ID)
			case "agent archived":
				_, _, err := f.Store.Execution().ArchiveAgent(ctx, project, agent, toolsTestUserPrincipal(f.User.ID))
				require.NoError(t, err)
			case "project deleted":
				exec(`UPDATE projects SET deleted_at=now() WHERE id=$1`, project)
			case "org deleted":
				exec(`UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.Agent.OrgID)
			}
			owners, err := f.Store.Integrations().ListTurnInputLaunchOwners(ctx, project, agent, turn)
			require.NoError(t, err)
			if scenario != "active" {
				require.Empty(t, owners)
				return
			}
			require.Len(t, owners, 1)
			require.Equal(t, f.Target.ID, owners[0].ID)
			require.Equal(t, "default", owners[0].LaunchKey)
		})
	}
}

func TestRuntimeFailureRequiresTurnInputFromOwnedConversation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"omnara input", "other turn"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationToolFixtureWithOptions(t, t.Context(), "runtime-turn-input", toolFixtureOptions{
				withToolContext: scenario == "omnara input",
			})
			markRuntimeLaunchOwner(t, f)
			turnID := runtimeFailureTurnID(t, f)
			if scenario == "other turn" {
				turnID = uuid.New()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("a turn without input from the owned conversation must not notify: %s", r.URL.Path)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			notifier := integrationruntime.RuntimeFailureNotifier{
				Store: f.Store, HTTPClient: integrationProviderTestClient(server),
			}
			require.NoError(t, notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID, turnID))
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
		withGitHubIntegration: true,
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
	tx, err := f.Pool.Begin(t.Context())
	require.NoError(t, err)
	reassigned := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "123#8"}
	require.NoError(t, f.Store.Integrations().AssignAgentIntegrationConversationTx(t.Context(), tx,
		f.Agent.ProjectID, f.Agent.ID, f.Install.ID, reassigned))
	require.NoError(t, tx.Commit(t.Context()))
	notifier := integrationruntime.RuntimeFailureNotifier{
		Store: f.Store, HTTPClient: integrationProviderTestClient(server),
	}
	err = notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID, runtimeFailureTurnID(t, f))
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
			require.Error(t, notifier.Notify(t.Context(), f.Agent.ProjectID, f.Agent.ID, f.Lock.ID, runtimeFailureTurnID(t, f)))
			require.Zero(t, posts.Load(), "changed authority must prevent provider mutation")
		})
	}
}
