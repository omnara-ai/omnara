//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func assistantFixture(t *testing.T) (*pgxpool.Pool, http.Handler, publicHTTPProject) {
	t.Helper()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://example.com"),
		WithPublicAPIURL("https://api.example.com/api/v1"),
	)
	project := bootstrapPublicHTTPProject(t, handler, "assistant")
	return pool, handler, project
}

func assistantBody(t *testing.T, project publicHTTPProject, requestID uuid.UUID, revision int, automatic bool) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"project_id": project.ProjectID, "request_id": requestID,
		"expected_revision": revision, "automatic": automatic,
	})
	require.NoError(t, err)
	return string(data)
}

func assistantCounts(t *testing.T, pool *pgxpool.Pool) []int {
	t.Helper()
	result := make([]int, 6)
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT
        (SELECT count(*) FROM personal_access_tokens),
        (SELECT count(*) FROM secrets),
        (SELECT count(*) FROM agent_configs),
        (SELECT count(*) FROM agents),
        (SELECT count(*) FROM assistant_agent_creations),
        (SELECT count(*) FROM agent_inputs WHERE input_kind = 'content')`,
	).Scan(&result[0], &result[1], &result[2], &result[3], &result[4], &result[5]))
	return result
}

func TestAgentPanelCreationReplayAndSelection(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	headers := project.adminBrowserAuthHeaders()
	panel := requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, "empty", panel["state"])
	setup := testutil.RequireType[map[string]any](t, panel["setup"])
	require.Equal(t, true, setup["ready"])
	require.Equal(t, project.ProjectUUID.String(), setup["request_id"])
	before := assistantCounts(t, pool)
	body := assistantBody(t, project, project.ProjectUUID, 0, true)
	path := "/api/v1/orgs/" + project.OrgID + "/assistant-agents"
	result := requestJSONWithHeaders(t, handler, "POST", path, body, "", 200, headers)
	after := assistantCounts(t, pool)
	require.Equal(t, []int{before[0] + 1, before[1] + 1, before[2] + 1, before[3] + 1, before[4] + 1, before[5]}, after)
	agentID := testutil.RequireType[string](t, result["agent_id"])
	selected := testutil.RequireType[map[string]any](t, result["panel"])
	require.Equal(t, "ready", selected["state"])
	require.NotContains(t, selected, "setup")
	require.Equal(t, agentID, testutil.RequireType[map[string]any](t, selected["selection"])["agent_id"])
	replay := requestJSONWithHeaders(t, handler, "POST", path, body, "", 200, headers)
	require.Equal(t, result, replay)
	require.Equal(t, after, assistantCounts(t, pool))
	for _, change := range []map[string]any{
		{"automatic": false},
		{"model_id": testPublicID(t, publicid.KindConfiguredModel, uuid.New())},
	} {
		var altered map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &altered))
		for key, value := range change {
			altered[key] = value
		}
		encoded, err := json.Marshal(altered)
		require.NoError(t, err)
		requestJSONWithHeaders(t, handler, "POST", path, string(encoded), "", 409, headers)
		require.Equal(t, after, assistantCounts(t, pool))
	}
	requestJSONWithHeaders(
		t,
		handler,
		"PATCH",
		"/api/v1/me/agent-panel",
		`{"agent_id":null,"enabled":false}`,
		"",
		200,
		headers,
	)
	replay = requestJSONWithHeaders(t, handler, "POST", path, body, "", 200, headers)
	replayPanel := testutil.RequireType[map[string]any](t, replay["panel"])
	require.Equal(t, false, replayPanel["enabled"])
	require.NotContains(t, replayPanel, "selection")
	require.Equal(t, after, assistantCounts(t, pool))
	// The preset is visible in ordinary lists, but does not finish onboarding.
	overview := requestJSONWithHeaders(t, handler, "GET", "/api/v1/orgs/"+project.OrgID+"/overview", "", "", 200, headers)
	require.Empty(t, overview["recent_agents"])
	agents := requestJSONWithHeaders(t, handler, "GET", project.ProjectPath+"/agents", "", "", 200, headers)
	require.Len(t, agents["data"], 1)
	var machines int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM machines`).Scan(&machines))
	require.Zero(t, machines)
	detail := requestJSONWithHeaders(t, handler, "GET", project.ProjectPath+"/agents/"+agentID, "", "", 200, headers)
	configID := testutil.RequireType[string](
		t,
		testutil.RequireType[map[string]any](t, detail["agent"])["current_config_id"],
	)
	config := requestJSONWithHeaders(
		t,
		handler,
		"GET",
		project.ProjectPath+"/agent-configs/"+configID,
		"",
		"",
		200,
		headers,
	)
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "omnara_pat_v1_")
}

