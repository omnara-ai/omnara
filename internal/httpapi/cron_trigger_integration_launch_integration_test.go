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

func TestCronTriggerIntegrationHTTP(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "integration-cron-http")
	profile := createPublicHTTPAgent(t, handler, project, "scheduled-profile", project.AdminToken)
	profileID := testutil.RequireType[string](t, profile["id"])
	integration := createSlackHTTPIntegration(t, ctx, project, "A123", "T123", "Scheduled integration")
	integrationID, err := publicid.Encode(publicid.KindProjectIntegration, integration.ID)
	require.NoError(t, err)
	settings := map[string]any{
		"agent_profile_id": profileID, "channel_id": "C123",
		"opening_message_template": "Daily {{.trigger.local_date}}", "message_template": "Run the daily task.",
	}
	target := map[string]any{"type": "integration", "integration_id": integrationID, "settings": settings}
	body := map[string]any{
		"name":     "Daily integration task",
		"target":   target,
		"cron":     "0 9 * * *",
		"timezone": "America/Los_Angeles",
	}
	path, headers := project.ProjectPath+"/cron-triggers", authHeaders(project.AdminToken)
	requestJSONWithHeaders(t, handler, http.MethodPost, path, projectIntegrationHTTPJSON(t, map[string]any{
		"name": "Missing task", "cron": "0 9 * * *", "target": map[string]any{"type": "profile", "agent_profile_id": profileID},
	}), "", http.StatusBadRequest, headers)
	unsafeSettings := map[string]any{}
	for name, value := range settings {
		unsafeSettings[name] = value
	}
	unsafeSettings["message_template"] = "Run\x00"
	requestJSONWithHeaders(t, handler, http.MethodPost, path, projectIntegrationHTTPJSON(t, map[string]any{
		"name": "Unsafe integration task", "cron": "0 9 * * *",
		"target": map[string]any{"type": "integration", "integration_id": integrationID, "settings": unsafeSettings},
	}), "", http.StatusBadRequest, headers)
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectIntegrationHTTPJSON(t, body),
		"integration-cron",
		http.StatusCreated,
		headers,
	)
	require.Equal(t, target, created["target"])
	require.Nil(t, created["last_run"])
	require.NotContains(t, created, "message_template")
	id := testutil.RequireType[string](t, created["id"])
	replay := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectIntegrationHTTPJSON(t, body),
		"integration-cron",
		http.StatusOK,
		headers,
	)
	require.Equal(t, id, replay["id"])
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path+
		"?integration_id="+integrationID, "", "", http.StatusOK, headers)
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
	require.Len(t, testutil.RequireType[[]any](t, page["data"]), 0)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		path+"?integration_id="+profileID,
		"",
		"",
		http.StatusBadRequest,
		headers,
	)
	settings["channel_id"] = "G456"
	settings["opening_message_template"] = "Revised {{.trigger.local_date}}"
	updated := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectIntegrationHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusOK,
		headers,
	)
	require.Equal(t, target, updated["target"])
	for _, channel := range []string{"", "D123", "bad"} {
		settings["channel_id"] = channel
		requestJSONWithHeaders(t, handler, http.MethodPatch, path+"/"+id,
			projectIntegrationHTTPJSON(t, map[string]any{"target": target}), "", http.StatusBadRequest, headers)
	}
	settings["channel_id"] = "C123"
	settings["guild_id"] = "123"
	requestJSONWithHeaders(t, handler, http.MethodPatch, path+"/"+id,
		projectIntegrationHTTPJSON(t, map[string]any{"target": target}), "", http.StatusBadRequest, headers)
	delete(settings, "guild_id")
	settings["opening_message_template"] = strings.Repeat("🚀", 2001)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectIntegrationHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	for _, opening := range []string{
		`{{printf "%1024s" "a"}}{{printf "%1024s" "b"}}`, "Daily\x00", `Daily{{printf "%c" 0}}`,
	} {
		settings["opening_message_template"] = opening
		requestJSONWithHeaders(t, handler, http.MethodPatch, path+"/"+id,
			projectIntegrationHTTPJSON(t, map[string]any{"target": target}), "", http.StatusBadRequest, headers)
	}
	unchanged := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusOK, headers)
	require.Equal(t, updated["target"], unchanged["target"])
	settings["opening_message_template"] = "Daily"
	other := projectIntegrationHTTPSecondProject(t, handler, project)
	foreignProfile := createPublicHTTPAgent(t, handler, other, "foreign-profile", other.AdminToken)
	foreignIntegration := createSlackHTTPIntegration(t, ctx, other, "A999", "T999", "Foreign integration")
	foreignIntegrationID, err := publicid.Encode(publicid.KindProjectIntegration, foreignIntegration.ID)
	require.NoError(t, err)
	target["integration_id"] = foreignIntegrationID
	body["name"] = "Foreign integration schedule"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectIntegrationHTTPJSON(t, body),
		"",
		http.StatusNotFound,
		headers,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectIntegrationHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusBadRequest,
		headers,
	)
	target["integration_id"] = integrationID
	settings["agent_profile_id"] = testutil.RequireType[string](t, foreignProfile["id"])
	body["name"] = "Foreign profile schedule"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		projectIntegrationHTTPJSON(t, body),
		"",
		http.StatusCreated,
		headers,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		path+"/"+id,
		projectIntegrationHTTPJSON(t, map[string]any{"target": target}),
		"",
		http.StatusOK,
		headers,
	)
	triggerUUID := mustPublicHTTPID(t, publicid.KindCronTrigger, id)
	_, err = pool.Exec(
		ctx,
		`WITH receipt AS (
 INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload,source,state,last_error,completed_at)
 VALUES ($1,$2,'diagnostic',convert_to('{}','UTF8'),'scheduled','failed','private provider error',now()) RETURNING id
) UPDATE cron_triggers SET last_integration_receipt_id=(SELECT id FROM receipt) WHERE id=$3`,
		project.ProjectUUID,
		integration.ID,
		triggerUUID,
	)
	require.NoError(t, err)
	got := requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusOK, headers)
	last := testutil.RequireType[map[string]any](t, got["last_run"])
	require.Equal(t, "failed", last["state"])
	require.Equal(t, "Scheduled integration action failed.", last["failure_message"])
	require.NotContains(t, projectIntegrationHTTPJSON(t, last), "private provider error")
	require.Nil(t, got["failure_report"])
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodDelete,
		project.ProjectPath+"/integrations/"+integrationID,
		"",
		"",
		http.StatusNoContent,
		headers,
	)
	requestJSONWithHeaders(t, handler, http.MethodGet, path+"/"+id, "", "", http.StatusNotFound, headers)
}
