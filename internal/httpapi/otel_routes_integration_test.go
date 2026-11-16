//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestOtelExporterRoutes(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "otel-exporter-routes")
	endpoint := "/api/v1/orgs/" + f.project.OrgID + "/otel-exporters"
	admin := authHeaders(f.project.AdminToken)
	operator := authHeaders(customIntegrationHTTPKey(t, f.handler, f.project, "otel-operator", "operator"))
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		integrationHTTPJSON(t, map[string]any{"config": f.configID, "profile": f.profileID}),
		"otel-agent", http.StatusCreated, authHeaders(f.launchToken))
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])

	scopes := []map[string]any{
		{"type": "org"},
		{"type": "project", "project_id": f.project.ProjectID},
		{"type": "agent", "project_id": f.project.ProjectID, "agent_id": agentID},
	}
	var ids []string
	for _, scope := range scopes {
		body := integrationHTTPJSON(t, map[string]any{
			"scope": scope, "endpoint": "https://otel.example.com/v1/traces", "variant": "langfuse",
			"headers": map[string]any{"x-langfuse-ingestion-version": "4"}, "sample_percent": 12.345,
		})
		requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusForbidden, operator)
		created := requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusCreated, admin)
		require.Equal(t, scope, created["scope"])
		require.Equal(t, "langfuse", created["variant"])
		require.Equal(t, "json", created["encoding"])
		require.Equal(t, 12.345, created["sample_percent"])
		require.Equal(t, map[string]any{"x-langfuse-ingestion-version": "4"}, created["headers"])
		require.Equal(t, map[string]any{}, created["secret_headers"])
		requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusConflict, admin)
		id := testutil.RequireType[string](t, created["id"])
		ids = append(ids, id)
		require.Equal(t, created,
			requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id, "", "", http.StatusOK, admin))
	}

	defaults := requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint,
		`{"scope":{"type":"org"},"endpoint":"https://defaults.example.com/v1/traces"}`, "", http.StatusCreated, admin)
	require.Equal(t, "otel", defaults["variant"])
	require.EqualValues(t, 100, defaults["sample_percent"])

	for _, invalid := range []string{
		`{"scope":{"type":"org"},"endpoint":"http://otel.example.com/v1/traces"}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","variant":"datadog"}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","sample_percent":101}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","encoding":"grpc"}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","headers":{"Content-Type":"text/plain"}}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","headers":{"Content-Encoding":"gzip"}}`,
		`{"scope":{"type":"org"},"endpoint":"https://other.example.com","headers":{"Transfer-Encoding":"chunked"}}`,
		`{"scope":{"type":"agent","agent_id":"` + agentID + `"},"endpoint":"https://other.example.com"}`,
		`{"endpoint":"https://other.example.com"}`,
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, invalid, "", http.StatusBadRequest, admin)
	}

	listed := requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint, "", "", http.StatusOK, admin)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 4)
	projectQuery := "&project_id=" + f.project.ProjectID
	for query, expected := range map[string][]string{
		"?scope_type=org":                                           {ids[0], testutil.RequireType[string](t, defaults["id"])},
		"?scope_type=project" + projectQuery:                        {ids[1]},
		"?scope_type=agent" + projectQuery + "&agent_id=" + agentID: {ids[2]},
	} {
		listed = requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+query, "", "", http.StatusOK, admin)
		var got []string
		for _, item := range testutil.RequireType[[]any](t, listed["data"]) {
			got = append(got, testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, item)["id"]))
		}
		require.Equal(t, expected, got, query)
	}
	for _, invalid := range []string{
		"?project_id=" + f.project.ProjectID, "?agent_id=" + agentID, "?scope_type=project",
		"?scope_type=agent&agent_id=" + agentID, "?scope_type=org" + projectQuery, "?scope_type=profile",
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+invalid, "", "", http.StatusBadRequest, admin)
	}
	requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint, "", "", http.StatusForbidden, operator)

	updated := requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+ids[0],
		`{"variant":"braintrust","encoding":"protobuf","sample_percent":0,"headers":{}}`, "", http.StatusOK, admin)
	require.Equal(t, "braintrust", updated["variant"])
	require.Equal(t, "protobuf", updated["encoding"])
	require.EqualValues(t, 0, updated["sample_percent"])
	require.Equal(t, map[string]any{}, updated["headers"])
	require.Equal(t, "https://otel.example.com/v1/traces", updated["endpoint"])
	requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+ids[0], `{}`, "", http.StatusBadRequest, admin)

	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+ids[0], "", "", http.StatusForbidden, operator)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+ids[0], "", "", http.StatusNoContent, admin)
	requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+ids[0], "", "", http.StatusNotFound, admin)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+ids[0], "", "", http.StatusNotFound, admin)
}
