//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func providerControlHTTPPath(t *testing.T, f channelReceiptHTTPFixture) string {
	t.Helper()
	return "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, f.app.ID)
}

func providerStateHTTPPath(t *testing.T, f channelReceiptHTTPFixture) string {
	t.Helper()
	return providerControlHTTPPath(t, f) + "/installations/" +
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID) + "/provider-state"
}

func providerStateHTTPRequest(f channelReceiptHTTPFixture) openapi.SetChannelConnectorInstallationProviderStateRequest {
	return openapi.SetChannelConnectorInstallationProviderStateRequest{
		ProviderTenantId: f.install.ProviderTenantID, ProviderAccountRef: f.install.ProviderAccountRef,
		ExpectedAppConfigurationRevision: f.app.ConfigurationRevision,
		ExpectedConfigurationRevision:    f.install.ConfigurationRevision,
		State:                            openapi.ChannelConnectorInstallationProviderStateActive,
	}
}

func TestChannelConnectorProviderControlHTTPPaginationAndScope(t *testing.T) {
	t.Parallel()
	f := newChannelReceiptHTTPFixture(t)
	second, err := f.project.Store.Integrations().UpsertIntegrationInstall(t.Context(),
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: f.app.OrgID, ProjectID: f.otherInstall.ProjectID, IntegrationAppID: f.app.ID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID), Provider: f.app.Provider,
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateDisabled, ProviderTenantID: f.install.ProviderTenantID,
			ProviderAccountRef: "other-repository", ProviderIdentity: json.RawMessage(`{"repository_node_id":"R_other"}`),
		})
	require.NoError(t, err)
	path := providerControlHTTPPath(t, f) + "/installation-control-scopes?provider_tenant_id=" +
		url.QueryEscape(f.install.ProviderTenantID) + "&limit=1"
	get := func(path, token string, status int) map[string]any {
		return requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", status, authHeaders(token))
	}
	first := get(path, f.token, http.StatusOK)
	after := channelReceiptString(t, first, "next_after_installation_id")
	through := channelReceiptString(t, first, "through_installation_id")
	require.Equal(t, testPublicID(t, publicid.KindIntegrationInstall, second.ID), through)
	newer, err := f.project.Store.Integrations().UpsertIntegrationInstall(t.Context(),
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: f.app.OrgID, ProjectID: f.install.ProjectID, IntegrationAppID: f.app.ID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID), Provider: f.app.Provider,
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: f.install.ProviderTenantID,
			ProviderAccountRef: "added-after-boundary",
		})
	require.NoError(t, err)
	last := get(path+"&after_installation_id="+after+"&through_installation_id="+through, f.token, http.StatusOK)
	require.Nil(t, last["next_after_installation_id"])
	require.Equal(t, through, last["through_installation_id"])
	want := map[string]string{
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID): "active",
		testPublicID(t, publicid.KindIntegrationInstall, second.ID):    "disabled",
	}
	for _, page := range []map[string]any{first, last} {
		require.EqualValues(t, f.app.ConfigurationRevision, page["app_configuration_revision"])
		rows := testutil.RequireType[[]any](t, page["installations"])
		require.Len(t, rows, 1)
		row := testutil.RequireType[map[string]any](t, rows[0])
		require.Len(t, row, 7, "control projection includes verified identity but no credentials or agent authority")
		id := channelReceiptString(t, row, "id")
		require.Equal(t, want[id], row["state"])
		identity := map[string]any{}
		if id == through {
			identity["repository_node_id"] = "R_other"
		}
		require.Equal(t, identity, row["provider_identity"])
		delete(want, id)
		require.NotContains(t, channelReceiptString(t, row, "project_id"), "-")
	}
	require.Empty(t, want)
	more := get(path+"&after_installation_id="+through, f.token, http.StatusOK)
	rows := testutil.RequireType[[]any](t, more["installations"])
	require.Len(t, rows, 1)
	require.Equal(t, testPublicID(t, publicid.KindIntegrationInstall, newer.ID),
		testutil.RequireType[map[string]any](t, rows[0])["id"])
	get(path, f.otherToken, http.StatusNotFound)
	empty := get(providerControlHTTPPath(t, f)+"/installation-control-scopes?provider_tenant_id=other"+
		"&after_installation_id="+after+"&through_installation_id="+through, f.token, http.StatusOK)
	require.Empty(t, empty["installations"], "ordering IDs cannot authorize another tenant")
	other := f
	other.app = f.otherApp
	empty = get(providerControlHTTPPath(t, other)+"/installation-control-scopes?provider_tenant_id="+
		url.QueryEscape(f.install.ProviderTenantID)+"&after_installation_id="+after+"&through_installation_id="+through,
		f.token, http.StatusOK)
	require.Empty(t, empty["installations"], "ordering IDs cannot authorize another app")
	get(path+"&after_installation_id=invalid", f.token, http.StatusBadRequest)
	get(path+"&through_installation_id=invalid", f.token, http.StatusBadRequest)
	get(providerControlHTTPPath(t, f)+"/installation-control-scopes", f.token, http.StatusBadRequest)
}

