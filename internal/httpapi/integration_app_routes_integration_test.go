//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestPublicIntegrationAppManagementJourney(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-management")
	secretID := f.secret(t, f.project.OrgID, "")
	body := publicAppCreateBody(secretID, "")
	body["name"] = "Cafe\u0301 review app"
	created := f.request(t, http.MethodPost, f.apps, body, http.StatusCreated)
	id := channelReceiptString(t, created, "id")
	path := f.apps + "/" + id
	require.Equal(t, "Café review app", created["name"])
	require.Equal(t, secretID, created["credential_secret_id"])
	require.Equal(t, f.project.OrgID, created["org_id"])
	require.Equal(t, "github", created["provider"])
	require.Equal(t, body["provider_app_ref"], created["provider_app_ref"])
	require.Equal(t, "active", created["state"])
	require.NotContains(t, created, "owner_project_id")
	require.Equal(t, created, f.request(t, http.MethodGet, path, nil, http.StatusOK))
	assertPublicAppRedaction(t, created, false)

	original := f.stored(t, id)
	replacementID := f.secret(t, f.project.OrgID, "")
	updated := f.request(t, http.MethodPatch, path, map[string]any{
		"name": "Reviews (production)", "credential_secret_id": replacementID,
		"provider_config": map[string]any{"client_id": "replacement-client"},
	}, http.StatusOK)
	require.Equal(t, "Reviews (production)", updated["name"])
	require.Equal(t, replacementID, updated["credential_secret_id"])
	require.Equal(t, map[string]any{"client_id": "replacement-client"}, updated["provider_config"])
	after := f.stored(t, id)
	require.Equal(t, original.ConfigurationRevision+1, after.ConfigurationRevision)
	require.Equal(t, original.ProviderAppRef, after.ProviderAppRef)
	require.Equal(t, original.ConnectorKey, after.ConnectorKey)
	require.Equal(t, original.CreatedAt, after.CreatedAt)

	disabled := f.request(t, http.MethodPatch, path, map[string]any{"state": "disabled"}, http.StatusOK)
	require.Equal(t, updated["name"], disabled["name"])
	require.Equal(t, updated["provider_config"], disabled["provider_config"])
	require.Equal(t, replacementID, disabled["credential_secret_id"])
	require.Equal(t, "disabled", f.request(t, http.MethodGet, path, nil, http.StatusOK)["state"])
	require.Equal(t, []string{id}, publicAppIDs(t, f.request(t, http.MethodGet, f.apps, nil, http.StatusOK)))
	require.Empty(t, publicAppIDs(t, f.request(t, http.MethodGet, f.eligible, nil, http.StatusOK)))
	restored := f.request(t, http.MethodPatch, path, map[string]any{"state": "active"}, http.StatusOK)
	require.Equal(t, id, restored["id"])
	require.Equal(t, []string{id}, publicAppIDs(t, f.request(t, http.MethodGet, f.eligible, nil, http.StatusOK)))
	assertPublicAppRedaction(t, restored, false)

	unknown := testPublicID(t, publicid.KindIntegrationApp, uuid.New())
	for _, test := range []struct {
		id     string
		status int
	}{{unknown, http.StatusNotFound}, {"not-an-app-id", http.StatusBadRequest}} {
		f.request(t, http.MethodGet, f.apps+"/"+test.id, nil, test.status)
		f.request(t, http.MethodPatch, f.apps+"/"+test.id, map[string]any{"state": "active"}, test.status)
	}
}