func TestAgentPanelCreationRollsBackAndRequiresBrowserOwner(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	path := "/api/v1/orgs/" + project.OrgID + "/assistant-agents"
	body := assistantBody(t, project, uuid.New(), 0, false)
	before := assistantCounts(t, pool)
	requestJSONWithHeaders(t, handler, "POST", path, body, "", 403, authHeaders(project.AdminToken))
	noCSRF := map[string]string{"Cookie": project.adminBrowserAuthHeaders()["Cookie"], "Origin": "http://example.com"}
	requestJSONWithHeaders(t, handler, "POST", path, body, "", 403, noCSRF)
	// A failure after secret creation must not leak a token, secret or selection.
	principal, _, err := project.Store.Identity().AuthenticateBrowserSession(t.Context(), project.AdminSession)
	require.NoError(t, err)
	_, err = project.Store.Organizations().CreateAssistantAgent(t.Context(), orglifecycle.CreateAssistantAgentInput{
		Principal: principal, OrgID: project.OrgUUID, ProjectID: project.ProjectUUID,
		RequestID: uuid.New(), MCPURL: "http://not-allowed.example/mcp",
	})
	require.Error(t, err)
	require.Equal(t, before, assistantCounts(t, pool))
	_, err = pool.Exec(t.Context(), `UPDATE org_memberships SET role='admin' WHERE user_id=$1`, project.AdminUserUUID)
	require.NoError(t, err)
	requestJSONWithHeaders(t, handler, "POST", path, body, "", 403, project.adminBrowserAuthHeaders())
	require.Equal(t, before, assistantCounts(t, pool))
	_, err = project.Store.Organizations().CreateAssistantAgent(t.Context(), orglifecycle.CreateAssistantAgentInput{
		Principal: principal, OrgID: project.OrgUUID, ProjectID: project.ProjectUUID,
		RequestID: uuid.New(), MCPURL: "https://api.example.com/mcp",
	})
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
}

func TestAgentPanelConcurrentCreationAndManualOverride(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	body := assistantBody(t, project, uuid.New(), 0, false)
	path := "/api/v1/orgs/" + project.OrgID + "/assistant-agents"
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			for key, value := range project.adminBrowserAuthHeaders() {
				req.Header.Set(key, value)
			}
			results <- performRequest(handler, req)
		})
	}
	wg.Wait()
	close(results)
	var agentID string
	for result := range results {
		require.Equal(t, 200, result.Code, result.Body.String())
		var data map[string]any
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &data))
		id := testutil.RequireType[string](t, data["agent_id"])
		if agentID == "" {
			agentID = id
		} else {
			require.Equal(t, agentID, id)
		}
	}
	require.Equal(t, 1, assistantCounts(t, pool)[4])
	requestJSONWithHeaders(
		t,
		handler,
		"PATCH",
		"/api/v1/me/agent-panel",
		`{"agent_id":null}`,
		"",
		200,
		project.adminBrowserAuthHeaders(),
	)
	before := assistantCounts(t, pool)
	requestJSONWithHeaders(
		t,
		handler,
		"POST",
		path,
		assistantBody(t, project, uuid.New(), 0, false),
		"",
		409,
		project.adminBrowserAuthHeaders(),
	)
	require.Equal(t, before, assistantCounts(t, pool))
}

