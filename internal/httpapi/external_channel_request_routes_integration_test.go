//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestPublicExternalChannelRequestJourney(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "external-requests")
	channel := createExternalRequestHTTPChannel(t, handler, project, "primary")
	otherChannel := createExternalRequestHTTPChannel(t, handler, project, "other-connection")
	waiting := createExternalRequestHTTPWaitingTools(t, project,
		[]integrationstore.IntegrationTargetRecord{channel, channel, otherChannel})
	foreign := project
	otherProject, err := project.Store.Identity().CreateProjectForPrincipal(ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: project.OrgUUID, Creator: identitystore.NewUserPrincipal(project.AdminUserUUID),
			Name: "Other external customer", IdempotencyKey: "external-other-project",
		})
	require.NoError(t, err)
	foreign.ProjectUUID = otherProject.ID
	foreign.ProjectID = testPublicID(t, publicid.KindProject, otherProject.ID)
	foreign.ProjectPath = "/api/v1/orgs/" + project.OrgID + "/projects/" + foreign.ProjectID
	foreignChannel := createExternalRequestHTTPChannel(t, handler, foreign, "foreign")
	foreignWaiting := createExternalRequestHTTPWaitingTools(t, foreign,
		[]integrationstore.IntegrationTargetRecord{foreignChannel})

	path := externalRequestHTTPPath(t, project, channel.IntegrationInstallID)
	otherPath := externalRequestHTTPPath(t, project, otherChannel.IntegrationInstallID)
	foreignPath := externalRequestHTTPPath(t, foreign, foreignChannel.IntegrationInstallID)
	requestID := testPublicID(t, publicid.KindExternalChannelRequest, waiting[0].request.ID)
	resultPath := path + "/" + requestID + "/result"
	body := `{"outcome":"completed","payload":{"publication":"published","message_channel":"reply_channel",` +
		`"message_id":"published-1","reply_channel":{"implementation_key":"conversation",` +
		`"provider_ref":"thread-1","provider_ref_kind":"thread"}}}`
	requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusUnauthorized, nil)

	token, role := createChannelHTTPKey(t, project, "viewer")
	first := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=1", "", "",
		http.StatusOK, authHeaders(token))
	items := testutil.RequireType[[]any](t, first["data"])
	require.Len(t, items, 1)
	item := testutil.RequireType[map[string]any](t, items[0])
	require.Equal(t, requestID, item["id"])
	require.Equal(t, "pending", item["state"])
	require.Equal(t, "send", item["operation"])
	require.Equal(t, testPublicID(t, publicid.KindToolCall, waiting[0].request.ToolCallID), item["tool_call_id"])
	require.Equal(t, testPublicID(t, publicid.KindIntegrationTarget, channel.ID), item["channel_id"])
	payload := testutil.RequireType[map[string]any](t, item["payload"])
	require.Equal(t, map[string]any{"receive": true, "read": false, "send": true}, payload["reply_channel_grants"])
	deadline, err := time.Parse(time.RFC3339Nano, channelReceiptString(t, item, "deadline_at"))
	require.NoError(t, err)
	require.True(t, waiting[0].request.Deadline.Equal(deadline))
	cursor := channelReceiptString(t, first, "next_cursor")
	second := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=1&cursor="+url.QueryEscape(cursor),
		"", "", http.StatusOK, authHeaders(token))
	requireExternalRequestHTTPIDs(t, second, waiting[1].request.ID)
	require.Nil(t, second["next_cursor"])
	repeated := requestJSONWithHeaders(t, handler, http.MethodGet, path+"?limit=1", "", "",
		http.StatusOK, authHeaders(token))
	require.Equal(t, first, repeated, "polling neither claims work nor changes its accepted facts")
	requestJSONWithHeaders(t, handler, http.MethodPost, resultPath, body, "",
		http.StatusForbidden, authHeaders(token))
	role.Role = "operator"
	_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
	require.NoError(t, err)

	other := requestJSONWithHeaders(t, handler, http.MethodGet, otherPath, "", "",
		http.StatusOK, authHeaders(token))
	requireExternalRequestHTTPIDs(t, other, waiting[2].request.ID)
	foreignPage := requestJSONWithHeaders(t, handler, http.MethodGet, foreignPath, "", "",
		http.StatusOK, authHeaders(project.AdminToken))
	requireExternalRequestHTTPIDs(t, foreignPage, foreignWaiting[0].request.ID)
	for _, scopedPath := range []string{otherPath, foreignPath} {
		requestJSONWithHeaders(t, handler, http.MethodGet, scopedPath+"?cursor="+url.QueryEscape(cursor), "", "",
			http.StatusBadRequest, authHeaders(project.AdminToken))
		requestJSONWithHeaders(t, handler, http.MethodPost, scopedPath+"/"+requestID+"/result", body, "",
			http.StatusNotFound, authHeaders(project.AdminToken))
	}
	requestJSONWithHeaders(t, handler, http.MethodGet,
		externalRequestHTTPPath(t, foreign, channel.IntegrationInstallID), "", "",
		http.StatusNotFound, authHeaders(project.AdminToken))
	foreignRequestID := testPublicID(t, publicid.KindExternalChannelRequest, foreignWaiting[0].request.ID)
	requestJSONWithHeaders(t, handler, http.MethodPost, path+"/"+foreignRequestID+"/result", body, "",
		http.StatusNotFound, authHeaders(token))
	requestJSONWithHeaders(t, handler, http.MethodGet, foreignPath, "", "",
		http.StatusNotFound, authHeaders(token))
	agentID := testPublicID(t, publicid.KindAgent, waiting[0].request.AgentID)
	toolID := testPublicID(t, publicid.KindToolCall, waiting[0].request.ToolCallID)
	requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/tool-calls/"+toolID+"/result",
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"wrong completion authority"}]}`, "",
		http.StatusConflict, authHeaders(token))

	// A new server/store handles completion; no process-local request state is needed.
	fresh := newIntegrationServer(pool)
	completed := requestJSONWithHeaders(t, fresh, http.MethodPost, resultPath, body, "",
		http.StatusOK, authHeaders(token))
	completedRequest := testutil.RequireType[map[string]any](t, completed["request"])
	require.Equal(t, requestID, completedRequest["id"])
	require.Equal(t, "completed", completedRequest["state"])
	require.NotEmpty(t, completedRequest["terminal_at"])
	call := testutil.RequireType[map[string]any](t, completed["tool_call"])
	require.Equal(t, toolID, call["id"])
	require.Equal(t, "built_in", call["type"])
	require.Equal(t, "completed", call["state"])
	require.Equal(t, "succeeded", call["outcome"])
	blocks := testutil.RequireType[[]any](t, completed["tool_result_content_blocks"])
	require.Len(t, blocks, 1)
	block := testutil.RequireType[map[string]any](t, blocks[0])
	require.Equal(t, "structured_data", block["type"])
	result := testutil.RequireType[map[string]any](t, block["value"])
	require.Len(t, result, 2, "success uses the canonical request_id/message shape")
	require.Equal(t, requestID, result["request_id"])
	message := testutil.RequireType[map[string]any](t, result["message"])
	require.Equal(t, "published", message["publication"])
	require.Equal(t, "published-1", message["message_id"])
	require.Equal(t, map[string]any{"text": "message 0"}, message["content"])
	childID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, message, "reply_channel_id"))
	require.Equal(t, message["reply_channel_id"], message["channel_id"])
	child, err := project.Store.Integrations().GetAgentChannelAccess(ctx,
		project.ProjectUUID, waiting[0].request.AgentID, childID)
	require.NoError(t, err)
	require.Equal(t, channel.ID, child.ParentChannelID)
	require.Equal(t, channel.IntegrationInstallID, child.IntegrationInstallID)
	require.True(t, child.ReceiveAllowed)
	require.True(t, child.Capabilities.Send)
	require.False(t, child.Capabilities.Read)
	require.False(t, child.Capabilities.CreatesReplyChannel)
	remaining := requestJSONWithHeaders(t, fresh, http.MethodGet, path, "", "",
		http.StatusOK, authHeaders(token))
	requireExternalRequestHTTPIDs(t, remaining, waiting[1].request.ID)

	var childBindingID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM integration_target_bindings
		WHERE project_id = $1 AND agent_id = $2 AND integration_target_id = $3 AND revoked_at IS NULL`,
		project.ProjectUUID, waiting[0].request.AgentID, childID).Scan(&childBindingID))
	require.NoError(t, project.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx, project.ProjectUUID, childBindingID))
	require.NoError(t, project.Store.Integrations().RevokeIntegrationTargetBinding(
		ctx, project.ProjectUUID, waiting[0].bindingID))
	replayed := requestJSONWithHeaders(t, fresh, http.MethodPost, resultPath, body, "",
		http.StatusOK, authHeaders(token))
	require.Equal(t, completed, replayed, "terminal replay exposes the saved canonical result after revocation")
	requestJSONWithHeaders(t, fresh, http.MethodPost, resultPath,
		`{"outcome":"completed","payload":{"publication":"draft","message_channel":"destination"}}`, "",
		http.StatusConflict, authHeaders(token))
	role.Role = "viewer"
	_, err = project.Store.Identity().SetOrgAPIKeyProjectRole(ctx, role)
	require.NoError(t, err)
	requestJSONWithHeaders(t, fresh, http.MethodPost, resultPath, body, "",
		http.StatusForbidden, authHeaders(token))
	var resultCount, liveChildBindings int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tool_call_results WHERE tool_call_id = $1`,
		waiting[0].request.ToolCallID).Scan(&resultCount))
	require.Equal(t, 1, resultCount)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings
		WHERE integration_target_id = $1 AND revoked_at IS NULL`, childID).Scan(&liveChildBindings))
	require.Zero(t, liveChildBindings)
	storedCall, err := project.Store.Execution().GetToolCall(ctx,
		project.ProjectUUID, waiting[0].request.AgentID, waiting[0].request.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, waiting[0].originalInput, storedCall.Input, "the accepted original tool arguments remain immutable")
	foreignRecord, err := project.Store.Execution().GetExternalChannelRequest(ctx,
		foreign.ProjectUUID, foreignChannel.IntegrationInstallID, foreignWaiting[0].request.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.ExternalChannelRequestPending, foreignRecord.State)
}

