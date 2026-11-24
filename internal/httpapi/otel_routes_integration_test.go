//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func otelExporterIDs(t *testing.T, listed map[string]any) []string {
	t.Helper()
	ids := []string{}
	for _, item := range testutil.RequireType[[]any](t, listed["data"]) {
		ids = append(ids, testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, item)["id"]))
	}
	return ids
}

func TestOtelExporterRoutes(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "otel-exporter-routes")
	orgPath := "/api/v1/orgs/" + f.project.OrgID
	endpoint := f.project.ProjectPath + "/otel-exporters"
	orgEndpoint := orgPath + "/otel-exporters"
	admin := authHeaders(f.project.AdminToken)
	// An operator reads the project but does not manage it.
	operator := authHeaders(customIntegrationHTTPKey(t, f.handler, f.project, "otel-operator", "operator"))
	outsiderKey := requestJSONWithHeaders(t, f.handler, http.MethodPost, orgPath+"/api-keys",
		integrationHTTPJSON(t, map[string]any{"name": "otel-outsider", "org_role": "member"}),
		"", http.StatusCreated, f.project.adminBrowserAuthHeaders())
	outsider := authHeaders(testutil.RequireType[string](t, outsiderKey["token"]))

	newSecret := func(name, owner string) (string, secretstore.SecretRecord) {
		input := secretstore.CreateSecretInput{
			OrgID: f.project.OrgUUID, OwnerKind: owner, Name: name,
			Material: secrets.GenericMaterial{Value: "sk-lf-secret"}, Actor: httpUserPrincipal(f.project.AdminUserUUID),
		}
		if owner == "project" {
			input.OwnerProjectID = f.project.ProjectUUID
		}
		secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), input)
		require.NoError(t, err)
		return testPublicID(t, publicid.KindSecret, secret.ID), secret
	}
	secretID, _ := newSecret("otel-exporter-key", "project")

	body := integrationHTTPJSON(t, map[string]any{
		"endpoint": "https://otel.example.com/v1/traces", "variant": "langfuse",
		"auth_kind": "basic", "auth_options": map[string]any{"username": "pk-lf-123"},
		"credential_secret_id": secretID,
		"headers":              map[string]any{"x-langfuse-ingestion-version": "4"}, "sample_percent": 12.345,
	})
	requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusForbidden, operator)
	created := requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusCreated, admin)
	require.Equal(t, f.project.OrgID, created["org_id"])
	require.Equal(t, f.project.ProjectID, created["project_id"])
	require.NotContains(t, created, "scope")
	require.Equal(t, "langfuse", created["variant"])
	require.Equal(t, "protobuf", created["encoding"])
	require.Equal(t, "basic", created["auth_kind"])
	require.Equal(t, map[string]any{"username": "pk-lf-123"}, created["auth_options"])
	require.Equal(t, secretID, created["credential_secret_id"])
	require.Equal(t, 12.345, created["sample_percent"])
	require.Equal(t, map[string]any{"x-langfuse-ingestion-version": "4"}, created["headers"])
	require.Equal(t, map[string]any{}, created["secret_headers"])
	requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, body, "", http.StatusConflict, admin)
	id := testutil.RequireType[string](t, created["id"])
	for _, reader := range []map[string]string{admin, operator} {
		require.Equal(t, created,
			requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id, "", "", http.StatusOK, reader))
	}
	requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id, "", "", http.StatusNotFound, outsider)

	defaults := requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint,
		`{"endpoint":"https://defaults.example.com/v1/traces"}`, "", http.StatusCreated, admin)
	require.Equal(t, "otel", defaults["variant"])
	require.Equal(t, "none", defaults["auth_kind"])
	require.Equal(t, map[string]any{}, defaults["auth_options"])
	require.NotContains(t, defaults, "credential_secret_id")
	require.EqualValues(t, 100, defaults["sample_percent"])
	defaultsID := testutil.RequireType[string](t, defaults["id"])

	// An organization secret is usable only once it is granted to the project.
	orgSecretID, orgSecret := newSecret("otel-exporter-org-key", "org")
	orgSecretBody := `{"endpoint":"https://org-secret.example.com/v1/traces","auth_kind":"bearer_token","credential_secret_id":"` +
		orgSecretID + `"}`
	requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, orgSecretBody, "", http.StatusBadRequest, admin)
	_, err := f.project.Store.Secrets().CreateSecretGrant(t.Context(), secretstore.CreateSecretGrantInput{
		OrgID: f.project.OrgUUID, SecretID: orgSecret.ID, TargetProjectID: f.project.ProjectUUID,
		Actor: httpUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	granted := requestJSONWithHeaders(
		t, f.handler, http.MethodPost, endpoint, orgSecretBody, "", http.StatusCreated, admin)
	grantedID := testutil.RequireType[string](t, granted["id"])

	for _, invalid := range []string{
		`{"scope":{"type":"project","project_id":"` + f.project.ProjectID + `"},"endpoint":"https://other.example.com"}`,
		`{"project_id":"` + f.project.ProjectID + `","endpoint":"https://other.example.com"}`,
		`{"endpoint":"http://otel.example.com/v1/traces"}`,
		`{"endpoint":"https://other.example.com","variant":"datadog"}`,
		`{"endpoint":"https://other.example.com","sample_percent":101}`,
		`{"endpoint":"https://other.example.com","encoding":"grpc"}`,
		`{"endpoint":"https://other.example.com","variant":"braintrust","encoding":"json"}`,
		`{"endpoint":"https://other.example.com","headers":{"Content-Type":"text/plain"}}`,
		`{"endpoint":"https://other.example.com","headers":{"Content-Encoding":"gzip"}}`,
		`{"endpoint":"https://other.example.com","headers":{"Transfer-Encoding":"chunked"}}`,
		`{"endpoint":"https://other.example.com","auth_kind":"bearer_token"}`,
		`{"endpoint":"https://other.example.com","auth_kind":"basic","credential_secret_id":"` + secretID + `"}`,
		`{"endpoint":"https://other.example.com","auth_kind":"api_key_header","credential_secret_id":"` + secretID + `"}`,
		`{"endpoint":"https://other.example.com","auth_kind":"bearer_token","credential_secret_id":"` + secretID + `","headers":{"Authorization":"Bearer dup"}}`,
		`{}`,
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, invalid, "", http.StatusBadRequest, admin)
	}

	// The project list and the organization-wide list page the same exporters;
	// the organization one is filtered to projects the caller can read.
	want := []string{id, defaultsID, grantedID}
	for _, reader := range []map[string]string{admin, operator} {
		for _, path := range []string{endpoint, orgEndpoint} {
			listed := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, reader)
			require.Equal(t, want, otelExporterIDs(t, listed), path)
			require.Nil(t, listed["next_cursor"])
			first := requestJSONWithHeaders(t, f.handler, http.MethodGet, path+"?limit=2", "", "", http.StatusOK, reader)
			require.Equal(t, want[:2], otelExporterIDs(t, first), path)
			cursor := testutil.RequireType[string](t, first["next_cursor"])
			rest := requestJSONWithHeaders(t, f.handler, http.MethodGet, path+"?limit=2&cursor="+cursor,
				"", "", http.StatusOK, reader)
			require.Equal(t, want[2:], otelExporterIDs(t, rest), path)
		}
	}
	projectCursor := testutil.RequireType[string](t, requestJSONWithHeaders(
		t, f.handler, http.MethodGet, endpoint+"?limit=1", "", "", http.StatusOK, admin)["next_cursor"])
	requestJSONWithHeaders(t, f.handler, http.MethodGet, orgEndpoint+"?cursor="+projectCursor,
		"", "", http.StatusBadRequest, admin)
	require.Empty(t, otelExporterIDs(t,
		requestJSONWithHeaders(t, f.handler, http.MethodGet, orgEndpoint, "", "", http.StatusOK, outsider)))
	requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint, "", "", http.StatusNotFound, outsider)
	// The organization-wide view is read-only.
	requestJSONWithHeaders(t, f.handler, http.MethodPost, orgEndpoint, body, "", http.StatusNotFound, admin)
	requestJSONWithHeaders(t, f.handler, http.MethodGet, orgEndpoint+"/"+id, "", "", http.StatusNotFound, admin)

	patch := `{"variant":"otel","encoding":"json","auth_kind":"api_key_header","auth_options":{"header_name":"x-api-key"},"sample_percent":0,"headers":{}}`
	requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id, patch, "", http.StatusForbidden, operator)
	updated := requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id, patch, "", http.StatusOK, admin)
	require.Equal(t, "otel", updated["variant"])
	require.Equal(t, "json", updated["encoding"])
	require.Equal(t, "api_key_header", updated["auth_kind"])
	require.Equal(t, map[string]any{"header_name": "x-api-key"}, updated["auth_options"])
	require.Equal(t, secretID, updated["credential_secret_id"])
	require.EqualValues(t, 0, updated["sample_percent"])
	require.Equal(t, map[string]any{}, updated["headers"])
	require.Equal(t, "https://otel.example.com/v1/traces", updated["endpoint"])
	require.Equal(t, f.project.ProjectID, updated["project_id"])

	clearedAuth := requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id,
		`{"auth_kind":"none","credential_secret_id":null}`, "", http.StatusOK, admin)
	require.Equal(t, "none", clearedAuth["auth_kind"])
	require.Equal(t, map[string]any{}, clearedAuth["auth_options"])
	require.NotContains(t, clearedAuth, "credential_secret_id")

	requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id, `{}`, "", http.StatusBadRequest, admin)
	// A variant change is checked against the encoding the exporter already has.
	requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id,
		`{"variant":"langsmith"}`, "", http.StatusBadRequest, admin)

	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+id, "", "", http.StatusForbidden, operator)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+id, "", "", http.StatusNoContent, admin)
	requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id, "", "", http.StatusNotFound, admin)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, endpoint+"/"+id, "", "", http.StatusNotFound, admin)
	require.Equal(t, want[1:], otelExporterIDs(t,
		requestJSONWithHeaders(t, f.handler, http.MethodGet, orgEndpoint, "", "", http.StatusOK, admin)))
}
