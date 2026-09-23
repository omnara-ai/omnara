//go:build integration

package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProjectAppRuntimeFailureOnlyOnGet(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithDiscordClientConfig(appSetupDiscordConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "app-runtime-failure")
	app := createSetupHTTPApp(t, handler, project, "discord", appdefinition.DiscordThread)
	headers := authHeaders(project.AdminToken)
	body := appSetupDiscordBody(t, handler, project, "111")
	setup := requestJSONWithHeaders(t, handler, http.MethodPost, appSetupPath(t, project, app),
		projectAppHTTPJSON(t, body), "", http.StatusOK, headers)
	require.NotContains(t, setup, "runtime_failure")
	app, err := project.Store.Integrations().GetProjectApp(ctx, project.ProjectUUID, app.ID)
	require.NoError(t, err)
	path := project.ProjectPath + "/apps/" + testPublicID(t, publicid.KindProjectApp, app.ID)
	get := func() map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	}
	require.NotContains(t, get(), "runtime_failure")
	var versionID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT current_version_id FROM secrets WHERE id=$1`, app.CredentialSecretID).Scan(&versionID)
	require.NoError(t, err)
	claim, found, err := project.Store.Integrations().ClaimAppRuntime(ctx, integrationstore.AppRuntimeRevision{
		ProjectID: project.ProjectUUID, AppID: app.ID, Key: "discord/shard/0",
		SetupRevision: app.SetupRevision, CredentialVersionID: versionID,
	}, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	err = project.Store.Integrations().ReleaseAppRuntime(ctx, claim.Lease, time.Hour, "Discord Gateway closed: 4014")
	require.NoError(t, err)
	response := get()
	require.Equal(t, "active", response["state"])
	failure := testutil.RequireType[map[string]any](t, response["runtime_failure"])
	require.Len(t, failure, 2)
	require.Equal(t, "Discord Gateway closed: 4014", failure["message"])
	_, err = time.Parse(time.RFC3339Nano, testutil.RequireType[string](t, failure["retry_at"]))
	require.NoError(t, err)
	listed := requestJSONWithHeaders(t, handler, http.MethodGet,
		project.ProjectPath+"/apps", "", "", http.StatusOK, headers)
	for _, item := range testutil.RequireType[[]any](t, listed["data"]) {
		require.NotContains(t, testutil.RequireType[map[string]any](t, item), "runtime_failure")
	}
	updated := requestJSONWithHeaders(t, handler, http.MethodPut, path,
		projectAppHTTPJSON(t, projectAppHTTPBody(app.Name, string(app.AppType))), "", http.StatusOK, headers)
	require.NotContains(t, updated, "runtime_failure")
	require.Contains(t, get(), "runtime_failure", "launcher edits do not change the connection revision")
}
