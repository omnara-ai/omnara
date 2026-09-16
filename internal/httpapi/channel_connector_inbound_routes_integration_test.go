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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

type channelReceiptHTTPFixture struct {
	pool         *pgxpool.Pool
	handler      http.Handler
	project      publicHTTPProject
	token        string
	otherToken   string
	app          integrationstore.IntegrationAppRecord
	otherApp     integrationstore.IntegrationAppRecord
	install      integrationstore.IntegrationInstallRecord
	otherInstall integrationstore.IntegrationInstallRecord
}

func newChannelReceiptHTTPFixture(t *testing.T) channelReceiptHTTPFixture {
	t.Helper()
	ctx := t.Context()
	f := channelReceiptHTTPFixture{pool: openIntegrationDB(t, ctx)}
	var err error
	f.token, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	f.otherToken, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{ID: "receipt-gateway", Token: f.token, Capabilities: connectorTestCapabilities("discord")},
		{ID: "other-gateway", Token: f.otherToken, Capabilities: []channelconnector.Capability{
			{ConnectorKey: "test_connector", Provider: "telegram"},
			{ConnectorKey: "custom_v1", Provider: "discord"},
		}},
	})
	require.NoError(t, err)
	f.handler = newIntegrationServer(f.pool, WithChannelConnectorAuthenticator(auth),
		WithInternalAPIOrigins([]string{"http://api:8080"}), WithPublicURL("https://omnara.example.test"))
	f.project = bootstrapPublicHTTPProject(t, f.handler, "receipt-http")
	createApp := func(ref string) integrationstore.IntegrationAppRecord {
		app, err := f.project.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
			OrgID: f.project.OrgUUID, Provider: "discord", ProviderAppRef: ref,
			DisplayName: ref, ConnectorKey: "test_connector", State: integrationstore.IntegrationAppStateActive,
		})
		require.NoError(t, err)
		return app
	}
	f.app = createApp("receipt-app")
	f.otherApp = createApp("other-receipt-app")
	f.install = f.createInstall(t, f.app, f.project.ProjectUUID, "receipt-install")
	otherProject, err := f.project.Store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: f.project.OrgUUID, Creator: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
			Name: "Other receipt customer", IdempotencyKey: "other-receipt-project",
		},
	)
	require.NoError(t, err)
	f.otherInstall = f.createInstall(t, f.app, otherProject.ID, "other-receipt-install")
	return f
}

func (f channelReceiptHTTPFixture) createInstall(
	t *testing.T,
	app integrationstore.IntegrationAppRecord,
	projectID uuid.UUID,
	ref string,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	install, err := f.project.Store.Integrations().UpsertIntegrationInstall(
		t.Context(),
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: f.project.OrgUUID, ProjectID: projectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(f.project.AdminUserUUID), Provider: app.Provider,
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: ref, ProviderAccountRef: "bot", DisplayName: "Receipt bot",
		},
	)
	require.NoError(t, err)
	return install
}

func (f channelReceiptHTTPFixture) eventPath(t *testing.T, app integrationstore.IntegrationAppRecord) string {
	t.Helper()
	return "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, app.ID) + "/events"
}

func (f channelReceiptHTTPFixture) completePath(
	t *testing.T,
	app integrationstore.IntegrationAppRecord,
	install integrationstore.IntegrationInstallRecord,
	receiptID string,
) string {
	t.Helper()
	return "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, app.ID) +
		"/installations/" + testPublicID(t, publicid.KindIntegrationInstall, install.ID) + "/events/" + receiptID + "/complete"
}

func (f channelReceiptHTTPFixture) event(
	t *testing.T,
	install integrationstore.IntegrationInstallRecord,
	eventID, payload string,
) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"event_id": eventID, "integration_install_id": testPublicID(t, publicid.KindIntegrationInstall, install.ID),
		"payload": json.RawMessage(payload),
	})
	require.NoError(t, err)
	return string(raw)
}

func (f channelReceiptHTTPFixture) post(t *testing.T, path, body, token string, status int) map[string]any {
	t.Helper()
	return requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", status, authHeaders(token))
}

const channelReceiptClaimPath = "/api/v1/channel-connector/events/claim-next"
const channelReceiptClaimBody = `{"lease_ms":60000,` +
	`"capability":{"connector_key":"test_connector","provider":"discord"}}`

