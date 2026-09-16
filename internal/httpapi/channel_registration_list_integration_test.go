//go:build integration

package httpapi

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestRegisteredChannelInventoryIsIndependentOfAgentBindings(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "registered-inventory")
	parent := createExternalRequestHTTPChannel(t, handler, project, "parent")
	other := createExternalRequestHTTPChannel(t, handler, project, "other-install")
	base := project.ProjectPath + "/integration-installs/"
	path := base + testPublicID(t, publicid.KindIntegrationInstall, parent.IntegrationInstallID) + "/channels"
	otherPath := base + testPublicID(t, publicid.KindIntegrationInstall, other.IntegrationInstallID) + "/channels"
	body := workflowHTTPJSON(t, map[string]any{
		"source": "external", "definition_id": testPublicID(t, publicid.KindChannelDefinition, parent.ChannelDefinitionID),
		"parent_channel_id": testPublicID(t, publicid.KindIntegrationTarget, parent.ID),
		"provider_ref":      "empty-label-address", "provider_ref_kind": "thread", "name": "",
		"provider_metadata": map[string]any{"private_provider_field": "not-in-inventory"},
	})
	registered := requestJSONWithHeaders(t, handler, http.MethodPost, path, body, "", http.StatusOK,
		authHeaders(project.AdminToken))
	viewer, _ := createChannelHTTPKey(t, project, "viewer")
	get := func(target string, status int) map[string]any {
		return requestJSONWithHeaders(t, handler, http.MethodGet, target, "", "", status, authHeaders(viewer))
	}
	page := get(path, http.StatusOK)
	items := testutil.RequireType[[]any](t, page["channels"])
	require.Len(t, items, 2)
	require.Equal(t, registered, items[0], "GET and POST use the same projection")
	item := testutil.RequireType[map[string]any](t, items[0])
	require.Equal(t, "", item["name"])
	require.Equal(t, "empty-label-address", item["provider_ref"])
	require.Equal(t, "thread", item["provider_ref_kind"])
	require.NotContains(t, item, "provider_metadata")
	require.NotContains(t, item, "capabilities")
	require.NotContains(t, item, "grants")

	// One transaction gives both addresses the same PostgreSQL timestamp. The
	// UUID tie-breaker must still allow every row to appear exactly once.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var sameTime []integrationstore.IntegrationTargetRecord
	for _, ref := range []string{"same-time-a", "same-time-b"} {
		channel, err := project.Store.Integrations().CreateIntegrationTargetTx(ctx, tx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: project.ProjectUUID, IntegrationInstallID: parent.IntegrationInstallID,
				ChannelDefinitionID: parent.ChannelDefinitionID, ProviderRef: ref, ProviderRefKind: "thread",
			})
		require.NoError(t, err)
		sameTime = append(sameTime, channel)
	}
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, sameTime[0].CreatedAt, sameTime[1].CreatedAt)
	seen := make(map[string]bool)
	cursor := ""
	for i := range 4 {
		page = get(path+"?limit=1&cursor="+url.QueryEscape(cursor), http.StatusOK)
		items = testutil.RequireType[[]any](t, page["channels"])
		require.Len(t, items, 1)
		id := channelReceiptString(t, testutil.RequireType[map[string]any](t, items[0]), "channel_id")
		require.False(t, seen[id], "no duplicate across equal-timestamp boundaries")
		seen[id] = true
		if i < 3 {
			cursor = channelReceiptString(t, page, "next_cursor")
			get(otherPath+"?cursor="+url.QueryEscape(cursor), http.StatusBadRequest)
		} else {
			require.Nil(t, page["next_cursor"])
		}
	}
	require.Len(t, seen, 4)
	get(path+"?cursor=invalid", http.StatusBadRequest)
	get(path+"?limit=101", http.StatusBadRequest)
	get(base+testPublicID(t, publicid.KindIntegrationInstall, uuid.New())+"/channels", http.StatusNotFound)
	foreign, err := project.Store.Identity().CreateProjectForPrincipal(ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: project.OrgUUID, Creator: identitystore.NewUserPrincipal(project.AdminUserUUID),
			Name: "Foreign inventory", IdempotencyKey: "foreign-channel-inventory",
		})
	require.NoError(t, err)
	foreignPath := "/api/v1/orgs/" + project.OrgID + "/projects/" +
		testPublicID(t, publicid.KindProject, foreign.ID) + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, parent.IntegrationInstallID) + "/channels"
	requestJSONWithHeaders(t, handler, http.MethodGet, foreignPath, "", "", http.StatusNotFound,
		authHeaders(project.AdminToken))
	// Retiring addresses hides only local registration. Inventory never reads or
	// mutates provider content and does not create any agent bindings.
	_, err = pool.Exec(ctx, `UPDATE integration_targets SET deleted_at=clock_timestamp()
		WHERE project_id=$1 AND integration_install_id=$2`, project.ProjectUUID, parent.IntegrationInstallID)
	require.NoError(t, err)
	page = get(path, http.StatusOK)
	require.Empty(t, testutil.RequireType[[]any](t, page["channels"]))
	require.Nil(t, page["next_cursor"])
	var grants int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
		WHERE project_id=$1 AND integration_install_id=$2`, project.ProjectUUID, parent.IntegrationInstallID).Scan(&grants))
	require.Zero(t, grants)
	err = project.Store.Integrations().DeleteIntegrationInstall(ctx, project.ProjectUUID, parent.IntegrationInstallID)
	require.NoError(t, err)
	get(path, http.StatusNotFound)
}
