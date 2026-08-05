//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestErrorResponseCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "error-codes")

	assertError := func(t *testing.T, response map[string]any, wantCode string) {
		t.Helper()
		if got, _ := response["code"].(string); got != wantCode {
			t.Fatalf("error code = %q, want %q (response %v)", got, wantCode, response)
		}
		if message, _ := response["error"].(string); message == "" {
			t.Fatalf("error message missing from response %v", response)
		}
	}

	t.Run("unauthorized", func(t *testing.T) {
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			project.ProjectPath+"/agents",
			"",
			"",
			http.StatusUnauthorized,
			nil,
		)
		assertError(t, response, "unauthorized")
	})

	t.Run("route not found", func(t *testing.T) {
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			"/api/v1/does-not-exist",
			"",
			"",
			http.StatusNotFound,
			authHeaders(project.AdminToken),
		)
		assertError(t, response, "not_found")
	})

	t.Run("oversized body", func(t *testing.T) {
		body := `{"padding":"` + strings.Repeat("a", int(maxRequestBodyBytes)) + `"}`
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			project.ProjectPath+"/agents",
			body,
			"",
			http.StatusRequestEntityTooLarge,
			authHeaders(project.AdminToken),
		)
		assertError(t, response, "request_too_large")
	})

	t.Run("resource not found", func(t *testing.T) {
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			project.ProjectPath+"/agents/agt_aaaaaaaaaaaaaaaaaaaaaaaaaa",
			"",
			"",
			http.StatusNotFound,
			authHeaders(project.AdminToken),
		)
		assertError(t, response, "not_found")
	})

	// Method mismatches on known paths surface as 404: the route identity
	// includes the method, so an undeclared method is an unknown route.
	t.Run("method mismatch", func(t *testing.T) {
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodDelete,
			"/api/v1/orgs",
			"",
			"",
			http.StatusNotFound,
			authHeaders(project.AdminToken),
		)
		assertError(t, response, "not_found")
	})

	t.Run("validation failed", func(t *testing.T) {
		response := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			project.ProjectPath+"/agents?bogus_param=1",
			"",
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
		assertError(t, response, "validation_failed")
	})
}