func TestChannelConnectorReceiptCommitAndReplay(t *testing.T) {
	t.Parallel()
	f := newChannelReceiptHTTPFixture(t)
	path := f.eventPath(t, f.app)
	body := f.event(t, f.install, "event-1", `{"sequence":9007199254740993,"message":"hello"}`)
	receipt := f.post(t, path, body, f.token, http.StatusAccepted)
	require.Len(t, receipt, 2, "ACK reports only receipt identity and durable state")
	require.Equal(t, "pending", receipt["state"])
	receiptID := mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, channelReceiptString(t, receipt, "receipt_id"))
	var projectID, installID uuid.UUID
	var savedPayload string
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT project_id, integration_install_id, payload::text FROM integration_event_receipts WHERE id = $1`, receiptID,
	).Scan(&projectID, &installID, &savedPayload))
	require.Equal(t, f.install.ProjectID, projectID)
	require.Equal(t, f.install.ID, installID)
	require.Contains(t, savedPayload, "9007199254740993", "HTTP preserves exact JSON numbers")
	var inputCount int
	require.NoError(t, f.pool.QueryRow(
		t.Context(),
		`SELECT count(*) FROM agent_inputs WHERE project_id = $1`, projectID).Scan(&inputCount))
	require.Zero(t, inputCount, "receipt does not dispatch Go behavior or launch agents")
	replayBody := f.event(t, f.install, "event-1", `{"message":"hello","sequence":9007199254740993.0}`)
	replay := f.post(t, path, replayBody, f.token, http.StatusAccepted)
	require.Equal(t, receipt, replay)
	conflict := f.post(t, path, f.event(t, f.install, "event-1", `{"message":"different"}`), f.token, http.StatusConflict)
	require.Equal(t, "idempotency_key_conflict", conflict["code"])
	other := f.post(t, path, f.event(t, f.otherInstall, "event-1", `{}`), f.token, http.StatusAccepted)
	require.NotEqual(t, receipt["receipt_id"], other["receipt_id"], "event IDs are installation scoped")

	// Fail at COMMIT, after INSERT and the receipt read have both succeeded.
	// This trigger exists only in this test's isolated local database.
	_, err := f.pool.Exec(
		t.Context(),
		`
		CREATE FUNCTION reject_test_receipt_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.event_id = 'commit-fails' THEN RAISE EXCEPTION 'test receipt commit failure'; END IF;
		  RETURN NEW;
		END $$;
		CREATE CONSTRAINT TRIGGER reject_test_receipt_commit AFTER INSERT ON integration_event_receipts
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_test_receipt_commit();`)
	require.NoError(t, err)
	failure := f.post(t, path, f.event(t, f.install, "commit-fails", `{}`), f.token, http.StatusInternalServerError)
	require.NotContains(t, failure, "receipt_id", "failed commits cannot be acknowledged")
	require.NotContains(t, mustMarshalChannelRequest(t, failure), "test receipt commit failure")
	var count int
	require.NoError(t, f.pool.QueryRow(
		t.Context(),
		`SELECT count(*) FROM integration_event_receipts WHERE event_id = 'commit-fails'`).Scan(&count))
	require.Zero(t, count)
	_, err = f.pool.Exec(
		t.Context(),
		`DROP TRIGGER reject_test_receipt_commit ON integration_event_receipts`)
	require.NoError(t, err)
	f.post(t, path, f.event(t, f.install, "commit-fails", `{}`), f.token, http.StatusAccepted)
}

func TestChannelConnectorReceiptScopeAndValidation(t *testing.T) {
	f := newChannelReceiptHTTPFixture(t)
	path := f.eventPath(t, f.app)
	body := f.event(t, f.install, "scope-event", `{}`)
	for _, test := range []struct {
		name, path, body, token string
		status                  int
	}{
		{"missing bearer", path, body, "", http.StatusUnauthorized},
		{"account bearer", path, body, f.project.AdminToken, http.StatusForbidden},
		{"cross capability pair", path, body, f.otherToken, http.StatusNotFound},
		{"wrong app", f.eventPath(t, f.otherApp), body, f.token, http.StatusNotFound},
		{"raw installation UUID",
			path, strings.Replace(body,
				testPublicID(t, publicid.KindIntegrationInstall, f.install.ID), f.install.ID.String(), 1), f.token,
			http.StatusBadRequest},
		{"caller project",
			path, strings.TrimSuffix(body, "}") + `,"project_id":"` + f.project.ProjectID + `"}`, f.token,
			http.StatusBadRequest},
		{"array payload", path, f.event(t, f.install, "bad-array", `[]`), f.token, http.StatusBadRequest},
		{"null payload", path, f.event(t, f.install, "bad-null", `null`), f.token, http.StatusBadRequest},
		{"duplicate nested keys",
			path, f.event(t, f.install, "bad-duplicate", `{"nested":{"value":1,"value":2}}`), f.token,
			http.StatusBadRequest},
		{"Postgres unsafe payload",
			path, f.event(t, f.install, "bad-text", `{"value":"\u0000"}`), f.token,
			http.StatusBadRequest},
		{"blank event ID", path, f.event(t, f.install, " ", `{}`), f.token, http.StatusBadRequest},
		{"trailing body", path, body + `{}`, f.token, http.StatusBadRequest},
		{"cross pair claim", channelReceiptClaimPath, channelReceiptClaimBody, f.otherToken, http.StatusForbidden},
		{"batch claim",
			channelReceiptClaimPath, strings.TrimSuffix(channelReceiptClaimBody, "}") + `,"limit":2}`, f.token,
			http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) { f.post(t, test.path, test.body, test.token, test.status) })
	}
	unknownReceiptID := testPublicID(t, publicid.KindIntegrationEventReceipt, uuid.New())
	for _, path := range []string{
		path, channelReceiptClaimPath, f.completePath(t, f.app, f.install, unknownReceiptID),
	} {
		req := httptest.NewRequest(http.MethodPost, "https://untrusted.example"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, req)
		require.Equal(t, http.StatusNotFound, response.Code, "unconfigured origins cannot access connector routes")
	}
	large := f.event(t, f.install, "oversized",
		`{"text":"`+strings.Repeat("x", integrationstore.MaxIntegrationEventPayloadBytes)+`"}`,
	)
	f.post(t, path, large, f.token, http.StatusBadRequest)
	var count int
	require.NoError(t, f.pool.QueryRow(
		t.Context(),
		`SELECT count(*) FROM integration_event_receipts`).Scan(&count))
	require.Zero(t, count, "rejected requests cannot persist a receipt")

	// Authority-looking fields in the payload remain data and cannot select another project.
	opaqueBody := f.event(t, f.install, "opaque-scope", `{"project_id":"foreign","integration_install_id":"foreign"}`)
	accepted := f.post(t, path, opaqueBody, f.token, http.StatusAccepted)
	id := mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, channelReceiptString(t, accepted, "receipt_id"))
	var projectID uuid.UUID
	require.NoError(t, f.pool.QueryRow(
		t.Context(),
		`SELECT project_id FROM integration_event_receipts WHERE id = $1`, id).Scan(&projectID))
	require.Equal(t, f.install.ProjectID, projectID)
}

func TestChannelConnectorReceiptClaimAndComplete(t *testing.T) {
	f := newChannelReceiptHTTPFixture(t)
	ctx := t.Context()
	path := f.eventPath(t, f.app)
	first := f.post(t, path, f.event(t, f.install, "first", `{"value":9007199254740993}`), f.token, http.StatusAccepted)
	second := f.post(t, path, f.event(t, f.otherInstall, "second", `{}`), f.token, http.StatusAccepted)
	otherApp, err := f.project.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: f.project.OrgUUID, Provider: "discord", ConnectorKey: "custom_v1", ProviderAppRef: "other-capability",
		DisplayName: "Other capability", State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	otherInstall := f.createInstall(t, otherApp, f.project.ProjectUUID, "other-capability")
	f.post(t, f.eventPath(t, otherApp), f.event(t, otherInstall, "other-capability", `{}`),
		f.otherToken, http.StatusAccepted)
	claimRaw := requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusOK, authHeaders(f.token))
	require.Contains(t, claimRaw, "9007199254740993")
	var claim map[string]any
	require.NoError(t, json.Unmarshal([]byte(claimRaw), &claim))
	require.Equal(t, first["receipt_id"], claim["receipt_id"])
	require.Equal(t, "processing", claim["state"])
	require.Equal(t, testPublicID(t, publicid.KindIntegrationApp, f.app.ID), claim["integration_app_id"])
	require.Equal(t, testPublicID(t, publicid.KindIntegrationInstall, f.install.ID), claim["integration_install_id"])
	require.Equal(t, float64(1), claim["attempt_count"])
	require.NotContains(t, claim, "project_id")
	_, err = uuid.Parse(channelReceiptString(t, claim, "lease_token"))
	require.NoError(t, err)
	replayBody := f.event(t, f.install, "first", `{"value":9007199254740993}`)
	replay := f.post(t, path, replayBody, f.token, http.StatusAccepted)
	require.Equal(t, "processing", replay["state"])
	secondClaim := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, second["receipt_id"], secondClaim["receipt_id"], "one receipt per claim")
	require.Empty(t, requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusNoContent, authHeaders(f.token)),
		"no claim crosses into another connector key")
	completion := map[string]any{
		"lease_token": claim["lease_token"], "lease_generation": claim["lease_generation"], "state": "completed",
	}
	completePath := f.completePath(t, f.app, f.install, channelReceiptString(t, first, "receipt_id"))
	for _, test := range []struct {
		name, path, token string
		change            func(map[string]any)
		status            int
	}{
		{"wrong installation/project",
			f.completePath(t, f.app, f.otherInstall, channelReceiptString(t, first, "receipt_id")), f.token, nil,
			http.StatusConflict},
		{"wrong app",
			f.completePath(t, f.otherApp, f.install, channelReceiptString(t, first, "receipt_id")), f.token, nil,
			http.StatusNotFound},
		{"cross capability", completePath, f.otherToken, nil, http.StatusNotFound},
		{"wrong token",
			completePath, f.token, func(b map[string]any) { b["lease_token"] = uuid.New().String() },
			http.StatusConflict},
		{"stale generation",
			completePath, f.token, func(b map[string]any) { b["lease_generation"] = float64(2) },
			http.StatusConflict},
		{"caller project",
			completePath, f.token, func(b map[string]any) { b["project_id"] = f.project.ProjectID },
			http.StatusBadRequest},
		{"invalid state", completePath, f.token, func(b map[string]any) { b["state"] = "processing" }, http.StatusBadRequest},
		{"retry without error",
			completePath, f.token, func(b map[string]any) { b["state"] = "pending" },
			http.StatusBadRequest},
		{"failure without error",
			completePath, f.token, func(b map[string]any) { b["state"] = "failed" },
			http.StatusBadRequest},
		{"completed with error",
			completePath, f.token, func(b map[string]any) { b["last_error"] = map[string]any{"message": "failure"} },
			http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := mapsClone(completion)
			if test.change != nil {
				test.change(body)
			}
			f.post(t, test.path, mustMarshalChannelRequest(t, body), test.token, test.status)
		})
	}
	completed := f.post(t, completePath, mustMarshalChannelRequest(t, completion), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"receipt_id": first["receipt_id"], "state": "completed"}, completed)
	f.post(t, completePath, mustMarshalChannelRequest(t, completion), f.token, http.StatusConflict)
	require.Equal(t, completed, f.post(t, path, replayBody, f.token, http.StatusAccepted))

	// Retry requeues durably, replaces the lease, and cannot accept an old consumer's completion.
	secondPath := f.completePath(t, f.app, f.otherInstall, channelReceiptString(t, second, "receipt_id"))
	retryBody := map[string]any{
		"lease_token": secondClaim["lease_token"], "lease_generation": secondClaim["lease_generation"],
		"state": "pending", "last_error": map[string]any{"message": "temporary"},
	}
	retried := f.post(t, secondPath, mustMarshalChannelRequest(t, retryBody), f.token, http.StatusOK)
	require.Equal(t, "pending", retried["state"])
	secondID := mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, channelReceiptString(t, second, "receipt_id"))
	var waiting bool
	require.NoError(t, f.pool.QueryRow(
		ctx,
		`SELECT available_at > statement_timestamp() AND lease_token IS NULL FROM integration_event_receipts WHERE id = $1`, secondID).Scan(&waiting))
	require.True(t, waiting)
	_, err = f.pool.Exec(
		ctx,
		`UPDATE integration_event_receipts SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, secondID)
	require.NoError(t, err)
	reclaimed := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, float64(2), reclaimed["lease_generation"])
	require.NotEqual(t, secondClaim["lease_token"], reclaimed["lease_token"])
	require.Equal(t, map[string]any{"message": "temporary"}, reclaimed["last_error"])
	f.post(t, secondPath, mustMarshalChannelRequest(t, retryBody), f.token, http.StatusConflict)
	failedBody := map[string]any{
		"lease_token": reclaimed["lease_token"], "lease_generation": reclaimed["lease_generation"],
		"state": "failed", "last_error": map[string]any{"message": "permanent"},
	}
	failed := f.post(t, secondPath, mustMarshalChannelRequest(t, failedBody), f.token, http.StatusOK)
	require.Equal(t, "failed", failed["state"])
	require.Empty(t, requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusNoContent, authHeaders(f.token)))
}