func TestPublicIntegrationAppProjectReadAndOrgManage(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-permissions")
	otherProject := f.otherProject(t)
	shared := f.create(t, f.secret(t, f.project.OrgID, ""), "")
	owned := f.create(t, f.secret(t, f.project.OrgID, f.project.ProjectID), f.project.ProjectID)
	other := f.create(t, f.secret(t, f.project.OrgID, otherProject), otherProject)
	disabled := f.create(t, f.secret(t, f.project.OrgID, ""), "")
	f.request(t, http.MethodPatch, f.apps+"/"+disabled, map[string]any{"state": "disabled"}, http.StatusOK)
	viewer, _ := createChannelHTTPKey(t, f.project, "viewer")
	eligible := requestJSONWithHeaders(t, f.handler, http.MethodGet, f.eligible, "", "",
		http.StatusOK, authHeaders(viewer))
	require.ElementsMatch(t, []string{shared, owned}, publicAppIDs(t, eligible))
	require.ElementsMatch(t, []string{shared, owned, other, disabled},
		publicAppIDs(t, f.request(t, http.MethodGet, f.apps, nil, http.StatusOK)))
	for _, value := range testutil.RequireType[[]any](t, eligible["data"]) {
		assertPublicAppRedaction(t, testutil.RequireType[map[string]any](t, value), true)
	}
	for _, test := range []struct{ method, path, body string }{
		{http.MethodGet, f.apps, ""},
		{http.MethodGet, f.apps + "/" + shared, ""},
		{http.MethodPost, f.apps, workflowHTTPJSON(t, publicAppCreateBody(f.secret(t, f.project.OrgID, ""), ""))},
		{http.MethodPatch, f.apps + "/" + shared, `{"state":"disabled"}`},
	} {
		requestJSONWithHeaders(t, f.handler, test.method, test.path, test.body, "",
			http.StatusForbidden, authHeaders(viewer))
	}
	requestJSONWithHeaders(t, f.handler, http.MethodGet, f.eligible, "", "", http.StatusUnauthorized, nil)
	require.Equal(t, integrationstore.IntegrationAppStateActive, f.stored(t, shared).State)
}

//nolint:tparallel // Cases share one app and verify its unchanged state before subsequent mutations.
func TestPublicIntegrationAppCredentialAndIdentityScope(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-scope")
	orgSecret := f.secret(t, f.project.OrgID, "")
	projectSecret := f.secret(t, f.project.OrgID, f.project.ProjectID)
	otherProject := f.otherProject(t)
	otherSecret := f.secret(t, f.project.OrgID, otherProject)
	foreign := f.request(t, http.MethodPost, "/api/v1/orgs", map[string]any{"name": "Foreign apps"}, http.StatusCreated)
	foreignOrg := channelReceiptString(t, testutil.RequireType[map[string]any](t, foreign["org"]), "id")
	foreignProject := channelReceiptString(t, testutil.RequireType[map[string]any](t, foreign["project"]), "id")
	foreignSecret := f.secret(t, foreignOrg, "")
	deletedSecret := f.secret(t, f.project.OrgID, "")
	f.request(t, http.MethodDelete, "/api/v1/orgs/"+f.project.OrgID+"/secrets/"+deletedSecret,
		nil, http.StatusNoContent)
	generic := f.request(t, http.MethodPost, "/api/v1/orgs/"+f.project.OrgID+"/secrets", map[string]any{
		"name": "Wrong credential kind", "owner": map[string]any{"kind": "org"},
		"material": map[string]any{"kind": "generic", "value": "local-credential-never-returned"},
	}, http.StatusCreated)
	app := f.create(t, projectSecret, f.project.ProjectID)
	before := f.stored(t, app)
	for _, test := range []struct {
		name, secretID, ownerProjectID string
		status                         int
	}{
		{"raw_uuid", uuid.NewString(), f.project.ProjectID, http.StatusBadRequest},
		{"wrong_kind", channelReceiptString(t, generic, "id"), f.project.ProjectID, http.StatusBadRequest},
		{"foreign_org", foreignSecret, f.project.ProjectID, http.StatusNotFound},
		{"deleted_secret", deletedSecret, f.project.ProjectID, http.StatusNotFound},
		{"unknown_secret", testPublicID(t, publicid.KindSecret, uuid.New()), f.project.ProjectID, http.StatusNotFound},
		{"other_project_secret", otherSecret, f.project.ProjectID, http.StatusNotFound},
		{"org_secret_for_project", orgSecret, f.project.ProjectID, http.StatusNotFound},
		{"project_secret_for_shared", projectSecret, "", http.StatusNotFound},
		{"foreign_owner_project", orgSecret, foreignProject, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.request(t, http.MethodPost, f.apps, publicAppCreateBody(test.secretID, test.ownerProjectID), test.status)
			if test.ownerProjectID == f.project.ProjectID {
				f.request(t, http.MethodPatch, f.apps+"/"+app,
					map[string]any{"credential_secret_id": test.secretID}, test.status)
			}
			require.Equal(t, before, f.stored(t, app))
		})
	}
	foreignPath := "/api/v1/orgs/" + foreignOrg + "/integration-apps/" + app
	f.request(t, http.MethodGet, foreignPath, nil, http.StatusNotFound)
	f.request(t, http.MethodPatch, foreignPath, map[string]any{"name": "Wrong organization"}, http.StatusNotFound)
	for _, patch := range []map[string]any{
		{"provider": "discord"}, {"provider_app_ref": "replacement"}, {"owner_project_id": otherProject},
		{"org_id": foreignOrg}, {"credential_secret_id": nil}, {"state": "deleted"},
	} {
		f.request(t, http.MethodPatch, f.apps+"/"+app, patch, http.StatusBadRequest)
	}
	require.Equal(t, before, f.stored(t, app))
	require.Equal(t, []string{app}, publicAppIDs(t, f.request(t, http.MethodGet, f.apps, nil, http.StatusOK)))
}

