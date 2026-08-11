//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestPublicActorPutUpsertsExternalActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-put")
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		project.ProjectPath+"/actors",
		`{"provider_tenant_id":"crm-eu","provider_user_id":"cust-1","display_name":"Ada Lovelace","metadata":{"tier":"gold"}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	actorID, _ := created["id"].(string)
	if actorID == "" ||
		created["provider"] != "external" ||
		created["provider_tenant_id"] != "crm-eu" ||
		created["provider_user_id"] != "cust-1" ||
		created["display_name"] != "Ada Lovelace" ||
		created["org_id"] != project.OrgID ||
		created["project_id"] != project.ProjectID {
		t.Fatalf("unexpected created actor: %+v", created)
	}
	if metadata, ok := created["metadata"].(map[string]any); !ok || metadata["tier"] != "gold" {
		t.Fatalf("created actor metadata = %+v, want tier gold", created["metadata"])
	}

	replaced := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		project.ProjectPath+"/actors",
		`{"provider_tenant_id":"crm-eu","provider_user_id":"cust-1","display_name":"Ada L.","metadata":{"tier":"silver"}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if replaced["id"] != actorID || replaced["display_name"] != "Ada L." {
		t.Fatalf("upsert should keep the id and replace display_name, got %+v", replaced)
	}
	if metadata, ok := replaced["metadata"].(map[string]any); !ok || metadata["tier"] != "silver" {
		t.Fatalf("upsert metadata = %+v, want replaced tier silver", replaced["metadata"])
	}

	identityOnly := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		project.ProjectPath+"/actors",
		`{"provider_tenant_id":"crm-eu","provider_user_id":"cust-1"}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if identityOnly["id"] != actorID || identityOnly["display_name"] != "Ada L." {
		t.Fatalf("identity-only upsert should keep stored attributes, got %+v", identityOnly)
	}
	if metadata, ok := identityOnly["metadata"].(map[string]any); !ok || metadata["tier"] != "silver" {
		t.Fatalf("identity-only upsert metadata = %+v, want kept tier silver", identityOnly["metadata"])
	}
	if identityOnly["updated_at"] != replaced["updated_at"] {
		t.Fatalf(
			"unchanged upsert should not bump updated_at, got %v want %v",
			identityOnly["updated_at"],
			replaced["updated_at"],
		)
	}

	fetched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors/"+actorID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if fetched["id"] != actorID || fetched["display_name"] != "Ada L." {
		t.Fatalf("unexpected fetched actor: %+v", fetched)
	}

	cleared := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		project.ProjectPath+"/actors",
		`{"provider_tenant_id":"crm-eu","provider_user_id":"cust-1","display_name":"","metadata":{}}`,
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if cleared["id"] != actorID {
		t.Fatalf("clearing upsert should keep the id, got %+v", cleared)
	}
	if _, has := cleared["display_name"]; has {
		t.Fatalf("explicit empty display_name should clear it, got %+v", cleared)
	}
	if metadata, ok := cleared["metadata"].(map[string]any); !ok || len(metadata) != 0 {
		t.Fatalf("explicit empty metadata should clear it, got %+v", cleared["metadata"])
	}
}

func TestPublicActorPutRejectsOversizedAttributes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-put-oversized")
	for _, body := range []string{
		`{"provider_user_id":"` + strings.Repeat("u", executionstore.MaxActorProviderUserIDLength+1) + `"}`,
		`{"provider_tenant_id":"` + strings.Repeat("t", executionstore.MaxActorProviderTenantIDLength+1) + `","provider_user_id":"cust-1"}`,
		`{"provider_user_id":"cust-1","display_name":"` + strings.Repeat("d", executionstore.MaxActorDisplayNameLength+1) + `"}`,
		`{"provider_user_id":""}`,
		`{"provider_tenant_id":"","provider_user_id":"cust-1"}`,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			project.ProjectPath+"/actors",
			body,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
}

// The provider is implicit: actors written through the API are always
// external, and the request schema rejects a provider field outright.
func TestPublicActorPutRejectsProviderField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-put-provider")
	for _, provider := range []string{"omnara", "slack", "external", ""} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			project.ProjectPath+"/actors",
			`{"provider":"`+provider+`","provider_user_id":"cust-1"}`,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
}

func TestPublicActorPutRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-put-metadata")

	oversizedEntries := strings.Builder{}
	oversizedEntries.WriteString("{")
	for i := range resourcemeta.MaxEntries + 1 {
		if i > 0 {
			oversizedEntries.WriteString(",")
		}
		fmt.Fprintf(&oversizedEntries, `"key-%d":"value"`, i)
	}
	oversizedEntries.WriteString("}")

	invalidMetadata := []string{
		`{"count":3}`,
		`{"nested":{"a":"b"}}`,
		`{"value":"` + strings.Repeat("v", resourcemeta.MaxValueLength+1) + `"}`,
		`{"` + strings.Repeat("k", resourcemeta.MaxKeyLength+1) + `":"value"}`,
		oversizedEntries.String(),
	}
	for _, metadata := range invalidMetadata {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			project.ProjectPath+"/actors",
			`{"provider_user_id":"cust-meta","metadata":`+metadata+`}`,
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}
}

func TestPublicActorGetUnknownReturnsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-get-missing")
	missingID := testPublicID(t, publicid.KindActor, uuid.New())
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors/"+missingID,
		"",
		"",
		http.StatusNotFound,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors/not-an-actor-id",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)
}

func TestPublicActorListPaginatesAndFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "actor-list")
	createdIDs := map[string]bool{}
	for providerUserID, providerTenantID := range map[string]string{
		"cust-a": "",
		"cust-b": "crm-eu",
		"cust-c": "",
	} {
		body := `{"provider_user_id":"` + providerUserID + `"}`
		if providerTenantID != "" {
			body = `{"provider_tenant_id":"` + providerTenantID + `","provider_user_id":"` + providerUserID + `"}`
		}
		created := requestJSONWithHeaders(
			t,
			handler,
			http.MethodPut,
			project.ProjectPath+"/actors",
			body,
			"",
			http.StatusOK,
			authHeaders(project.AdminToken),
		)
		createdIDs[created["id"].(string)] = true
	}

	firstPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?limit=2",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstData, ok := firstPage["data"].([]any)
	if !ok || len(firstData) != 2 {
		t.Fatalf("first page = %+v, want 2 actors", firstPage)
	}
	cursor, ok := firstPage["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("first page next_cursor = %+v, want non-empty cursor", firstPage["next_cursor"])
	}
	secondPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?limit=2&cursor="+cursor,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	secondData, ok := secondPage["data"].([]any)
	if !ok || len(secondData) != 1 {
		t.Fatalf("second page = %+v, want 1 actor", secondPage)
	}
	if secondPage["next_cursor"] != nil {
		t.Fatalf("second page next_cursor = %+v, want null", secondPage["next_cursor"])
	}
	listedIDs := map[string]bool{}
	for _, item := range append(firstData, secondData...) {
		listedIDs[item.(map[string]any)["id"].(string)] = true
	}
	if len(listedIDs) != 3 {
		t.Fatalf("paginated ids = %v, want 3 distinct actors", listedIDs)
	}
	for id := range createdIDs {
		if !listedIDs[id] {
			t.Fatalf("created actor %s missing from paginated listing %v", id, listedIDs)
		}
	}

	filtered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?provider=external&provider_user_id=cust-b",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	filteredData, ok := filtered["data"].([]any)
	if !ok || len(filteredData) != 1 ||
		filteredData[0].(map[string]any)["provider_user_id"] != "cust-b" {
		t.Fatalf("filtered listing = %+v, want only cust-b", filtered)
	}

	tenantFiltered := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?provider_tenant_id=crm-eu",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	tenantData, ok := tenantFiltered["data"].([]any)
	if !ok || len(tenantData) != 1 ||
		tenantData[0].(map[string]any)["provider_user_id"] != "cust-b" {
		t.Fatalf("tenant-filtered listing = %+v, want only cust-b", tenantFiltered)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?provider=crm",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	empty := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors?provider=omnara",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if emptyData, ok := empty["data"].([]any); !ok || len(emptyData) != 0 {
		t.Fatalf("omnara listing before any input = %+v, want empty", empty)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/actors",
		"",
		"",
		http.StatusUnauthorized,
		nil,
	)
}
