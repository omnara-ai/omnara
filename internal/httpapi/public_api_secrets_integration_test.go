//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestPublicCanonicalSecretsAndProjectAvailability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "canonical-secrets")
	for name, body := range map[string]string{
		"missing owner":                `{"name":"bad","material":{"kind":"generic","value":"x"}}`,
		"missing project id":           `{"owner":{"kind":"project"},"name":"bad","material":{"kind":"generic","value":"x"}}`,
		"user id not allowed":          `{"owner":{"kind":"user","user_id":"usr_agjaaaaaaaaaaaaaaaaaaaaaae"},"name":"bad","material":{"kind":"generic","value":"x"}}`,
		"incomplete refresh":           `{"owner":{"kind":"org"},"name":"bad","material":{"kind":"oauth_token_set","access_token":"access","refresh":{"refresh_token":"refresh"}}}`,
		"aws external id without role": `{"owner":{"kind":"org"},"name":"bad","material":{"kind":"aws_credentials","access_key_id":"AKIAEXAMPLE","secret_access_key":"secret","external_id":"external"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/secrets",
				body, "", http.StatusBadRequest, authHeaders(project.AdminToken))
		})
	}

	second := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/projects", `{"name":"Consumer"}`,
		"canonical-secret-consumer", http.StatusCreated, authHeaders(project.AdminToken))
	secondID := second["id"].(string)
	secondUUID := mustPublicHTTPID(t, publicid.KindProject, secondID)
	secondPath := "/api/v1/orgs/" + project.OrgID + "/projects/" + secondID
	otherOrg := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs", `{"name":"Other Secret Owner Org"}`,
		"canonical-secret-other-org", http.StatusCreated, authHeaders(project.AdminToken))
	otherProject := otherOrg["project"].(map[string]any)
	requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+otherProject["id"].(string)+`"},"name":"cross-org","material":{"kind":"generic","value":"x"}}`,
		"", http.StatusForbidden, authHeaders(project.AdminToken))

	orgSecret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"org-key","metadata":{"env":"prod"},"material":{"kind":"generic","value":"org-secret-value"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	orgSecretID := orgSecret["id"].(string)
	if orgSecret["management_kind"] != string(management.Tenant) {
		t.Fatalf("org secret management_kind = %v, want tenant", orgSecret["management_kind"])
	}
	assertSecretOwner(t, orgSecret, secretstore.SecretOwnerOrg, "")
	assertSecretRedacted(t, orgSecret)
	gotOrg := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	assertSecretOwner(t, gotOrg, secretstore.SecretOwnerOrg, "")
	updatedOrg := requestJSONWithHeaders(t, handler, http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID,
		`{"metadata":{"env":"production"}}`, "", http.StatusOK, authHeaders(project.AdminToken))
	if updatedOrg["metadata"].(map[string]any)["env"] != "production" {
		t.Fatalf("org update = %+v", updatedOrg)
	}
	rotatedOrg := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID+"/versions",
		`{"material":{"kind":"generic","value":"rotated-org-value"}}`, "", http.StatusOK, authHeaders(project.AdminToken))
	if rotatedOrg["current_version_number"] != float64(2) {
		t.Fatalf("org rotation = %+v", rotatedOrg)
	}
	awsSecret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"aws-read-only","material":{"kind":"aws_credentials","access_key_id":"AKIAEXAMPLE","secret_access_key":"secret","session_token":"session-token","role_arn":"arn:aws:iam::123456789012:role/ReadOnly"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	if awsSecret["kind"] != "aws_credentials" {
		t.Fatalf("aws secret = %+v", awsSecret)
	}
	assertSecretRedacted(t, awsSecret)

	projectSecret := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"project-key","material":{"kind":"generic","value":"project-secret-value"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	projectSecretID := projectSecret["id"].(string)
	assertSecretOwner(t, projectSecret, secretstore.SecretOwnerProject, project.ProjectID)
	gotProject := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+projectSecretID,
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	assertSecretOwner(t, gotProject, secretstore.SecretOwnerProject, project.ProjectID)
	requestJSONWithHeaders(t, handler, http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+projectSecretID,
		`{"owner":{"kind":"org"}}`, "", http.StatusBadRequest, authHeaders(project.AdminToken))

	list := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets", "", "", http.StatusOK,
		authHeaders(project.AdminToken))["data"].([]any)
	if !containsPublicSecret(list, orgSecretID) || !containsPublicSecret(list, projectSecretID) {
		t.Fatalf("canonical list missing mixed owners: %+v", list)
	}
	filtered := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets?owner_kind=project&owner_project_id="+project.ProjectID,
		"", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(filtered) != 1 || filtered[0].(map[string]any)["id"] != projectSecretID {
		t.Fatalf("project owner filter mismatch: %+v", filtered)
	}

	direct := requestJSONWithHeaders(t, handler, http.MethodGet, project.ProjectPath+"/secrets",
		"", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(direct) != 1 {
		t.Fatalf("direct inventory = %+v", direct)
	}
	assertProjectAccess(t, direct[0].(map[string]any), projectSecretID, "direct", "")
	directOnly := requestJSONWithHeaders(t, handler, http.MethodGet,
		project.ProjectPath+"/secrets?availability_source=direct&owner_kind=project",
		"", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(directOnly) != 1 {
		t.Fatalf("direct availability filter = %+v", directOnly)
	}
	assertProjectAccess(t, directOnly[0].(map[string]any), projectSecretID, "direct", "")

	requestJSONWithHeaders(t, handler, http.MethodGet, secondPath+"/secrets/"+orgSecretID,
		"", "", http.StatusNotFound, authHeaders(project.AdminToken))
	grant := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID+"/grants",
		`{"target_project_id":"`+secondID+`"}`, "", http.StatusCreated, authHeaders(project.AdminToken))
	grantID := grant["id"].(string)
	granted := requestJSONWithHeaders(t, handler, http.MethodGet, secondPath+"/secrets",
		"", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(granted) != 1 {
		t.Fatalf("granted inventory = %+v", granted)
	}
	assertProjectAccess(t, granted[0].(map[string]any), orgSecretID, "grant", grantID)
	grantedOnly := requestJSONWithHeaders(t, handler, http.MethodGet,
		secondPath+"/secrets?availability_source=grant&owner_kind=org",
		"", "", http.StatusOK, authHeaders(project.AdminToken))["data"].([]any)
	if len(grantedOnly) != 1 {
		t.Fatalf("grant availability filter = %+v", grantedOnly)
	}
	assertProjectAccess(t, grantedOnly[0].(map[string]any), orgSecretID, "grant", grantID)

	target, targetToken := createHTTPOrgMemberToken(t, ctx, pool, store, project.OrgUUID, "grant-target")
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{OrgID: project.OrgUUID,
		ProjectID: secondUUID, UserID: target.ID, Role: authz.ProjectRoleDeveloper}); err != nil {
		t.Fatalf("add target manager: %v", err)
	}
	requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID, "", "", http.StatusNotFound,
		authHeaders(targetToken))
	requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID+"/grants", "", "", http.StatusNotFound,
		authHeaders(targetToken))
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID+"/grants/"+grantID,
		"", "", http.StatusNoContent, authHeaders(targetToken))
	requestJSONWithHeaders(t, handler, http.MethodGet, secondPath+"/secrets/"+orgSecretID,
		"", "", http.StatusNotFound, authHeaders(targetToken))
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+orgSecretID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))

	updated := requestJSONWithHeaders(t, handler, http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+projectSecretID,
		`{"metadata":{"rotated":"true"}}`, "", http.StatusOK, authHeaders(project.AdminToken))
	if updated["metadata"].(map[string]any)["rotated"] != "true" {
		t.Fatalf("update = %+v", updated)
	}
	rotated := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+projectSecretID+"/versions",
		`{"material":{"kind":"generic","value":"rotated-value"}}`, "", http.StatusOK, authHeaders(project.AdminToken))
	if rotated["current_version_number"] != float64(2) {
		t.Fatalf("rotation = %+v", rotated)
	}
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+projectSecretID,
		"", "", http.StatusNoContent, authHeaders(project.AdminToken))
}

