//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/stretchr/testify/require"
)

// make test-channel-journey builds the runner and supplies its absolute path.
// OMNARA_TEST_SLACK_GATEWAY_RUNNER enables this test. It uses the real
// generated TS client and Slack behavior against real Go HTTP and PostgreSQL.
func TestSlackGatewaySavedReceiptToAgentJourney(t *testing.T) {
	runner := os.Getenv("OMNARA_TEST_SLACK_GATEWAY_RUNNER")
	if runner == "" {
		t.Skip("OMNARA_TEST_SLACK_GATEWAY_RUNNER is required for the cross-service gateway journey")
	}
	node := os.Getenv("OMNARA_TEST_NODE")
	if node == "" {
		node = "node"
	}
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	core := httptest.NewUnstartedServer(nil)
	origin := "http://" + core.Listener.Addr().String()
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "journey", Token: token,
		Capabilities: []channelconnector.Capability{{
			ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "slack",
		}},
	}})
	require.NoError(t, err)
	f := newSlackEventsFixtureWithOptions(t, ctx, pool, slackServer, "slack-real-gateway", slackServer.Client(),
		WithChannelConnectorAuthenticator(auth), WithInternalAPIOrigins([]string{origin}))
	core.Config.Handler = f.Handler
	core.Start()
	defer core.Close()
	drain := func(want int) {
		t.Helper()
		input, err := json.Marshal(map[string]string{
			"coreUrl": core.URL + "/api/v1", "slackUrl": slackServer.URL, "token": token,
		})
		require.NoError(t, err)
		childCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		command := exec.CommandContext(childCtx, node, runner)
		command.Stdin = bytes.NewReader(input)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "gateway journey: %s", output)
		var result struct {
			Processed int `json:"processed"`
		}
		require.NoError(t, json.Unmarshal(output, &result))
		require.Equal(t, want, result.Processed)
	}
	body := slackReceiptBody("A123", "T123", "U_BOT", "gateway-mention")
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
		http.StatusOK, unitSlackSignedHeaders(body, "signing-secret"))
	drain(1)
	var agentID, targetID, bindingID storage.ID
	var state string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT input.agent_id,input.integration_target_id,input.integration_target_binding_id,receipt.state
FROM integration_event_receipts receipt
JOIN integration_event_outcomes outcome ON outcome.receipt_id=receipt.id AND outcome.project_id=receipt.project_id
JOIN agent_inputs input ON input.project_id=outcome.project_id AND input.agent_id=outcome.agent_id `+
			`AND input.id=outcome.agent_input_id
WHERE receipt.integration_install_id=$1 AND receipt.event_id='gateway-mention'`,
		f.Install.ID).Scan(&agentID, &targetID, &bindingID, &state))
	require.Equal(t, "completed", state)
	require.NotEqual(t, storage.NilID, agentID)
	require.NotEqual(t, storage.NilID, targetID)
	require.NotEqual(t, storage.NilID, bindingID)
	// A distinct Slack callback for the same provider message reuses its input.
	var callback map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &callback))
	callback["event_id"] = "gateway-mention-copy"
	copyBody := workflowHTTPJSON(t, callback)
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, copyBody, "",
		http.StatusOK, unitSlackSignedHeaders(copyBody, "signing-secret"))
	drain(1)
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND integration_target_id=$2`,
		agentID, targetID).Scan(&count))
	require.Equal(t, 1, count)
	callback["event_id"] = "gateway-thread-reply"
	callback["event"] = map[string]any{
		"type": "message", "user": "U123", "text": "continue", "channel": "C123", "thread_ts": "111.222",
		"ts": "111.333", "event_ts": "111.333", "channel_type": "channel", "team": "T123",
	}
	reply := workflowHTTPJSON(t, callback)
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, reply, "",
		http.StatusOK, unitSlackSignedHeaders(reply, "signing-secret"))
	drain(1)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND integration_target_id=$2`,
		agentID, targetID).Scan(&count))
	require.Equal(t, 2, count, "unmentioned replies continue the same agent")
	drain(0)
}
