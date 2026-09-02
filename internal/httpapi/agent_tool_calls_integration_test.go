//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestPublicCustomToolCallLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "custom-tools")
	launch := createHTTPRuntimeAgent(
		t,
		ctx,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		project.AdminUserUUID,
		"custom-tool-result",
	)
	agent := launch.Agent
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: project.ProjectUUID,
			AgentID:   agent.ID,
			Actor:     httpOmnaraActorParams(t, project.OrgUUID, project.AdminUserUUID),
			ContentBlocks: json.RawMessage(
				`[{"type":"text","text":"lookup customer"}]`,
			),
			IdempotencyKey: "custom-tools-input",
		},
	)
	if err != nil {
		t.Fatalf("create input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("input was not admitted")
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		project.ProjectUUID,
		agent.ID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		project.ProjectUUID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	contextRow := modelCall.Context
	toolCalls := []model.ToolCall{
		{
			ID:    "call_lookup_customer",
			Name:  "lookup_customer",
			Input: json.RawMessage(`{"email":"ada@example.com"}`),
		},
		{
			ID:    "call_lookup_customer_2",
			Name:  "lookup_customer",
			Input: json.RawMessage(`{"email":"grace@example.com"}`),
		},
		{
			ID:    "call_lookup_customer_3",
			Name:  "lookup_customer",
			Input: json.RawMessage(`{"email":"katherine@example.com"}`),
		},
		{
			ID:    "call_lookup_customer_malformed",
			Name:  "lookup_customer",
			Input: json.RawMessage(`{}`),
		},
		{
			ID:    "call_lookup_customer_empty",
			Name:  "lookup_customer",
			Input: json.RawMessage(`{"email":"empty@example.com"}`),
		},
		{
			ID:    "call_read_process",
			Name:  "read_process",
			Input: json.RawMessage(`{"process_id":"proc_test"}`),
		},
		{
			ID:    "call_mcp_greet",
			Name:  toolcatalog.MCPRuntimeToolName("docs", "greet"),
			Input: json.RawMessage(`{"name":"Ada"}`),
		},
	}
	providerIdentity := loadModelCallProviderIdentityForHTTPTest(
		t, ctx, store, project.ProjectUUID, modelCall.Context,
	)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		providerIdentity.Slug,
		providerIdentity.APIFormat,
		providerIdentity.APIVariant,
		model.Response{
			ID:         "resp_custom_tools",
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(toolCalls),
		},
	)
	if err != nil {
		t.Fatalf("build provider response: %v", err)
	}
	preMintedToolCallID := uuid.New()
	toolCallBindings := []executionstore.ToolCallBindingInput{{
		ID:             preMintedToolCallID,
		ProviderCallID: "call_lookup_customer",
		Type:           toolcatalog.ToolTypeCustom,
	}, {
		ProviderCallID: "call_lookup_customer_2",
		Type:           toolcatalog.ToolTypeCustom,
	}, {
		ProviderCallID: "call_lookup_customer_3",
		Type:           toolcatalog.ToolTypeCustom,
	}, {
		ProviderCallID: "call_lookup_customer_malformed",
		Type:           toolcatalog.ToolTypeCustom,
	}, {
		ProviderCallID: "call_lookup_customer_empty",
		Type:           toolcatalog.ToolTypeCustom,
	}, {
		ProviderCallID: "call_read_process",
		Type:           toolcatalog.ToolTypeBuiltIn,
	}, {
		ProviderCallID: "call_mcp_greet",
		Type:           toolcatalog.ToolTypeMCP,
	}}
	sourceEvent, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx, executionstore.RecordToolCallSourceAndCompleteContextInput{
		ProjectID:          project.ProjectUUID,
		AgentID:            agent.ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRow.ID,
		ProviderResponse:   providerResponse,
		ToolCallBindings:   toolCallBindings,
	})
	if err != nil {
		t.Fatalf("record tool call source: %v", err)
	}
	if len(calls) != 7 {
		t.Fatalf("tool calls = %d, want 7", len(calls))
	}
	if calls[0].ID != preMintedToolCallID {
		t.Fatalf("recorded tool call id = %s, want pre-minted id %s", calls[0].ID, preMintedToolCallID)
	}
	replayedSourceEvent, replayedCalls, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          project.ProjectUUID,
			AgentID:            agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRow.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   toolCallBindings,
		},
	)
	if err != nil {
		t.Fatalf("replay exact tool call source: %v", err)
	}
	if replayedSourceEvent.ID != sourceEvent.ID ||
		replayedSourceEvent.Sequence != sourceEvent.Sequence {
		t.Fatalf(
			"replayed source event = %s/%d, want %s/%d",
			replayedSourceEvent.ID,
			replayedSourceEvent.Sequence,
			sourceEvent.ID,
			sourceEvent.Sequence,
		)
	}
	if len(replayedCalls) != len(calls) {
		t.Fatalf("replayed tool calls = %d, want %d", len(replayedCalls), len(calls))
	}
	replayedCallsByProviderID := make(map[string]executionstore.ToolCallRecord, len(replayedCalls))
	for _, call := range replayedCalls {
		replayedCallsByProviderID[call.ProviderCallID] = call
	}
	for _, call := range calls {
		replayedCall, ok := replayedCallsByProviderID[call.ProviderCallID]
		if !ok || replayedCall.ID != call.ID {
			t.Fatalf(
				"replayed tool call %q = %+v, want id %s",
				call.ProviderCallID,
				replayedCall,
				call.ID,
			)
		}
	}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agent.ID)
	if err != nil {
		t.Fatalf("public agent id: %v", err)
	}
	initialEvents := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	publicToolCallIDs := make([]string, len(calls))
	for index := range calls {
		publicToolCallIDs[index], err = publicid.Encode(publicid.KindToolCall, calls[index].ID)
		if err != nil {
			t.Fatalf("public tool call id %d: %v", index, err)
		}
	}
	toolCallsPath := project.ProjectPath + "/agents/" + agentPublicID + "/tool-calls"
	seenPageIDs := make(map[string]struct{}, len(calls))
	cursor := ""
	for pageIndex, expectedCount := range []int{2, 2, 2, 1} {
		pagePath := toolCallsPath + "?limit=2"
		if cursor != "" {
			pagePath += "&cursor=" + url.QueryEscape(cursor)
		}
		page := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			pagePath,
			"",
			"",
			http.StatusOK,
			authHeaders(project.AdminToken),
		)
		items := page["data"].([]any)
		if len(items) != expectedCount {
			t.Fatalf("tool call page %d has %d items, want %d", pageIndex+1, len(items), expectedCount)
		}
		for _, rawItem := range items {
			id := rawItem.(map[string]any)["id"].(string)
			if _, exists := seenPageIDs[id]; exists {
				t.Fatalf("tool call %s appeared on multiple pages", id)
			}
			seenPageIDs[id] = struct{}{}
		}
		nextCursor, hasNext := page["next_cursor"].(string)
		if pageIndex < 3 {
			if !hasNext || nextCursor == "" {
				t.Fatalf("tool call page %d has no next cursor", pageIndex+1)
			}
			cursor = nextCursor
		} else if page["next_cursor"] != nil {
			t.Fatalf("final tool call page has next_cursor=%v, want null", page["next_cursor"])
		}
	}
	if len(seenPageIDs) != len(calls) {
		t.Fatalf("paginated tool calls = %d, want %d", len(seenPageIDs), len(calls))
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+publicToolCallIDs[0]+"/result",
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"Unauthorized result."}]}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	initialCalls := publicCustomToolCallsByID(t, initialEvents)
	if len(initialCalls) != 5 {
		t.Fatalf("public custom tool calls = %+v, want all five immutable calls", initialCalls)
	}
	for _, toolCallID := range publicToolCallIDs[:5] {
		if initialCalls[toolCallID] == nil {
			t.Fatalf("public custom tool calls = %+v, missing %s", initialCalls, toolCallID)
		}
	}
	for i := range calls[:3] {
		calls[i], err = store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     project.ProjectUUID,
				AgentID:       agent.ID,
				ID:            calls[i].ID,
				RuntimeLockID: lock.ID,
			},
		)
		if err != nil {
			t.Fatalf("allow custom tool call %d: %v", i, err)
		}
		if calls[i].State != executionstore.ToolCallStateReady {
			t.Fatalf("custom tool call %d = %+v, want ready", i, calls[i])
		}
	}
	calls[4], err = store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ID:            calls[4].ID,
			RuntimeLockID: lock.ID,
		},
	)
	if err != nil {
		t.Fatalf("allow empty custom tool call: %v", err)
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ID:            calls[5].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark built-in tool call ready: %v", err)
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ID:            calls[6].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark mcp tool call ready: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+publicToolCallIDs[5]+"/result",
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"External built-in result."}]}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+publicToolCallIDs[6]+"/result",
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"External MCP result."}]}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	mcpCalls := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		toolCallsPath+"?type=mcp",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	mcpByProviderID := publicToolCallsByProviderID(t, mcpCalls)
	if len(mcpByProviderID) != 1 ||
		mcpByProviderID["call_mcp_greet"]["type"] != toolcatalog.ToolTypeMCP {
		t.Fatalf("mcp tool calls = %+v, want one mcp call", mcpByProviderID)
	}
	calls[3], err = store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ID:            calls[3].ID,
			Outcome:       executionstore.ToolResultOutcomeFailed,
			RuntimeLockID: lock.ID,
			ResultContentParts: json.RawMessage(
				`[{"type":"structured_data","value":{"ok":false,"error":"invalid input"}}]`,
			),
		},
	)
	if err != nil {
		t.Fatalf("complete malformed custom tool call: %v", err)
	}
	if calls[3].State != executionstore.ToolCallStateCompleted ||
		calls[3].Outcome != executionstore.ToolResultOutcomeFailed {
		t.Fatalf("malformed custom tool call = %+v", calls[3])
	}
	eventsAfterBlockedSource, err := store.Execution().ListAgentEventsForRead(
		ctx,
		project.ProjectUUID,
		agent.ID,
		sourceEvent.Sequence,
		10,
	)
	if err != nil {
		t.Fatalf("list events after blocked custom source: %v", err)
	}
	if len(eventsAfterBlockedSource) != 1 ||
		eventsAfterBlockedSource[0].ToolCallID != calls[3].ID {
		t.Fatalf(
			"events after blocked custom source = %+v, want malformed tool result",
			eventsAfterBlockedSource,
		)
	}
	readyCalls := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		toolCallsPath+"?state=ready&type=custom",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	readyByProviderID := publicToolCallsByProviderID(t, readyCalls)
	if len(readyByProviderID) != 4 {
		t.Fatalf("ready custom tool calls = %+v, want four", readyByProviderID)
	}
	for _, providerCallID := range []string{
		"call_lookup_customer",
		"call_lookup_customer_2",
		"call_lookup_customer_3",
		"call_lookup_customer_empty",
	} {
		if readyByProviderID[providerCallID]["state"] != "ready" {
			t.Fatalf("tool call %s = %+v, want ready", providerCallID, readyByProviderID[providerCallID])
		}
	}
	if _, err := store.Execution().CompleteToolCall(
		ctx,
		executionstore.CompleteToolCallInput{
			ProjectID:          project.ProjectUUID,
			AgentID:            agent.ID,
			ID:                 calls[0].ID,
			Outcome:            executionstore.ToolResultOutcomeSucceeded,
			RuntimeLockID:      lock.ID,
			ResultContentParts: json.RawMessage(`[{"type":"text","text":"worker result"}]`),
		},
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("worker completion of ready custom execution error = %v, want state transition conflict", err)
	}

	toolCallID := publicToolCallIDs[0]
	secondToolCallID := publicToolCallIDs[1]
	mediaToolCallID := publicToolCallIDs[2]
	emptyToolCallID := publicToolCallIDs[4]
	pngBase64 := base64.StdEncoding.EncodeToString(testPNGBytes)
	resultPath := toolCallsPath + "/" + toolCallID + "/result"
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing body"},
		{
			name: "unknown error property",
			body: `{"outcome":"succeeded","error":"unexpected","content_blocks":[{"type":"text","text":"done"}]}`,
		},
		{
			name: "unsupported outcome",
			body: `{"outcome":"canceled","content_blocks":[{"type":"text","text":"canceled"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestJSONWithHeaders(
				t,
				handler,
				http.MethodPost,
				resultPath,
				test.body,
				"",
				http.StatusBadRequest,
				authHeaders(project.AdminToken),
			)
		})
	}
	for _, test := range []struct {
		name       string
		toolCallID string
		body       string
	}{
		{
			name:       "text NUL",
			toolCallID: toolCallID,
			body: `{"outcome":"succeeded","content_blocks":[` +
				`{"type":"text","text":"before\u0000after"}]}`,
		},
		{
			name:       "structured data NUL",
			toolCallID: secondToolCallID,
			body: `{"outcome":"succeeded","content_blocks":[` +
				`{"type":"structured_data","value":{"nested":["before\u0000after"]}}]}`,
		},
		{
			name:       "metadata NUL",
			toolCallID: mediaToolCallID,
			body: `{"outcome":"succeeded","content_blocks":[` +
				`{"type":"media","media_type":"image/png","data":"` + pngBase64 + `",` +
				`"metadata":{"source":"before\u0000after"}}]}`,
		},
		{
			name:       "media filename NUL",
			toolCallID: mediaToolCallID,
			body: `{"outcome":"succeeded","content_blocks":[` +
				`{"type":"media","media_type":"image/png","filename":"before\u0000after.png","data":"` +
				pngBase64 + `"}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSONWithHeaders(
				t,
				handler,
				http.MethodPost,
				toolCallsPath+"/"+test.toolCallID+"/result",
				test.body,
				"",
				http.StatusBadRequest,
				authHeaders(project.AdminToken),
			)
			if response["code"] != "invalid_request" {
				t.Fatalf("database-unsafe result response = %+v, want invalid_request", response)
			}
		})
	}
	var artifactCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM artifacts WHERE agent_id = $1`,
		agent.ID,
	).Scan(&artifactCount); err != nil {
		t.Fatalf("count artifacts after rejected results: %v", err)
	}
	if artifactCount != 0 {
		t.Fatalf("rejected results left %d artifact rows", artifactCount)
	}
	readyAfterRejectedResults := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		toolCallsPath+"?state=ready&type=custom",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	readyAfterRejectedResultsByProviderID := publicToolCallsByProviderID(
		t,
		readyAfterRejectedResults,
	)
	for _, providerCallID := range []string{
		"call_lookup_customer",
		"call_lookup_customer_2",
		"call_lookup_customer_3",
	} {
		if readyAfterRejectedResultsByProviderID[providerCallID]["state"] != "ready" {
			t.Fatalf(
				"tool call %s after rejected result = %+v, want ready",
				providerCallID,
				readyAfterRejectedResultsByProviderID[providerCallID],
			)
		}
	}
	submitted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		resultPath,
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"Customer found."}]}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	completedCall := submitted["tool_call"].(map[string]any)
	if completedCall["state"] != "completed" || completedCall["outcome"] != "succeeded" {
		t.Fatalf("completed tool call = %+v", completedCall)
	}
	toolResult := submitted["tool_result"].(map[string]any)
	if toolResult["tool_call_id"] != toolCallID ||
		toolResult["agent_id"] != agentPublicID ||
		toolResult["outcome"] != "succeeded" ||
		!publicEventTextEquals(toolResult, "Customer found.") {
		t.Fatalf("submitted tool_result = %+v", toolResult)
	}
	if toolResult["content_blocks"].([]any)[0].(map[string]any)["type"] != "text" {
		t.Fatalf(
			"submitted tool_result content was not canonicalized: %+v",
			toolResult,
		)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		resultPath,
		`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"Customer found."}]}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		resultPath,
		`{"outcome":"failed","content_blocks":[{"type":"text","text":"Customer not found."}]}`,
		"",
		http.StatusConflict,
		authHeaders(project.AdminToken),
	)

	failedSubmitted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+secondToolCallID+"/result",
		`{"outcome":"failed","content_blocks":[{"type":"structured_data","value":{"message":"before\ud800after"}}]}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	failedCall := failedSubmitted["tool_call"].(map[string]any)
	if failedCall["state"] != "completed" || failedCall["outcome"] != "failed" {
		t.Fatalf("failed tool call = %+v", failedCall)
	}
	failedToolResult := failedSubmitted["tool_result"].(map[string]any)
	expectedFailedContentBlocks := []any{map[string]any{
		"type": "structured_data",
		"value": map[string]any{
			"message": "before\uFFFDafter",
		},
	}}
	if failedToolResult["outcome"] != "failed" ||
		!reflect.DeepEqual(failedToolResult["content_blocks"], expectedFailedContentBlocks) {
		t.Fatalf("failed tool result = %+v", failedToolResult)
	}
	completedCalls, err := storagetest.ListCompletedToolCallsForTurn(
		ctx,
		store,
		project.ProjectUUID,
		agent.ID,
		calls[1].TurnID,
	)
	if err != nil {
		t.Fatalf("list completed tool calls: %v", err)
	}
	var failedRecord *executionstore.ToolCallRecord
	for i := range completedCalls {
		if completedCalls[i].ID == calls[1].ID {
			failedRecord = &completedCalls[i]
			break
		}
	}
	var failedContentBlocks []any
	if failedRecord != nil {
		if err := json.Unmarshal(failedRecord.ResultContentParts, &failedContentBlocks); err != nil {
			t.Fatalf("decode failed custom tool content: %v", err)
		}
	}
	if failedRecord == nil ||
		failedRecord.Outcome != executionstore.ToolResultOutcomeFailed ||
		!reflect.DeepEqual(failedContentBlocks, expectedFailedContentBlocks) {
		t.Fatalf("failed custom tool call = %+v", failedRecord)
	}

	mediaSubmitted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+mediaToolCallID+"/result",
		`{"outcome":"succeeded","content_blocks":[{"type":"media","media_type":"image/png","filename":"pixel.png","data":"`+pngBase64+`"}]}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	mediaBlocks := mediaSubmitted["tool_result"].(map[string]any)["content_blocks"].([]any)
	if len(mediaBlocks) != 1 {
		t.Fatalf("media tool_result content_blocks = %+v, want one media_ref", mediaBlocks)
	}
	mediaBlock := mediaBlocks[0].(map[string]any)
	artifactPublicID, ok := mediaBlock["artifact_id"].(string)
	if !ok || mediaBlock["type"] != "media_ref" {
		t.Fatalf("media tool_result block = %+v, want media_ref with public artifact id", mediaBlock)
	}
	if _, err := publicid.Decode(publicid.KindArtifact, artifactPublicID); err != nil {
		t.Fatalf("media tool_result artifact_id = %q, want public artifact id: %v", artifactPublicID, err)
	}
	emptySubmitted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		toolCallsPath+"/"+emptyToolCallID+"/result",
		`{"outcome":"succeeded","content_blocks":[]}`,
		"",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	emptyResult := emptySubmitted["tool_result"].(map[string]any)
	if emptyResult["outcome"] != "succeeded" ||
		len(emptyResult["content_blocks"].([]any)) != 0 {
		t.Fatalf("empty tool result = %+v", emptyResult)
	}

	finalEvents := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents/"+agentPublicID+"/events",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	wantResultOutcomes := map[string]string{
		toolCallID:       "succeeded",
		secondToolCallID: "failed",
		mediaToolCallID:  "succeeded",
		emptyToolCallID:  "succeeded",
	}
	for _, rawEvent := range finalEvents["data"].([]any) {
		event := rawEvent.(map[string]any)
		if event["event_kind"] != "tool_result" {
			continue
		}
		eventToolCallID, _ := event["tool_call_id"].(string)
		want, tracked := wantResultOutcomes[eventToolCallID]
		if !tracked {
			continue
		}
		if event["outcome"] != want {
			t.Fatalf("tool result event %s = %+v, want outcome %s", eventToolCallID, event, want)
		}
		delete(wantResultOutcomes, eventToolCallID)
	}
	if len(wantResultOutcomes) != 0 {
		t.Fatalf("missing tool result events for %+v", wantResultOutcomes)
	}
	finalCalls := publicCustomToolCallsByID(t, finalEvents)
	for toolCallID, initial := range initialCalls {
		final := finalCalls[toolCallID]
		if final == nil ||
			final["tool_call_id"] != initial["tool_call_id"] ||
			final["name"] != initial["name"] ||
			!reflect.DeepEqual(final["input"], initial["input"]) {
			t.Fatalf("immutable tool call %s changed: initial=%+v final=%+v", toolCallID, initial, final)
		}
	}
}

func publicCustomToolCallsByID(
	t *testing.T,
	response map[string]any,
) map[string]map[string]any {
	t.Helper()
	calls := make(map[string]map[string]any)
	for _, rawEvent := range response["data"].([]any) {
		event := rawEvent.(map[string]any)
		for _, rawBlock := range event["content_blocks"].([]any) {
			block := rawBlock.(map[string]any)
			if block["type"] == "tool_call" {
				if block["tool_type"] != toolcatalog.ToolTypeCustom {
					continue
				}
				if _, exists := block["permission_state"]; exists {
					t.Fatalf("immutable tool call leaked permission state: %+v", block)
				}
				if _, exists := block["state"]; exists {
					t.Fatalf("immutable event tool call leaked lifecycle state: %+v", block)
				}
				toolCallID, ok := block["tool_call_id"].(string)
				if !ok {
					t.Fatalf("immutable custom tool call has no public id: %+v", block)
				}
				calls[toolCallID] = block
			}
		}
	}
	return calls
}

func publicToolCallsByProviderID(
	t *testing.T,
	response map[string]any,
) map[string]map[string]any {
	t.Helper()
	calls := make(map[string]map[string]any)
	for _, rawCall := range response["data"].([]any) {
		call := rawCall.(map[string]any)
		calls[call["provider_call_id"].(string)] = call
	}
	return calls
}