func TestChannelConnectorProviderStateHTTPRestorationAndFences(t *testing.T) {
	t.Parallel()
	f := newChannelReceiptHTTPFixture(t)
	path := providerStateHTTPPath(t, f)
	body := providerStateHTTPRequest(f)
	unchanged := f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.EqualValues(t, f.install.ConfigurationRevision+1, unchanged["configuration_revision"])
	body.State = openapi.ChannelConnectorInstallationProviderStateDisabled
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusConflict)
	body.ExpectedConfigurationRevision++
	disabled := f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, "disabled", disabled["state"])
	f.post(t, f.eventPath(t, f.app), f.event(t, f.install, "during-suspension", `{}`), f.token, http.StatusNotFound)
	body.ExpectedConfigurationRevision++
	body.State = openapi.ChannelConnectorInstallationProviderStateActive
	restored := f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, "active", restored["state"])
	f.post(t, f.eventPath(t, f.app), f.event(t, f.install, "after-restoration", `{}`), f.token, http.StatusAccepted)
	body.ExpectedConfigurationRevision++
	_, err := f.pool.Exec(t.Context(),
		`UPDATE integration_apps SET provider_config='{"rotation":1}' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusConflict)
	app, err := f.project.Store.Integrations().GetIntegrationApp(t.Context(), f.app.OrgID, f.app.ID)
	require.NoError(t, err)
	body.ExpectedAppConfigurationRevision = app.ConfigurationRevision
	err = f.project.Store.Integrations().DeleteIntegrationInstall(t.Context(), f.install.ProjectID, f.install.ID)
	require.NoError(t, err)
	replacement := f.createInstall(t, f.app, f.install.ProjectID, f.install.ProviderTenantID)
	require.NotEqual(t, f.install.ID, replacement.ID)
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusNotFound)
	current, err := f.project.Store.Integrations().GetIntegrationInstall(
		t.Context(), replacement.ProjectID, replacement.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.ConfigurationRevision, current.ConfigurationRevision)
}

func TestChannelConnectorProviderStateHTTPRejectsForeignAndMalformedRequests(t *testing.T) {
	t.Parallel()
	f := newChannelReceiptHTTPFixture(t)
	path := providerStateHTTPPath(t, f)
	valid := providerStateHTTPRequest(f)
	f.post(t, path, workflowHTTPJSON(t, valid), f.otherToken, http.StatusNotFound)
	other := f
	other.app = f.otherApp
	f.post(t, providerStateHTTPPath(t, other), workflowHTTPJSON(t, valid), f.token, http.StatusNotFound)
	for _, tc := range []struct {
		name   string
		change func(*openapi.SetChannelConnectorInstallationProviderStateRequest)
		status int
	}{
		{"tenant", func(b *openapi.SetChannelConnectorInstallationProviderStateRequest) { b.ProviderTenantId = "other" },
			http.StatusNotFound},
		{"repository", func(b *openapi.SetChannelConnectorInstallationProviderStateRequest) {
			b.ProviderAccountRef = "other"
		}, http.StatusNotFound},
		{"nul", func(b *openapi.SetChannelConnectorInstallationProviderStateRequest) { b.ProviderAccountRef = "bad\x00" },
			http.StatusBadRequest},
		{"zero_revision", func(b *openapi.SetChannelConnectorInstallationProviderStateRequest) {
			b.ExpectedConfigurationRevision = 0
		}, http.StatusBadRequest},
		{"state", func(b *openapi.SetChannelConnectorInstallationProviderStateRequest) { b.State = "deleted" },
			http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := valid
			tc.change(&body)
			f.post(t, path, workflowHTTPJSON(t, body), f.token, tc.status)
		})
	}
	f.post(t, path, `{"state":null}`, f.token, http.StatusBadRequest)
	current, err := f.project.Store.Integrations().GetIntegrationInstall(t.Context(), f.install.ProjectID, f.install.ID)
	require.NoError(t, err)
	require.Equal(t, f.install.ConfigurationRevision, current.ConfigurationRevision)
}
