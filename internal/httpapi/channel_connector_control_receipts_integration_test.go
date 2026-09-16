//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

const controlReceiptClaimHTTPPath = "/api/v1/channel-connector/control-events/claim-next"

func TestChannelConnectorControlReceiptCommitDedupeAndFixedBoundary(t *testing.T) {
	t.Parallel()
	f := newControlReceiptHTTPFixture(t)
	path := controlReceiptHTTPPath(t, f)
	body := controlReceiptHTTPBody(t, "delivery-1", `{"number":9007199254740993,"action":"unsuspend"}`)
	accepted := f.post(t, path, body, f.token, http.StatusAccepted)
	require.Equal(t, "pending", accepted["state"])
	require.Nil(t, accepted["last_installation_id"])
	endID := testPublicID(t, publicid.KindIntegrationInstall, f.otherInstall.ID)
	require.Equal(t, endID, accepted["end_installation_id"])
	require.Len(t, accepted, 4, "ACK contains durable receipt state and boundaries, not launched agents")
	id := mustPublicHTTPID(t, publicid.KindIntegrationControlReceipt, channelReceiptString(t, accepted, "receipt_id"))
	var appID, orgID, end uuid.UUID
	var payload string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT org_id,integration_app_id,end_install_id,payload::text
		FROM integration_control_receipts WHERE id=$1`, id).Scan(&orgID, &appID, &end, &payload))
	require.Equal(t, f.app.OrgID, orgID)
	require.Equal(t, f.app.ID, appID)
	require.Equal(t, f.otherInstall.ID, end)
	require.Contains(t, payload, "9007199254740993")
	createControlHTTPInstallation(t, f, f.project.ProjectUUID, "103")
	replayed := f.post(t, path,
		controlReceiptHTTPBody(t, "delivery-1", `{"action":"unsuspend","number":9007199254740993.0}`),
		f.token, http.StatusAccepted)
	require.Equal(t, accepted, replayed, "a later connection cannot move an existing receipt's end")
	f.post(t, path, controlReceiptHTTPBody(t, "delivery-1", `{"action":"suspend"}`), f.token, http.StatusConflict)
	otherTenant := strings.Replace(body, `"provider_tenant_id":"42"`, `"provider_tenant_id":"43"`, 1)
	f.post(t, path, otherTenant, f.token, http.StatusConflict)
	other := f
	other.app = f.otherApp
	independent := other.post(t, controlReceiptHTTPPath(t, other), body, f.token, http.StatusAccepted)
	require.NotEqual(t, accepted["receipt_id"], independent["receipt_id"])
	require.Nil(t, independent["end_installation_id"], "control receipt does not require an active child")
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_control_receipt_commit() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.event_id='commit-fails' THEN RAISE EXCEPTION 'test control commit failure'; END IF;
		RETURN NEW; END $$;
		CREATE CONSTRAINT TRIGGER reject_control_receipt_commit AFTER INSERT ON integration_control_receipts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_control_receipt_commit()`)
	require.NoError(t, err)
	failed := f.post(t, path, controlReceiptHTTPBody(t, "commit-fails", `{}`), f.token, http.StatusInternalServerError)
	require.NotContains(t, failed, "receipt_id")
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM integration_control_receipts WHERE event_id='commit-fails'`).Scan(&count))
	require.Zero(t, count)
	_, err = f.pool.Exec(t.Context(), `DROP TRIGGER reject_control_receipt_commit ON integration_control_receipts`)
	require.NoError(t, err)
	f.post(t, path, controlReceiptHTTPBody(t, "commit-fails", `{}`), f.token, http.StatusAccepted)
}

func TestChannelConnectorControlReceiptAuthenticationAndValidation(t *testing.T) {
	f := newControlReceiptHTTPFixture(t)
	path := controlReceiptHTTPPath(t, f)
	body := controlReceiptHTTPBody(t, "valid-event", `{}`)
	for _, tc := range []struct {
		name, body, token string
		status            int
	}{
		{"missing bearer", body, "", http.StatusUnauthorized},
		{"account bearer", body, f.project.AdminToken, http.StatusForbidden},
		{"cross capability pair", body, f.otherToken, http.StatusNotFound},
		{"array payload", controlReceiptHTTPBody(t, "array", `[]`), f.token, http.StatusBadRequest},
		{"null payload", controlReceiptHTTPBody(t, "null", `null`), f.token, http.StatusBadRequest},
		{"duplicate key", `{"event_id":"duplicate","provider_tenant_id":"42","payload":{"v":1,"v":2}}`,
			f.token, http.StatusBadRequest},
		{"unsafe text", controlReceiptHTTPBody(t, "unsafe", `{"v":"\u0000"}`), f.token, http.StatusBadRequest},
		{"empty event", controlReceiptHTTPBody(t, " ", `{}`), f.token, http.StatusBadRequest},
		{"caller project", strings.TrimSuffix(body, "}") + `,"project_id":"foreign"}`, f.token, http.StatusBadRequest},
		{"trailing body", body + `{}`, f.token, http.StatusBadRequest},
		{"payload size", controlReceiptHTTPBody(t, "large", `{"v":"`+strings.Repeat("x", 65536)+`"}`),
			f.token, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) { f.post(t, path, tc.body, tc.token, tc.status) })
	}
	f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.otherToken, http.StatusForbidden)
	for _, route := range []string{path, controlReceiptClaimHTTPPath} {
		req := httptest.NewRequest(http.MethodPost, "https://untrusted.example"+route, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, req)
		require.Equal(t, http.StatusNotFound, response.Code)
	}
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_control_receipts`).Scan(&count))
	require.Zero(t, count)
	accepted := f.post(t, path, controlReceiptHTTPBody(t, "opaque-authority",
		`{"project_id":"foreign","integration_app_id":"foreign","last_installation_id":"foreign"}`),
		f.token, http.StatusAccepted)
	require.Nil(t, accepted["last_installation_id"], "payload fields cannot choose authority or mutable progress")
}