func TestChannelConnectorReceiptRetryAfter(t *testing.T) {
	f := newChannelReceiptHTTPFixture(t)
	ctx := t.Context()
	accepted := f.post(t, f.eventPath(t, f.app),
		f.event(t, f.install, "provider-throttled", `{"message":"hello"}`), f.token, http.StatusAccepted)
	receiptPublicID := channelReceiptString(t, accepted, "receipt_id")
	receiptID := mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, receiptPublicID)
	path := f.completePath(t, f.app, f.install, receiptPublicID)
	claim := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, receiptPublicID, claim["receipt_id"])
	finish := map[string]any{
		"lease_token": claim["lease_token"], "lease_generation": claim["lease_generation"],
		"state": "pending", "retry_after_ms": time.Hour.Milliseconds(),
		"last_error": map[string]any{"code": "rate_limited"},
	}
	readState := func(t *testing.T) string {
		t.Helper()
		var state string
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT jsonb_build_array(
state, attempt_count, available_at, lease_token, lease_generation, lease_expires_at,
last_error, completed_at, updated_at)::text FROM integration_event_receipts WHERE id = $1`, receiptID).Scan(&state))
		return state
	}
	before := readState(t)
	for _, test := range []struct {
		name, state  string
		retryAfterMs int64
	}{
		{"negative", "pending", -1},
		{"over one day", "pending", (24 * time.Hour).Milliseconds() + 1},
		{"completed with delay", "completed", time.Hour.Milliseconds()},
		{"failed with delay", "failed", time.Hour.Milliseconds()},
		{"completed with explicit zero", "completed", 0},
		{"failed with explicit zero", "failed", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := mapsClone(finish)
			invalid["state"], invalid["retry_after_ms"] = test.state, test.retryAfterMs
			if test.state == "completed" {
				delete(invalid, "last_error")
			}
			f.post(t, path, mustMarshalChannelRequest(t, invalid), f.token, http.StatusBadRequest)
			require.Equal(t, before, readState(t), "HTTP rejection must preserve the current lease and retry state")
		})
	}
	retried := f.post(t, path, mustMarshalChannelRequest(t, finish), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"receipt_id": receiptPublicID, "state": "pending"}, retried)
	var delayed bool
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT available_at = updated_at + interval '1 hour'
AND lease_token IS NULL AND lease_expires_at IS NULL AND completed_at IS NULL AND attempt_count = 1
FROM integration_event_receipts WHERE id = $1`, receiptID).Scan(&delayed))
	require.True(t, delayed, "HTTP milliseconds must persist a one-hour delay from the DB timestamp")
	require.Empty(t, requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusNoContent, authHeaders(f.token)))
	makeReady := func() {
		t.Helper()
		_, err := f.pool.Exec(ctx, `UPDATE integration_event_receipts
SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, receiptID)
		require.NoError(t, err)
	}
	makeReady()
	reclaimed := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, receiptPublicID, reclaimed["receipt_id"])
	require.Equal(t, float64(2), reclaimed["attempt_count"])
	require.Equal(t, float64(2), reclaimed["lease_generation"])
	require.NotEqual(t, claim["lease_token"], reclaimed["lease_token"])
	require.Equal(t, claim["payload"], reclaimed["payload"])
	before = readState(t)
	f.post(t, path, mustMarshalChannelRequest(t, finish), f.token, http.StatusConflict)
	require.Equal(t, before, readState(t), "stale proof must not reschedule the replacement consumer's receipt")

	finish["lease_token"], finish["lease_generation"] = reclaimed["lease_token"], reclaimed["lease_generation"]
	finish["retry_after_ms"] = int64(0)
	f.post(t, path, mustMarshalChannelRequest(t, finish), f.token, http.StatusOK)
	var coreBackoff bool
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT available_at - updated_at BETWEEN interval '3.2 seconds'
AND interval '4.8 seconds' FROM integration_event_receipts WHERE id = $1`, receiptID).Scan(&coreBackoff))
	require.True(t, coreBackoff, "explicit zero must preserve the second attempt's existing jittered backoff")
	require.Empty(t, requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusNoContent, authHeaders(f.token)))
	makeReady()
	final := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, receiptPublicID, final["receipt_id"])
	require.Equal(t, float64(3), final["attempt_count"])
	completedBody := map[string]any{
		"lease_token": final["lease_token"], "lease_generation": final["lease_generation"], "state": "completed",
	}
	completed := f.post(t, path, mustMarshalChannelRequest(t, completedBody), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"receipt_id": receiptPublicID, "state": "completed"}, completed)
	require.Empty(t, requestRawWithHeaders(t, f.handler, http.MethodPost,
		channelReceiptClaimPath, channelReceiptClaimBody, http.StatusNoContent, authHeaders(f.token)))
}

