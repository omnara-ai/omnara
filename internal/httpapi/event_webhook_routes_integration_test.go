//go:build integration

package httpapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookRoutes(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "webhook-routes")
	endpoint := f.project.ProjectPath + "/event-webhooks"
	other := integrationHTTPSecondProject(t, f.handler, f.project)
	operator := customIntegrationHTTPKey(t, f.handler, f.project, "webhook-operator", "operator")
	plainLaunch := map[string]any{"config": f.configID, "profile": f.profileID}
	launched := requestJSONWithHeaders(t, f.handler, http.MethodPost, f.project.ProjectPath+"/agents",
		integrationHTTPJSON(t, plainLaunch), "webhook-agent", http.StatusCreated, authHeaders(f.launchToken))
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	for _, scopeType := range []string{"project", "profile", "agent"} {
		scope := map[string]any{"type": scopeType}
		query := "?scope_type=" + scopeType
		if scopeType == "profile" {
			scope["agent_profile_id"] = f.profileID
			query += "&agent_profile_id=" + f.profileID
		}
		if scopeType == "agent" {
			scope["agent_id"] = agentID
			query += "&agent_id=" + agentID
		}
		body := integrationHTTPJSON(t, map[string]any{"scope": scope,
			"url": "https://example.com/events", "event_types": []string{"tool_call_update"}})
		requestJSONWithHeaders(t, f.handler, http.MethodPost,
			endpoint, body, "", http.StatusForbidden,
			authHeaders(operator))
		token := f.launchToken
		first := requestJSONWithHeaders(t, f.handler, http.MethodPost,
			endpoint, body, "", http.StatusCreated,
			authHeaders(token))
		require.Equal(t, scope, first["scope"])
		require.Equal(t, []any{"tool_call_update"}, first["event_types"])
		requestJSONWithHeaders(t, f.handler, http.MethodPost,
			endpoint, body, "", http.StatusConflict, authHeaders(token))
		id := testutil.RequireType[string](t, first["id"])
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			crossProjectBody := ""
			if method == http.MethodPatch {
				crossProjectBody = `{"event_types":["model_output"]}`
			}
			requestJSONWithHeaders(t, f.handler, method, other.ProjectPath+"/event-webhooks/"+id,
				crossProjectBody, "", http.StatusNotFound, authHeaders(f.project.AdminToken))
		}
		for _, invalid := range []string{`{}`, `{"url":null}`, `{"event_types":null}`, `{"event_types":[]}`} {
			requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id, invalid,
				"", http.StatusBadRequest, authHeaders(token))
		}
		listed := requestJSONWithHeaders(t, f.handler, http.MethodGet,
			endpoint+query, "", "", http.StatusOK, authHeaders(token))
		require.Equal(t, []any{first}, listed["data"])
		requestJSONWithHeaders(t, f.handler, http.MethodGet,
			endpoint+query, "", "", http.StatusOK, authHeaders(operator))
		if scopeType != "project" {
			requestJSONWithHeaders(t, f.handler, http.MethodGet,
				other.ProjectPath+"/event-webhooks"+query, "", "", http.StatusNotFound,
				authHeaders(f.project.AdminToken))
		}
		for _, method := range []string{http.MethodPatch, http.MethodDelete} {
			deniedBody := ""
			if method == http.MethodPatch {
				deniedBody = `{"url":"https://example.com/updated","event_types":["model_output"]}`
			}
			requestJSONWithHeaders(t, f.handler, method, endpoint+"/"+id,
				deniedBody, "", http.StatusForbidden, authHeaders(operator))
		}
		updated := requestJSONWithHeaders(t, f.handler, http.MethodPatch, endpoint+"/"+id,
			`{"url":"https://example.com/updated","event_types":["model_output"]}`, "", http.StatusOK, authHeaders(token))
		require.Equal(t, first["scope"], updated["scope"])
		require.Equal(t, f.project.ProjectID, updated["project_id"])
		require.Equal(t, "https://example.com/updated", updated["url"])
		second := requestJSONWithHeaders(t, f.handler, http.MethodPost,
			endpoint, body, "", http.StatusCreated, authHeaders(token))
		requestJSONWithHeaders(t, f.handler, http.MethodPatch,
			endpoint+"/"+id, `{"url":"https://example.com/events","event_types":["tool_call_update"]}`,
			"", http.StatusConflict, authHeaders(token))
		requestJSONWithHeaders(t, f.handler, http.MethodDelete,
			endpoint+"/"+testutil.RequireType[string](t, second["id"]), "", "", http.StatusNoContent, authHeaders(token))
		requestJSONWithHeaders(t, f.handler, http.MethodPatch,
			endpoint+"/"+id, body, "", http.StatusBadRequest,
			authHeaders(token))
		requestJSONWithHeaders(t, f.handler, http.MethodDelete,
			endpoint+"/"+id, "", "", http.StatusNoContent, authHeaders(token))
		listed = requestJSONWithHeaders(t, f.handler, http.MethodGet,
			endpoint+query, "", "", http.StatusOK, authHeaders(token))
		require.Empty(t, testutil.RequireType[[]any](t, listed["data"]))
		requestJSONWithHeaders(t, f.handler, http.MethodPost,
			endpoint, integrationHTTPJSON(t, map[string]any{"scope": scope,
				"url": "https://example.com/updated", "event_types": []string{"model_output"}}),
			"", http.StatusCreated,
			authHeaders(token))
	}

	for _, scope := range []map[string]any{
		{"type": "org"},
		{},
		{"type": "project", "agent_id": agentID},
		{"type": "agent"},
		{"type": "profile", "agent_id": agentID},
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodPost, endpoint, integrationHTTPJSON(t,
			map[string]any{"scope": scope, "url": "https://example.com/events",
				"event_types": []string{"tool_call_update"}}),
			"", http.StatusBadRequest, authHeaders(f.project.AdminToken))
	}
	for _, query := range []string{
		"", "?scope_type=org", "?scope_type=agent", "?scope_type=project&agent_id=" + agentID,
		"?scope_type=agent&agent_id=invalid", "?scope_type=profile&agent_profile_id=invalid",
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+query, "", "",
			http.StatusBadRequest, authHeaders(f.project.AdminToken))
	}
	for _, query := range []string{
		"?scope_type=agent&agent_id=" + testPublicID(t, publicid.KindAgent, uuid.New()),
		"?scope_type=profile&agent_profile_id=" + testPublicID(t, publicid.KindAgentProfile, uuid.New()),
	} {
		requestJSONWithHeaders(t, f.handler, http.MethodGet, endpoint+query, "", "",
			http.StatusNotFound, authHeaders(f.project.AdminToken))
	}
	requestJSONWithHeaders(t, f.handler, http.MethodPost, other.ProjectPath+"/event-webhooks",
		integrationHTTPJSON(t, map[string]any{
			"scope": map[string]any{"type": "agent", "agent_id": agentID},
			"url":   "https://example.com/events", "event_types": []string{"tool_call_update"},
		}), "", http.StatusNotFound, authHeaders(f.project.AdminToken))
	require.NoError(t, f.project.Store.Execution().DeleteAgentProfile(t.Context(), f.project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindAgentProfile, f.profileID)))
	requestJSONWithHeaders(t, f.handler, http.MethodGet,
		endpoint+"?scope_type=profile&agent_profile_id="+f.profileID, "", "",
		http.StatusNotFound, authHeaders(f.project.AdminToken))
}

