//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestPublicOrgAPIKeyManagement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "org-api-key")
	keysPath := "/api/v1/orgs/" + project.OrgID + "/api-keys"

	// Key management is browser-session-only: a bearer PAT cannot mint keys.
	requestJSONWithHeaders(
		t, handler, http.MethodPost, keysPath, `{"name":"CI key","org_role":"member"}`, "",
		http.StatusForbidden, authHeaders(project.AdminToken),
	)

	created := requestJSONWithHeaders(
		t, handler, http.MethodPost, keysPath, `{"name":"CI key","org_role":"member"}`, "",
		http.StatusCreated, project.adminBrowserAuthHeaders(),
	)
	keyToken, _ := created["token"].(string)
	if !strings.HasPrefix(keyToken, identitystore.OrgAPIKeyPlaintextPrefix) {
		t.Fatalf("expected one-time org api key plaintext, got %+v", created)
	}
	apiKey, _ := created["api_key"].(map[string]any)
	keyID, _ := apiKey["id"].(string)
	if !strings.HasPrefix(keyID, "oak_") || apiKey["org_role"] != "member" {
		t.Fatalf("unexpected created key: %+v", created)
	}

	requestJSONWithHeaders(
		t, handler, http.MethodPost, keysPath, `{"name":"CI key","org_role":"member"}`, "",
		http.StatusConflict, project.adminBrowserAuthHeaders(),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodPost, keysPath, `{"name":"Owner key","org_role":"owner"}`, "",
		http.StatusBadRequest, project.adminBrowserAuthHeaders(),
	)

	listed := requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if data, _ := listed["data"].([]any); len(data) != 1 {
		t.Fatalf("unexpected key list: %+v", listed)
	}

	fetched := requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath+"/"+keyID, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if fetched["name"] != "CI key" || fetched["token_id"] == "" {
		t.Fatalf("unexpected fetched key: %+v", fetched)
	}

	// The member key authenticates but lacks org manage, so the key inventory
	// refuses it; user-only routes refuse any org API key regardless of role.
	requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath, "", "",
		http.StatusForbidden, authHeaders(keyToken),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/me", "", "",
		http.StatusForbidden, authHeaders(keyToken),
	)

	// Account-scoped product routes accept the key through its org role.
	visibleProjects := requestJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/projects", "", "",
		http.StatusOK, authHeaders(keyToken),
	)
	if data, _ := visibleProjects["data"].([]any); len(data) != 0 {
		t.Fatalf("member key without project roles should see no projects, got %+v", visibleProjects)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/machines", "", "",
		http.StatusOK, authHeaders(keyToken),
	)

	updated := requestJSONWithHeaders(
		t, handler, http.MethodPatch, keysPath+"/"+keyID, `{"name":"Deploy key","org_role":"admin"}`, "",
		http.StatusOK, project.adminBrowserAuthHeaders(),
	)
	if updated["name"] != "Deploy key" || updated["org_role"] != "admin" {
		t.Fatalf("unexpected updated key: %+v", updated)
	}

	// Promoted to admin, the key itself can read the org's key inventory.
	adminListed := requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath, "", "",
		http.StatusOK, authHeaders(keyToken),
	)
	if data, _ := adminListed["data"].([]any); len(data) != 1 {
		t.Fatalf("admin key should list org api keys, got %+v", adminListed)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath+"/"+keyID, "", "",
		http.StatusOK, authHeaders(keyToken),
	)

	grantListPath := keysPath + "/" + keyID + "/projects"
	emptyGrants := requestJSONWithHeaders(
		t, handler, http.MethodGet, grantListPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if data, _ := emptyGrants["data"].([]any); len(data) != 0 {
		t.Fatalf("key without project roles should have no grants, got %+v", emptyGrants)
	}

	grantPath := grantListPath + "/" + project.ProjectID
	grant := requestJSONWithHeaders(
		t, handler, http.MethodPut, grantPath, `{"role":"developer"}`, "",
		http.StatusOK, project.adminBrowserAuthHeaders(),
	)
	if grant["role"] != "developer" || grant["project_id"] != project.ProjectID {
		t.Fatalf("unexpected project grant: %+v", grant)
	}
	grantedProjects := requestJSONWithHeaders(
		t, handler, http.MethodGet, "/api/v1/orgs/"+project.OrgID+"/projects", "", "",
		http.StatusOK, authHeaders(keyToken),
	)
	if data, _ := grantedProjects["data"].([]any); len(data) != 1 {
		t.Fatalf("key with project role should see the project, got %+v", grantedProjects)
	}
	listedGrants := requestJSONWithHeaders(
		t, handler, http.MethodGet, grantListPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	grantRows, _ := listedGrants["data"].([]any)
	if len(grantRows) != 1 {
		t.Fatalf("expected one project grant, got %+v", listedGrants)
	}
	if row, _ := grantRows[0].(map[string]any); row["project_id"] != project.ProjectID || row["role"] != "developer" {
		t.Fatalf("unexpected listed project grant: %+v", listedGrants)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusNoContent, project.adminBrowserAuthHeaders(),
	)
	clearedGrants := requestJSONWithHeaders(
		t, handler, http.MethodGet, grantListPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken),
	)
	if data, _ := clearedGrants["data"].([]any); len(data) != 0 {
		t.Fatalf("removed grant should not be listed, got %+v", clearedGrants)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodDelete, grantPath, "", "",
		http.StatusNotFound, project.adminBrowserAuthHeaders(),
	)

	revoked := requestJSONWithHeaders(
		t, handler, http.MethodPost, keysPath+"/"+keyID+"/revoke", "", "",
		http.StatusOK, project.adminBrowserAuthHeaders(),
	)
	if revoked["revoked_at"] == nil || revoked["org_role"] != "" {
		t.Fatalf("unexpected revoked key: %+v", revoked)
	}

	// A revoked key no longer authenticates at all.
	requestJSONWithHeaders(
		t, handler, http.MethodGet, keysPath, "", "",
		http.StatusUnauthorized, authHeaders(keyToken),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodPatch, keysPath+"/"+keyID, `{"name":"Zombie"}`, "",
		http.StatusConflict, project.adminBrowserAuthHeaders(),
	)
}
