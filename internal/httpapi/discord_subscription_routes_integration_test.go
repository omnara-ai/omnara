//go:build integration

package httpapi

import (
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDiscordSubscriptionHTTPThreadIDOnly(t *testing.T) {
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool, WithDiscordClientConfig(discordSetupTestConfig(t)))
	project := bootstrapPublicHTTPProject(t, handler, "discord-thread-subscription")
	secretID := createIntegrationSetupHTTPSecret(t, handler, project, "discord-credentials",
		map[string]any{"kind": "generic", "value": "private-discord-token"})
	integration := configureDiscordHTTPIntegration(t, handler, project, map[string]any{
		"expected_setup_revision": 1, "provider_tenant_id": "111", "credential_secret_id": secretID,
	})
	config := createPublicHTTPAgentConfig(t, handler, project, "subscription-config", "json",
		integrationHTTPJSON(t, integrationHTTPSource(nil)), project.AdminToken, http.StatusCreated)
	headers := authHeaders(project.AdminToken)
	launched := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agents",
		integrationHTTPJSON(t, map[string]any{"config": config["id"]}), "subscription-agent", http.StatusCreated, headers)
	agentID := testutil.RequireType[map[string]any](t, launched["agent"])["id"]
	path := project.ProjectPath + "/integrations/" +
		testPublicID(t, publicid.KindIntegration, integration.ID) + "/subscriptions"
	body := map[string]any{
		"agent_id":     agentID,
		"conversation": map[string]any{"thread_id": "301"},
	}
	first := requestJSONWithHeaders(t, handler, http.MethodPost, path,
		integrationHTTPJSON(t, body), "", http.StatusCreated, headers)
	require.Equal(t, map[string]any{"thread_id": "301"}, first["conversation"])
	subscriptionID := mustPublicHTTPID(
		t, publicid.KindIntegrationSubscription, testutil.RequireType[string](t, first["id"]),
	)
	var kind, ref string
	require.NoError(t, pool.QueryRow(t.Context(),
		`SELECT scope_kind, scope_ref FROM integration_subscriptions WHERE id=$1`,
		subscriptionID).Scan(&kind, &ref))
	require.Equal(t, "thread", kind)
	require.Equal(t, "301", ref)
	body["conversation"] = map[string]any{"guild_id": "500", "channel_id": "300", "thread_id": "301"}
	duplicate := requestJSONWithHeaders(t, handler, http.MethodPost, path,
		integrationHTTPJSON(t, body), "", http.StatusCreated, headers)
	require.Equal(t, first, duplicate, "optional parent/guild metadata cannot split a thread's identity")
	page := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK, headers)
	require.Equal(t, []any{first}, page["data"])
	for _, invalid := range []map[string]any{
		{}, {"guild_id": "500"}, {"thread_id": "301", "channel_id": "invalid"},
	} {
		body["conversation"] = invalid
		requestJSONWithHeaders(t, handler, http.MethodPost, path,
			integrationHTTPJSON(t, body), "", http.StatusBadRequest, headers)
	}
}
