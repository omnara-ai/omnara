//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestModelProviderConfigPatchIdentifiesUnavailableSecret(t *testing.T) {
	for _, field := range []string{"secret_headers.X-Gateway-Key", "credential_secret_id"} {
		t.Run(field, func(t *testing.T) {
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			handler := newIntegrationServer(pool)
			project := bootstrapPublicHTTPProject(t, handler, "provider-secret-repair")
			path := "/api/v1/orgs/" + project.OrgID
			createSecret := func(name string) string {
				return testutil.RequireType[string](t, requestJSONWithHeaders(
					t, handler, http.MethodPost, path+"/secrets",
					`{"owner":{"kind":"org"},"name":"`+name+`","material":{"kind":"generic","value":"private-provider-value"}}`,
					"", http.StatusCreated, authHeaders(project.AdminToken),
				)["id"])
			}
			credentialID, headerID := createSecret("credential"), createSecret("header")
			config := createdModelProviderConfig(t, requestJSONWithHeaders(
				t, handler, http.MethodPost, path+"/model-provider-configs",
				`{"name":"gateway","preset":"openai","credential_secret_id":"`+credentialID+
					`","secret_headers":{"X-Gateway-Key":"`+headerID+`"}}`,
				"", http.StatusCreated, authHeaders(project.AdminToken),
			))
			configPath := path + "/model-provider-configs/" + testutil.RequireType[string](t, config["id"])
			missingID, repair := headerID, `{"secret_headers":{}}`
			if field == "credential_secret_id" {
				missingID, repair = credentialID, `{"credential_secret_id":"`+headerID+`"}`
			}
			// Migration preserves old references to soft-deleted secrets so they
			// fail closed. Reproduce that state without bypassing the new guard
			// through the public deletion API.
			missingUUID := mustPublicHTTPID(t, publicid.KindSecret, missingID)
			_, err := pool.Exec(ctx, "UPDATE secrets SET deleted_at = now() WHERE org_id = $1 AND id = $2",
				project.OrgUUID, missingUUID)
			require.NoError(t, err)
			response := requestJSONWithHeaders(
				t, handler, http.MethodPut, configPath, `{"request_timeout_ms":30000}`,
				"", http.StatusBadRequest, authHeaders(project.AdminToken),
			)
			require.Equal(t, "invalid_request", response["code"])
			message := testutil.RequireType[string](t, response["error"])
			require.Contains(t, message, field)
			require.Contains(t, message, "remove or replace")
			require.NotContains(t, message, missingID)
			require.NotContains(t, message, missingUUID.String())
			require.NotContains(t, message, "private-provider-value")
			read := requestJSONWithHeaders(
				t, handler, http.MethodGet, configPath, "", "", http.StatusOK, authHeaders(project.AdminToken),
			)
			require.Equal(t, config["request_timeout_ms"], read["request_timeout_ms"], "failed patch must not persist")
			requestJSONWithHeaders(t, handler, http.MethodPut, configPath, repair,
				"", http.StatusOK, authHeaders(project.AdminToken))
			updated := requestJSONWithHeaders(
				t, handler, http.MethodPut, configPath, `{"request_timeout_ms":30000}`,
				"", http.StatusOK, authHeaders(project.AdminToken),
			)
			require.Equal(t, float64(30000), updated["request_timeout_ms"])
		})
	}
}