func TestEventWebhookSigningSecrets(t *testing.T) {
	t.Parallel()
	handler := newIntegrationServer(openIntegrationDB(t, t.Context()))
	project := bootstrapPublicHTTPProject(t, handler, "webhook-signing")
	endpoint := project.ProjectPath + "/event-webhooks"
	for _, owner := range []string{"org", "project"} {
		for i, value := range []string{
			"invalid", base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")),
		} {
			input := secretstore.CreateSecretInput{OrgID: project.OrgUUID, OwnerKind: owner,
				Name:     fmt.Sprintf("webhook-%s-%d", owner, i),
				Material: secrets.GenericMaterial{Value: value},
				Actor:    httpUserPrincipal(project.AdminUserUUID)}
			if owner == "project" {
				input.OwnerProjectID = project.ProjectUUID
			}
			secret, _, err := project.Store.Secrets().CreateSecret(t.Context(), input)
			require.NoError(t, err)
			body := map[string]any{"scope": map[string]any{"type": "project"},
				"url":               "https://example.com/events",
				"event_types":       []string{"tool_call_update"},
				"signing_secret_id": testPublicID(t, publicid.KindSecret, secret.ID)}
			if owner == "org" {
				requestJSONWithHeaders(t, handler, http.MethodPost, endpoint, integrationHTTPJSON(t, body),
					"", http.StatusNotFound, authHeaders(project.AdminToken))
				_, err := project.Store.Secrets().CreateSecretGrant(t.Context(), secretstore.CreateSecretGrantInput{
					OrgID: project.OrgUUID, SecretID: secret.ID, TargetProjectID: project.ProjectUUID,
					Actor: httpUserPrincipal(project.AdminUserUUID),
				})
				require.NoError(t, err)
			}
			status := http.StatusCreated
			if i == 0 {
				status = http.StatusBadRequest
			}
			response := requestJSONWithHeaders(t, handler, http.MethodPost,
				endpoint, integrationHTTPJSON(t, body), "",
				status, authHeaders(project.AdminToken))
			if i == 1 {
				id := testutil.RequireType[string](t, response["id"])
				updated := requestJSONWithHeaders(t, handler, http.MethodPatch, endpoint+"/"+id,
					`{"url":"https://example.com/events"}`,
					"", http.StatusOK, authHeaders(project.AdminToken))
				require.Equal(t, body["signing_secret_id"], updated["signing_secret_id"])
				require.Equal(t, response["event_types"], updated["event_types"])
				updated = requestJSONWithHeaders(t, handler, http.MethodPatch, endpoint+"/"+id,
					`{"signing_secret_id":null}`, "", http.StatusOK, authHeaders(project.AdminToken))
				require.NotContains(t, updated, "signing_secret_id")
				require.Equal(t, response["url"], updated["url"])
				require.Equal(t, response["event_types"], updated["event_types"])
				requestJSONWithHeaders(t, handler, http.MethodDelete, endpoint+"/"+id,
					"", "", http.StatusNoContent, authHeaders(project.AdminToken))
			}
		}
	}
}