func TestAgentPanelConcurrentManualChoiceSupersedesCreation(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	before := assistantCounts(t, pool)
	patchReady, releasePatch := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePatch) })
	defer release()
	var wrotePreference atomic.Bool
	trace := &integrationdb.QueryTrace{OnQuery: func(sql string) {
		if strings.HasPrefix(sql, "-- name: SetAgentPanelPreference ") {
			wrotePreference.Store(true)
		}
		if strings.EqualFold(sql, "commit") && wrotePreference.CompareAndSwap(true, false) {
			close(patchReady)
			<-releasePatch
		}
	}}
	patchHandler := newIntegrationServer(poolWithQueryTracer(t, t.Context(), pool, trace))
	send := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for key, value := range project.adminBrowserAuthHeaders() {
			req.Header.Set(key, value)
		}
		return performRequest(h, req)
	}
	patch := integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
		return send(patchHandler, http.MethodPatch, "/api/v1/me/agent-panel", `{"agent_id":null,"enabled":false}`), nil
	})
	integrationdb.Await(t, patchReady, "manual preference waiting to commit")
	body := assistantBody(t, project, project.ProjectUUID, 0, true)
	creation := integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
		return send(handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/assistant-agents", body), nil
	})
	// Prove the creation is in flight and blocked behind the uncommitted manual choice.
	integrationdb.WaitForNamedLockWaiters(t, t.Context(), pool, "LockUserForUpdate", 1)
	release()
	patched := integrationdb.AwaitSuccess(t, patch, "manual panel choice")
	require.Equal(t, http.StatusOK, patched.Code, patched.Body.String())
	created := integrationdb.AwaitSuccess(t, creation, "stale assistant creation")
	require.Equal(t, http.StatusConflict, created.Code, created.Body.String())
	require.Equal(t, before, assistantCounts(t, pool))
	panel := requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200,
		project.adminBrowserAuthHeaders())
	require.Equal(t, false, panel["enabled"])
	require.Equal(t, float64(1), panel["revision"])
	require.NotContains(t, panel, "selection")
	require.NotContains(t, panel, "setup")
}