func TestPublicUserOwnedSecretAndGrantParties(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "user-secret-parties")
	owner, ownerToken := createHTTPOrgMemberToken(t, ctx, pool, store, project.OrgUUID, "personal-owner")
	_, otherToken := createHTTPOrgMemberToken(t, ctx, pool, store, project.OrgUUID, "unrelated-member")
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{OrgID: project.OrgUUID,
		ProjectID: project.ProjectUUID, UserID: owner.ID, Role: authz.ProjectRoleDeveloper}); err != nil {
		t.Fatal(err)
	}

	personal := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"user"},"name":"personal","material":{"kind":"oauth_token_set","access_token":"personal-access","refresh":{"refresh_token":"personal-refresh","token_endpoint":"https://issuer.example/token","client_id":"client-id","resource":"https://mcp.example"}}}`,
		"", http.StatusCreated, authHeaders(ownerToken))
	secretID := personal["id"].(string)
	assertSecretOwner(t, personal, secretstore.SecretOwnerUser, mustPublicUserID(t, owner.ID))
	gotPersonal := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
		"", "", http.StatusOK, authHeaders(ownerToken))
	assertSecretOwner(t, gotPersonal, secretstore.SecretOwnerUser, mustPublicUserID(t, owner.ID))
	personalList := requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets?owner_kind=user", "", "", http.StatusOK,
		authHeaders(ownerToken))["data"].([]any)
	if len(personalList) != 1 || personalList[0].(map[string]any)["id"] != secretID {
		t.Fatalf("personal canonical list = %+v", personalList)
	}
	requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID, "", "", http.StatusNotFound,
		authHeaders(otherToken))
	requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets?owner_kind=project&owner_project_id="+project.ProjectID,
		"", "", http.StatusForbidden, authHeaders(otherToken))
	requestJSONWithHeaders(t, handler, http.MethodGet,
		"/api/v1/orgs/"+project.OrgID+"/secrets?owner_kind=org",
		"", "", http.StatusForbidden, authHeaders(otherToken))
	grant := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID+"/grants",
		`{"target_project_id":"`+project.ProjectID+`"}`, "", http.StatusCreated, authHeaders(ownerToken))
	grantID := grant["id"].(string)

	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID+"/grants/"+grantID,
		"", "", http.StatusNotFound, authHeaders(otherToken))
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID+"/grants/"+grantID,
		"", "", http.StatusNoContent, authHeaders(ownerToken))
	updated := requestJSONWithHeaders(t, handler, http.MethodPatch,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
		`{"metadata":{"purpose":"personal"}}`, "", http.StatusOK, authHeaders(ownerToken))
	if updated["metadata"].(map[string]any)["purpose"] != "personal" {
		t.Fatalf("personal update = %+v", updated)
	}
	requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID+"/versions",
		`{"material":{"kind":"generic","value":"wrong-kind"}}`,
		"", http.StatusBadRequest, authHeaders(ownerToken))
	rotated := requestJSONWithHeaders(t, handler, http.MethodPost,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID+"/versions",
		`{"material":{"kind":"oauth_token_set","access_token":"next-access","refresh":{"refresh_token":"next-refresh","token_endpoint":"https://issuer.example/token","client_id":"client-id","resource":"https://mcp.example"}}}`,
		"", http.StatusOK, authHeaders(ownerToken))
	if rotated["current_version_number"] != float64(2) {
		t.Fatalf("personal rotation = %+v", rotated)
	}
	requestJSONWithHeaders(t, handler, http.MethodDelete,
		"/api/v1/orgs/"+project.OrgID+"/secrets/"+secretID,
		"", "", http.StatusNoContent, authHeaders(ownerToken))

	for _, legacy := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/orgs/" + project.OrgID + "/users/me/secrets"},
		{http.MethodPost, project.ProjectPath + "/secrets"},
		{http.MethodPost, project.ProjectPath + "/secrets/mcp-oauth"},
		{http.MethodPost, project.ProjectPath + "/secret-grants/" + grantID + "/revoke"},
	} {
		requestJSONWithHeaders(t, handler, legacy.method, legacy.path, "", "", http.StatusNotFound, authHeaders(ownerToken))
	}
}

func TestPublicSecretErrorsDoNotEchoPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "secret-redaction")
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"duplicate","material":{"kind":"generic","value":"original-private-value"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/secrets",
		strings.NewReader(`{"owner":{"kind":"org"},"name":"duplicate","material":{"kind":"generic","value":"new-private-value"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+project.AdminToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-value") {
		t.Fatalf("response leaked payload: %s", rec.Body.String())
	}
}