func TestPublicIntegrationAppPaginationScopesAndRedaction(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-pagination")
	secret := f.secret(t, f.project.OrgID, "")
	var expected []string
	for range 3 {
		expected = append(expected, f.create(t, secret, ""))
	}
	first := f.request(t, http.MethodGet, f.apps+"?limit=1", nil, http.StatusOK)
	cursor := channelReceiptString(t, first, "next_cursor")
	page := first
	var actual []string
	for range 3 {
		ids := publicAppIDs(t, page)
		require.Len(t, ids, 1)
		actual = append(actual, ids...)
		for _, item := range testutil.RequireType[[]any](t, page["data"]) {
			assertPublicAppRedaction(t, testutil.RequireType[map[string]any](t, item), true)
		}
		if page["next_cursor"] == nil {
			break
		}
		page = f.request(t, http.MethodGet,
			f.apps+"?limit=1&cursor="+url.QueryEscape(channelReceiptString(t, page, "next_cursor")), nil, http.StatusOK)
	}
	require.Equal(t, []string{expected[2], expected[1], expected[0]}, actual)
	require.Nil(t, page["next_cursor"])
	for _, suffix := range []string{"?limit=0", "?limit=101", "?cursor=malformed"} {
		f.request(t, http.MethodGet, f.apps+suffix, nil, http.StatusBadRequest)
	}
	f.request(t, http.MethodGet, f.eligible+"?cursor="+url.QueryEscape(cursor), nil, http.StatusBadRequest)
	eligible := f.request(t, http.MethodGet, f.eligible+"?limit=1", nil, http.StatusOK)
	eligibleCursor := channelReceiptString(t, eligible, "next_cursor")
	f.request(t, http.MethodGet, f.apps+"?cursor="+url.QueryEscape(eligibleCursor), nil, http.StatusBadRequest)
	other := f.otherProject(t)
	f.request(t, http.MethodGet, "/api/v1/orgs/"+f.project.OrgID+"/projects/"+other+
		"/integration-apps?cursor="+url.QueryEscape(eligibleCursor), nil, http.StatusBadRequest)
	foreign := f.request(t, http.MethodPost, "/api/v1/orgs", map[string]any{"name": "Cursor scope"}, http.StatusCreated)
	foreignOrg := channelReceiptString(t, testutil.RequireType[map[string]any](t, foreign["org"]), "id")
	f.request(t, http.MethodGet, "/api/v1/orgs/"+foreignOrg+"/integration-apps?cursor="+url.QueryEscape(cursor),
		nil, http.StatusBadRequest)
}