func TestEventWebhookSigningValidationUsesTransactionConnection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "webhook-signing-connection")
	secret, _, err := project.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: project.OrgUUID, OwnerKind: "project", OwnerProjectID: project.ProjectUUID, Name: "webhook-signing",
		Material: secrets.GenericMaterial{
			Value: base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012")),
		},
		Actor: httpUserPrincipal(project.AdminUserUUID),
	})
	require.NoError(t, err)
	for range pool.Config().MaxConns - 1 {
		conn, err := pool.Acquire(ctx)
		require.NoError(t, err)
		t.Cleanup(conn.Release)
	}
	settings := webhookconfig.Settings{
		URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}, SigningSecretID: secret.ID,
	}
	webhook, err := project.Store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
		ProjectID: project.ProjectUUID, Scope: executionstore.EventWebhookScope{
			Type: executionstore.EventWebhookScopeProject,
		}, Settings: settings,
	})
	require.NoError(t, err)
	settings.URL = "https://example.com/updated"
	_, err = project.Store.Execution().UpdateEventWebhook(ctx, executionstore.UpdateEventWebhookInput{
		ProjectID: project.ProjectUUID,
		WebhookID: webhook.ID,
		URL:       &settings.URL,
	})
	require.NoError(t, err)
}

func TestProfileEventWebhookReceivesLaunchEvents(t *testing.T) {
	t.Parallel()
	f := newIntegrationLaunchHTTPFixture(t, "webhook-launch")
	webhook := requestJSONWithHeaders(t, f.handler, http.MethodPost,
		f.project.ProjectPath+"/event-webhooks",
		integrationHTTPJSON(t, map[string]any{
			"scope": map[string]any{"type": "profile",
				"agent_profile_id": f.profileID},
			"url": "https://example.com/events", "event_types": []string{"agent_input"},
		}), "", http.StatusCreated, authHeaders(f.project.AdminToken))
	body := integrationHTTPJSON(t, map[string]any{"config": f.configID, "profile": f.profileID})
	endpoint := f.project.ProjectPath + "/agents"
	first := requestJSONWithHeaders(t, f.handler, http.MethodPost,
		endpoint, body, "launch", http.StatusCreated, authHeaders(f.launchToken))
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, first["agent"])["id"])
	replay := requestJSONWithHeaders(t, f.handler, http.MethodPost,
		endpoint, body, "launch", http.StatusOK, authHeaders(f.launchToken))
	require.Equal(t, agentID, testutil.RequireType[map[string]any](t, replay["agent"])["id"])
	id, err := publicid.Decode(publicid.KindAgent, agentID)
	require.NoError(t, err)
	var webhookIDs []uuid.UUID
	require.NoError(t, integrationPoolForHandler(t, f.handler).QueryRow(t.Context(),
		`SELECT array_agg(webhook_id) FROM event_webhook_deliveries WHERE agent_id=$1`, id).Scan(&webhookIDs))
	require.Equal(t, []uuid.UUID{mustPublicHTTPID(t, publicid.KindEventWebhook,
		testutil.RequireType[string](t, webhook["id"]))}, webhookIDs)
}