func TestAgentPanelFirstOrgEligibilityAndDeletedHome(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	headers := project.adminBrowserAuthHeaders()
	body := assistantBody(t, project, project.ProjectUUID, 0, true)
	result := requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
		body,
		"",
		200,
		headers,
	)
	agentID := testutil.RequireType[string](t, result["agent_id"])
	second := requestJSONWithHeaders(t, handler, "POST", "/api/v1/orgs", `{"name":"second"}`, "", 201, headers)
	require.NotEmpty(t, second)
	panel := requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.NotContains(t, panel, "setup")
	require.Equal(t, agentID, testutil.RequireType[map[string]any](t, panel["selection"])["agent_id"])
	requestJSONWithHeaders(t, handler, "DELETE", project.ProjectPath, "", "", 204, headers)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, "unavailable", panel["state"])
	require.NotContains(t, panel, "selection")
	var revoked bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT token.revoked_at IS NOT NULL
        FROM personal_access_tokens token
        JOIN assistant_agent_creations creation ON creation.personal_access_token_id=token.id
        WHERE creation.agent_id=$1`, mustPublicHTTPID(t, publicid.KindAgent, agentID)).Scan(&revoked))
	require.True(t, revoked)
	// Deleting the home does not reopen first-organization eligibility.
	third := requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs",
		fmt.Sprintf(`{"name":%q}`, "third"),
		"",
		201,
		headers,
	)
	require.NotEmpty(t, third)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.NotContains(t, panel, "setup")
}

func TestAgentPanelQuotaFailuresRollbackEveryResource(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{
		"max_active_tenant_secrets_per_owner", "max_agent_configs_per_project",
		"max_active_agents_per_project", "personal_access_tokens",
	} {
		t.Run(limit, func(t *testing.T) {
			t.Parallel()
			pool, handler, project := assistantFixture(t)
			if limit == "personal_access_tokens" {
				_, err := pool.Exec(t.Context(), `INSERT INTO personal_access_tokens(user_id,name,token_id,token_hash,created_at)
        SELECT $1,'quota','quota-'||n,'quota-hash-'||n,transaction_timestamp()
        FROM generate_series(1,999) n`, project.AdminUserUUID)
				require.NoError(t, err)
			} else {
				_, err := pool.Exec(
					t.Context(),
					fmt.Sprintf(`INSERT INTO org_resource_limit_overrides(org_id,%s) VALUES($1,0)`, limit),
					project.OrgUUID,
				)
				require.NoError(t, err)
			}
			before := assistantCounts(t, pool)
			requestJSONWithHeaders(
				t,
				handler,
				"POST",
				"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
				assistantBody(t, project, project.ProjectUUID, 0, true),
				"",
				409,
				project.adminBrowserAuthHeaders(),
			)
			require.Equal(t, before, assistantCounts(t, pool))
			panel := requestJSONWithHeaders(
				t,
				handler,
				"GET",
				"/api/v1/me/agent-panel",
				"",
				"",
				200,
				project.adminBrowserAuthHeaders(),
			)
			require.Equal(t, "empty", panel["state"])
			require.Contains(t, panel, "setup")
			require.Equal(t, float64(0), panel["revision"])
		})
	}
}

func TestAgentPanelCreationWithOneConnection(t *testing.T) {
	t.Parallel()
	pool, _, project := assistantFixture(t)
	config := pool.Config()
	config.MaxConns = 1
	single, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(single.Close)
	store := newIntegrationStore(single)
	principal, _, err := store.Identity().AuthenticateBrowserSession(t.Context(), project.AdminSession)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = store.Organizations().CreateAssistantAgent(ctx, orglifecycle.CreateAssistantAgentInput{
		Principal: principal, OrgID: project.OrgUUID, ProjectID: project.ProjectUUID,
		RequestID: uuid.New(), MCPURL: "https://api.example.com/mcp",
	})
	require.NoError(t, err)
}

func TestAgentPanelSkipsInvalidOldestModel(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	_, err := pool.Exec(
		t.Context(),
		`UPDATE project_model_grants SET context_window_tokens=1000000 WHERE project_id=$1`,
		project.ProjectUUID,
	)
	require.NoError(t, err)
	headers := project.adminBrowserAuthHeaders()
	panel := requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, false, testutil.RequireType[map[string]any](t, panel["setup"])["ready"])
	before := assistantCounts(t, pool)
	requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
		assistantBody(t, project, uuid.New(), 0, false),
		"",
		409,
		headers,
	)
	require.Equal(t, before, assistantCounts(t, pool))
	var providerID uuid.UUID
	require.NoError(
		t,
		pool.QueryRow(
			t.Context(),
			`SELECT id FROM model_provider_configs WHERE org_id=$1`,
			project.OrgUUID,
		).Scan(&providerID),
	)
	modelsPath := "/api/v1/orgs/" + project.OrgID + "/model-provider-configs/" +
		testPublicID(t, publicid.KindModelProviderConfig, providerID) + "/models"
	model := requestJSONWithHeaders(t, handler, http.MethodPost, modelsPath, `{
  "name": "valid-later",
  "provider_model_slug": "valid-later",
  "context_window_tokens": 128000,
  "max_output_tokens": 8192,
  "default_max_output_tokens": 4096
}`, "", 201, headers)
	modelID := testutil.RequireType[string](t, model["id"])
	requestJSONWithHeaders(
		t,
		handler,
		"POST",
		project.ProjectPath+"/model-grants",
		fmt.Sprintf(`{"configured_model_id":%q}`, modelID),
		"",
		201,
		headers,
	)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, true, testutil.RequireType[map[string]any](t, panel["setup"])["ready"])
	created := requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
		assistantBody(t, project, uuid.New(), 0, false),
		"",
		200,
		headers,
	)
	var chosenModel uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT definition.configured_model_id
        FROM agents agent
        JOIN agent_configs config ON config.id=agent.current_config_id
        JOIN agent_config_definitions definition ON definition.id=config.definition_id
        WHERE agent.id=$1`,
		mustPublicHTTPID(t, publicid.KindAgent, testutil.RequireType[string](t, created["agent_id"])),
	).Scan(&chosenModel))
	require.Equal(t, mustPublicHTTPID(t, publicid.KindConfiguredModel, modelID), chosenModel)
}

