//go:build integration

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeFailureFollowsBackgroundProcessToItsConversation(t *testing.T) {
	for _, notice := range []string{"exit", "output match"} {
		t.Run(notice, func(t *testing.T) {
			testRuntimeFailureFollowsBackgroundProcess(t, notice)
		})
	}
}

func testRuntimeFailureFollowsBackgroundProcess(t *testing.T, notice string) {
	t.Helper()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	)
	now := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		Email:       "tools-background-failure-" + strings.ReplaceAll(notice, " ", "-") + "@example.com",
		DisplayName: "Tools Background Failure",
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, 'Tools Test Org', 'tools-test-org-background-failure', $2, $2)`, toolsTestOrgID, now)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, 'Tools Test Project', 'tools-test-project-background-failure', $3, $3)`,
		toolsTestProjectID, toolsTestOrgID, now)
	require.NoError(t, err)
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)
	binding := createExecutableBinding(t, ctx, store, user.ID, "background-failure", now.Add(time.Second))
	install := createSlackToolIntegration(t, ctx, store, user.ID, "chat", "background-failure")
	base := createToolsRuntimeAgentWithMachineSources(t, ctx, store, user.ID, "background-failure",
		[]toolsAgentMachineSource{{MachineName: binding.DisplayName, Cwd: "/work"}})
	sender, err := executionstore.IntegrationActorParams(install, "U_SENDER", nil)
	require.NoError(t, err)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, ProfileID: base.Agent.AgentProfileID, AgentConfigID: base.AgentConfig.ID,
		LaunchedBy: toolsTestUserPrincipal(user.ID), IdempotencyKey: "tools-background-failure-launch",
		InitialInput: &executionstore.LaunchInitialInput{
			ContentBlocks:    json.RawMessage(`[{"type":"text","text":"build it"}]`),
			SemanticEventKey: "tools-background-failure-input",
			Actor:            &sender,
			Origin: &executionstore.AgentInputOrigin{
				IntegrationID: install.ID,
				Address:       integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
			},
		},
	})
	require.NoError(t, err)
	agent := launch.Agent
	_, err = pool.Exec(ctx, `UPDATE integration_targets SET launch_key='default' WHERE id=$1`, launch.IntegrationTarget.ID)
	require.NoError(t, err)

	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	lock := claim.RuntimeLock
	first := claimNormalModelCallForToolsTest(t, ctx, store, toolsTestProjectID, agent.ID, lock,
		[]uuid.UUID{launch.AgentInput.ID}, launch.AgentConfig.ID, claim.Model.AdmittedInputTurn.Events[0].Sequence, uuid.Nil)
	runCall := model.ToolCall{ID: "call_build", Name: "run_command", Input: json.RawMessage(`{"command":"make"}`)}
	watchCall := model.ToolCall{ID: "call_watch", Name: "watch_process", Input: json.RawMessage(`{"pattern":"ERROR"}`)}
	runResponse, err := model.NewResponseEnvelopeForStorage("tools-test",
		modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "resp_background_build", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{runCall, watchCall}),
		})
	require.NoError(t, err)
	_, runRecords, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, RuntimeLockID: lock.ID,
			ModelCallContextID: first.Context.ID, ProviderResponse: runResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: runCall.ID, Type: toolcatalog.ToolTypeBuiltIn},
				{ProviderCallID: watchCall.ID, Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, runRecords, 2)
	for _, record := range runRecords {
		_, err = store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, ID: record.ID, RuntimeLockID: lock.ID,
		})
		require.NoError(t, err)
	}
	process, err := storagetest.StartProcessForToolCall(ctx, store,
		executionstore.ExecuteToolCallInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, ToolCallID: runRecords[0].ID, RuntimeLockID: lock.ID,
		},
		executionstore.CreateProcessInput{
			ExecutionSpec:         processcmd.ForShell("make", "sh", "pipe"),
			AgentMachineBindingID: launch.MachineBindings[0].ID,
			Cwd:                   "/work",
		})
	require.NoError(t, err)
	authority := executionstore.DaemonRuntimeAuthority{
		OrgID: toolsTestOrgID, MachineID: binding.MachineID,
		DaemonRuntimeID: binding.RuntimeID, DaemonTokenID: binding.DaemonTokenID,
	}
	_, found, err = store.Execution().AcceptDaemonProcess(ctx, executionstore.AcceptDaemonProcessInput{
		Authority: authority, ProcessID: process.ID,
	})
	require.NoError(t, err)
	require.True(t, found)
	_, err = store.Execution().MarkProcessStarted(ctx, executionstore.MarkProcessStartedInput{
		ProjectID: toolsTestProjectID, AgentID: agent.ID, ID: process.ID, Authority: authority,
		SourceStartedAt: now.Add(2 * time.Second),
		Result:          json.RawMessage(`{"output":"","cursor":0,"next_cursor":0,"truncated":false,"done":false}`),
	})
	require.NoError(t, err)
	watchPayload, err := json.Marshal(processaction.WatchPayload{Pattern: "ERROR", TimeoutSeconds: 60})
	require.NoError(t, err)
	watch, err := storagetest.CreateProcessActionForToolCall(ctx, store,
		executionstore.ExecuteToolCallInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, ToolCallID: runRecords[1].ID, RuntimeLockID: lock.ID,
		},
		executionstore.CreateProcessActionInput{
			ProcessID: process.ID, ActionKind: executionstore.ProcessActionKindWatch, Payload: watchPayload,
		})
	require.NoError(t, err)
	_, found, err = store.Execution().AcceptDaemonProcessAction(ctx, executionstore.AcceptDaemonProcessActionInput{
		Authority: authority, ProcessID: process.ID, ID: watch.ID,
	})
	require.NoError(t, err)
	require.True(t, found)
	_, err = store.Execution().ApplyDaemonProcessAction(ctx, executionstore.CompleteDaemonProcessActionInput{
		ProjectID: toolsTestProjectID, AgentID: agent.ID, ProcessID: process.ID, ID: watch.ID,
		Authority: authority, Result: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	continued := claimNormalModelCallForToolsTest(t, ctx, store, toolsTestProjectID, agent.ID, lock,
		[]uuid.UUID{launch.AgentInput.ID}, launch.AgentConfig.ID, 0, first.Context.ID)
	doneResponse, err := model.NewResponseEnvelopeForStorage("tools-test",
		modelprotocol.APIFormatOpenAIResponses, modelprotocol.APIVariantDefault,
		model.Response{
			ID: "resp_background_started", StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "The build is running."}},
		})
	require.NoError(t, err)
	_, err = store.Execution().RecordModelOutputAndCompleteContext(ctx,
		executionstore.RecordModelOutputAndCompleteContextInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, RuntimeLockID: lock.ID,
			ModelCallContextID: continued.Context.ID, ProviderResponse: doneResponse,
		})
	require.NoError(t, err)
	require.NoError(t, store.Execution().ReleaseAgentRuntimeLock(ctx, toolsTestProjectID, agent.ID, lock.ID))

	if notice == "exit" {
		exitCode := 2
		_, err = store.Execution().CompleteDaemonProcess(ctx, executionstore.CompleteDaemonProcessInput{
			ProjectID: toolsTestProjectID, AgentID: agent.ID, ID: process.ID, Authority: authority,
			State: executionstore.ProcessStateExited, ExitCode: &exitCode,
			Result:        json.RawMessage(`{"state":"exited","output":"error\n","cursor":0,"next_cursor":6,"truncated":false,"done":true}`),
			SourceEndedAt: now.Add(10 * time.Second),
		})
	} else {
		watchID, encodeErr := publicid.Encode(publicid.KindProcessAction, watch.ID)
		require.NoError(t, encodeErr)
		err = store.Execution().NotifyProcessOutputMatch(ctx, executionstore.NotifyProcessOutputMatchInput{
			Authority: authority, ProjectID: toolsTestProjectID, AgentID: agent.ID, ID: process.ID, Sequence: 1,
			Result: json.RawMessage(`{"watch_id":"` + watchID + `","pattern":"ERROR","lines":["ERROR boom"]}`),
		})
	}
	require.NoError(t, err)
	wake, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, wake.Kind)
	require.Len(t, wake.Model.AdmittedInputTurn.Inputs, 1)
	notification := wake.Model.AdmittedInputTurn.Inputs[0]
	require.Equal(t, uuid.Nil, notification.IntegrationTargetID, "a process notice has no conversation of its own")
	failing := claimNormalModelCallForToolsTest(t, ctx, store, toolsTestProjectID, agent.ID, wake.RuntimeLock,
		[]uuid.UUID{notification.ID}, launch.AgentConfig.ID, wake.Model.AdmittedInputTurn.Events[0].Sequence, uuid.Nil)
	turnOf := func(contextID uuid.UUID) uuid.UUID {
		var turnID uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `SELECT turn_id FROM model_call_contexts WHERE id=$1`, contextID).Scan(&turnID))
		return turnID
	}
	failingTurn := turnOf(failing.Context.ID)
	require.NotEqual(t, turnOf(first.Context.ID), failingTurn, "the process notice opens a new turn")

	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		var body struct {
			Channel  string `json:"channel"`
			ThreadTS string `json:"thread_ts"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		assert.Equal(t, "C123", body.Channel)
		assert.Equal(t, "111.222", body.ThreadTS)
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "333.444"})
	}))
	defer server.Close()
	notifier := integrationruntime.RuntimeFailureNotifier{Store: store, HTTPClient: integrationProviderTestClient(server)}
	require.NoError(t, notifier.Notify(ctx, toolsTestProjectID, agent.ID, wake.RuntimeLock.ID, failingTurn))
	require.EqualValues(t, 1, posts.Load(), "background work reports failures where it was requested")
}