//nolint:tparallel // Invalid writes share an app whose revision is checked after all cases finish.
func TestPublicIntegrationAppNamesAndProviderReferences(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-validation")
	secret := f.secret(t, f.project.OrgID, "")
	app := f.create(t, secret, "")
	before := f.stored(t, app)
	for _, test := range []struct{ name, value string }{
		{"empty", ""}, {"edge_space", " app "}, {"overlong", strings.Repeat("a", 65)},
		{"control", "app\x00name"}, {"invisible", "app\u200bname"},
	} {
		t.Run("name_"+test.name, func(t *testing.T) {
			body := publicAppCreateBody(secret, "")
			body["name"] = test.value
			f.request(t, http.MethodPost, f.apps, body, http.StatusBadRequest)
			f.request(t, http.MethodPatch, f.apps+"/"+app, map[string]any{"name": test.value}, http.StatusBadRequest)
		})
	}
	for _, test := range []struct{ name, value string }{
		{"whitespace", "   "}, {"nul", "native\x00id"}, {"utf8_bytes", strings.Repeat("界", 171)},
	} {
		t.Run("provider_ref_"+test.name, func(t *testing.T) {
			body := publicAppCreateBody(secret, "")
			body["provider_app_ref"] = test.value
			f.request(t, http.MethodPost, f.apps, body, http.StatusBadRequest)
		})
	}
	for _, test := range []struct {
		provider string
		config   map[string]any
		status   int
	}{
		{"slack", map[string]any{"client_id": "slack-client"}, http.StatusCreated},
		{"discord", map[string]any{}, http.StatusCreated},
		{"github", map[string]any{}, http.StatusBadRequest},
		{"slack", map[string]any{"client_id": "  "}, http.StatusBadRequest},
		{"discord", map[string]any{"client_id": "discord-client"}, http.StatusBadRequest},
		{"github", map[string]any{"client_id": "client", "client_secret": "never-configuration"}, http.StatusBadRequest},
	} {
		body := publicAppCreateBody(secret, "")
		body["provider"], body["provider_config"] = test.provider, test.config
		f.request(t, http.MethodPost, f.apps, body, test.status)
	}
	require.Equal(t, before, f.stored(t, app), "rejected renames must not advance the app revision")
}

func TestPublicIntegrationAppHistoricalDisplayNames(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-old-names")
	secret := f.secret(t, f.project.OrgID, "")
	secretID := mustPublicHTTPID(t, publicid.KindSecret, secret)
	for _, label := range []string{"", strings.Repeat("x", 65)} {
		// Storage still accepts provider labels produced by the migration. Public mutation uses ResourceName.
		input := integrationstore.CreateIntegrationAppInput{
			OrgID: f.project.OrgUUID, Provider: integrationstore.IntegrationProviderGitHub,
			ProviderAppRef: uuid.NewString(), DisplayName: label, ConnectorKey: channelconnector.BuiltInConnectorKey,
			CredentialSecretID: secretID, ProviderConfig: json.RawMessage(`{"client_id":"public","private":"local-secret"}`),
			State: integrationstore.IntegrationAppStateActive,
		}
		app, err := f.project.Store.Integrations().CreateIntegrationApp(t.Context(), input)
		require.NoError(t, err)
		id := testPublicID(t, publicid.KindIntegrationApp, app.ID)
		path := f.apps + "/" + id
		response := f.request(t, http.MethodGet, path, nil, http.StatusOK)
		require.Equal(t, label, response["name"])
		require.Equal(t, map[string]any{"client_id": "public"}, response["provider_config"])
		assertPublicAppRedaction(t, response, false)
		for _, collection := range []string{f.apps, f.eligible} {
			page := f.request(t, http.MethodGet, collection, nil, http.StatusOK)
			var found bool
			for _, item := range testutil.RequireType[[]any](t, page["data"]) {
				summary := testutil.RequireType[map[string]any](t, item)
				assertPublicAppRedaction(t, summary, true)
				if summary["id"] == id {
					found = true
					require.Equal(t, label, summary["name"])
				}
			}
			require.True(t, found)
		}
		disabled := f.request(t, http.MethodPatch, path, map[string]any{"state": "disabled"}, http.StatusOK)
		require.Equal(t, label, disabled["name"], "an unrelated patch must not rewrite historical labels")
		f.request(t, http.MethodPatch, path, map[string]any{"name": label}, http.StatusBadRequest)
		renamed := f.request(t, http.MethodPatch, path, map[string]any{"name": "A canonical name"}, http.StatusOK)
		require.Equal(t, "A canonical name", renamed["name"])
	}
}

