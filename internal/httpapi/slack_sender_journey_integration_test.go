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
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSlackGatewayAgentStartedThreadJourney(t *testing.T) {
	if os.Getenv("OMNARA_TEST_SLACK_GATEWAY_RUNNER") == "" {
		t.Skip("OMNARA_TEST_SLACK_GATEWAY_RUNNER is required for the cross-service gateway journey")
	}
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	fallback := newSlackEventsTestServer(t)
	defer fallback.Close()
	var sends, historyReads atomic.Int32
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			sends.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": "C123", "ts": "555.000001"})
		case "/conversations.history", "/conversations.replies":
			historyReads.Add(1)
			fallback.Config.Handler.ServeHTTP(w, r)
		default:
			fallback.Config.Handler.ServeHTTP(w, r)
		}
	}))
	defer slackServer.Close()
	core := httptest.NewUnstartedServer(nil)
	origin := "http://" + core.Listener.Addr().String()
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	capability := channelconnector.Capability{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "slack"}
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "sender-journey", Token: token, Capabilities: []channelconnector.Capability{capability},
	}})
	require.NoError(t, err)
	f := newSlackEventsFixtureWithOptions(t, ctx, pool, slackServer, "slack-sender-journey", slackServer.Client(),
		WithChannelConnectorAuthenticator(auth), WithInternalAPIOrigins([]string{origin}))
	core.Config.Handler = f.Handler
	core.Start()
	defer core.Close()
	// Replies are authorized by the sender's child grant even with no behavior
	// configured to launch agents for new Slack mentions.
	routes, err := f.Project.Store.Integrations().ListActiveIntegrationRoutes(ctx, f.Project.ProjectUUID, f.Install.ID)
	require.NoError(t, err)
	for _, route := range routes {
		require.NoError(t, f.Project.Store.Integrations().DeleteIntegrationRoute(
			ctx, f.Project.ProjectUUID, f.Install.ID, route.ID))
	}
	parent := createSlackJourneyParent(t, f, capability)
	owner, prepared, payload := prepareSlackJourneySend(t, f.Project, parent)
	requestID := testPublicID(t, publicid.KindToolCall, owner.ToolCallID)
	scope := channelconnector.OperationScope{
		ProjectID:            f.Project.ProjectID,
		IntegrationAppID:     testPublicID(t, publicid.KindIntegrationApp, f.Install.IntegrationAppID),
		IntegrationInstallID: testPublicID(t, publicid.KindIntegrationInstall, f.Install.ID),
		AgentID:              testPublicID(t, publicid.KindAgent, owner.AgentID),
		ChannelID:            testPublicID(t, publicid.KindIntegrationTarget, parent.ID),
	}
	configuration := map[string]any{"coreUrl": core.URL + "/api/v1", "slackUrl": slackServer.URL, "token": token}
	configuration["send"] = map[string]any{"request_id": requestID, "scope": scope, "payload": payload}
	result := runSlackSenderJourney(t, configuration)
	require.Equal(t, 0, result.Processed)
	require.NotNil(t, result.SendResult)
	require.Equal(t, channelconnector.OperationCompleted, result.SendResult.Outcome)
	require.Equal(t, int32(1), sends.Load())
	record, err := f.Project.Store.Execution().CompleteChannelOperation(ctx, executionstore.CompleteChannelOperationInput{
		Prepared: prepared, Payload: payload, Result: *result.SendResult,
	})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	child, err := f.Project.Store.Integrations().GetIntegrationTargetByProviderRef(ctx,
		f.Project.ProjectUUID, f.Install.ID, "C123:555.000001")
	require.NoError(t, err)
	require.Equal(t, parent.ID, child.ParentChannelID)
	access, err := f.Project.Store.Integrations().GetAgentChannelAccess(
		ctx, f.Project.ProjectUUID, owner.AgentID, child.ID)
	require.NoError(t, err)
	require.True(t, access.ReceiveAllowed)
	require.False(t, access.Capabilities.Read)
	delete(configuration, "send")
	callback := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(slackReceiptBody("A123", "T123", "U_BOT", "sender-reply")), &callback))
	callback["event"] = map[string]any{
		"type": "message", "user": "U123", "text": "tell me more", "channel": "C123", "channel_type": "channel",
		"thread_ts": "555.000001", "ts": "555.000002", "event_ts": "555.000002", "team": "T123",
	}
	for _, eventID := range []string{"sender-reply", "sender-reply-copy"} {
		callback["event_id"] = eventID
		body := workflowHTTPJSON(t, callback)
		requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
			http.StatusOK, unitSlackSignedHeaders(body, "signing-secret"))
		require.Equal(t, 1, runSlackSenderJourney(t, configuration).Processed)
	}
	var inputs, workflows int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND integration_target_id=$3`,
		f.Project.ProjectUUID, owner.AgentID, child.ID).Scan(&inputs))
	require.Equal(t, 1, inputs, "one semantic reply returns to the agent that started the thread")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_workflows WHERE integration_install_id=$1`,
		f.Install.ID).Scan(&workflows))
	require.Zero(t, workflows)
	require.Zero(t, historyReads.Load(), "an existing recipient does not need first-launch thread history")
	binding, err := f.Project.Store.Integrations().GetActiveReceiveBindingForTarget(
		ctx, f.Project.ProjectUUID, owner.AgentID, child.ID)
	require.NoError(t, err)
	require.NoError(t, f.Project.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx, f.Project.ProjectUUID, binding.ID))
	callback["event_id"] = "sender-after-revoke"
	callback["event"] = map[string]any{
		"type": "app_mention", "user": "U123", "text": "<@U_BOT> again", "channel": "C123",
		"thread_ts": "555.000001", "ts": "555.000003", "team": "T123",
	}
	body := workflowHTTPJSON(t, callback)
	requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "",
		http.StatusOK, unitSlackSignedHeaders(body, "signing-secret"))
	require.Equal(t, 1, runSlackSenderJourney(t, configuration).Processed)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND agent_id=$2 AND integration_target_id=$3`,
		f.Project.ProjectUUID, owner.AgentID, child.ID).Scan(&inputs))
	require.Equal(t, 1, inputs)
	require.Equal(t, int32(1), sends.Load(), "receipt replay never resends the original message")
}

func createSlackJourneyParent(
	t *testing.T, f slackEventsIntegrationFixture, capability channelconnector.Capability,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	store, ctx := f.Project.Store.Integrations(), t.Context()
	var parentDefinition integrationstore.ChannelDefinition
	for _, kind := range []string{"channel", "thread"} {
		channelKind := integrationstore.ChannelKindSlackChannel
		if kind == "thread" {
			channelKind = integrationstore.ChannelKindSlackThread
		}
		definition, err := store.PublishConnectorChannelDefinition(ctx, integrationstore.PublishChannelDefinitionInput{
			ProjectID: f.Project.ProjectUUID, IntegrationInstallID: f.Install.ID,
			ImplementationKey: "slack_" + kind, Kind: channelKind,
			SendParamsSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Send: true, Read: true, Text: true, Artifacts: true, Permissions: true, Questions: true,
				CreatesReplyChannel: kind == "channel",
			}, ConnectorCapabilities: []channelconnector.Capability{capability},
		})
		require.NoError(t, err)
		if kind == "channel" {
			parentDefinition = definition
		}
	}
	target, err := store.CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: f.Project.ProjectUUID, IntegrationInstallID: f.Install.ID, ChannelDefinitionID: parentDefinition.ID,
		ProviderRef: "C123", ProviderRefKind: "channel",
	})
	require.NoError(t, err)
	return target
}

type slackSenderJourneyResult struct {
	Processed  int                               `json:"processed"`
	SendResult *channelconnector.OperationResult `json:"send_result"`
}

func runSlackSenderJourney(t *testing.T, configuration map[string]any) slackSenderJourneyResult {
	t.Helper()
	node := os.Getenv("OMNARA_TEST_NODE")
	if node == "" {
		node = "node"
	}
	input, err := json.Marshal(configuration)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, os.Getenv("OMNARA_TEST_SLACK_GATEWAY_RUNNER"))
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "gateway journey: %s", output)
	var result slackSenderJourneyResult
	require.NoError(t, json.Unmarshal(output, &result))
	return result
}

func prepareSlackJourneySend(
	t *testing.T, project publicHTTPProject, parent integrationstore.IntegrationTargetRecord,
) (executionstore.PrepareChannelOperationInput, executionstore.PreparedChannelOperation, json.RawMessage) {
	t.Helper()
	ctx, store := t.Context(), project.Store
	launch := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "scheduled-sender")
	agentID := launch.Agent.ID
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: project.ProjectUUID, AgentID: agentID,
		Actor:         httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Post the scheduled update"}]`), IdempotencyKey: "scheduled-input",
	})
	require.NoError(t, err)
	_, err = store.Integrations().CreateIntegrationTargetBinding(ctx, integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: project.ProjectUUID, AgentID: agentID, IntegrationInstallID: parent.IntegrationInstallID,
		IntegrationTargetID: parent.ID, Source: "scheduled-output", SendAllowed: true,
		ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
	})
	require.NoError(t, err)
	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, agentID, work.RuntimeLock.AgentID)
	claim := claimNormalModelCallForHTTPTest(t, ctx, store, project.ProjectUUID, agentID, work.RuntimeLock,
		[]integrationstore.ID{input.ID}, launch.AgentConfig.ID, work.Model.AdmittedInputTurn.Events[0].Sequence)
	channelID := testPublicID(t, publicid.KindIntegrationTarget, parent.ID)
	proposal := model.ToolCall{ID: "scheduled-send", Name: toolcatalog.ToolNameSendChannelMessage,
		Input: json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"Scheduled update"}}`)}
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "scheduled-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{proposal})})
	require.NoError(t, err)
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: proposal.ID, Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	_, err = store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: project.ProjectUUID, AgentID: agentID, ID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
	})
	require.NoError(t, err)
	owner := executionstore.PrepareChannelOperationInput{
		ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, ToolCallID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
		}, TurnID: calls[0].TurnID, ChannelID: parent.ID, Operation: integrationstore.ChannelBindingOperationSend,
	}
	_, err = store.Execution().ExecuteToolCall(ctx, owner.ExecuteToolCallInput,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		})
	require.NoError(t, err)
	prepared, err := store.Execution().PrepareChannelOperation(ctx, owner)
	require.NoError(t, err)
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelconnector.OperationDestination{ImplementationKey: "slack_channel",
			ProviderRef: parent.ProviderRef, ProviderRefKind: parent.ProviderRefKind, ProviderMetadata: parent.ProviderMetadata},
		Message: channelconnector.Message{Text: "Scheduled update"}, Params: json.RawMessage(`{}`),
		ReplyChannelGrants: &channelconnector.ChannelGrants{Receive: true, Send: true},
	})
	require.NoError(t, err)
	return owner, prepared, payload
}
