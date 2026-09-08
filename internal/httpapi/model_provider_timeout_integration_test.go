//go:build integration

package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestModelProviderTimeoutAPI(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "provider-timeouts")
	request := func(method, path, body string, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, handler, method, path, body, "", status, authHeaders(project.AdminToken))
	}
	assertTimeouts := func(got map[string]any, total, idle float64) {
		t.Helper()
		if got["request_timeout_ms"] != total || got["idle_timeout_ms"] != idle {
			t.Fatalf("provider timeouts=%v", got)
		}
	}
	base := "/api/v1/orgs/" + project.OrgID
	secret := request(http.MethodPost, base+"/secrets",
		`{"owner":{"kind":"org"},"name":"timeout-key","material":{"kind":"generic","value":"test-key"}}`,
		http.StatusCreated)
	createPath := base + "/model-provider-configs"
	body := fmt.Sprintf(
		`{"name":"timeout-provider","preset":"openai","credential_secret_id":%q,"request_timeout_ms":30000,"idle_timeout_ms":45000}`,
		secret["id"],
	)
	created := request(http.MethodPost, createPath, body, http.StatusCreated)
	provider := createdModelProviderConfig(t, created)
	path := createPath + "/" + testutil.RequireType[string](t, provider["id"])
	assertTimeouts(provider, 30000, 45000)
	replay := fmt.Sprintf(`{"name":"timeout-provider","preset":"openai","credential_secret_id":%q}`, secret["id"])
	replayed := request(http.MethodPost, createPath, replay, http.StatusOK)
	assertTimeouts(createdModelProviderConfig(t, replayed), 30000, 45000)
	assertTimeouts(request(http.MethodPut, path, `{"idle_timeout_ms":60000}`, http.StatusOK), 30000, 60000)
	assertTimeouts(request(http.MethodPut, path, `{"request_timeout_ms":90000}`, http.StatusOK), 90000, 60000)
	for _, field := range []string{"request_timeout_ms", "idle_timeout_ms"} {
		for _, value := range []string{"0", "-1", "2147483648"} {
			request(http.MethodPut, path, fmt.Sprintf(`{%q:%s}`, field, value), http.StatusBadRequest)
			invalid := fmt.Sprintf(`{"name":"invalid-timeout","preset":"openai","credential_secret_id":%q,%q:%s}`,
				secret["id"], field, value)
			request(http.MethodPost, createPath, invalid, http.StatusBadRequest)
		}
	}
	assertTimeouts(request(http.MethodGet, path, "", http.StatusOK), 90000, 60000)
}
