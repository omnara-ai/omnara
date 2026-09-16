//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exerciseGitHubControlRecovery(
	t *testing.T, handler http.Handler, pool *pgxpool.Pool, project publicHTTPProject,
	appID, originalID uuid.UUID, token string,
) {
	t.Helper()
	ctx := t.Context()
	second, err := project.Store.Integrations().UpsertIntegrationInstall(
		ctx, integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: appID,
			InstalledBy: httpUserPrincipal(project.AdminUserUUID), Provider: "github",
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "123", ProviderAccountRef: "789",
			DisplayName: "example/second", ProviderIdentity: json.RawMessage(`{"repository_owner":"example",` +
				`"repository_name":"second","repository_node_id":"R_second"}`),
		})
	require.NoError(t, err)
	firstID, lostID, lostName := originalID, second.ID, "second"
	if strings.Compare(firstID.String(), lostID.String()) > 0 {
		firstID, lostID, lostName = lostID, firstID, "project"
	}
	lostPublicID := testPublicID(t, publicid.KindIntegrationInstall, lostID)
	var originalRevision int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT configuration_revision FROM integration_installs WHERE id=$1`,
		lostID).Scan(&originalRevision))
	var lost, unavailable atomic.Bool
	var tokens atomic.Int32
	native := githubControlJourneyProvider(t, lostName, &unavailable, &tokens)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/installations/"+lostPublicID+"/provider-state") || lost.Load() {
			handler.ServeHTTP(w, r)
			return
		}
		// Commit through real authenticated Go HTTP, then lose only its response.
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if !assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String()) {
			http.Error(w, "unexpected state result", http.StatusInternalServerError)
			return
		}
		lost.Store(true)
		unavailable.Store(true)
		hijacker, ok := w.(http.Hijacker)
		if !assert.True(t, ok) {
			return
		}
		conn, _, hijackErr := hijacker.Hijack()
		if assert.NoError(t, hijackErr) {
			assert.NoError(t, conn.Close())
		}
	}))
	t.Cleanup(core.Close)
	webhook := githubJourneyWebhook(t, "control")
	configuration := map[string]any{
		"coreUrl": core.URL + "/api/v1", "githubUrl": native.URL, "token": token,
		"appID":    testPublicID(t, publicid.KindIntegrationApp, appID),
		"webhooks": []any{webhook}, "processControl": true,
	}
	first := runGitHubJourney(t, configuration)
	require.Equal(t, []int{http.StatusAccepted}, first.WebhookStatuses)
	require.Len(t, first.ControlResults, 1)
	require.Equal(t, "retry", first.ControlResults[0].Outcome)
	require.Equal(t, testPublicID(t, publicid.KindIntegrationInstall, firstID),
		first.ControlResults[0].LastInstallationID)
	require.True(t, lost.Load())
	require.EqualValues(t, 1, tokens.Load())
	var state string
	var last, end uuid.UUID
	var generation int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,last_install_id,end_install_id,lease_generation
FROM integration_control_receipts WHERE integration_app_id=$1 AND event_id=$2`, appID, webhook["delivery"]).Scan(
		&state, &last, &end, &generation))
	require.Equal(t, "pending", state)
	require.Equal(t, firstID, last)
	require.Equal(t, lostID, end)
	require.EqualValues(t, 1, generation)
	var revision int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,configuration_revision FROM integration_installs WHERE id=$1`,
		lostID).Scan(&state, &revision))
	require.Equal(t, "active", state)
	require.Equal(t, originalRevision+1, revision, "the unacknowledged write actually committed")
	// Make the existing bounded retry due without sleeping or rewriting its lease/checkpoint.
	_, err = pool.Exec(ctx, `UPDATE integration_control_receipts SET available_at=now()-interval '1 second'
WHERE integration_app_id=$1 AND event_id=$2`, appID, webhook["delivery"])
	require.NoError(t, err)
	delete(configuration, "webhooks")
	resumed := runGitHubJourney(t, configuration) // A new Node process, factory and native session.
	require.Len(t, resumed.ControlResults, 1)
	require.Equal(t, "completed", resumed.ControlResults[0].Outcome)
	require.Equal(t, lostPublicID, resumed.ControlResults[0].LastInstallationID)
	require.EqualValues(t, 2, tokens.Load(), "restart must mint fresh native credentials")
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,configuration_revision FROM integration_installs WHERE id=$1`,
		lostID).Scan(&state, &revision))
	require.Equal(t, "disabled", state, "fresh native membership supersedes the lost active observation")
	require.Equal(t, originalRevision+2, revision)
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,last_install_id,lease_generation
FROM integration_control_receipts WHERE integration_app_id=$1 AND event_id=$2`, appID, webhook["delivery"]).Scan(
		&state, &last, &generation))
	require.Equal(t, "completed", state)
	require.Equal(t, lostID, last)
	require.EqualValues(t, 2, generation)
}

func githubControlJourneyProvider(
	t *testing.T, unavailableName string, unavailable *atomic.Bool, tokens *atomic.Int32,
) *httptest.Server {
	t.Helper()
	installation := map[string]any{
		"id": 123, "app_id": 42, "suspended_at": nil,
		"permissions": map[string]string{"pull_requests": "write", "issues": "read", "metadata": "read"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value any
		switch r.URL.Path {
		case "/app":
			value = map[string]int{"id": 42}
		case "/app/installations/123":
			value = installation
		case "/app/installations/123/access_tokens":
			var body map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			assert.Equal(t, map[string]any{"permissions": map[string]any{"metadata": "read"}}, body)
			tokens.Add(1)
			value = map[string]any{"token": "local-metadata-token", "permissions": map[string]string{"metadata": "read"},
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
		case "/graphql":
			var body struct {
				Query     string `json:"query"`
				Variables struct {
					ID string `json:"id"`
				} `json:"variables"`
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			assert.Contains(t, body.Query, "query RepositoryControlIdentity")
			name := "project"
			if body.Variables.ID == "R_second" {
				name = "second"
			} else {
				assert.Equal(t, "R_selected", body.Variables.ID)
			}
			value = map[string]any{"data": map[string]any{"node": map[string]any{
				"__typename": "Repository", "id": body.Variables.ID, "name": name,
				"owner": map[string]string{"id": "O_1", "login": "example"},
			}}}
		case "/repos/example/project/installation", "/repos/example/second/installation":
			if unavailable.Load() && strings.Contains(r.URL.Path, "/"+unavailableName+"/") {
				http.NotFound(w, r)
				return
			}
			value = installation
		default:
			assert.Fail(t, "unexpected native path", "%s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	t.Cleanup(server.Close)
	return server
}