func TestChannelConnectorReceiptRuntimeProof(t *testing.T) {
	t.Parallel()
	f := newChannelReceiptHTTPFixture(t)
	ctx := t.Context()
	unit, err := f.project.Store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: f.project.OrgUUID, IntegrationAppID: f.app.ID,
			ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID,
			UnitKey: "receipt-runtime", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning, SpecRevision: 1,
		},
	)
	require.NoError(t, err)
	leases, err := f.project.Store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "receipt-runtime", LeaseDuration: time.Minute, Capability: connectorTestCapability("discord"), Limit: 1,
		},
	)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	lease := leases[0]
	runtimePath := "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, f.app.ID) +
		"/runtime-units/" + testPublicID(t, publicid.KindIntegrationRuntimeUnit, unit.ID) + "/events"
	body := map[string]any{"lease_token": lease.LeaseToken.String(), "lease_generation": lease.LeaseGeneration,
		"event": json.RawMessage(f.event(t, f.install, "runtime-event", `{}`))}
	stale := mapsClone(body)
	stale["lease_generation"] = lease.LeaseGeneration + 1
	f.post(t, runtimePath, mustMarshalChannelRequest(t, stale), f.token, http.StatusConflict)
	stale["lease_generation"] = lease.LeaseGeneration
	stale["lease_token"] = uuid.New().String()
	f.post(t, runtimePath, mustMarshalChannelRequest(t, stale), f.token, http.StatusConflict)
	stale["lease_token"] = uuid.Nil.String()
	f.post(t, runtimePath, mustMarshalChannelRequest(t, stale), f.token, http.StatusBadRequest)
	wrongInstall := mapsClone(body)
	wrongInstall["event"] = json.RawMessage(f.event(t, f.otherInstall, "cross-install-runtime", `{}`))
	f.post(t, runtimePath, mustMarshalChannelRequest(t, wrongInstall), f.token, http.StatusConflict)
	wrongAppPath := strings.Replace(runtimePath,
		testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
		testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
	f.post(t, wrongAppPath, mustMarshalChannelRequest(t, body), f.token, http.StatusNotFound)
	f.post(t, runtimePath, mustMarshalChannelRequest(t, body), f.otherToken, http.StatusNotFound)
	var count int
	require.NoError(t, f.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM integration_event_receipts`).Scan(&count))
	require.Zero(t, count)
	receipt := f.post(t, runtimePath, mustMarshalChannelRequest(t, body), f.token, http.StatusAccepted)
	require.Equal(t, "pending", receipt["state"])
	_, err = f.pool.Exec(
		ctx,
		`UPDATE integration_runtime_units SET leased_at = statement_timestamp() - interval '2 minutes', renewed_at = statement_timestamp() - interval '2 minutes', lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, unit.ID)
	require.NoError(t, err)
	f.post(t, runtimePath, mustMarshalChannelRequest(t, body), f.token, http.StatusConflict)
	// The ordinary webhook path retains its own receipt contract and accepts no outer lease proof.
	webhook := f.event(t, f.install, "webhook-event", `{}`)
	forged := strings.TrimSuffix(webhook, "}") + `,"lease_token":"` + lease.LeaseToken.String() + `"}`
	f.post(t, f.eventPath(t, f.app), forged, f.token, http.StatusBadRequest)
	f.post(t, f.eventPath(t, f.app), webhook, f.token, http.StatusAccepted)
}

func channelReceiptString(t *testing.T, body map[string]any, key string) string {
	t.Helper()
	value, ok := body[key].(string)
	require.True(t, ok, "%s must be a string in %v", key, body)
	require.NotEmpty(t, value)
	return value
}