func TestAgentPanelUnnamedArchivedAndUsedAssistant(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	headers := project.adminBrowserAuthHeaders()
	created := requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
		assistantBody(t, project, project.ProjectUUID, 0, true),
		"",
		200,
		headers,
	)
	agentID := testutil.RequireType[string](t, created["agent_id"])
	_, err := pool.Exec(
		t.Context(),
		`UPDATE agents SET name='' WHERE id=$1`,
		mustPublicHTTPID(t, publicid.KindAgent, agentID),
	)
	require.NoError(t, err)
	panel := requestJSONWithHeaders(
		t,
		handler,
		"PATCH",
		"/api/v1/me/agent-panel",
		fmt.Sprintf(`{"agent_id":%q}`, agentID),
		"",
		200,
		headers,
	)
	require.Equal(t, "", testutil.RequireType[map[string]any](t, panel["selection"])["name"])
	requestJSONWithHeaders(
		t,
		handler,
		"POST",
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		`{"content_blocks":[{"type":"text","text":"Hello"}]}`,
		"",
		201,
		headers,
	)
	overview := requestJSONWithHeaders(t, handler, "GET", "/api/v1/orgs/"+project.OrgID+"/overview", "", "", 200, headers)
	require.Len(t, overview["recent_agents"], 1)
	requestJSONWithHeaders(t, handler, "POST", project.ProjectPath+"/agents/"+agentID+"/archive", "", "", 200, headers)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, "archived", panel["state"])
}

func TestAgentPanelJoinConnectAndAccessRevocation(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	ordinary := launchPublicHTTPAgent(t, handler, project, "ordinary", project.AdminToken, 201)
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, ordinary["agent"])["id"])
	user, _ := createOrgRouteUser(t, pool, project.Store, "panel-member")
	_, err := project.Store.Identity().AddOrgMembership(t.Context(), identitystore.AddOrgMembershipInput{
		OrgID: project.OrgUUID, UserID: user.ID, Role: "member",
	})
	require.NoError(t, err)
	_, err = project.Store.Identity().AddProjectMembership(t.Context(), identitystore.AddProjectMembershipInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, UserID: user.ID, Role: "viewer",
	})
	require.NoError(t, err)
	member := project
	member.AdminSession, member.AdminCSRF = "panel-member-session", "panel-member-csrf"
	_, err = project.Store.Identity().CreateBrowserSession(t.Context(), identitystore.CreateBrowserSessionInput{
		UserID: user.ID, Token: member.AdminSession, CSRFToken: member.AdminCSRF, TTL: time.Hour,
	})
	require.NoError(t, err)
	headers := member.adminBrowserAuthHeaders()
	panel := requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, "empty", panel["state"])
	require.NotContains(t, panel, "setup", "joining must never provision an assistant")
	panel = requestJSONWithHeaders(
		t,
		handler,
		"PATCH",
		"/api/v1/me/agent-panel",
		fmt.Sprintf(`{"agent_id":%q}`, agentID),
		"",
		200,
		headers,
	)
	selection := testutil.RequireType[map[string]any](t, panel["selection"])
	require.Equal(t, false, selection["can_operate"])
	require.Equal(t, agentID, selection["agent_id"])
	// Creating the member's first org must preserve their explicit choice.
	requestJSONWithHeaders(t, handler, "POST", "/api/v1/orgs", `{"name":"member-owned"}`, "", 201, headers)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.NotContains(t, panel, "setup")
	require.Equal(t, selection, panel["selection"])
	_, err = pool.Exec(t.Context(), `DELETE FROM project_memberships WHERE org_membership_id IN
        (SELECT id FROM org_memberships WHERE user_id=$1) AND project_id=$2`, user.ID, project.ProjectUUID)
	require.NoError(t, err)
	panel = requestJSONWithHeaders(t, handler, "GET", "/api/v1/me/agent-panel", "", "", 200, headers)
	require.Equal(t, "unavailable", panel["state"])
	require.NotContains(t, panel, "selection", "inaccessible names and IDs must not leak")
	requestJSONWithHeaders(
		t,
		handler,
		"PATCH",
		"/api/v1/me/agent-panel",
		fmt.Sprintf(`{"agent_id":%q}`, agentID),
		"",
		404,
		headers,
	)
	ownerPanel := requestJSONWithHeaders(
		t,
		handler,
		"GET",
		"/api/v1/me/agent-panel",
		"",
		"",
		200,
		project.adminBrowserAuthHeaders(),
	)
	require.Equal(t, "empty", ownerPanel["state"], "preferences belong to each user")
	require.Zero(t, assistantCounts(t, pool)[4], "connecting ordinary agents creates no preset resources")
}