func TestChannelConnectorControlReceiptClaimProgressAndRetry(t *testing.T) {
	t.Parallel()
	f := newControlReceiptHTTPFixture(t)
	accepted := f.post(t, controlReceiptHTTPPath(t, f), controlReceiptHTTPBody(t, "progress", `{}`),
		f.token, http.StatusAccepted)
	claim := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	require.Equal(t, accepted["receipt_id"], claim["receipt_id"])
	require.Equal(t, float64(1), claim["attempts_since_progress"])
	require.Equal(t, testPublicID(t, publicid.KindIntegrationApp, f.app.ID), claim["integration_app_id"])
	require.NotContains(t, claim, "project_id")
	require.NotContains(t, claim, "integration_install_id")
	path := controlReceiptHTTPPath(t, f) + "/" + channelReceiptString(t, accepted, "receipt_id") + "/complete"
	finish := controlReceiptHTTPFinish(t, claim, openapi.ChannelControlOutcomeYield)
	firstID := testPublicID(t, publicid.KindIntegrationInstall, f.install.ID)
	finish.LastInstallationId = &firstID
	wrong := finish
	wrong.LeaseToken = uuid.New()
	f.post(t, path, workflowHTTPJSON(t, wrong), f.token, http.StatusConflict)
	wrong = finish
	wrong.LeaseGeneration++
	f.post(t, path, workflowHTTPJSON(t, wrong), f.token, http.StatusConflict)
	f.post(t, path, workflowHTTPJSON(t, finish), f.otherToken, http.StatusNotFound)
	progress := f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusOK)
	require.Equal(t, "pending", progress["state"])
	require.Equal(t, firstID, progress["last_installation_id"])
	require.Equal(t, accepted["end_installation_id"], progress["end_installation_id"])
	second := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	require.Equal(t, firstID, second["last_installation_id"])
	require.Equal(t, float64(1), second["attempts_since_progress"], "acknowledged progress resets retry pressure")
	require.NotEqual(t, claim["lease_token"], second["lease_token"])
	f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusConflict)
	finish = controlReceiptHTTPFinish(t, second, openapi.ChannelControlOutcomeYield)
	finish.LastInstallationId = &firstID
	f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusConflict)
	newer := createControlHTTPInstallation(t, f, f.install.ProjectID, "after-progress-bound")
	newerID := testPublicID(t, publicid.KindIntegrationInstall, newer.ID)
	finish.LastInstallationId = &newerID
	f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusConflict)
	finish = controlReceiptHTTPFinish(t, second, openapi.ChannelControlOutcomeRetry)
	finish.LastError = json.RawMessage(`{"code":"rate_limited"}`)
	delay := int64(60000)
	finish.RetryAfterMs = &delay
	retried := f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusOK)
	require.Equal(t, firstID, retried["last_installation_id"])
	id := mustPublicHTTPID(t, publicid.KindIntegrationControlReceipt, channelReceiptString(t, accepted, "receipt_id"))
	var honorsDelay bool
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT available_at >= updated_at + interval '60 seconds'
		AND lease_token IS NULL FROM integration_control_receipts WHERE id=$1`, id).Scan(&honorsDelay))
	require.True(t, honorsDelay)
	requestRawWithHeaders(t, f.handler, http.MethodPost, controlReceiptClaimHTTPPath,
		controlReceiptHTTPClaimBody(t, f.app), http.StatusNoContent, authHeaders(f.token))
	_, err := f.pool.Exec(t.Context(), `UPDATE integration_control_receipts SET available_at=now()-interval '1 second'
		WHERE id=$1`, id)
	require.NoError(t, err)
	third := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	require.Equal(t, float64(2), third["attempts_since_progress"])
	require.Equal(t, firstID, third["last_installation_id"])
	finish = controlReceiptHTTPFinish(t, third, openapi.ChannelControlOutcomeCompleted)
	endID := channelReceiptString(t, accepted, "end_installation_id")
	finish.LastInstallationId = &endID
	completed := f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusOK)
	require.Equal(t, "completed", completed["state"])
	require.Equal(t, endID, completed["last_installation_id"])
}

func TestChannelConnectorControlReceiptLostCompletionPreservesProgress(t *testing.T) {
	t.Parallel()
	f := newControlReceiptHTTPFixture(t)
	body := controlReceiptHTTPBody(t, "lost-completion", `{}`)
	accepted := f.post(t, controlReceiptHTTPPath(t, f), body, f.token, http.StatusAccepted)
	claim := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	path := controlReceiptHTTPPath(t, f) + "/" + channelReceiptString(t, accepted, "receipt_id") + "/complete"
	finish := controlReceiptHTTPFinish(t, claim, openapi.ChannelControlOutcomeYield)
	lastID := testPublicID(t, publicid.KindIntegrationInstall, f.install.ID)
	finish.LastInstallationId = &lastID
	committed := make(chan int, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		recorded := httptest.NewRecorder()
		f.handler.ServeHTTP(recorded, req)
		committed <- recorded.Code
		// The real handler committed, but the client receives no status or body.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test HTTP server does not support connection hijacking")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path,
		strings.NewReader(workflowHTTPJSON(t, finish)))
	require.NoError(t, err)
	req.Host = "api:8080"
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(req)
	if response != nil {
		_ = response.Body.Close()
	}
	require.Error(t, err, "transport must actually lose the committed response")
	require.Equal(t, http.StatusOK, <-committed)
	duplicate := f.post(t, controlReceiptHTTPPath(t, f), body, f.token, http.StatusAccepted)
	require.Equal(t, lastID, duplicate["last_installation_id"])
	require.Equal(t, accepted["end_installation_id"], duplicate["end_installation_id"])
	claimed := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	require.Equal(t, lastID, claimed["last_installation_id"])
	require.Equal(t, float64(1), claimed["attempts_since_progress"])
	f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusConflict)
	finish = controlReceiptHTTPFinish(t, claimed, openapi.ChannelControlOutcomeCompleted)
	f.post(t, path, workflowHTTPJSON(t, finish), f.token, http.StatusOK)
	duplicate = f.post(t, controlReceiptHTTPPath(t, f), body, f.token, http.StatusAccepted)
	require.Equal(t, "completed", duplicate["state"])
}

func TestChannelConnectorControlReceiptCannotAuthorizeAgentAdmission(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	message := f.delivery(t, "ordinary-message")
	base := f.channelReceiptHTTPFixture
	accepted := f.post(t, controlReceiptHTTPPath(t, base), workflowHTTPJSON(t, openapi.ChannelInboundControlEventRequest{
		EventId: "control-event", ProviderTenantId: f.install.ProviderTenantID, Payload: json.RawMessage(`{}`),
	}), f.token, http.StatusAccepted)
	claim := f.post(t, controlReceiptClaimHTTPPath, controlReceiptHTTPClaimBody(t, f.app), f.token, http.StatusOK)
	controlProof := controlReceiptHTTPFinish(t, claim, openapi.ChannelControlOutcomeCompleted)
	controlID := mustPublicHTTPID(t, publicid.KindIntegrationControlReceipt,
		channelReceiptString(t, accepted, "receipt_id"))
	forged := message
	forged.ContentBlocks = []openapi.CreateAgentInputContentBlock{}
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"text","text":"not an authorized message"}]`),
		&forged.ContentBlocks))
	forged.Receipt = openapi.ChannelEventLease{
		ReceiptId:  channelReceiptString(t, accepted, "receipt_id"),
		LeaseToken: controlProof.LeaseToken, LeaseGeneration: controlProof.LeaseGeneration,
	}
	f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, forged), f.token, http.StatusBadRequest)
	forged.Receipt.ReceiptId = testPublicID(t, publicid.KindIntegrationEventReceipt, controlID)
	f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, forged), f.token, http.StatusConflict)
	var inputs int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1`, f.project.ProjectUUID).Scan(&inputs))
	require.Zero(t, inputs)
	messageID := mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, message.Receipt.ReceiptId)
	wrongPath := controlReceiptHTTPPath(t, base) + "/" +
		testPublicID(t, publicid.KindIntegrationControlReceipt, messageID) + "/complete"
	messageProof := openapi.CompleteChannelConnectorControlEventRequest{
		LeaseToken: message.Receipt.LeaseToken, LeaseGeneration: message.Receipt.LeaseGeneration,
		Outcome: openapi.ChannelControlOutcomeCompleted,
	}
	f.post(t, wrongPath, workflowHTTPJSON(t, messageProof), f.token, http.StatusConflict)
	ordinaryFinish := openapi.CompleteChannelConnectorEventRequest{
		LeaseToken: controlProof.LeaseToken, LeaseGeneration: controlProof.LeaseGeneration,
		State: openapi.ChannelEventOutcomeCompleted,
	}
	f.post(t, f.completePath(t, f.app, f.install, testPublicID(t, publicid.KindIntegrationEventReceipt, controlID)),
		workflowHTTPJSON(t, ordinaryFinish), f.token, http.StatusConflict)
	controlPath := controlReceiptHTTPPath(t, base) + "/" + channelReceiptString(t, accepted, "receipt_id") + "/complete"
	f.post(t, controlPath, workflowHTTPJSON(t, controlProof), f.token, http.StatusOK)
	f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, message), f.token, http.StatusOK)
}

func controlReceiptHTTPFinish(
	t *testing.T, claim map[string]any, outcome openapi.ChannelControlOutcome,
) openapi.CompleteChannelConnectorControlEventRequest {
	t.Helper()
	var typed openapi.ChannelConnectorControlReceipt
	require.NoError(t, json.Unmarshal([]byte(workflowHTTPJSON(t, claim)), &typed))
	return openapi.CompleteChannelConnectorControlEventRequest{
		LeaseToken: typed.LeaseToken, LeaseGeneration: typed.LeaseGeneration, Outcome: outcome,
	}
}

func controlReceiptHTTPPath(t *testing.T, f channelReceiptHTTPFixture) string {
	t.Helper()
	return providerControlHTTPPath(t, f) + "/control-events"
}

func controlReceiptHTTPBody(t *testing.T, eventID, payload string) string {
	t.Helper()
	return workflowHTTPJSON(t, openapi.ChannelInboundControlEventRequest{
		EventId: eventID, ProviderTenantId: "42", Payload: json.RawMessage(payload),
	})
}

func controlReceiptHTTPClaimBody(t *testing.T, app integrationstore.IntegrationAppRecord) string {
	t.Helper()
	return workflowHTTPJSON(t, openapi.ClaimNextChannelConnectorControlEventRequest{
		Capability: openapi.ChannelConnectorCapability{ConnectorKey: app.ConnectorKey, Provider: app.Provider},
		LeaseMs:    60000,
	})
}

func newControlReceiptHTTPFixture(t *testing.T) channelReceiptHTTPFixture {
	t.Helper()
	f := channelReceiptHTTPFixture{pool: openIntegrationDB(t, t.Context())}
	var err error
	f.token, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	f.otherToken, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{ID: "control-gateway", Token: f.token, Capabilities: []channelconnector.Capability{
			{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "github"},
		}},
		{ID: "other-control-gateway", Token: f.otherToken, Capabilities: []channelconnector.Capability{
			{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "discord"},
			{ConnectorKey: "customer_connector", Provider: "github"},
		}},
	})
	require.NoError(t, err)
	f.handler = newIntegrationServer(f.pool, WithChannelConnectorAuthenticator(auth),
		WithInternalAPIOrigins([]string{"http://api:8080"}), WithPublicURL("https://omnara.example.test"))
	f.project = bootstrapPublicHTTPProject(t, f.handler, "control-receipt")
	createApp := func(ref string) integrationstore.IntegrationAppRecord {
		app, err := f.project.Store.Integrations().CreateIntegrationApp(t.Context(),
			integrationstore.CreateIntegrationAppInput{
				OrgID: f.project.OrgUUID, Provider: "github", ProviderAppRef: ref, DisplayName: "Control app",
				ConnectorKey: channelconnector.BuiltInConnectorKey, State: integrationstore.IntegrationAppStateActive,
			})
		require.NoError(t, err)
		return app
	}
	f.app, f.otherApp = createApp("41"), createApp("51")
	f.install = createControlHTTPInstallation(t, f, f.project.ProjectUUID, "101")
	other, err := f.project.Store.Identity().CreateProjectForPrincipal(t.Context(),
		identitystore.CreateProjectForPrincipalInput{
			OrgID: f.project.OrgUUID, Creator: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
			Name: "Other control project", IdempotencyKey: "other-control-project",
		})
	require.NoError(t, err)
	f.otherInstall = createControlHTTPInstallation(t, f, other.ID, "102")
	return f
}

func createControlHTTPInstallation(
	t *testing.T, f channelReceiptHTTPFixture, projectID uuid.UUID, repository string,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	install, err := f.project.Store.Integrations().UpsertIntegrationInstall(t.Context(),
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: f.app.OrgID, ProjectID: projectID, IntegrationAppID: f.app.ID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID), Provider: "github",
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateDisabled, ProviderTenantID: "42", ProviderAccountRef: repository,
		})
	require.NoError(t, err)
	return install
}
