//go:build integration

package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCronTriggerAppLaunchHTTP(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "app-cron-http")
	profile := createPublicHTTPAgent(t, handler, project, "scheduled-profile", project.AdminToken)
	profileID := testutil.RequireType[string](t, profile["id"])
	app := createSlackHTTPApp(t, ctx, project, "A123", "T123", "Scheduled app")
	appID, err := publicid.Encode(publicid.KindProjectApp, app.ID)
	require.NoError(t, err)
	target := map[string]any{
		"type":                     "app_launch",
		"app_id":                   appID,
		"agent_profile_id":         profileID,
		"destination":              map[string]any{"channel_id": "C123"},
		"opening_message_template": "Daily {{.trigger.local_date}}",
	}
	body := map[string]any{
		"name":             "Daily app task",
		"target":           target,
		"cron":             "0 9 * * *",
		"timezone":         "America/Los_Angeles",
		"message_template": "Run the daily task.",
	}
	path, headers := project.ProjectPath+"/cron-triggers", authHeaders(project.AdminToken)
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"app-cron",
		http.StatusCreated,
		headers,
	)
	require.Equal(t, target, created["target"])
	require.Nil(t, created["last_run"])
	id := testutil.RequireType[string](t, created["id"])
	replay := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"app-cron",
		http.StatusOK,
		headers,
	)
	require.Equal(t, id, replay["id"])
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?app_id="+appID, "", "", http.StatusOK, headers)
	require.Len(t, testutil.RequireType[[]any](t, page["data"]), 1)
	page = requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?agent_profile_id="+profileID,
		"",
		"",
		http.StatusOK,
		headers,
	)
	require.Len(t, testutil.RequireType[[]any](t, page["data"]), 1)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?app_id="+profileID,
		"",
		"",
		http.StatusBadRequest,
		headers,
	)
	target["destination"] = map[string]any{"channel_id": "G456"}
	target["opening_message_template"] = "Revised {{.trigger.local_date}}"
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectAppHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, target, updated["target"])
	for _, destination := range []map[string]any{
		{}, {"channel_id": "D123"}, {"channel_id": "C123", "thread_ts": "1.2"}, {"channel_id": "C123", "guild_id": "123"},
	} {
		target["destination"] = destination
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPatch,
			path+"/"+id,
			projectAppHTTPJSON(t, map[string]any{"target": target}),
			"",
			http.StatusBadRequest,
			headers,
		)
	}
	target["destination"] = map[string]any{"channel_id": "C123"}
	target["opening_message_template"] = strings.Repeat("🚀", 2001)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectAppHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	target["opening_message_template"] = `{{printf "%1024s" "a"}}{{printf "%1024s" "b"}}`
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectAppHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	target["opening_message_template"] = "Daily"
	other := projectAppHTTPSecondProject(t, handler, project)
	foreignProfile := createPublicHTTPAgent(t, handler, other, "foreign-profile", other.AdminToken)
	foreignApp := createSlackHTTPApp(t, ctx, other, "A999", "T999", "Foreign app")
	foreignAppID, err := publicid.Encode(publicid.KindProjectApp, foreignApp.ID)
	require.NoError(t, err)
	target["app_id"] = foreignAppID
	body["name"] = "Foreign app schedule"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectAppHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	target["app_id"] = appID
	target["agent_profile_id"] = testutil.RequireType[string](t, foreignProfile["id"])
	body["name"] = "Foreign profile schedule"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectAppHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	// The diagnostic exposes the public outcome, not raw inbox errors or UUIDs.
	triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, id)
	_, err = pool.Exec(
		ctx,
		`WITH receipt AS (INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload,source,preparation,state,last_error) VALUES ($1,$2,'diagnostic',convert_to('{}','UTF8'),'scheduled_launch','{}','failed','private provider error') RETURNING id) UPDATE cron_triggers SET last_app_receipt_id=(SELECT id FROM receipt) WHERE id=$3`,
		project.ProjectUUID,
		app.ID,
		triggerUUID,
	)
	require.NoError(t, err)
	got := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusOK, headers)
	last := testutil.RequireType[map[string]any](t, got["last_run"])
	require.Equal(t, "failed", last["state"])
	require.Equal(t, "Scheduled app launch failed.", last["failure_message"])
	require.NotContains(t, projectAppHTTPJSON(t, last), "private provider error")
	require.Nil(t, got["failure_report"])
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/apps/"+appID,
		"",
		"",
		http.StatusNoContent,
		headers,
	)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusNotFound, headers)
}