func assertSecretOwner(t *testing.T, secret map[string]any, kind, id string) {
	t.Helper()
	owner := secret["owner"].(map[string]any)
	if owner["kind"] != kind {
		t.Fatalf("owner = %+v, want %s", owner, kind)
	}
	if kind == secretstore.SecretOwnerProject && owner["project_id"] != id {
		t.Fatalf("owner = %+v", owner)
	}
	if kind == secretstore.SecretOwnerUser && owner["user_id"] != id {
		t.Fatalf("owner = %+v", owner)
	}
}

func assertSecretRedacted(t *testing.T, secret map[string]any) {
	t.Helper()
	for _, field := range []string{"payload", "current_version_id"} {
		if _, ok := secret[field]; ok {
			t.Fatalf("secret leaked %s: %+v", field, secret)
		}
	}
}

func assertProjectAccess(t *testing.T, access map[string]any, secretID, source, grantID string) {
	t.Helper()
	secret := access["secret"].(map[string]any)
	availability := access["availability"].(map[string]any)
	if secret["id"] != secretID || availability["source"] != source {
		t.Fatalf("access = %+v", access)
	}
	if source == "grant" && availability["grant_id"] != grantID {
		t.Fatalf("access = %+v", access)
	}
	assertSecretRedacted(t, secret)
}

func containsPublicSecret(secrets []any, id string) bool {
	for _, value := range secrets {
		if value.(map[string]any)["id"] == id {
			return true
		}
	}
	return false
}

func mustPublicUserID(t *testing.T, id storage.ID) string {
	t.Helper()
	value, err := publicid.Encode(publicid.KindUser, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func createHTTPOrgMemberToken(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	orgID storage.ID,
	seed string,
) (identitystore.UserRecord, string) {
	t.Helper()
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{Email: seed + "@example.com", DisplayName: seed})
	if err != nil {
		t.Fatalf("create %s user: %v", seed, err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{OrgID: orgID, UserID: user.ID, Role: authz.OrgRoleMember}); err != nil {
		t.Fatalf("add %s org membership: %v", seed, err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: seed, TokenID: seed})
	if err != nil {
		t.Fatalf("create %s pat: %v", seed, err)
	}
	return user, pat.Token
}

func requestRawWithHeaders(t *testing.T, handler http.Handler, method, path, body string, wantStatus int, headers map[string]string) string {
	t.Helper()
	req := newJSONRequest(method, path, body)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	return rec.Body.String()
}
