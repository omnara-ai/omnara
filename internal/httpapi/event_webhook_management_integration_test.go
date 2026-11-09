//go:build integration

package httpapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestProjectWebhookManagement(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "project-webhook-management")
	ctx := t.Context()
	endpoint := f.project.ProjectPath + "/event-webhooks"
	headers := authHeaders(f.project.AdminToken)
	launch := func(profile bool) string {
		t.Helper()
		body := map[string]any{"config": f.configID, "name": "Webhook target"}
		if profile {
			body["profile"] = f.profileID
		}
		response := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
			integrationHTTPJSON(t, body), uuid.NewString(), http.StatusCreated, authHeaders(f.launchToken))
		return testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, response["agent"])["id"])
	}
	agentID := launch(true)
	secret, _, err := f.project.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: f.project.OrgUUID, OwnerKind: "project", OwnerProjectID: f.project.ProjectUUID, Name: "webhook-signing",
		Material: secrets.GenericMaterial{
			Value: base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")),
		}, Actor: httpUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	signingID := testPublicID(t, publicid.KindSecret, secret.ID)
	selections := []map[string]any{
		{"type": "project"}, {"type": "profile", "agent_profile_id": f.profileID},
		{"type": "agent", "agent_id": agentID},
	}
	var created []any
	for i, selection := range selections {
		response := requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint,
			integrationHTTPJSON(t, map[string]any{"scope": selection,
				"url":         fmt.Sprintf("https://example.com/events/%d", i),
				"event_types": []string{"agent_input"}, "signing_secret_id": signingID}),
			"", http.StatusCreated, headers)
		created = append(created, response)
	}
	require.NotContains(t, testutil.RequireType[map[string]any](t, created[0]), "scope_name")
	require.NotEmpty(t, testutil.RequireType[map[string]any](t, created[1])["scope_name"])
	require.Equal(t, "Webhook target", testutil.RequireType[map[string]any](t, created[2])["scope_name"])
	first := requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"?limit=2", "", "", http.StatusOK, headers)
	require.Equal(t, created[:2], first["data"])
	cursor := testutil.RequireType[string](t, first["next_cursor"])
	last := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		endpoint+"?limit=2&cursor="+url.QueryEscape(cursor), "", "", http.StatusOK, headers)
	require.Equal(t, created[2:], last["data"])
	require.Nil(t, last["next_cursor"])
	for _, query := range []string{
		"scope_type=project",
		"scope_type=profile&agent_profile_id=" + f.profileID,
		"scope_type=agent&agent_id=" + agentID,
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"?"+query+"&cursor="+url.QueryEscape(cursor),
			"", "", http.StatusBadRequest, headers)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?cursor=invalid"} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+query, "", "", http.StatusBadRequest, headers)
	}

	subscription := testutil.RequireType[map[string]any](t, created[2])
	id := testutil.RequireType[string](t, subscription["id"])
	patchScope := func(scope map[string]any, status int) map[string]any {
		t.Helper()
		return requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id,
			integrationHTTPJSON(t, map[string]any{"scope": scope}), "", status, headers)
	}
	for _, scope := range []map[string]any{selections[0], selections[1], selections[2], selections[0]} {
		updated := patchScope(scope, http.StatusOK)
		require.Equal(t, scope, updated["scope"])
		for _, field := range []string{"id", "url", "created_at", "signing_secret_id", "event_types"} {
			require.Equal(t, subscription[field], updated[field], field)
		}
	}
	queuedAgent := launch(false)
	patchScope(selections[1], http.StatusOK)
	profileList := endpoint + "?scope_type=profile&agent_profile_id=" + f.profileID + "&limit=1"
	profileFirst := requestJSONWithHeaders(t, f.handler, http.MethodGet, profileList,
		"", "", http.StatusOK, headers)
	require.Equal(t, created[1:2], profileFirst["data"])
	profileCursor := testutil.RequireType[string](t, profileFirst["next_cursor"])
	profileLast := requestJSONWithHeaders(t, f.handler, http.MethodGet,
		profileList+"&cursor="+url.QueryEscape(profileCursor), "", "", http.StatusOK, headers)
	profileRows := testutil.RequireType[[]any](t, profileLast["data"])
	require.Len(t, profileRows, 1)
	require.Equal(t, id, testutil.RequireType[map[string]any](t, profileRows[0])["id"])
	require.Nil(t, profileLast["next_cursor"])
	for _, query := range []string{
		"", "scope_type=project&",
		"scope_type=profile&agent_profile_id=" + testPublicID(t, publicid.KindAgentProfile, uuid.New()) + "&",
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"?"+query+"cursor="+url.QueryEscape(profileCursor),
			"", "", http.StatusBadRequest, headers)
	}
	excludedAgent := launch(false)
	includedAgent := launch(true)
	pool := integrationPoolForHandler(t, f.handler)
	webhookUUID := mustPublicHTTPID(t, publicid.KindEventWebhook, id)
	var queuedAgents []uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT array_agg(agent_id ORDER BY agent_id) FROM event_webhook_deliveries WHERE webhook_id=$1",
		webhookUUID).Scan(&queuedAgents))
	require.ElementsMatch(t, []uuid.UUID{
		mustPublicHTTPID(t, publicid.KindAgent, queuedAgent), mustPublicHTTPID(t, publicid.KindAgent, includedAgent),
	}, queuedAgents)
	require.NotContains(t, queuedAgents, mustPublicHTTPID(t, publicid.KindAgent, excludedAgent))
	queuedEndpoint, err := f.project.Store.Execution().GetEventWebhookEndpoint(ctx, f.project.OrgUUID,
		webhookUUID, mustPublicHTTPID(t, publicid.KindAgent, queuedAgent))
	require.NoError(t, err)
	require.Equal(t, subscription["url"], queuedEndpoint.URL)
	require.Equal(t, secret.ID, queuedEndpoint.SigningSecretID)

	other := integrationHTTPSecondProject(t, f.handler, f.project)
	requestJSONWithHeaders(t, f.handler, http.MethodGet,
		other.ProjectPath+"/event-webhooks?cursor="+url.QueryEscape(cursor), "", "", http.StatusBadRequest, headers)
	requestJSONWithHeaders(t, f.handler, http.MethodPatch,
		other.ProjectPath+"/event-webhooks/"+id, `{"scope":{"type":"project"}}`, "", http.StatusNotFound, headers)
	otherSubscription := requestJSONWithHeaders(t, f.handler, http.MethodPost, other.ProjectPath+"/event-webhooks",
		`{"scope":{"type":"project"},"url":"https://example.com/other","event_types":["agent_input"]}`,
		"", http.StatusCreated, headers)
	otherID := testutil.RequireType[string](t, otherSubscription["id"])
	for _, foreignScope := range selections[1:] {
		requestJSONWithHeaders(t, f.handler, http.MethodPatch, other.ProjectPath+"/event-webhooks/"+otherID,
			integrationHTTPJSON(t, map[string]any{"scope": foreignScope}), "", http.StatusNotFound, headers)
	}
	isolated := requestJSONWithHeaders(t, f.handler, http.MethodGet, other.ProjectPath+"/event-webhooks",
		"", "", http.StatusOK, headers)
	require.Equal(t, []any{otherSubscription}, isolated["data"])

	requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint,
		integrationHTTPJSON(t, map[string]any{"scope": selections[0], "url": subscription["url"],
			"event_types": []string{"agent_input"}}), "", http.StatusCreated, headers)
	patchScope(selections[0], http.StatusConflict)
	unchanged := requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id,
		"", "", http.StatusOK, headers)
	require.Equal(t, selections[1], unchanged["scope"])
	requestJSONWithHeaders(t, f.handler, http.MethodPatch, f.project.ProjectPath+"/agent-profiles/"+f.profileID,
		`{"name":"Renamed webhook profile"}`, "", http.StatusOK, headers)
	renamed := requestJSONWithHeaders(t, f.handler, http.MethodGet, profileList, "", "", http.StatusOK, headers)
	for _, row := range testutil.RequireType[[]any](t, renamed["data"]) {
		require.Equal(t, "Renamed webhook profile", testutil.RequireType[map[string]any](t, row)["scope_name"])
	}
	detail := requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+"/"+id, "", "", http.StatusOK, headers)
	require.Equal(t, "Renamed webhook profile", detail["scope_name"])
}