func createExternalRequestHTTPChannel(
	t *testing.T, handler http.Handler, project publicHTTPProject, name string,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	created := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/integration-installs",
		workflowHTTPJSON(t, map[string]any{"display_name": name}), "", http.StatusCreated, authHeaders(project.AdminToken))
	require.Equal(t, "external", created["integration_kind"])
	installID := mustPublicHTTPID(t, publicid.KindIntegrationInstall, channelReceiptString(t, created, "id"))
	path := project.ProjectPath + "/integration-installs/" + channelReceiptString(t, created, "id")
	definition := requestJSONWithHeaders(t, handler, http.MethodPost, path+"/channel-definitions",
		externalHTTPDefinitionBody(), "", http.StatusOK, authHeaders(project.AdminToken))
	registered := requestJSONWithHeaders(t, handler, http.MethodPost, path+"/channels",
		workflowHTTPJSON(t, map[string]any{
			"source": "external", "definition_id": definition["id"], "provider_ref": name, "provider_ref_kind": "conversation", "name": name,
		}), "", http.StatusOK, authHeaders(project.AdminToken))
	target, err := project.Store.Integrations().GetIntegrationTarget(t.Context(), project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelReceiptString(t, registered, "channel_id")))
	require.NoError(t, err)
	require.Equal(t, installID, target.IntegrationInstallID)
	return target
}

