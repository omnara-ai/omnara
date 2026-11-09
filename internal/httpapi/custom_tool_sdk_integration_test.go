//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/stretchr/testify/require"
)

func TestCustomToolSDKWebhookAndRecoveryJourney(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)))
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "sdk-tool-journey")
	agents := make([]executionstore.AgentRecord, 0, 2)
	callIDs := make([]uuid.UUID, 0, 2)
	for _, name := range []string{"webhook", "recovery"} {
		agent := createHTTPRuntimeAgent(t, ctx, store,
			project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, name).Agent
		agents = append(agents, agent)
		callIDs = append(callIDs, createHTTPCustomToolCallForAgent(t, ctx, store,
			project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, agent))
	}

	update, err := publicevents.ToolCallUpdate(notifications.ToolCallUpdatedCommitted{
		AgentID: agents[0].ID, ToolCallID: callIDs[0], State: "ready", Name: "lookup_customer",
		Input: json.RawMessage(`{"email":"ada@example.com"}`),
	}, publicevents.AgentContext{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, AgentID: agents[0].ID,
		AgentProfileID: agents[0].AgentProfileID, RootAgentID: agents[0].RootAgentID,
	})
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"event": "tool_call_update", "data": update})
	require.NoError(t, err)
	key := bytes.Repeat([]byte{7}, 32)
	id, timestamp := uuid.NewString(), strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, key)
	_, err = mac.Write([]byte(id + "." + timestamp + "." + string(body)))
	require.NoError(t, err)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	config, err := json.Marshal(map[string]any{
		"baseUrl": server.URL + "/api/v1", "token": project.AdminToken,
		"orgID": project.OrgID, "projectID": project.ProjectID,
		"recoveryCallID": testPublicID(t, publicid.KindToolCall, callIDs[1]),
		"webhook": map[string]any{
			"body": string(body), "signingSecret": base64.StdEncoding.EncodeToString(key),
			"headers": map[string]string{
				"Webhook-Id": id, "Webhook-Timestamp": timestamp,
				"Webhook-Signature": "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
			},
		},
	})
	require.NoError(t, err)
	command := exec.CommandContext(ctx, "node", "testdata/custom_tool_sdk_journey.mjs")
	command.Stdin = bytes.NewReader(config)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "SDK journey (build with make build-sdk): %s", output)

	for i, callID := range callIDs {
		call, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agents[i].ID, callID)
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateCompleted, call.State)
		require.Equal(t, executionstore.ToolResultOutcomeSucceeded, call.Outcome)
		require.JSONEq(t, `[{"type":"structured_data","value":{"email":"ada@example.com","customer":"Ada"}}]`,
			string(call.ResultContentParts))
		var results, events int
		require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*), count(event.id)
FROM tool_call_results result
LEFT JOIN agent_events event ON event.tool_call_result_id = result.id AND event.agent_id = result.agent_id
WHERE result.tool_call_id = $1`, callID).Scan(&results, &events))
		require.Equal(t, 1, results, "one result per call, including duplicate notification")
		require.Equal(t, 1, events, "one result event per call")
	}
}
