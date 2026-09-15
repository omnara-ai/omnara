//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func slackReceiptBody(app, workspace, bot, eventID string) string {
	return fmt.Sprintf(`{"type":"event_callback","team_id":%q,"api_app_id":%q,"event_id":%q,
"authorizations":[{"team_id":%q,"user_id":%q,"is_bot":true}],
"event":{"type":"app_mention","user":"U123","text":"hello","channel":"C123","ts":"111.222","team":%q}}`,
		workspace, app, eventID, workspace, bot, workspace)
}

func TestSlackReceiptAcknowledgesOnlyDurableVerifiedInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	f := newSlackEventsIntegrationFixture(t, ctx, pool, slackServer, "durable-slack")
	body := slackReceiptBody("A123", "T123", "U_BOT", "durable-event")
	// A database failure must produce a retryable HTTP error, never a success
	// followed by an in-memory callback that can disappear on process shutdown.
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_test_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected local receipt write failure'; END $$;
CREATE TRIGGER reject_test_receipt BEFORE INSERT ON integration_event_receipts
FOR EACH ROW EXECUTE FUNCTION reject_test_receipt()`)
	require.NoError(t, err)
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
		http.StatusInternalServerError, unitSlackSignedHeaders(body, "signing-secret"))
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_receipts WHERE event_id='durable-event'`).Scan(&count))
	require.Zero(t, count)
	_, err = pool.Exec(ctx,
		`DROP TRIGGER reject_test_receipt ON integration_event_receipts; DROP FUNCTION reject_test_receipt()`)
	require.NoError(t, err)
	for range 2 {
		response := requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
			http.StatusOK, unitSlackSignedHeaders(body, "signing-secret"))
		require.Equal(t, "accepted", response["ok"])
	}
	var payload json.RawMessage
	var state, connector, provider string
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload,state,connector_key,provider FROM integration_event_receipts
WHERE integration_install_id=$1 AND event_id='durable-event'`,
		f.Install.ID).Scan(&payload, &state, &connector, &provider))
	require.JSONEq(t, body, string(payload))
	require.Equal(t, "pending", state)
	require.Equal(t, "chat_sdk", connector)
	require.Equal(t, "slack", provider)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_receipts WHERE event_id='durable-event'`).Scan(&count))
	require.Equal(t, 1, count, "provider retries reuse the durable receipt")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1`, f.Install.ID).Scan(&count))
	require.Zero(t, count, "intake does not run gateway conversation behavior")
}

func TestSlackReceiptRoutesByVerifiedPhysicalAppIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	f := newSlackEventsIntegrationFixture(t, ctx, pool, slackServer, "receipt-identity")
	routes, err := f.Project.Store.Integrations().ListActiveIntegrationRoutes(ctx, f.Project.ProjectUUID, f.Install.ID)
	require.NoError(t, err)
	require.Len(t, routes, 1)
	second := createSlackHTTPInstall(t, ctx, f.Project, routes[0].AgentProfileID,
		"A_SECOND", "T123", "U_SECOND", "second-secret")
	body := slackReceiptBody("A_SECOND", "T123", "U_SECOND", "second-app-event")
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
		http.StatusUnauthorized, unitSlackSignedHeaders(body, "signing-secret"))
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
		http.StatusOK, unitSlackSignedHeaders(body, "second-secret"))
	var installID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT integration_install_id FROM integration_event_receipts WHERE event_id='second-app-event'`).Scan(&installID))
	require.Equal(t, second.ID, installID)
	require.NotEqual(t, f.Install.ID, installID)
	badIdentity := slackReceiptBody("A_SECOND", "T123", "U_BOT", "wrong-bot")
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, badIdentity, "",
		http.StatusForbidden, unitSlackSignedHeaders(badIdentity, "second-secret"))
}