func TestPublicIntegrationAppDeletedProjectCannotRestore(t *testing.T) {
	t.Parallel()
	f := newPublicAppFixture(t, "app-deletion")
	project := f.otherProject(t)
	app := f.create(t, f.secret(t, f.project.OrgID, project), project)
	f.request(t, http.MethodDelete, "/api/v1/orgs/"+f.project.OrgID+"/projects/"+project, nil, http.StatusNoContent)
	f.request(t, http.MethodGet, f.apps+"/"+app, nil, http.StatusNotFound)
	f.request(t, http.MethodPatch, f.apps+"/"+app, map[string]any{"state": "active"}, http.StatusNotFound)
	require.Empty(t, publicAppIDs(t, f.request(t, http.MethodGet, f.apps, nil, http.StatusOK)))
}

type publicAppFixture struct {
	handler        http.Handler
	project        publicHTTPProject
	apps, eligible string
}

func newPublicAppFixture(t *testing.T, seed string) publicAppFixture {
	t.Helper()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, seed)
	return publicAppFixture{handler: handler, project: project,
		apps: "/api/v1/orgs/" + project.OrgID + "/integration-apps", eligible: project.ProjectPath + "/integration-apps"}
}

func (f publicAppFixture) request(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	var raw string
	if body != nil {
		raw = workflowHTTPJSON(t, body)
	}
	return requestJSONWithHeaders(t, f.handler, method, path, raw, "", status, authHeaders(f.project.AdminToken))
}

func (f publicAppFixture) secret(t *testing.T, orgID, projectID string) string {
	t.Helper()
	owner := map[string]any{"kind": "org"}
	if projectID != "" {
		owner = map[string]any{"kind": "project", "project_id": projectID}
	}
	response := f.request(t, http.MethodPost, "/api/v1/orgs/"+orgID+"/secrets", map[string]any{
		"owner": owner, "name": "App credentials " + uuid.NewString(),
		"material": map[string]any{"kind": "integration_credentials",
			"values": map[string]any{"client_secret": "local-credential-never-returned"}},
	}, http.StatusCreated)
	return channelReceiptString(t, response, "id")
}

func publicAppCreateBody(secretID, ownerProjectID string) map[string]any {
	body := map[string]any{
		"provider": "github", "provider_app_ref": uuid.NewString(), "name": "Review app",
		"credential_secret_id": secretID, "provider_config": map[string]any{"client_id": "public-client"},
	}
	if ownerProjectID != "" {
		body["owner_project_id"] = ownerProjectID
	}
	return body
}

func (f publicAppFixture) create(t *testing.T, secretID, ownerProjectID string) string {
	t.Helper()
	response := f.request(t, http.MethodPost, f.apps, publicAppCreateBody(secretID, ownerProjectID), http.StatusCreated)
	return channelReceiptString(t, response, "id")
}

func (f publicAppFixture) stored(t *testing.T, id string) integrationstore.IntegrationAppRecord {
	t.Helper()
	app, err := f.project.Store.Integrations().GetIntegrationApp(t.Context(), f.project.OrgUUID,
		mustPublicHTTPID(t, publicid.KindIntegrationApp, id))
	require.NoError(t, err)
	return app
}

func (f publicAppFixture) otherProject(t *testing.T) string {
	t.Helper()
	response := f.request(t, http.MethodPost, "/api/v1/orgs/"+f.project.OrgID+"/projects",
		map[string]any{"name": "Another app project"}, http.StatusCreated)
	return channelReceiptString(t, response, "id")
}

func publicAppIDs(t *testing.T, page map[string]any) []string {
	t.Helper()
	var ids []string
	for _, value := range testutil.RequireType[[]any](t, page["data"]) {
		ids = append(ids, channelReceiptString(t, testutil.RequireType[map[string]any](t, value), "id"))
	}
	return ids
}

func assertPublicAppRedaction(t *testing.T, response map[string]any, summary bool) {
	t.Helper()
	for _, field := range []string{"material", "values", "client_secret", "configuration_revision",
		"connector_key", "installation_credential_kind"} {
		require.NotContains(t, response, field)
	}
	if summary {
		require.NotContains(t, response, "credential_secret_id")
		require.NotContains(t, response, "provider_config")
	}
	raw := workflowHTTPJSON(t, response)
	require.NotContains(t, raw, "local-credential-never-returned")
	require.NotContains(t, raw, "local-secret")
}