func TestAgentPanelOrgAndAccountDeletionCleanup(t *testing.T) {
	t.Parallel()
	pool, handler, project := assistantFixture(t)
	headers := project.adminBrowserAuthHeaders()
	requestJSONWithHeaders(
		t,
		handler,
		"POST",
		"/api/v1/orgs/"+project.OrgID+"/assistant-agents",
		assistantBody(t, project, uuid.New(), 0, false),
		"",
		200,
		headers,
	)
	// Revocation follows provenance even after disconnecting.
	requestJSONWithHeaders(t, handler, "PATCH", "/api/v1/me/agent-panel", `{"agent_id":null}`, "", 200, headers)
	requestJSONWithHeaders(t, handler, "DELETE", "/api/v1/orgs/"+project.OrgID, "", "", 204, headers)
	var active int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*)
        FROM assistant_agent_creations creation
        JOIN personal_access_tokens token ON token.id=creation.personal_access_token_id
        WHERE token.revoked_at IS NULL`).Scan(&active))
	require.Zero(t, active)
	require.NoError(t, project.Store.Identity().DeleteUserAccount(t.Context(), project.AdminUserUUID))
	var preferences, provenance int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT (SELECT count(*)
        FROM agent_panel_preferences),
        (SELECT count(*)
        FROM assistant_agent_creations)`).Scan(&preferences, &provenance))
	require.Zero(t, preferences)
	require.Equal(t, 1, provenance, "retain provenance for the ordinary agent's lifecycle")
}

func TestAgentPanelExistingUserAndPendingHomeDeletion(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			t.Parallel()
			pool, handler, project := assistantFixture(t)
			user, token := createOrgRouteUser(t, pool, project.Store, "eligibility")
			if existing {
				_, err := pool.Exec(t.Context(), `UPDATE users SET agent_panel_bootstrap_eligible=false WHERE id=$1`, user.ID)
				require.NoError(t, err)
			}
			created := requestJSONWithHeaders(
				t,
				handler,
				"POST",
				"/api/v1/orgs",
				`{"name":"eligibility"}`,
				"",
				201,
				authHeaders(token),
			)
			var pending *uuid.UUID
			err := pool.QueryRow(
				t.Context(),
				`SELECT pending_project_id FROM agent_panel_preferences WHERE user_id=$1`,
				user.ID,
			).Scan(&pending)
			if existing {
				require.ErrorIs(t, err, pgx.ErrNoRows, "existing users must not receive an automatic preference")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, pending)
			orgID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, created["org"])["id"])
			requestJSONWithHeaders(t, handler, "DELETE", "/api/v1/orgs/"+orgID, "", "", 204, authHeaders(token))
			principal := identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: user.ID}
			panel, err := project.Store.Organizations().GetAgentPanel(t.Context(), principal)
			require.NoError(t, err)
			require.Nil(t, panel.Setup)
			requestJSONWithHeaders(t, handler, "POST", "/api/v1/orgs", `{"name":"later"}`, "", 201, authHeaders(token))
			panel, err = project.Store.Organizations().GetAgentPanel(t.Context(), principal)
			require.NoError(t, err)
			require.Nil(t, panel.Setup, "deleting the first home must not restore bootstrap eligibility")
		})
	}
}