func externalHTTPDefinitionBody() string {
	return `{"implementation_key":"conversation","kind":"EXTERNAL","description":"Customer conversation",` +
		`"send_params_schema":{"type":"object"},"capabilities":{"read":true,"send":true,"text":true,` +
		`"artifacts":false,"permissions":false,"questions":false,"creates_reply_channel":true}}`
}

type externalRequestHTTPWaitingTool struct {
	request       executionstore.ExternalChannelRequestRecord
	bindingID     uuid.UUID
	originalInput json.RawMessage
}

func createExternalRequestHTTPWaitingTools(
	t *testing.T, project publicHTTPProject, channels []integrationstore.IntegrationTargetRecord,
) []externalRequestHTTPWaitingTool {
	t.Helper()
	ctx := t.Context()
	store := project.Store
	launch := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
		project.AdminUserUUID, "external-request-agent")
	agentID := launch.Agent.ID
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: project.ProjectUUID, AgentID: agentID,
		Actor:         httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"send channel messages"}]`), IdempotencyKey: "external-input",
	})
	require.NoError(t, err)
	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, work.Kind)
	require.Equal(t, agentID, work.RuntimeLock.AgentID)
	claim := claimNormalModelCallForHTTPTest(t, ctx, store, project.ProjectUUID, agentID, work.RuntimeLock,
		[]uuid.UUID{input.ID}, launch.AgentConfig.ID, work.Model.AdmittedInputTurn.Events[0].Sequence)
	proposals := make([]model.ToolCall, len(channels))
	bindings := make([]executionstore.ToolCallBindingInput, len(channels))
	for i, channel := range channels {
		proposals[i] = model.ToolCall{ID: fmt.Sprintf("send-%d", i), Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"message":{"text":"message %d"},`+
				`"params":{"large":9007199254740993,"omnara_channel":"business"}}`,
				testPublicID(t, publicid.KindIntegrationTarget, channel.ID), i))}
		bindings[i] = executionstore.ToolCallBindingInput{ProviderCallID: proposals[i].ID, Type: toolcatalog.ToolTypeBuiltIn}
	}
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "external-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls(proposals)})
	require.NoError(t, err)
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response, ToolCallBindings: bindings,
		})
	require.NoError(t, err)
	require.Len(t, calls, len(channels))
	grants := make(map[uuid.UUID]uuid.UUID)
	result := make([]externalRequestHTTPWaitingTool, len(calls))
	for i, call := range calls {
		channel := channels[i]
		if _, found := grants[channel.ID]; !found {
			binding, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
				integrationstore.CreateIntegrationTargetBindingInput{
					ProjectID: project.ProjectUUID, AgentID: agentID, IntegrationInstallID: channel.IntegrationInstallID,
					IntegrationTargetID: channel.ID, Source: "external-http", SendAllowed: true, ReceiveAllowed: true,
					ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
				})
			require.NoError(t, err)
			grants[channel.ID] = binding.ID
		}
		ready, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, ID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
		})
		require.NoError(t, err)
		payload, err := json.Marshal(channelconnector.SendPayload{
			Destination: channelconnector.OperationDestination{
				ImplementationKey: "conversation", ProviderRef: channel.ProviderRef,
				ProviderRefKind: channel.ProviderRefKind, ProviderMetadata: channel.ProviderMetadata,
			},
			Message: channelconnector.Message{Text: fmt.Sprintf("message %d", i)},
			Params:  json.RawMessage(`{"large":9007199254740993,"omnara_channel":"business"}`),
		})
		require.NoError(t, err)
		request, err := storagetest.ExecuteToolCallCommand[executionstore.ExternalChannelRequestRecord](
			ctx, store.Execution(), executionstore.ExecuteToolCallInput{
				ProjectID: project.ProjectUUID, AgentID: agentID, ToolCallID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
			}, executionstore.CreateExternalChannelRequestForToolCall(executionstore.CreateExternalChannelRequestInput{
				TurnID: call.TurnID, ChannelID: channel.ID, Operation: channelconnector.OperationSend,
				Payload: payload, CreatesReplyChannel: true, Timeout: executionstore.ExternalChannelRequestTimeout,
			}))
		require.NoError(t, err)
		waiting, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agentID, call.ID)
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStateWaiting, waiting.State)
		result[i] = externalRequestHTTPWaitingTool{
			request: request, bindingID: grants[channel.ID], originalInput: ready.Input,
		}
	}
	return result
}

func externalRequestHTTPPath(t *testing.T, project publicHTTPProject, installID uuid.UUID) string {
	t.Helper()
	return project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, installID) + "/requests"
}

func requireExternalRequestHTTPIDs(t *testing.T, response map[string]any, ids ...uuid.UUID) {
	t.Helper()
	items := testutil.RequireType[[]any](t, response["data"])
	require.Len(t, items, len(ids))
	for i, id := range ids {
		item := testutil.RequireType[map[string]any](t, items[i])
		require.Equal(t, testPublicID(t, publicid.KindExternalChannelRequest, id), item["id"])
	}
}
