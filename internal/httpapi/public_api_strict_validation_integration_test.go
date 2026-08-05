//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// TestStrictBodyValidationThroughRoutes exercises the shared OpenAPI request
// validator through the real router and middleware chain.
func TestStrictBodyValidationThroughRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "strict-body-validation")
	auth := authHeaders(project.AdminToken)
	secretsPath := "/api/v1/orgs/" + project.OrgID + "/secrets"

	t.Run("missing required body", func(t *testing.T) {
		resp := requestJSONWithHeaders(t, handler, http.MethodPost, secretsPath, ``, "", http.StatusBadRequest, auth)
		if resp["error"] == "" {
			t.Fatalf("empty body should be rejected as required, got response=%v", resp)
		}
	})

	t.Run("path-owned field rejected as unknown", func(t *testing.T) {
		resp := requestJSONWithHeaders(t, handler, http.MethodPost, secretsPath, `{"org_id":"x"}`, "", http.StatusBadRequest, auth)
		if resp["error"] == "" {
			t.Fatalf("path-owned body field should be rejected as unknown, got response=%v", resp)
		}
	})

	noBodyRoutes := map[string]string{
		"AcceptInvitation":  "/api/v1/invitations/inv-missing/accept",
		"DeclineInvitation": "/api/v1/invitations/inv-missing/decline",
	}
	for op, path := range noBodyRoutes {
		for name, body := range map[string]string{"json body": `{"unexpected":true}`, "empty object body": `{}`} {
			t.Run(op+" rejects "+name, func(t *testing.T) {
				resp := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "", http.StatusBadRequest, auth)
				if resp["error"] == "" {
					t.Fatalf("%s should reject a provided body, got response=%v", op, resp)
				}
			})
		}
	}
}
