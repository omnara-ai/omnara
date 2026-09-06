//go:build integration

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestAuthorizationStorageFailureRemainsOperational(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "authorization-storage-failure")
	server := mustNewServer(t, project.Store)
	requestCtx := context.WithValue(
		ctx,
		principalContextKey{},
		httpUserPrincipal(project.AdminUserUUID),
	)
	pool.Close()

	projectErr := server.authorizeProject(
		requestCtx,
		project.OrgUUID,
		project.ProjectUUID,
		identitystore.ProjectActionRead,
	)
	if projectErr == nil {
		t.Fatal("project authorization did not return an error")
	}
	assertOperationalAuthorizationError(t, *projectErr)
	assertOperationalAuthorizationError(
		t,
		server.authorizeOrgManage(requestCtx, project.OrgUUID),
	)
}

func assertOperationalAuthorizationError(t *testing.T, err error) {
	t.Helper()
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("authorization error = %T %v, want ResponseError", err, err)
	}
	if responseErr.Status != http.StatusInternalServerError {
		t.Fatalf("authorization status = %d, want %d", responseErr.Status, http.StatusInternalServerError)
	}
	if errors.Unwrap(responseErr) == nil {
		t.Fatal("authorization error did not preserve its cause")
	}
}
