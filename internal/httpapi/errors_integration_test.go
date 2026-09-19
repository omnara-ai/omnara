//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestRequestLogPostgresTimeouts(t *testing.T) {
	t.Parallel()
	setupCtx := context.Background()
	pool := poolWithQueryTracer(t, setupCtx, openIntegrationDB(t, setupCtx),
		metrics.NewDBRecorder(metrics.New(), metrics.SubsystemDB))
	holder, err := pool.Begin(setupCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Rollback(setupCtx) })
	_, err = holder.Exec(setupCtx, "SELECT pg_advisory_xact_lock(344)")
	require.NoError(t, err)

	for _, tt := range []struct {
		name, setting, query, kind, code, source string
	}{
		{"statement", "statement_timeout", "SELECT pg_sleep(1)", "postgres_statement_timeout", "57014", "postgres"},
		{"lock", "lock_timeout", "SELECT pg_advisory_xact_lock(344)", "postgres_lock_timeout", "55P03", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(setupCtx) }()
			_, err = tx.Exec(ctx, "SELECT set_config($1, '25ms', true)", tt.setting)
			require.NoError(t, err)

			buf, logger := newRequestEventCapture()
			handler := requestLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := tx.Exec(r.Context(), "-- name: TimeoutQuery :exec\n"+tt.query)
				if err == nil {
					t.Error("query succeeded instead of timing out")
					return
				}
				openAPIResponseErrorHandler(w, r, err)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
			require.Equal(t, http.StatusInternalServerError, response.Code)
			event := decodeRequestEvent(t, buf)
			for key, want := range map[string]string{
				"name": "TimeoutQuery", "error_kind": tt.kind, "sqlstate": tt.code, "cancel_source": tt.source,
			} {
				got, _ := event["db.queries.0."+key].(string)
				require.Equal(t, want, got, key)
			}
			require.Equal(t, tt.kind, event["error.message"])
		})
	}
}

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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
		t.Parallel()
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
